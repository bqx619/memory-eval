package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"memeval/internal/adapter"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"reflect"
	"strings"
	"testing"
	"time"
)

func toy() *suite.Suite {
	return &suite.Suite{SchemaVersion: "2.0", SuiteID: "software-unit-fixture", Provenance: map[string]any{"source_url": "unit-test://not-a-benchmark"}, Episodes: []suite.Episode{{ID: "PRIVATE-EPISODE", Events: []suite.Event{{SourceID: "private-a", Owner: "u", Session: "PRIVATE-SESSION", Content: "blue box", Attributes: map[string]any{"has_answer": true, "gold": "SECRET-GOLD"}}, {SourceID: "private-b", Owner: "u", Content: "FUTURE-FORBIDDEN"}}, Cases: []suite.Case{{ID: "PRIVATE-QUESTION", Kind: "qa", Query: "box?", User: "u", Category: "PRIVATE-CATEGORY", Cutoff: 1, Gold: suite.Gold{Answers: []string{"SECRET-GOLD"}, EvidenceIDs: []string{"private-a"}}}}}}}
}

type spy struct {
	adapter.Adapter
	inputs     []protocol.PublicEvent
	request    protocol.RetrieveRequest
	names      []string
	batches    []int
	ready      bool
	closed     bool
	retrieve   func(*spy) (protocol.RetrieveResponse, error)
	ingest     func(protocol.IngestHandle) protocol.IngestHandle
	visibility bool
}

