package runner_test

import (
	"encoding/json"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/result"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBEAMRoundedJudgeReportRecordReplay(t *testing.T) {
	s := answerInputBEAM(t)
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "rounded-reader-fixture")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "rounded-judge-fixture")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			json.NewEncoder(w).Encode(map[string]any{"model": "reader-fixture", "choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "blue box"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": map[string]any{"score": map[string]any{"type": "score", "score": 1.91, "probabilities": map[string]float64{"0": .02, "1": .04, "2": .93}, "confidence": .88, "legend": map[string]string{"0": "no", "1": "partial", "2": "yes"}}}, "usage": map[string]int{"input_tokens": 100, "output_tokens": 17}})
	}))
	defer server.Close()
	cfg := llm.DefaultConfig()
	cfg.BaseURL, cfg.CassetteDir, cfg.PricesPath = server.URL, t.TempDir(), ""
	var scores string
	for _, mode := range []string{"record", "replay"} {
		cfg.Mode, cfg.LedgerPath = mode, filepath.Join(t.TempDir(), "ledger.jsonl")
		engine, err := llm.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { engine.Close() })
		o := fixedOpts(engine)
		o.FixedReader.DecisionClient = &decision.Client{Engine: engine}
		r := run(t, s, newSpy(), o)
		row := r.Cases[0]
		if row.Status != result.Scored || row.QA == nil || !strings.Contains(strings.Join(row.QA.Deviations, " "), "rounding") {
			t.Fatal(row)
		}
		official := r.Scores.Official.(result.Official)
		if official.Score == nil || *official.Score != 1 || official.Errors != 0 || !strings.Contains(strings.Join(official.Deviations, " "), "rounding") {
			t.Fatal(official)
		}
		b, _ := json.Marshal(r.Scores)
		if mode == "record" {
			scores = string(b)
		} else {
			if scores != string(b) {
				t.Fatalf("replay scores drift:\n%s\n%s", scores, b)
			}
			if *engine.Billing().CostNew != 0 {
				t.Fatal(engine.Billing())
			}
		}
		server.Close()
	}
	if calls.Load() != 2 {
		t.Fatal("replay reached upstream", calls.Load())
	}
}
