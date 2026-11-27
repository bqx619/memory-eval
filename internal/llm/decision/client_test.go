package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"memeval/internal/llm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func questions() []Question {
	return []Question{
		{Name: "correct", Type: "noul", Instructions: "Does the state meet the criterion?"},
		{Name: "action", Type: "choice", Instructions: "Choose one.", Criteria: map[string]string{"keep": "retain", "drop": "discard"}, Options: []string{"keep", "drop"}},
		{Name: "quality", Type: "score", Instructions: "Grade quality.", Criteria: []string{"low", "medium", "high"}, Levels: 3},
	}
}
func answers(provider string) map[string]any {
	a := map[string]any{
		"correct": map[string]any{"type": "noul", "noul": true, "probabilities": map[string]float64{"true": .8, "false": .2}, "confidence": .65},
		"action":  map[string]any{"type": "choice", "choice": "keep", "probabilities": map[string]float64{"keep": .7, "drop": .3}, "confidence": .4},
		"quality": map[string]any{"type": "score", "score": 2, "probabilities": map[string]float64{"1": .2, "2": .6, "3": .2}, "confidence": .5},
	}
	if provider == "typesafe" {
		a["correct"] = map[string]any{"type": "noul", "noul": .8}
		a["quality"] = map[string]any{"type": "score", "score": 1.3, "legend": map[string]string{"0": "low", "1": "medium", "2": "high"}, "probabilities": map[string]float64{"0": .1, "1": .5, "2": .4}, "confidence": .5}
	}
	return a
}
func config(t *testing.T, url string) llm.Config {
	t.Helper()
	cfg := llm.DefaultConfig()
	cfg.Mode = "record"
	cfg.BaseURL = url
	cfg.CassetteDir = t.TempDir()
	cfg.LedgerPath = filepath.Join(t.TempDir(), "ledger.jsonl")
	cfg.PricesPath = ""
	cfg.RetryDelay = time.Millisecond
	return cfg
}
func client(t *testing.T, cfg llm.Config) *Client {
	t.Helper()
	engine, err := llm.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Start("decision-fixture"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	return &Client{Engine: engine}
}
func TestQuestionOrderAndHeadersInKey(t *testing.T) {
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "fixture")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"model": "resolved", "answers": answers("typesafe"), "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}})
	}))
	defer server.Close()
	cfg := config(t, server.URL)
	cfg.Mode = "record-once"
	c := client(t, cfg)
	req := DecisionRequest{Purpose: "decision", Provider: "typesafe", Model: "alias", State: "s", Questions: questions(), SampleID: "anon", TemplateID: "v1"}
	a, err := c.Decide(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Questions[0], req.Questions[2] = req.Questions[2], req.Questions[0]
	b, err := c.Decide(context.Background(), req)
	if err != nil || !b.CassetteHit || a.RequestSHA != b.RequestSHA || calls.Load() != 1 {
		t.Fatal("question names not normalized", err)
	}
	for i := range req.Questions {
		if req.Questions[i].Type == "choice" {
			req.Questions[i].Options = []string{"drop", "keep"}
		}
	}
	b, err = c.Decide(context.Background(), req)
	if err != nil || b.CassetteHit || a.RequestSHA == b.RequestSHA {
		t.Fatal("option order lost", err)
	}
	req.Headers = map[string]string{"TypeSafe-Danger-Zone-Test": "a"}
	h, err := c.Decide(context.Background(), req)
	if err != nil || h.RequestSHA == b.RequestSHA {
		t.Fatal("header omitted", err)
	}
	req.Headers = map[string]string{"typesafe-danger-zone-test": "a"}
	h2, err := c.Decide(context.Background(), req)
	if err != nil || h.RequestSHA != h2.RequestSHA || !h2.CassetteHit {
		t.Fatal("header case normalization", err)
	}
	req.Headers = map[string]string{"TypeSafe-Danger-Zone-Test": "b"}
	h3, err := c.Decide(context.Background(), req)
	if err != nil || h3.RequestSHA == h.RequestSHA {
		t.Fatal("header value omitted", err)
	}
	req.SampleID = "repeat-1"
	last, err := c.Decide(context.Background(), req)
	if err != nil || last.CassetteHit || last.RequestSHA == h3.RequestSHA {
		t.Fatal("repeat key", err)
	}
}
func TestValidationAndCalibration(t *testing.T) {
	for _, header := range []string{"Authorization", "X-API-Key", "Cookie", "TypeSafe-Danger-Zone-", "TypeSafe-Danger-Zone-\nBad"} {
		_, err := Normalize(DecisionRequest{Purpose: "decision", Provider: "typesafe", Model: "m", State: "s", Questions: questions(), SampleID: "anon", TemplateID: "v1", Headers: map[string]string{header: "x"}})
		if err == nil {
			t.Fatal(header)
		}
	}
	cases := []struct {
		q    Question
		a    Answer
		want float64
	}{{Question{Type: "noul"}, Answer{Noul: new(.5)}, 0}, {Question{Type: "noul"}, Answer{Noul: new(0.)}, 1}, {Question{Type: "noul"}, Answer{Noul: new(.8)}, .6}, {Question{Type: "choice", Options: []string{"a", "b", "c"}}, Answer{Probabilities: map[string]float64{"a": 1. / 3, "b": 1. / 3, "c": 1. / 3}}, 0}, {Question{Type: "choice", Options: []string{"a", "b", "c"}}, Answer{Probabilities: map[string]float64{"a": 1, "b": 0, "c": 0}}, 1}}
	for _, tc := range cases {
		v, ok := CalibratedConfidence(tc.q, tc.a)
		if !ok || math.Abs(v-tc.want) > 1e-12 {
			t.Fatal(v, tc.want)
		}
	}
	if _, ok := CalibratedConfidence(Question{Type: "score"}, Answer{}); ok {
		t.Fatal("invented score calibration")
	}
	for _, change := range []func(*DecisionRequest){func(r *DecisionRequest) { r.Questions = append(r.Questions, r.Questions[0]) }, func(r *DecisionRequest) { r.Questions[1].Options = make([]string, 256) }, func(r *DecisionRequest) { r.Questions[2].Levels = 11 }, func(r *DecisionRequest) { r.Questions[2].Levels = 1 }, func(r *DecisionRequest) { r.Questions[1].Criteria = nil }} {
		r := DecisionRequest{Purpose: "decision", Provider: "typesafe", Model: "m", Questions: questions(), SampleID: "anon", TemplateID: "v1"}
		change(&r)
		if _, err := Normalize(r); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
func TestRetryAndInvalidProbabilityAccounting(t *testing.T) {
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "fake")
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(529)
			fmt.Fprint(w, "do not log this body")
			return
		}
		a := answers("typesafe")
		a["correct"].(map[string]any)["noul"] = 1.2
		json.NewEncoder(w).Encode(map[string]any{"model": "resolved", "answers": a, "usage": map[string]int{"input_tokens": 1000, "output_tokens": 0}})
	}))
	defer server.Close()
	c := client(t, config(t, server.URL))
	_, err := c.Decide(context.Background(), DecisionRequest{Purpose: "decision", Provider: "typesafe", Model: "m", State: "s", Questions: questions(), SampleID: "anon", TemplateID: "v1"})
	if err == nil || err.Error() != "DecisionResponseInvalid" || count.Load() != 2 {
		t.Fatal(err, count.Load())
	}
	if c.Engine.Billing().PromptTokens != 1000 || math.Abs(*c.Engine.Billing().CostNew-.000042) > 1e-12 {
		t.Fatal("paid invalid output lost from ledger", c.Engine.Billing())
	}
}
