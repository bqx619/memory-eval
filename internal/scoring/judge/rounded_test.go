package judge

import (
	"encoding/json"
	"fmt"
	"math"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge/inputlog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercise native responses through the real Engine, cassette, and judge router.
// These are probability shapes observed on the wire, without dataset content.
func TestRoundedDecisionsRecordReplay(t *testing.T) {
	for _, tc := range []struct {
		name               string
		probabilities      map[string]float64
		expectation, score float64
		choice             bool
	}{
		{"high", map[string]float64{"0": .02, "1": .04, "2": .93}, 1.91, 1, false},
		{"middle", map[string]float64{"0": .11, "1": .56, "2": .32}, 1.21, .5, false},
		{"rounded-up", map[string]float64{"0": .34, "1": .34, "2": .33}, .99, 0, false},
		{"tie-low", map[string]float64{"0": .33, "1": .33, "2": .33}, 1, 0, false},
		{"choice-field", map[string]float64{"0": .02, "1": .04, "2": .93}, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "rounded-fixture-key")
			q := decision.Question{Name: "value", Type: "score", Instructions: "Grade the response.", Criteria: []string{"low", "middle", "high"}, Levels: 3}
			a := map[string]any{"type": "score", "score": tc.expectation, "legend": map[string]string{"0": "low", "1": "middle", "2": "high"}, "probabilities": tc.probabilities, "confidence": .88}
			if tc.choice {
				q = decision.Question{Name: "value", Type: "choice", Instructions: "Select the label.", Options: []string{"0", "1", "2"}, Criteria: map[string]string{"0": "low", "1": "middle", "2": "high"}}
				a = map[string]any{"type": "choice", "choice": "1", "probabilities": tc.probabilities, "confidence": .88}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": map[string]any{"value": a}, "usage": map[string]int{"input_tokens": 100, "output_tokens": 17}})
			}))
			defer server.Close()
			cfg := llm.DefaultConfig()
			cfg.BaseURL, cfg.PricesPath, cfg.MaxRetries = server.URL, "", 0
			cfg.CassetteDir = filepath.Join(t.TempDir(), "cassettes")
			var recorded []byte
			for _, mode := range []string{"record", "replay"} {
				cfg.Mode = mode
				dir := t.TempDir()
				cfg.LedgerPath = filepath.Join(dir, "ledger.jsonl")
				engine, err := llm.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { engine.Close() })
				if err = engine.Start("rounding-fixture"); err != nil {
					t.Fatal(err)
				}
				log, err := inputlog.New(t.Context(), dir)
				if err != nil {
					t.Fatal(err)
				}
				ctx := inputlog.WithCase(inputlog.WithRecorder(t.Context(), log), "q", "ep", "cluster", 0)
				r := Router{Decision: &decision.Client{Engine: engine}, DecisionModel: "jev-1.14.0"}
				var outcome any
				if tc.choice {
					v, e := r.EvaluateChoices(ctx, "s", "halumem_question", "rounding-v1", "{}", []decision.Question{q})
					if e != nil {
						t.Fatal(e)
					}
					if v.Labels["value"] != "1" || !reflect.DeepEqual(v.Probabilities["value"], tc.probabilities) {
						t.Fatal(v)
					}
					outcome = v
				} else {
					v, e := r.EvaluateScore(ctx, "s", "beam_rubric", "rounding-v1", "{}", q)
					if e != nil {
						t.Fatal(e)
					}
					if v.Score != tc.score || v.Expectation == nil || math.Abs(*v.Expectation-tc.expectation/2) > 1e-12 || !reflect.DeepEqual(v.Probabilities, tc.probabilities) {
						t.Fatal(v)
					}
					outcome = v
				}
				b, _ := json.Marshal(outcome)
				if !strings.Contains(string(b), "rounding") {
					t.Fatalf("missing rounding disclosure: %s", b)
				}
				if mode == "record" {
					recorded = b
				} else if string(recorded) != string(b) {
					t.Fatalf("replay changed outcome: %s / %s", recorded, b)
				}
				if err := log.Close(); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"ledger.jsonl", inputlog.Filename} {
					b, e := os.ReadFile(filepath.Join(dir, name))
					if e != nil || !strings.Contains(string(b), "rounding") {
						t.Fatalf("%s: %s %v", name, b, e)
					}
				}
				if mode == "replay" && (engine.Billing().CostNew == nil || *engine.Billing().CostNew != 0) {
					t.Fatal("replay incurred cost", engine.Billing())
				}
				server.Close()
			}
			if calls.Load() != 1 {
				t.Fatal(fmt.Sprint("unexpected HTTP calls: ", calls.Load()))
			}
		})
	}
}
