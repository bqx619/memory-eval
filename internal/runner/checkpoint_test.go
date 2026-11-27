package runner

import (
	"context"
	"encoding/json"
	"errors"
	"memeval/internal/adapter"
	imp "memeval/internal/importers/halumem"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type checkpointSpy struct {
	*adapter.Baseline
	cap                     bool
	resets, readies, dumps  int
	batches, visibleAtQuery []int
	written                 []protocol.PublicEvent
	requests                []protocol.RetrieveRequest
	editID                  bool
}

func (a *checkpointSpy) Describe(ctx context.Context) (protocol.Describe, error) {
	d, e := a.Baseline.Describe(ctx)
	if !a.cap {
		d.Capabilities = slices.DeleteFunc(d.Capabilities, func(s string) bool { return s == "dump" })
	}
	return d, e
}
func (a *checkpointSpy) Reset(ctx context.Context, ns string) error {
	a.resets++
	a.written = nil
	return a.Baseline.Reset(ctx, ns)
}
func (a *checkpointSpy) Ingest(ctx context.Context, ns string, events []protocol.PublicEvent) (protocol.IngestHandle, error) {
	a.batches = append(a.batches, len(events))
	a.written = append(a.written, events...)
	return a.Baseline.Ingest(ctx, ns, events)
}
func (a *checkpointSpy) Ready(ctx context.Context, ns string, h protocol.IngestHandle, d time.Time) (protocol.ReadyResult, error) {
	a.readies++
	return a.Baseline.Ready(ctx, ns, h, d)
}
func (a *checkpointSpy) Retrieve(ctx context.Context, ns string, q protocol.RetrieveRequest) (protocol.RetrieveResponse, error) {
	a.visibleAtQuery = append(a.visibleAtQuery, len(a.written))
	a.requests = append(a.requests, q)
	return a.Baseline.Retrieve(ctx, ns, q)
}
func (a *checkpointSpy) Dump(ctx context.Context, ns string) (protocol.DumpResponse, error) {
	a.dumps++
	v, e := a.Baseline.Dump(ctx, ns)
	if a.editID && len(v.Items) > 0 {
		v.Items[0].Text = "same ID changed content"
	}
	return v, e
}

type auditClient struct {
	failAccuracy bool
	calls        int
	integrity    int
}

func (f *auditClient) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	f.calls++
	return llm.Response{Text: "answer"}, ctx.Err()
}
func (f *auditClient) Decide(ctx context.Context, r decision.DecisionRequest) (decision.DecisionResponse, error) {
	f.calls++
	if strings.Contains(r.TemplateID, "integrity") {
		f.integrity++
	}
	if strings.Contains(r.TemplateID, "accuracy") && f.failAccuracy {
		return decision.DecisionResponse{}, errors.New("synthetic failure")
	}
	d := testdecision.Client{P: 1}
	out, e := d.Decide(ctx, r)
	for _, q := range r.Questions {
		a := out.Answers[q.Name]
		if q.Name == "included" {
			a.Value = "true"
		} else if q.Options[0] == "0" {
			a.Value = "2"
		} else {
			a.Value = "Correct"
		}
		for k := range a.Probabilities {
			a.Probabilities[k] = 0
		}
		a.Probabilities[a.Value.(string)] = 1
		out.Answers[q.Name] = a
	}
	return out, e
}

