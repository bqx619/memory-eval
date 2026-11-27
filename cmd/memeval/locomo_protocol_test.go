package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"memeval/internal/config/dotenv"
	"memeval/internal/importers/locomo"
	"memeval/internal/llm"
	lr "memeval/internal/reader/locomo"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var f17Output = flag.String("f17-out", "", "preserve F17 synthetic record/replay evidence in a NEW directory")

func TestLoCoMoQuestionProtocolCLIRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "locomo")
	t.Chdir(dotenv.Root("."))
	out := t.TempDir()
	if *f17Output != "" {
		out = *f17Output
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("evidence output must not exist", out)
		}
		if err := os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range dotenv.Bindings {
		if strings.HasPrefix(b.Env, "MEMEVAL_") {
			t.Setenv(b.Env, "")
			os.Unsetenv(b.Env)
		}
	}
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "f17-local-fixture-only")
	envFile := filepath.Join(out, "empty-config")
	if err := os.WriteFile(envFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s, _, err := locomo.Import(t.Context(), "internal/importers/locomo/testdata/synthetic_locomo.json")
	if err != nil {
		t.Fatal(err)
	}
	for i := range s.Episodes {
		var cases []suite.Case
		for _, c := range s.Episodes[i].Cases {
			if c.QuestionType == "2" || c.QuestionType == "5" {
				cases = append(cases, c)
			}
		}
		s.Episodes[i].Cases = cases
	}
	path := filepath.Join(out, "synthetic-cat2-cat5")
	if err := suite.Pack(t.Context(), s, path); err != nil {
		t.Fatal(err)
	}
	var calls, temporal, adversarial atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var q struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
		}
		if q.Model != "f17-fixture-reader" || len(q.Messages) != 1 {
			t.Error("unexpected request", q.Model, len(q.Messages))
			http.Error(w, "bad request", 400)
			return
		}
		prompt := q.Messages[0].Content
		answer := " 3 "
		switch {
		case strings.Contains(prompt, "How many?"+lr.TemporalSuffix):
			temporal.Add(1)
		case strings.Contains(prompt, "Select the correct answer:"):
			adversarial.Add(1)
			if !strings.Contains(prompt, "PRIVATE SYNTHETIC SENTINEL") {
				t.Error("private distractor absent from reader prompt")
			}
			answer = " (B) "
			if strings.Contains(prompt, "(a) "+lr.NotMentioned) {
				answer = " (A) "
			} else if !strings.Contains(prompt, "(b) "+lr.NotMentioned) {
				t.Error("not mentioned choice missing")
			}
		default:
			t.Error("official suffix missing")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "model": q.Model, "choices": []any{map[string]any{"index": 0, "message": llm.Message{Role: "assistant", Content: answer}, "finish_reason": "stop"}}, "usage": llm.Usage{}})
	}))
	defer server.Close()
	args := []string{"--env-file", envFile, "run", "--suite", path, "--sut", "fullcontext", "--mode", "fixed_reader", "--reader-model", "f17-fixture-reader", "--reader-provider", "openai-compat", "--reader-input", "sources", "--reader-max-tokens", "512", "--max-context-chars", "1000000", "--run-seed", strings.Repeat("42", 32), "--llm-base-url", server.URL + "/v1", "--llm-retries", "0", "--cassette-dir", filepath.Join(out, "cassettes")}
	var runs []*result.Report
	for _, mode := range []string{"record-once", "replay"} {
		if mode == "replay" {
			server.Close()
		}
		dest := filepath.Join(out, mode)
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), append(append([]string{}, args...), "--llm", mode, "--out", dest), &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v %s", mode, err, stderr.String())
		}
		r, err := report.Load(t.Context(), filepath.Join(dest, "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Cases) != 4 || r.Summary.ExecutionErrors != 0 {
			t.Fatal(r.Summary)
		}
		for i := range r.Cases {
			c := &r.Cases[i]
			if c.Status != result.Scored || c.QA == nil || c.QA.LoCoMo == nil || !c.QA.LoCoMo.SuffixApplied || c.QA.LoCoMo.RawResponse == nil || *c.Metrics["locomo_official.overall"] != 1 {
				t.Fatal("reader/mapping/score", c)
			}
			if c.QA.QuestionType == "5" && (*c.QA.Hypothesis != lr.NotMentioned || c.QA.LoCoMo.MappedOption == "" || len(c.QA.LoCoMo.OptionOrder) != 2) {
				t.Fatal(c.QA)
			}
			c.Timings = nil
		}
		runs = append(runs, r)
	}
	if calls.Load() != 4 || temporal.Load() != 2 || adversarial.Load() != 2 {
		t.Fatal("unexpected HTTP", calls.Load(), temporal.Load(), adversarial.Load())
	}
	if !reflect.DeepEqual(runs[0].Cases, runs[1].Cases) || !reflect.DeepEqual(runs[0].Scores, runs[1].Scores) {
		t.Fatal("record/replay drift")
	}
	b, err := os.ReadFile(filepath.Join(out, "replay", "llm_ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte{'\n'})
	if len(lines) != 4 {
		t.Fatal("ledger size", len(lines))
	}
	for _, line := range lines {
		var row llm.LedgerEntry
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if !row.CassetteHit || row.Error != "" || row.Purpose != "reader" || row.CostNew == nil || *row.CostNew != 0 {
			t.Fatal("replay HTTP/cost", row)
		}
	}
	evidence := map[string]any{"cases": 4, "cat2": 2, "cat5": 2, "per_case_equal": true, "scores_equal": true, "record_http_calls": calls.Load(), "replay_http_calls": 0, "replay_cassette_hits": 4, "replay_cost_new": 0, "scores": runs[1].Scores, "note": "synthetic local httptest; server closed before replay"}
	if err := report.WriteJSON(t.Context(), filepath.Join(out, "evidence.json"), evidence); err != nil {
		t.Fatal(err)
	}
	t.Logf("F17 evidence: %s; cases 4/4 equal; HTTP record=4 replay=0; cost_new=0", out)
}
