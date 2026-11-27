package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func parallelSuite(n int) *suite.Suite {
	s := toy()
	ep := s.Episodes[0]
	s.Episodes = nil
	for i := range n {
		next := ep
		next.ID = fmt.Sprintf("ep-%02d", i)
		c := ep.Cases[0]
		c.ID, c.Query = fmt.Sprintf("case-%02d", i), fmt.Sprintf("query-%02d", i)
		next.Cases = []suite.Case{c}
		s.Episodes = append(s.Episodes, next)
	}
	return s
}

func orderedResult(t *testing.T, r *result.Report) string {
	t.Helper()
	for i := range r.Cases {
		r.Cases[i].Timings = nil
	}
	r.Summary.Timings = nil
	for k, v := range r.ByCategory {
		v.Timings = nil
		r.ByCategory[k] = v
	}
	b, e := json.Marshal([]any{r.Cases, r.Summary, r.ByCategory, r.Scores, r.Billing, r.Warnings})
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestParallelOrderingAndWorkerError(t *testing.T) {
	s := parallelSuite(9)
	newWorker := func() *spy {
		a := newSpy()
		a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
			if a.request.Query == "query-02" {
				return protocol.RetrieveResponse{}, errors.New("synthetic worker error")
			}
			return a.Adapter.Retrieve(t.Context(), a.names[len(a.names)-1], a.request)
		}
		return a
	}
	serial := run(t, s, newWorker(), opts())
	if serial.Parallel != 1 {
		t.Fatal("missing default parallel", serial.Parallel)
	}
	raw, _ := json.Marshal(serial)
	if !strings.Contains(string(raw), `"parallel":1`) {
		t.Fatal("parallel must always be written")
	}
	want := orderedResult(t, serial)
	o := opts()
	o.Parallel = 4
	var started, active, maximum atomic.Int32
	gate := make(chan struct{})
	factory := func(context.Context) (adapter.Adapter, error) {
		a := newWorker()
		retrieve := a.retrieve
		a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
			}
			if started.Add(1) == 4 {
				close(gate)
			}
			select {
			case <-gate:
			case <-time.After(5 * time.Second):
				return protocol.RetrieveResponse{}, errors.New("workers did not overlap")
			}
			return retrieve(a)
		}
		return a, nil
	}
	o.NewAdapter = factory
	first, _ := factory(t.Context())
	r := run(t, s, first, o)
	if r.Parallel != 4 {
		t.Fatal("wrong recorded parallel", r.Parallel)
	}
	if maximum.Load() != 4 || len(r.Cases) != 9 || r.Cases[2].Status != result.Error {
		t.Fatal(maximum.Load(), r.Summary)
	}
	if got := orderedResult(t, r); got != want {
		t.Fatalf("parallel result differs\n%s\n%s", want, got)
	}
}

func TestParallelStopsSchedulingAndDrainsInFlight(t *testing.T) {
	s := parallelSuite(7)
	o := opts()
	o.Parallel = 4
	o.ResetPolicy = runner.IncrementalPerEpisode
	o.Repeats = 2
	for i := range s.Episodes {
		s.Episodes[i].Cases[0].Cutoff = len(s.Episodes[i].Events)
		c := s.Episodes[i].Cases[0]
		c.ID += "-second"
		s.Episodes[i].Cases = append(s.Episodes[i].Cases, c)
	}
	var checks atomic.Int32
	o.StopScheduling = func() string {
		if checks.Add(1) > 4 {
			return "budget"
		}
		return ""
	}
	var workers []*writeSpy
	o.NewAdapter = func(context.Context) (adapter.Adapter, error) {
		a := &writeSpy{spy: newSpy(), readOnly: true}
		workers = append(workers, a)
		return a, nil
	}
	first, _ := o.NewAdapter(t.Context())
	r := run(t, s, first, o)
	if r.Interruption == nil || r.Interruption.Unscheduled != 12 || len(r.Cases) != 16 {
		t.Fatal(r.Interruption, len(r.Cases))
	}
	queries, resets := 0, 0
	for _, w := range workers {
		queries += len(w.queries)
		resets += len(w.names)
		if !w.closed {
			t.Fatal("worker not closed")
		}
	}
	if queries != 16 || resets != 8 {
		t.Fatal("in-flight episode/repeats did not finish", queries, resets)
	}
	for i, c := range r.Cases {
		if c.EpisodeID != s.Episodes[i/4].ID || c.RepeatIndex != i%2 {
			t.Fatal("ordering", c)
		}
	}
	plan, err := runner.NewResume(r, "error,invalid")
	if err != nil {
		t.Fatal(err)
	}
	o.Resume, o.StopScheduling = plan, nil
	nextFirst, _ := o.NewAdapter(t.Context())
	next := run(t, s, nextFirst, o)
	if next.Interruption != nil || len(next.Cases) != 28 {
		t.Fatal("parallel resume lost samples", next.Interruption, len(next.Cases))
	}
	serial := opts()
	serial.ResetPolicy, serial.Repeats = runner.IncrementalPerEpisode, 2
	want := run(t, s, &writeSpy{spy: newSpy(), readOnly: true}, serial)
	if orderedResult(t, next) != orderedResult(t, want) {
		t.Fatal("incremental parallel/resume differs from uninterrupted serial")
	}
}

