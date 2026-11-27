package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"memeval/internal/benchmark"
	"memeval/internal/llm"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var f18Source = flag.String("f18-source-report", "", "opt-in: copied historical PrefEval MCQ report.json in scratch")
var f18Cassettes = flag.String("f18-cassettes", "", "opt-in: copied historical cassette directory in scratch")
var f18Output = flag.String("f18-out", "", "NEW scratch evidence directory for the historical 1000-case replay")

func f18Config(t *testing.T, dir string) string {
	t.Helper()
	for _, env := range os.Environ() {
		k, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(k, "MEMEVAL_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	p := filepath.Join(dir, "empty-config")
	if err := os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func f18Run(t *testing.T, args []string, dest string) *result.Report {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), append(append([]string{}, args...), "--out", dest), &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	r, err := report.Load(t.Context(), filepath.Join(dest, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReaderBudgetCLI(t *testing.T) {
	testoracle.ProtocolSources(t, "prefeval")
	t.Chdir(benchmark.Root())
	dir := t.TempDir()
	env := f18Config(t, dir)
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "f18-fixture")
	c := suite.Case{ID: "private-question", Kind: "mcq", Query: "Which option?", User: "user", Category: "fixture", Cutoff: 1, Options: map[string]string{"A": "one", "B": "two", "C": "three", "D": "four"}, Gold: suite.Gold{Choice: "A", MCQ: &suite.MCQSpec{Parse: "prefeval_choice_tag"}}}
	empty := c
	empty.ID, empty.Query = "private-empty-question", "Empty fixture?"
	s := &suite.Suite{SchemaVersion: "3.0", SuiteID: "f18-fixture", ProtocolRef: "protocols/prefeval.json#explicit-mcq", Provenance: map[string]any{"source_url": "fixture://synthetic"}, Episodes: []suite.Episode{{ID: "private-episode", Events: []suite.Event{{SourceID: "private-source", Owner: "user", Role: "user", Content: "one is preferred"}}, Cases: []suite.Case{c, empty}}}}
	path := filepath.Join(dir, "suite")
	if err := suite.Pack(t.Context(), s, path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model, minimum string
		want           int
	}{{"gpt-6-luna", "", 128}, {"gpt-6.1-sol", "256", 256}, {"fixture-nonreasoning", "", 5}} {
		t.Run(tc.model, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["max_completion_tokens"] != float64(tc.want) {
					t.Errorf("wire budget: %v", body["max_completion_tokens"])
				}
				text, finish := "<choice>A</choice>", "stop"
				messages, _ := json.Marshal(body["messages"])
				if strings.Contains(string(messages), "Empty fixture?") {
					text, finish = "", "length"
				}
				json.NewEncoder(w).Encode(map[string]any{"model": tc.model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 6, "completion_tokens_details": map[string]int{"reasoning_tokens": 2}}})
			}))
			defer server.Close()
			args := []string{"--env-file", env, "run", "--suite", path, "--sut", "bm25", "--mode", "fixed_reader", "--reader-model", tc.model, "--reader-provider", "openai-compat", "--llm-base-url", server.URL + "/v1", "--cassette-dir", filepath.Join(dir, tc.model, "cassettes"), "--run-seed", strings.Repeat("42", 32)}
			if tc.minimum != "" {
				args = append(args, "--reader-min-tokens-reasoning", tc.minimum)
			}
			for _, mode := range []string{"record-once", "replay"} {
				if mode == "replay" {
					server.Close()
				}
				r := f18Run(t, append(append([]string{}, args...), "--llm", mode), filepath.Join(dir, tc.model, mode))
				m := r.Reader.(map[string]any)
				off := r.Scores.Official.(map[string]any)
				if m["official_max_tokens"] != float64(5) || m["effective_max_tokens"] != float64(tc.want) || r.Cases[0].Status != result.Scored || off["score"] != float64(1) {
					t.Fatal(m, off, r.Cases)
				}
				if len(r.Cases) != 2 || r.Cases[1].Status != result.Invalid || r.Summary.Invalid != 1 || off["question_set"].(map[string]any)["evaluated"] != float64(1) || !strings.Contains(strings.Join(r.Warnings, " "), "50.00%") {
					t.Fatal("invalid diluted valid denominator", r.Summary, off)
				}
				b, _ := json.Marshal(off["deviations"])
				if strings.Contains(string(b), "reasoning budget raised") != (tc.want > 5) {
					t.Fatal(string(b))
				}
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}

func TestF18HistoricalPrefEvalReplay(t *testing.T) {
	if *f18Source == "" {
		t.Skip("opt-in copied historical cassettes")
	}
	if *f18Cassettes == "" || *f18Output == "" {
		t.Fatal("require f18-cassettes and f18-out")
	}
	root := benchmark.Root()
	t.Chdir(root)
	for _, p := range []string{*f18Source, *f18Cassettes, *f18Output} {
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatal("historical verification only accepts scratch paths")
		}
	}
	if _, err := os.Stat(*f18Output); !os.IsNotExist(err) {
		t.Fatal("output must not exist")
	}
	if err := os.MkdirAll(*f18Output, 0700); err != nil {
		t.Fatal(err)
	}
	env := f18Config(t, *f18Output)
	b, err := os.ReadFile(*f18Source)
	if err != nil {
		t.Fatal(err)
	}
	var prior result.Report
	if err = json.Unmarshal(b, &prior); err != nil {
		t.Fatal(err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	var dials atomic.Int64
	tr.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("F18 offline: HTTP forbidden")
	}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old; tr.CloseIdleConnections() })
	args := []string{"--env-file", env, "run", "--suite", testoracle.Dataset(t, filepath.Join("normalized", prior.SuiteID)), "--sut", "bm25", "--mode", "fixed_reader", "--llm", "replay", "--cassette-dir", *f18Cassettes, "--run-seed", prior.RunSeed, "--reader-model", "gpt-6-luna", "--reader-provider", "openai-compat", "--reader-input", "sources", "--reader-max-tokens", "5", "--reader-min-tokens-reasoning", "0", "--top-k", "10", "--max-context-chars", "100000", "--batch-size", "64", "--repeats", "1", "--llm-retries", "0", "--trace-prompts=false"}
	r := f18Run(t, args, filepath.Join(*f18Output, "run"))
	if len(r.Cases) != 1000 || r.SuiteSHA256 != prior.SuiteSHA256 || r.Summary.Invalid != 1000 || r.Summary.ReaderEmptyAnswers != 1000 || r.Summary.ExecutionErrors != 0 || r.Summary.StatusCounts[result.Scored] != 0 {
		t.Fatal(r.Summary)
	}
	for _, c := range r.Cases {
		if c.Status != result.Invalid || c.Passed != nil || len(c.Metrics) != 0 || c.QA == nil || c.QA.Hypothesis == nil || *c.QA.Hypothesis != "" || c.QA.ReaderOutput.FinishReason != "length" {
			t.Fatal("historical empty response not invalid", c.CaseID)
		}
	}
	off := r.Scores.Official.(map[string]any)
	if off["score"] != nil || off["question_set"].(map[string]any)["evaluated"] != float64(0) || off["metrics"].(map[string]any)["mcq.correct"] != nil {
		t.Fatal(off)
	}
	ledger, err := os.ReadFile(filepath.Join(*f18Output, "run/llm_ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSpace(ledger), []byte{'\n'})
	reasoning := 0
	completion := 0
	for _, b := range rows {
		var e llm.LedgerEntry
		if err = json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		if !e.CassetteHit || e.Error != "" || e.CostNew == nil || *e.CostNew != 0 {
			t.Fatal(e)
		}
		reasoning += e.ReasoningTokens
		completion += e.CompletionTokens
	}
	if len(rows) != 1000 || dials.Load() != 0 || completion != 5000 {
		t.Fatal(len(rows), dials.Load(), completion)
	}
	evidence := map[string]any{"cases": 1000, "invalid": r.Summary.Invalid, "scored": r.Summary.StatusCounts[result.Scored], "official_score": off["score"], "official_evaluated": 0, "cassette_hits": len(rows), "http_dials": dials.Load(), "cost_new": 0, "completion_tokens": completion, "reasoning_tokens": reasoning, "reader": r.Reader, "warnings": r.Warnings, "command": append(args, "--out", filepath.Join(*f18Output, "run"))}
	if err = report.WriteJSON(t.Context(), filepath.Join(*f18Output, "evidence.json"), evidence); err != nil {
		t.Fatal(err)
	}
	t.Logf("1000 invalid, 0 scored; official score=null/evaluated=0; cassette_hits=1000; HTTP=0; new cost=0; completion=%d reasoning=%d; %s", completion, reasoning, *f18Output)
}