func newSpy() *spy { a, _ := adapter.NewBaseline("bm25"); return &spy{Adapter: a, visibility: true} }
func (s *spy) Reset(ctx context.Context, ns string) error {
	s.names = append(s.names, ns)
	s.ready = false
	return s.Adapter.Reset(ctx, ns)
}
func (s *spy) Ingest(ctx context.Context, ns string, events []protocol.PublicEvent) (protocol.IngestHandle, error) {
	s.inputs = append(s.inputs, events...)
	s.batches = append(s.batches, len(events))
	h, err := s.Adapter.Ingest(ctx, ns, events)
	if s.ingest != nil {
		h = s.ingest(h)
	}
	return h, err
}
func (s *spy) Ready(ctx context.Context, ns string, h protocol.IngestHandle, d time.Time) (protocol.ReadyResult, error) {
	s.ready = true
	r, err := s.Adapter.Ready(ctx, ns, h, d)
	r.Visible = s.visibility
	return r, err
}
func (s *spy) Retrieve(ctx context.Context, ns string, r protocol.RetrieveRequest) (protocol.RetrieveResponse, error) {
	s.request = r
	if !s.ready {
		return protocol.RetrieveResponse{}, errors.New("read before ready")
	}
	if s.retrieve != nil {
		return s.retrieve(s)
	}
	return s.Adapter.Retrieve(ctx, ns, r)
}
func (s *spy) Close(ctx context.Context) error { s.closed = true; return s.Adapter.Close(ctx) }
func opts() runner.Options {
	o := runner.DefaultOptions()
	o.RunSeed = strings.Repeat("42", 32)
	return o
}
func run(t *testing.T, s *suite.Suite, a adapter.Adapter, o runner.Options) *result.Report {
	t.Helper()
	r, err := runner.Run(context.Background(), s, a, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPublicBoundaryAndReady(t *testing.T) {
	s := newSpy()
	r := run(t, toy(), s, opts())
	if r.Isolation.FDHygiene != "cloexec_all" {
		t.Fatal("missing fd hygiene disclosure", r.Isolation)
	}
	wire, _ := json.Marshal([]any{s.inputs, s.request})
	for _, bad := range []string{"SECRET-GOLD", "PRIVATE-QUESTION", "PRIVATE-CATEGORY", "FUTURE-FORBIDDEN", "private-a", "PRIVATE-EPISODE", "PRIVATE-SESSION", "has_answer"} {
		if strings.Contains(string(wire), bad) {
			t.Fatalf("leaked %s", bad)
		}
	}
	if r.Cases[0].Status != result.Scored || *r.Cases[0].Metrics["recall"] != 1 || !s.closed {
		t.Fatal(r.Cases[0])
	}
}
func TestContractAndFailureStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*spy) (protocol.RetrieveResponse, error)
		want result.Status
	}{
		{"invented", func(s *spy) (protocol.RetrieveResponse, error) {
			return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "x", SourceIDs: []string{"fake"}}}}, nil
		}, result.ContractError},
		{"duplicate", func(s *spy) (protocol.RetrieveResponse, error) {
			id := s.inputs[0].ID
			return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "x", SourceIDs: []string{id, id}}}}, nil
		}, result.ContractError},
		{"no provenance", func(s *spy) (protocol.RetrieveResponse, error) {
			return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "x", SourceIDs: []string{}}}}, nil
		}, result.Unsupported},
		{"chars", func(s *spy) (protocol.RetrieveResponse, error) {
			return protocol.RetrieveResponse{Items: []protocol.Item{{Text: strings.Repeat("🙂", 101), SourceIDs: []string{s.inputs[0].ID}}}}, nil
		}, result.ContractError},
		{"top k", func(s *spy) (protocol.RetrieveResponse, error) {
			i := protocol.Item{Text: "x", SourceIDs: []string{s.inputs[0].ID}}
			return protocol.RetrieveResponse{Items: []protocol.Item{i, i}}, nil
		}, result.ContractError},
		{"error redacted", func(s *spy) (protocol.RetrieveResponse, error) {
			return protocol.RetrieveResponse{}, errors.New("PRIVATE-SECRET-TOKEN")
		}, result.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSpy()
			a.retrieve = tc.fn
			o := opts()
			o.Budget.TopK = 1
			o.Budget.MaxContextChars = 100
			r := run(t, toy(), a, o)
			if r.Cases[0].Status != tc.want {
				t.Fatal(r.Cases[0])
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "PRIVATE-SECRET-TOKEN") {
				t.Fatal("error body leaked")
			}
		})
	}
}
func TestTypedContractValidation(t *testing.T) {
	known := map[string]suite.Event{"e": {SourceID: "e"}}
	budget := protocol.Budget{TopK: 1, MaxContextChars: 2, MaxBeliefs: 0, HasMaxBeliefs: true}
	prelude := ""
	r := protocol.RetrieveResponse{Tiers: &protocol.Tiers{PinnedFacts: []protocol.Item{{Text: "中", SourceIDs: []string{"e"}}}, RelevantBeliefs: []protocol.Item{}, OpenQuestions: []protocol.Item{}}, PersonaPrelude: &prelude}
	if _, _, err := runner.ValidateResponse(r, known, budget, true); err == nil {
		t.Fatal("ignored maxBeliefs zero")
	}
	budget.HasMaxBeliefs = false
	if _, chars, err := runner.ValidateResponse(r, known, budget, true); err != nil || chars != 1 {
		t.Fatal(chars, err)
	}
	r.Tiers.OpenQuestions = nil
	if _, _, err := runner.ValidateResponse(r, known, budget, true); err == nil {
		t.Fatal("missing tier accepted")
	}
	if _, _, err := runner.ValidateResponse(protocol.RetrieveResponse{}, known, budget, true); err == nil {
		t.Fatal("missing tiers accepted")
	}
}
func TestIngestFailureAndBarrier(t *testing.T) {
	for _, mode := range []string{"failed", "invisible", "counts"} {
		t.Run(mode, func(t *testing.T) {
			a := newSpy()
			want := result.IngestFailed
			switch mode {
			case "failed":
				a.ingest = func(h protocol.IngestHandle) protocol.IngestHandle { h.Accepted--; h.Failed++; return h }
			case "invisible":
				a.visibility = false
			case "counts":
				want = result.ContractError
				a.ingest = func(h protocol.IngestHandle) protocol.IngestHandle { h.Accepted++; return h }
			}
			r := run(t, toy(), a, opts())
			if r.Cases[0].Status != want {
				t.Fatal(r.Cases[0])
			}
			if _, ok := r.Cases[0].Timings["query_ms"]; ok {
				t.Fatal("query after failed ingest/barrier")
			}
		})
	}
}
func TestExhaustiveEmptyV2(t *testing.T) {
	for _, exhaustive := range []bool{true, false} {
		s := toy()
		c := &s.Episodes[0].Cases[0]
		c.Kind = "retrieval"
		c.Gold = suite.Gold{EvidenceIDs: []string{}, Exhaustive: exhaustive}
		a, _ := adapter.NewBaseline("null")
		r := run(t, s, a, opts())
		if exhaustive {
			if r.Cases[0].Status != result.Scored || *r.Cases[0].Metrics["empty_result_correct"] != 1 {
				t.Fatal(r.Cases[0])
			}
		} else if r.Cases[0].Status != result.Unscored {
			t.Fatal(r.Cases[0])
		}
	}
}
func TestDiagnosticsDoNotFilter(t *testing.T) {
	s := toy()
	s.Episodes[0].Events[0].Owner = "other"
	a := newSpy()
	a.retrieve = func(s *spy) (protocol.RetrieveResponse, error) {
		i := protocol.Item{Text: "blue box", SourceIDs: []string{s.inputs[0].ID}}
		return protocol.RetrieveResponse{Items: []protocol.Item{i, i}}, nil
	}
	r := run(t, s, a, opts())
	d := r.Cases[0].Diagnostics
	if d["cross_user_source_count"] != 1 || d["duplicate_source_occurrences"] != 1 || *r.Cases[0].Metrics["recall"] != 1 {
		t.Fatal(r.Cases[0])
	}
}
func TestDeterministicRepeatsAndLimit(t *testing.T) {
	s := toy()
	second := s.Episodes[0].Cases[0]
	second.ID = "second"
	s.Episodes[0].Cases = append(s.Episodes[0].Cases, second)
	o := opts()
	o.Repeats = 2
	n := 1
	o.Limit = &n
	a, b := newSpy(), newSpy()
	x, y := run(t, s, a, o), run(t, s, b, o)
	if len(x.Cases) != 2 || len(a.names) != 2 || a.names[0] == a.names[1] || !reflect.DeepEqual(a.names, b.names) {
		t.Fatal("bad independent namespaces")
	}
	for i := range x.Cases {
		x.Cases[i].Timings = nil
		y.Cases[i].Timings = nil
	}
	xb, _ := json.Marshal(x.Cases)
	yb, _ := json.Marshal(y.Cases)
	if string(xb) != string(yb) {
		t.Fatal("fixed seed differs")
	}
	if x.Cases[0].PublicInputSHA256 == x.Cases[1].PublicInputSHA256 {
		t.Fatal("repeats reused identities")
	}
}
func TestPerSessionBatching(t *testing.T) {
	s := toy()
	s.Protocol.Ingestion = "per_session"
	s.Episodes[0].Events[1].Session = s.Episodes[0].Events[0].Session
	s.Episodes[0].Events = append(s.Episodes[0].Events, suite.Event{SourceID: "third", Owner: "u", Content: "third", Session: "next"})
	s.Episodes[0].Cases[0].Cutoff = 3
	o := opts()
	o.BatchSize = 1
	a := newSpy()
	run(t, s, a, o)
	if !reflect.DeepEqual(a.batches, []int{2, 1}) {
		t.Fatal(a.batches)
	}
}
func TestUnsupportedModesAndOptions(t *testing.T) {
	for _, mutate := range []func(*runner.Options){func(o *runner.Options) { o.Mode = runner.FixedReader }, func(o *runner.Options) { o.Mode = runner.NativeAnswer }, func(o *runner.Options) { o.ResetPolicy = runner.IncrementalPerEpisode }, func(o *runner.Options) { o.Parallel = 2 }, func(o *runner.Options) { o.Repeats = 0 }} {
		o := opts()
		mutate(&o)
		a := newSpy()
		if _, err := runner.Run(context.Background(), toy(), a, o); err == nil || !a.closed {
			t.Fatal("unsupported option accepted or adapter not closed")
		}
	}
}