func TestParallelFactoryFailureAndWarning(t *testing.T) {
	a := newSpy()
	allocated := newSpy()
	calls := 0
	o := opts()
	o.Parallel = 4
	o.NewAdapter = func(context.Context) (adapter.Adapter, error) {
		calls++
		if calls == 1 {
			return allocated, nil
		}
		return nil, errors.New("factory failed")
	}
	if _, e := runner.Run(t.Context(), parallelSuite(4), a, o); e == nil || !strings.Contains(e.Error(), "factory failed") {
		t.Fatal(e)
	}
	if !a.closed || !allocated.closed || len(a.names) > 0 {
		t.Fatal("factory failure leaked adapters or scheduled work")
	}
	if runner.ParallelWarning(4, 4) != "" || !strings.Contains(runner.ParallelWarning(4, 2), "host CPU count 2") {
		t.Fatal("CPU warning")
	}
}

type parallelDecision struct{}

func (parallelDecision) Decide(ctx context.Context, r decision.DecisionRequest) (decision.DecisionResponse, error) {
	return (&testdecision.Client{P: 1}).Decide(ctx, r)
}

func TestParallelEvaluatorBilling(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "offline-parallel-fixture")
	s := qaToy()
	s.Episodes = parallelSuite(8).Episodes
	for i := range s.Episodes {
		s.Episodes[i].Cases[0].QuestionType = "knowledge-update"
		s.Episodes[i].Cases[0].QueryPoint = "at_index"
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"model": "reader-fixture", "choices": []any{map[string]any{"message": map[string]any{"content": "blue"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 2}})
	}))
	defer server.Close()
	var want string
	for _, n := range []int{1, 4} {
		cfg := llm.DefaultConfig()
		cfg.Mode = "record-once"
		cfg.BaseURL = server.URL + "/v1"
		cfg.CassetteDir = t.TempDir()
		cfg.Parallel = 2
		cfg.MaxRetries = 0
		cfg.PricesPath = filepath.Join(t.TempDir(), "prices.json")
		if e := os.WriteFile(cfg.PricesPath, []byte(`{"openai-compat/reader-fixture":{"prompt_per_million":0.1,"completion_per_million":0.2}}`), 0600); e != nil {
			t.Fatal(e)
		}
		engine, e := llm.New(cfg)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { engine.Close() })
		o := fixedOpts(engine)
		o.FixedReader.DecisionClient = parallelDecision{}
		o.Parallel = n
		o.NewAdapter = func(context.Context) (adapter.Adapter, error) { return newSpy(), nil }
		r := run(t, s, newSpy(), o)
		if billing := engine.Billing(); billing.Calls != 8 || billing.PromptTokens != 24 || billing.CompletionTokens != 16 || billing.CostNew == nil {
			t.Fatal(billing)
		}
		got := orderedResult(t, r)
		if n == 1 {
			want = got
		} else if want != got {
			t.Fatal("parallel evaluator cases/scores/warnings/billing differ")
		}
	}
	if calls.Load() != 16 {
		t.Fatal(calls.Load())
	}
}

func singleEpisodeSuite(n int) *suite.Suite {
	s := parallelSuite(n)
	ep := s.Episodes[0]
	ep.Cases = nil
	for _, source := range s.Episodes {
		ep.Cases = append(ep.Cases, source.Cases...)
	}
	s.Episodes = []suite.Episode{ep}
	return s
}

type parallelDescribeSpy struct {
	*spy
	describes *atomic.Int32
}

func (a *parallelDescribeSpy) Describe(ctx context.Context) (protocol.Describe, error) {
	a.describes.Add(1)
	return a.spy.Describe(ctx)
}

