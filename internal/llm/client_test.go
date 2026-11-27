package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func request() Request {
	temp, tokens := 0., 10
	return Request{Purpose: "judge", Provider: "openai-compat", Model: "fixture", Messages: []Message{{Role: "user", Content: "hello"}}, Temperature: &temp, MaxTokens: &tokens, N: 1, SampleID: "anonymous-sample", TemplateID: "fixture-v1"}
}
func config(t *testing.T, base string) Config {
	t.Helper()
	c := DefaultConfig()
	c.BaseURL = base
	c.CassetteDir = t.TempDir()
	c.LedgerPath = filepath.Join(t.TempDir(), "ledger.jsonl")
	c.RetryDelay = time.Millisecond
	c.PricesPath = filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(c.PricesPath, []byte(`{"openai-compat/fixture":{"prompt_per_million":1,"completion_per_million":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return c
}
func engine(t *testing.T, c Config) *Engine {
	t.Helper()
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Start("fixture-run"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
func reply(w http.ResponseWriter) {
	fmt.Fprint(w, `{"choices":[{"message":{"content":"yes"},"finish_reason":"stop"}],"model":"fixture-returned","system_fingerprint":"fp_fixture","usage":{"prompt_tokens":10,"completion_tokens":2}}`)
}
func TestModesAndOfflineReplay(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "evaluator-secret")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer evaluator-secret" {
			t.Error("HTTP contract")
		}
		reply(w)
	}))
	defer server.Close()
	c := config(t, server.URL+"/v1")
	ctx := context.Background()
	req := request()
	c.Mode = "live"
	live := engine(t, c)
	if _, err := live.Complete(ctx, req); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(c.CassetteDir, "*/*/*.*"))
	if len(files) != 0 {
		t.Fatal("live wrote cassette")
	}
	c.Mode = "record"
	record := engine(t, c)
	a, err := record.Complete(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if record.Billing().Calls != 1 || *record.Billing().CostNew != .000014 {
		t.Fatal(record.Billing())
	}
	c.Mode = "record-once"
	once := engine(t, c)
	b, err := once.Complete(ctx, req)
	if err != nil || !b.CassetteHit || calls.Load() != 2 {
		t.Fatal(b, err, calls.Load())
	}
	req.SampleID = "another-anonymous-sample"
	if _, err = once.Complete(ctx, req); err != nil || calls.Load() != 3 {
		t.Fatal(err)
	}
	server.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	c.Mode = "replay"
	replay := engine(t, c)
	b, err = replay.Complete(ctx, request())
	if err != nil || !b.CassetteHit {
		t.Fatal(b, err)
	}
	b.CassetteHit = false
	if !reflect.DeepEqual(a, b) {
		t.Fatal("response not reproduced")
	}
	bill := replay.Billing()
	if bill.CassetteHits != 1 || bill.Calls != 1 || *bill.CostNew != 0 || *bill.CostOriginal != .000014 || calls.Load() != 3 {
		t.Fatal(bill)
	}
	req.SampleID = "miss"
	if _, err = replay.Complete(ctx, req); err == nil || err.Error() != "CassetteMiss" {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(c.LedgerPath)
	if err != nil || strings.Contains(string(bytes), "evaluator-secret") {
		t.Fatal("key leaked")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(bytes)), "\n") {
		var row LedgerEntry
		if json.Unmarshal([]byte(line), &row) != nil || row.RunID != "fixture-run" {
			t.Fatal(line)
		}
	}
}
func TestCassetteKeyDimensions(t *testing.T) {
	e := engine(t, config(t, "http://127.0.0.1:1/v1"))
	a := request()
	original, _, _ := e.key(a)
	changes := []func(*Request){func(r *Request) { r.Model = "other" }, func(r *Request) { r.Provider = "anthropic" }, func(r *Request) { r.TemplateID = "v2" }, func(r *Request) { r.SampleID = "sample2" }, func(r *Request) { r.Messages = []Message{{Role: "user", Content: "different"}} }, func(r *Request) { x := 1.; r.Temperature = &x }, func(r *Request) { x := 20; r.MaxTokens = &x }, func(r *Request) { r.N = 2 }, func(r *Request) { r.Extra = map[string]any{"seed": 2} }, func(r *Request) { r.Purpose = "reader" }}
	for i, change := range changes {
		r := a
		change(&r)
		h, _, err := e.key(r)
		if err != nil || h == original {
			t.Fatalf("dimension %d: %v", i, err)
		}
	}
	e.cfg.BaseURL = "http://127.0.0.1:2/v1"
	h, _, _ := e.key(a)
	if h == original {
		t.Fatal("endpoint missing")
	}
	a.Extra = map[string]any{"b": 1, "a": 2}
	h, _, _ = e.key(a)
	a.Extra = map[string]any{"a": 2, "b": 1}
	h2, _, _ := e.key(a)
	if h != h2 {
		t.Fatal("unstable normalization")
	}
}
func TestRetriesLedgerAndLimit(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "secret")
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n <= 2 {
			w.WriteHeader(429)
			fmt.Fprint(w, "secret upstream text")
			return
		}
		reply(w)
	}))
	defer server.Close()
	c := config(t, server.URL)
	c.Mode = "record"
	e := engine(t, c)
	if _, err := e.Complete(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	entries := e.Entries()
	if len(entries) != 3 || entries[0].Error != "LLMHTTPError (HTTP 429)" || entries[2].Error != "" {
		t.Fatal(entries)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(503) })
	c.Mode = "live"
	c.MaxRetries = 5
	e2 := engine(t, c)
	before := count.Load()
	if _, err := e2.Complete(context.Background(), request()); err == nil {
		t.Fatal("missing error")
	}
	if count.Load()-before != 6 || e2.Billing().Calls != 6 {
		t.Fatal("retry limit")
	}
}
func TestAnthropicAdaptiveEffortAndUsage(t *testing.T) {
	t.Setenv("MEMEVAL_ANTHROPIC_API_KEY", "anthropic-private")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-private" {
			t.Error("anthropic headers/path")
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if _, ok := b["temperature"]; ok {
			t.Error("Anthropic temperature must be omitted")
		}
		if b["system"] != "system instruction" || b["output_config"].(map[string]any)["effort"] != "low" || b["thinking"].(map[string]any)["type"] != "adaptive" {
			t.Error(b)
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"yes"}],"stop_reason":"end_turn","model":"claude-fixture","usage":{"input_tokens":11,"output_tokens":3}}`)
	}))
	defer server.Close()
	c := config(t, server.URL)
	c.Mode = "record"
	e := engine(t, c)
	r := request()
	r.Provider = "anthropic"
	r.Messages = append([]Message{{Role: "system", Content: "system instruction"}}, r.Messages...)
	response, err := e.Complete(context.Background(), r)
	if err != nil || response.Usage.PromptTokens != 11 || len(response.Deviations) != 1 || calls.Load() != 1 {
		t.Fatal(response, err)
	}
	if e.Billing().CostNew != nil {
		t.Fatal("unknown prices must be null")
	}
	server.Close()
	c.Mode = "replay"
	t.Setenv("MEMEVAL_ANTHROPIC_API_KEY", "")
	replay := engine(t, c)
	rr, err := replay.Complete(context.Background(), r)
	if err != nil || len(rr.Deviations) != 1 || *replay.Billing().CostNew != 0 || calls.Load() != 1 {
		t.Fatal(rr, err)
	}
	c.AnthropicEffort = "high"
	changed := engine(t, c)
	if _, err := changed.Complete(context.Background(), r); err == nil || err.Error() != "CassetteMiss" {
		t.Fatal("effort not included in cassette key", err)
	}
}
func TestParallelCapAndRecordOnceSingleFlight(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "secret")
	var active, peak, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		reply(w)
	}))
	defer server.Close()
	c := config(t, server.URL)
	c.Mode = "record-once"
	c.Parallel = 2
	e := engine(t, c)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request()
			r.SampleID = fmt.Sprint("sample", i%4)
			if _, err := e.Complete(context.Background(), r); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if peak.Load() > 2 || calls.Load() != 4 || e.Billing().CassetteHits != 4 {
		t.Fatal(peak.Load(), calls.Load(), e.Billing())
	}
}
func TestKeysOnlyMEMEVALAndTimeout(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "must-not-use")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		reply(w)
	}))
	defer s.Close()
	c := config(t, s.URL)
	c.Mode = "live"
	c.Timeout = 10 * time.Millisecond
	e := engine(t, c)
	if _, err := e.Complete(context.Background(), request()); err == nil || err.Error() != "EvaluatorKeyMissing" || calls.Load() != 0 {
		t.Fatal(err)
	}
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "test-only")
	if _, err := e.Complete(context.Background(), request()); err == nil || err.Error() != "LLMTransportError" {
		t.Fatal(err)
	}
}
