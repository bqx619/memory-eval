package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"memeval/internal/benchmark"
	"memeval/internal/llm"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var memoraEvidence = flag.String("memora-evidence-dir", "", "optional NEW scratch directory for Memora offline acceptance")

func TestMemoraCLIRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "memora")
	root := benchmark.Root()
	dir := t.TempDir()
	if *memoraEvidence != "" {
		dir = *memoraEvidence
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("evidence directory must be new")
		}
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, env := range os.Environ() {
		k, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(k, "MEMEVAL_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "p5-fixture-key")
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "memora-test-key")
	t.Chdir(root)
	envfile := filepath.Join(dir, "empty-config")
	os.WriteFile(envfile, nil, 0600)
	s, e := suite.Open(t.Context(), testoracle.Dataset(t, "normalized/memora@a6493188efc8/weekly"))
	if e != nil {
		t.Fatal(e)
	}
	var ep *suite.Episode
	stop := errors.New("one persona")
	e = s.Iter(t.Context(), func(x *suite.Episode) error { ep = x; return stop })
	if !errors.Is(e, stop) {
		t.Fatal(e)
	}
	if ep.ID != "academic_researcher_weekly" || len(ep.Cases) != 15 {
		t.Fatal("one complete persona")
	}
	tiny := *ep
	tiny.Cases = append([]suite.Case{}, ep.Cases[:1]...)
	g := *tiny.Cases[0].Gold.Rubric
	g.Items = append([]suite.RubricItem{}, g.Items[0], g.Items[1], g.Items[5])
	tiny.Cases[0].Gold.Rubric = &g
	tinyPath := filepath.Join(dir, "three-subquestions")
	w, e := suite.NewWriter(tinyPath)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Abort()
	if e = w.WriteEpisode(&tiny); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(s); e != nil {
		t.Fatal(e)
	}
	gold := map[string]string{}
	for _, c := range ep.Cases {
		for _, item := range c.Gold.Rubric.Items {
			gold[item.Text] = "yes"
			if item.Polarity == "must_not_contain" {
				gold[item.Text] = "no"
			}
		}
	}
	// The three-item sample is [presence pass, presence fail, absence pass]: FAMA=0.5.
	gold[g.Items[1].Text] = "no"
	var calls, dials atomic.Int64
	old := http.DefaultTransport
	transport := old.(*http.Transport).Clone()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("non-loopback forbidden")
		}
		return dial(ctx, network, address)
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var b struct {
			State       string                     `json:"state"`
			Questions   map[string]json.RawMessage `json:"questions"`
			Model       string                     `json:"model"`
			Messages    []llm.Message              `json:"messages"`
			Temperature *float64                   `json:"temperature"`
			Tokens      *int                       `json:"max_completion_tokens"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
			return
		}
		if len(b.Questions) > 0 {
			var state map[string]string
			json.Unmarshal([]byte(b.State), &state)
			testdecision.Wire(w, b.Model, b.Questions, func(name, kind string) any {
				answer, ok := gold[state["evaluation_question"]]
				if !ok {
					t.Error("evaluation slot changed")
				}
				if answer == "yes" {
					return 1.
				}
				return 0.
			})
			return
		}
		last := b.Messages[len(b.Messages)-1].Content
		text := "simulated reader answer"
		if b.Model == "gpt-6-luna" {
			full := strings.Contains(last, "# Current Question")
			if b.Temperature == nil || *b.Temperature != .7 || (!full && (b.Tokens == nil || *b.Tokens != 500)) || full && b.Tokens != nil {
				t.Error("reader declared parameters")
			}
			if strings.Contains(last, "# Current Question") && !strings.Contains(b.Messages[0].Content, "Today's date is 2025-06-07") {
				t.Error("missing current date")
			}
		}

		json.NewEncoder(w).Encode(map[string]any{"model": b.Model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 20, CompletionTokens: 3}})
	}))
	defer srv.Close()
	type target struct {
		name, sut, path string
		limit           int
	}
	targets := []target{{"three", "bm25", tinyPath, 1}, {"bm25", "bm25", testoracle.Dataset(t, "normalized/memora@a6493188efc8/weekly"), 15}, {"fullcontext", "fullcontext", testoracle.Dataset(t, "normalized/memora@a6493188efc8/weekly"), 15}}
	execute := func(mode string, x target) *result.Report {
		dest := filepath.Join(dir, x.name+"-"+mode)
		args := []string{"--env-file", envfile, "run", "--suite", x.path, "--sut", x.sut, "--mode", "fixed_reader", "--reset-policy", "incremental_per_episode", "--limit", fmt.Sprint(x.limit), "--top-k", "50", "--reader-model", "gpt-6-luna", "--reader-provider", "openai-compat", "--llm", mode, "--llm-base-url", srv.URL + "/v1", "--cassette-dir", filepath.Join(dir, "cassettes-"+x.name), "--llm-retries", "0", "--run-seed", strings.Repeat("83", 32), "--out", dest}
		var stdout, stderr bytes.Buffer
		if e := runCommand(t.Context(), args, &stdout, &stderr); e != nil {
			t.Fatalf("%s: %v %s %s", x.name, e, stdout.String(), stderr.String())
		}
		b, e := os.ReadFile(filepath.Join(dest, "report.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r result.Report
		if e = json.Unmarshal(b, &r); e != nil {
			t.Fatal(e)
		}
		return &r
	}
	records := map[string]*result.Report{}
	counts := map[string]int64{}
	for _, x := range targets {
		before := calls.Load()
		records[x.name] = execute("record", x)
		counts[x.name] = calls.Load() - before
	}
	n, nd := calls.Load(), dials.Load()
	srv.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	proofs := map[string]any{}
	for _, x := range targets {
		a, b := records[x.name], execute("replay", x)
		if len(a.Cases) != x.limit || len(b.Cases) != x.limit {
			t.Fatal("case count")
		}
		phase := map[string]int{}
		subquestions := 0
		sums := map[string]float64{}
		by := map[string]map[string]float64{}
		byN := map[string]int{}
		for i, c := range a.Cases {
			d := b.Cases[i]
			for k := range c.Timings {
				phase[k]++
			}
			if c.Status != result.Scored || c.Metrics["memora.fama"] == nil {
				t.Fatal(c.CaseID, c.Status, c.ErrorType)
			}
			if c.EpisodeID != ep.ID {
				t.Fatal("wrong persona")
			}
			subquestions += len(c.QA.JudgeDetails)
			if x.name == "three" && (*c.Metrics["memora.mpa"] != .5 || *c.Metrics["memora.faa"] != 1 || *c.Metrics["memora.fama"] != .5 || *c.Metrics["memora.question_correct"] != 0) {
				t.Fatal("three-item hand score", c.Metrics)
			}
			if by[c.Category] == nil {
				by[c.Category] = map[string]float64{}
			}
			byN[c.Category]++
			for k, v := range c.Metrics {
				if strings.HasPrefix(k, "memora.") {
					if v == nil {
						t.Fatal("null score")
					}
					sums[k] += *v
					by[c.Category][k] += *v
				}
			}
			c.Timings = nil
			d.Timings = nil
			if !reflect.DeepEqual(c, d) {
				t.Fatalf("%s case %d replay mismatch", x.name, i)
			}
		}
		if phase["reset_ms"] != 1 || phase["ingest_ms"] != 1 || phase["ready_ms"] != 1 || phase["query_ms"] != x.limit {
			t.Fatal(phase)
		}
		bb, _ := json.Marshal(b.Scores.Official)
		var official result.Official
		json.Unmarshal(bb, &official)
		if official.Fidelity != "partial" {
			t.Fatal("fidelity")
		}
		for k, v := range sums {
			if official.Metrics[k] == nil || *official.Metrics[k] != v/float64(x.limit) {
				t.Fatal("mean", k)
			}
		}
		for cat, ms := range by {
			for k, v := range ms {
				if *official.ByTypeMetrics[cat][k] != v/float64(byN[cat]) {
					t.Fatal("category mean")
				}
			}
		}
		bb, _ = json.Marshal(b.Billing.Evaluator)
		var bill llm.Billing
		json.Unmarshal(bb, &bill)
		if bill.CostNew == nil || *bill.CostNew != 0 || bill.CassetteHits != int(counts[x.name]) {
			t.Fatal("replay billing", bill)
		}
		proofs[x.name] = map[string]any{"cases": x.limit, "subquestions": subquestions, "phase_counts": phase, "record_http": counts[x.name], "replay_http": 0, "replay_dials": 0, "new_usd": 0, "case_differences_excluding_timings": 0, "metrics": official.Metrics, "by_category": official.ByTypeMetrics}
	}
	if calls.Load() != n || dials.Load() != nd {
		t.Fatal("replay HTTP/dial")
	}
	bb, _ := json.MarshalIndent(proofs, "", "  ")
	os.WriteFile(filepath.Join(dir, "proof.json"), append(bb, '\n'), 0600)
	t.Logf("one complete weekly persona: bm25/fullcontext 15 cases each; three-item sample; %d record calls; replay 0 HTTP/dials/new USD, 0 case differences", n)
}