func TestParallelSingleEpisodeOverlapAndRepeats(t *testing.T) {
	s := singleEpisodeSuite(9)
	serial := opts()
	serial.Repeats = 2
	newWorker := func() *spy {
		a := newSpy()
		a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
			if a.request.Query == "query-02" {
				return protocol.RetrieveResponse{}, errors.New("synthetic worker error")
			}
			return a.Adapter.Retrieve(t.Context(), a.names[len(a.names)-1], a.request)
		}
		return a
	}
	want := orderedResult(t, run(t, s, newWorker(), serial))
	o := serial
	o.Parallel = 4
	var created, describes, started, active, maximum atomic.Int32
	gate := make(chan struct{})
	var release sync.Once
	var mu sync.Mutex
	owners := map[string]int32{}
	o.NewAdapter = func(context.Context) (adapter.Adapter, error) {
		id := created.Add(1)
		a := newWorker()
		retrieve := a.retrieve
		a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
			mu.Lock()
			old, known := owners[a.request.Query]
			owners[a.request.Query] = id
			mu.Unlock()
			if known && old != id {
				return protocol.RetrieveResponse{}, fmt.Errorf("case repeats moved between workers")
			}
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
			}
			if started.Add(1) == 4 {
				release.Do(func() { close(gate) })
			}
			select {
			case <-gate:
			case <-time.After(5 * time.Second):
				release.Do(func() { close(gate) })
				return protocol.RetrieveResponse{}, errors.New("single-episode workers did not overlap")
			}
			return retrieve(a)
		}
		return &parallelDescribeSpy{spy: a, describes: &describes}, nil
	}
	first, _ := o.NewAdapter(t.Context())
	r := run(t, s, first, o)
	if maximum.Load() != 4 || created.Load() != 4 || describes.Load() != 1 || len(r.Cases) != 18 {
		t.Fatalf("active=%d workers=%d describe=%d cases=%d", maximum.Load(), created.Load(), describes.Load(), len(r.Cases))
	}
	if got := orderedResult(t, r); got != want {
		t.Fatalf("single-episode parallel differs from serial\n%s\n%s", want, got)
	}
	t.Logf("one suite episode: %d cases × 2 repeats; max concurrent workers=%d, describe=%d", len(s.Episodes[0].Cases), maximum.Load(), describes.Load())
}

func TestParallelSingleEpisodeLimitStopAndResume(t *testing.T) {
	s := singleEpisodeSuite(7)
	o := opts()
	o.Parallel = 4
	o.Repeats = 2
	limit := 5
	o.Limit = &limit
	created := 0
	o.NewAdapter = func(context.Context) (adapter.Adapter, error) { created++; return newSpy(), nil }
	checks := 0
	o.StopScheduling = func() string {
		checks++
		if checks > 2 {
			return "exhausted"
		}
		return ""
	}
	first, _ := o.NewAdapter(t.Context())
	r := run(t, s, first, o)
	if created != 4 || checks != 3 || len(r.Cases) != 4 || r.Interruption == nil || r.Interruption.Unscheduled != 6 {
		t.Fatal(created, checks, len(r.Cases), r.Interruption)
	}
	for i, c := range r.Cases {
		if c.CaseID != s.Episodes[0].Cases[i/2].ID || c.RepeatIndex != i%2 {
			t.Fatal(c)
		}
	}
	plan, e := runner.NewResume(r, "error,invalid")
	if e != nil {
		t.Fatal(e)
	}
	o.Resume, o.StopScheduling = plan, nil
	first, _ = o.NewAdapter(t.Context())
	next := run(t, s, first, o)
	serial := opts()
	serial.Repeats = 2
	serial.Limit = &limit
	if next.Interruption != nil || orderedResult(t, next) != orderedResult(t, run(t, s, newSpy(), serial)) {
		t.Fatal("limited parallel resume differs from uninterrupted serial")
	}
	// The source index has seven cases, but the effective limit allows only two jobs.
	limit = 2
	created = 0
	o.Resume = nil
	first, _ = o.NewAdapter(t.Context())
	r = run(t, s, first, o)
	if created != 2 || len(r.Cases) != 4 {
		t.Fatal("worker count ignored effective job count", created, len(r.Cases))
	}
}

func TestParallelSingleIncrementalEpisodeStaysSequential(t *testing.T) {
	s := singleEpisodeSuite(5)
	for i := range s.Episodes[0].Cases {
		s.Episodes[0].Cases[i].Cutoff = len(s.Episodes[0].Events)
	}
	o := opts()
	o.Parallel = 4
	o.Repeats = 2
	o.ResetPolicy = runner.IncrementalPerEpisode
	o.NewAdapter = func(context.Context) (adapter.Adapter, error) {
		t.Fatal("one incremental episode must use one worker")
		return nil, nil
	}
	a := &writeSpy{spy: newSpy(), readOnly: true}
	r := run(t, s, a, o)
	if len(a.names) != 2 || len(a.queries) != 10 {
		t.Fatal("incremental state split", len(a.names), len(a.queries))
	}
	serial := o
	serial.Parallel = 1
	serial.NewAdapter = nil
	if orderedResult(t, r) != orderedResult(t, run(t, s, &writeSpy{spy: newSpy(), readOnly: true}, serial)) {
		t.Fatal("incremental single episode changed")
	}
}