func auditSuite(t *testing.T) *suite.Suite {
	t.Helper()
	testoracle.ProtocolSources(t, "halumem")
	r, e := imp.ImportFile(t.Context(), imp.Options{Input: "../importers/halumem/testdata/synthetic.jsonl", Variant: "medium", Out: filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
	if e != nil {
		t.Fatal(e)
	}
	s, e := suite.Open(t.Context(), r.Path)
	if e != nil {
		t.Fatal(e)
	}
	var ep suite.Episode
	if e = s.Iter(t.Context(), func(x *suite.Episode) error { ep = *x; return nil }); e != nil {
		t.Fatal(e)
	}
	return &suite.Suite{SchemaVersion: s.SchemaVersion, SuiteID: s.SuiteID, ProtocolRef: s.ProtocolRef, Protocol: s.Protocol, Provenance: s.Provenance, Episodes: []suite.Episode{ep}}
}
func auditOptions(client *auditClient) Options {
	o := DefaultOptions()
	o.ResetPolicy = IncrementalPerEpisode
	o.Mode = FixedReader
	o.RunSeed = strings.Repeat("86", 32)
	o.FixedReader = &FixedReaderOptions{Client: client, Input: "sources", Model: "reader", Provider: "openai-compat", JudgeDecisionModel: "jev-1.14.0", DecisionClient: client, MaxTokens: 500}
	return o
}
func TestCheckpointOrderDumpDiffAndMissingCapability(t *testing.T) {
	for _, cap := range []bool{true, false} {
		t.Run(map[bool]string{true: "dump", false: "no-dump"}[cap], func(t *testing.T) {
			s := auditSuite(t)
			b, _ := adapter.NewBaseline("bm25")
			a := &checkpointSpy{Baseline: b, cap: cap, editID: true}
			client := &auditClient{}
			r, e := Run(t.Context(), s, a, auditOptions(client))
			if e != nil {
				t.Fatal(e)
			}
			if a.resets != 1 || a.readies != 3 || !reflect.DeepEqual(a.batches, []int{2, 2, 2}) {
				t.Fatal(a)
			}
			want := []int{2, 4, 4, 6}
			if !cap {
				want = []int{2, 4, 6}
			}
			if !reflect.DeepEqual(a.visibleAtQuery, want) {
				t.Fatal("future history visible", a.visibleAtQuery)
			}
			for i, c := range r.Cases {
				if i%2 == 0 {
					status := result.Scored
					if !cap {
						status = result.Unsupported
					}
					if c.Status != status {
						t.Fatal(c.Status, c.ErrorType)
					}
					if cap {
						var v auditResult
						json.Unmarshal(c.Audit, &v)
						if v.NewCount != 2 || v.DumpCount != i+2 {
							t.Fatal("ID-only difference", v)
						}
					}
				} else {
					if c.Status != result.Scored || c.Budget.TopK != 20 {
						t.Fatal("QA must run", c)
					}
				}
			}
			if cap {
				if a.dumps != 3 || a.requests[1].Budget.TopK != 10 {
					t.Fatal("dump/update top-k")
				}
			} else if a.dumps != 0 {
				t.Fatal("unadvertised dump invoked")
			}
			for _, q := range a.requests {
				if q.AsOf != "" {
					t.Fatal("reference_only")
				}
			}
			for _, ev := range a.written {
				if len(ev.Attributes) > 0 {
					t.Fatal("privileged annotations escaped", ev.Attributes)
				}
			}
		})
	}
}
func TestGeneratedCheckpointAndJudgeFailure(t *testing.T) {
	s := auditSuite(t)
	ep := &s.Episodes[0]
	ep.Cases[2].Attributes["is_generated_qa_session"] = true
	ep.Cases[2].Gold.MemoryPoints = []suite.MemoryPoint{}
	ep.Cases = append(ep.Cases[:3], ep.Cases[4:]...)
	b, _ := adapter.NewBaseline("bm25")
	a := &checkpointSpy{Baseline: b, cap: true}
	r, e := Run(t.Context(), s, a, auditOptions(&auditClient{failAccuracy: true}))
	if e != nil {
		t.Fatal(e)
	}
	if r.Cases[2].Status != result.Unscored || r.Cases[0].Status != result.Error {
		t.Fatal(r.Cases)
	}
	var skip, last auditResult
	json.Unmarshal(r.Cases[2].Audit, &skip)
	json.Unmarshal(r.Cases[3].Audit, &last)
	if !skip.Skipped || len(skip.Records.Integrity) != 0 || last.NewCount != 2 {
		t.Fatal("generated snapshot/scoring")
	}
	official := r.Scores.Official.(result.Official)
	if *official.Metrics["halumem.memory_accuracy.memory_num"] != 4 || *official.Metrics["halumem.memory_accuracy.memory_valid_num"] != 0 {
		t.Fatal("failed judge denominator", official.Metrics)
	}
}

func TestResumeCheckpointRebuildsPreviousDumpWithoutRejudging(t *testing.T) {
	s := auditSuite(t)
	b, _ := adapter.NewBaseline("bm25")
	old, err := Run(t.Context(), s, &checkpointSpy{Baseline: b, cap: true}, auditOptions(&auditClient{}))
	if err != nil {
		t.Fatal(err)
	}
	old.Cases[4].Status = result.Error
	plan, err := NewResume(old, "error,invalid")
	if err != nil {
		t.Fatal(err)
	}
	client := &auditClient{}
	o := auditOptions(client)
	o.Resume = plan
	b, _ = adapter.NewBaseline("bm25")
	a := &checkpointSpy{Baseline: b, cap: true}
	r, err := Run(t.Context(), s, a, o)
	if err != nil {
		t.Fatal(err)
	}
	if a.resets != 1 || a.dumps != 3 || a.readies != 3 || len(a.requests) != 0 {
		t.Fatal(a)
	}
	for i := range r.Cases {
		if i != 4 && !reflect.DeepEqual(old.Cases[i], r.Cases[i]) {
			t.Fatal("reused checkpoint changed", i)
		}
	}
	var audit auditResult
	if err = json.Unmarshal(r.Cases[4].Audit, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.NewCount != 2 || audit.DumpCount != 6 || r.Cases[4].PreviousStatus != result.Error || r.Cases[4].Status != result.Scored {
		t.Fatal(audit, r.Cases[4].Status)
	}
	if client.calls == 0 || client.calls >= 6 {
		t.Fatal("rejudged preserved checkpoints", client.calls)
	}
}
func TestEmptyDumpAndCheckpointLimit(t *testing.T) {
	s := auditSuite(t)
	b, _ := adapter.NewBaseline("null")
	a := &checkpointSpy{Baseline: b, cap: true}
	client := &auditClient{}
	o := auditOptions(client)
	limit := 3
	o.Limit = &limit
	r, e := Run(t.Context(), s, a, o)
	if e != nil {
		t.Fatal(e)
	}
	if a.readies != 2 || len(a.written) != 4 || client.integrity != 0 {
		t.Fatal("empty dump or future ingest", a, client)
	}
	v := r.Scores.Official.(result.Official)
	if *v.Metrics["halumem.memory_integrity.recall(all)"] != 0 || v.Metrics["halumem.memory_extraction_f1"] != nil || *v.Metrics["halumem.memory_update.update_memory_num"] != 0 {
		t.Fatal("empty fallback", v.Metrics)
	}
}
func TestCheckpointGuards(t *testing.T) {
	s := auditSuite(t)
	ep := &s.Episodes[0]
	ep.Cases[0], ep.Cases[1] = ep.Cases[1], ep.Cases[0]
	b, _ := adapter.NewBaseline("null")
	if _, e := Run(t.Context(), s, b, auditOptions(&auditClient{})); e == nil {
		t.Fatal("late audit accepted")
	}
}