func TestLongMemEvalPolicyMetricsAndAbstention(t *testing.T) {
	s := toy()
	s.Protocol.QueryTimePolicy = "reference_only"
	s.Protocol.RetrievalMetrics = []string{"longmemeval_official"}
	c := &s.Episodes[0].Cases[0]
	c.QueryTime = "2025-01-01T00:00:00Z"
	c.Gold.EvidenceUnit = "session"
	c.Gold.EvidenceSessionIDs = []string{"PRIVATE-SESSION"}
	c.Attributes = map[string]any{"question_date_raw": "PRIVATE-DATE"}
	s.Episodes[0].Events[0].Attributes["answer_session_ids"] = []string{"PRIVATE-SESSION"}
	a := newSpy()
	r := run(t, s, a, opts())
	wire, _ := json.Marshal([]any{a.inputs, a.request})
	for _, bad := range []string{"answer_session_ids", "has_answer", "PRIVATE-QUESTION", "PRIVATE-DATE"} {
		if strings.Contains(string(wire), bad) {
			t.Fatal("leaked", bad)
		}
	}
	if a.request.AsOf != "" || r.Protocol.QueryTimePolicy != "reference_only" || r.Summary.Metrics["longmemeval_official.session.recall_all@10"].N != 1 || r.Scores.MemoryOnly["longmemeval_official"] == nil {
		t.Fatal(r)
	}
	c.Gold.Abstain = true
	r = run(t, s, newSpy(), opts())
	if r.Cases[0].Status != result.Unscored || len(r.Cases[0].Metrics) != 0 {
		t.Fatal("abstention entered retrieval denominator")
	}
	c.Gold.Abstain = false
	s.Protocol.QueryTimePolicy = ""
	a = newSpy()
	run(t, s, a, opts())
	if a.request.AsOf != c.QueryTime {
		t.Fatal("default as_of behavior changed")
	}
}
