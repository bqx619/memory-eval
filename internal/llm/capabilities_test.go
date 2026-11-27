package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCapabilitiesConcurrent500AndReplay(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-private-api-key")
	var calls, rejected, active, peak atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["max_completion_tokens"] != float64(10) || body["max_tokens"] != nil || body["n"] != float64(1) {
			t.Error("token/n contract")
		}
		if _, ok := body["temperature"]; ok {
			rejected.Add(1)
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"unsupported_value","param":"temperature"}}`)
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		reply(w)
	}))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "record"
	cfg.Parallel = 8
	e := engine(t, cfg)
	responses := make([]Response, 500)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request()
			r.SampleID = fmt.Sprint(i)
			resp, err := e.Complete(context.Background(), r)
			if err != nil {
				t.Error(err)
				return
			}
			responses[i] = resp
		}(i)
	}
	wg.Wait()
	if calls.Load() != 501 || rejected.Load() != 1 || peak.Load() < 2 {
		t.Fatal("cold probe or warm concurrency", calls.Load(), rejected.Load(), peak.Load())
	}
	errors := 0
	for _, row := range e.Entries() {
		if row.Error != "" {
			errors++
		}
		if len(row.Deviations) != 1 || row.Deviations[0] != "temperature unavailable; provider default" {
			t.Fatal("lost capability disclosure", row)
		}
	}
	if errors != 1 || len(e.Entries()) != 501 {
		t.Fatal("ledger must record the probe only once")
	}
	// The OpenAI-compatible reader cache is scoped to model and run.
	for _, r := range []Request{func() Request { r := request(); r.Model = "another-model"; return r }()} {
		before := calls.Load()
		if _, err := e.Complete(context.Background(), r); err != nil || calls.Load()-before != 2 {
			t.Fatal("cache scope", err)
		}
	}
	cfg.Mode = "live"
	fresh := engine(t, cfg)
	before := calls.Load()
	if _, err := fresh.Complete(context.Background(), request()); err != nil || calls.Load()-before != 2 {
		t.Fatal("cache leaked across runs", err)
	}
	s.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	cfg.Mode = "replay"
	replay := engine(t, cfg)
	before = calls.Load()
	for i, a := range responses {
		r := request()
		r.SampleID = fmt.Sprint(i)
		b, err := replay.Complete(context.Background(), r)
		if err != nil || !b.CassetteHit {
			t.Fatal(err)
		}
		b.CassetteHit = false
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("warm capability changed cassette key or replay %d\nA=%#v\nB=%#v", i, a, b)
		}
	}
	if calls.Load() != before || replay.Billing().CassetteHits != 500 || *replay.Billing().CostNew != 0 {
		t.Fatal("replay made a new call/charge")
	}
	t.Logf("500 calls: HTTP=501, rejected probes=1, warm concurrency peak=%d; replay=500 hits, HTTP=0, new cost=0; model/run isolation passed", peak.Load())
}

func TestRunCapabilitiesLegacyTokensAndTemperature(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-private-api-key")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		param, code := "", ""
		if body["max_completion_tokens"] != nil {
			param, code = "max_completion_tokens", "unsupported_parameter"
		} else if _, ok := body["temperature"]; ok {
			param, code = "temperature", "unsupported_value"
		}
		if param != "" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"param": param, "code": code}})
			return
		}
		if body["max_tokens"] != float64(10) {
			t.Error("cached legacy limit missing")
		}
		reply(w)
	}))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "live"
	cfg.MaxRetries = 0
	e := engine(t, cfg)
	for i := range 10 {
		r := request()
		r.SampleID = fmt.Sprint(i)
		response, err := e.Complete(context.Background(), r)
		if err != nil || len(response.Deviations) != 2 {
			t.Fatal(response, err)
		}
	}
	if calls.Load() != 12 {
		t.Fatal("each unsupported field must be probed once", calls.Load())
	}
	t.Log("10 legacy calls: HTTP=12, one token probe and one temperature probe")
}

func TestRunCapabilityProbeWaitCancellation(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-private-api-key")
	entered, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; reply(w) }))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "live"
	e := engine(t, cfg)
	done := make(chan error, 1)
	go func() { _, err := e.Complete(context.Background(), request()); done <- err }()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := request()
	r.SampleID = "waiting"
	if _, err := e.Complete(ctx, r); err == nil || !strings.Contains(err.Error(), "Canceled") {
		t.Fatal("probe wait must respect cancellation", err)
	}
	// The server is released by defer; engine cleanup happens after the test body.
}

func TestAnthropicPrefillFallbackCacheReplay(t *testing.T) {
	t.Setenv("MEMEVAL_ANTHROPIC_API_KEY", "fixture-private-anthropic")
	var calls, rejected atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Messages[len(body.Messages)-1].Role == "assistant" {
			rejected.Add(1)
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Assistant prefill is not supported by this model"}}`)
			return
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"<choice>B</choice>"}],"usage":{"input_tokens":2,"output_tokens":3}}`)
	}))
	defer srv.Close()
	cfg := config(t, srv.URL)
	cfg.Mode = "record"
	e := engine(t, cfg)
	rs := []Request{}
	responses := []Response{}
	for i := 0; i < 2; i++ {
		r := request()
		r.Provider = "anthropic"
		r.Model = "test-prefill-model"
		r.SampleID = fmt.Sprint(i)
		r.Messages = []Message{{Role: "user", Content: "Q"}, {Role: "assistant", Content: "<choice>"}}
		rs = append(rs, r)
		v, err := e.Complete(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(v.Deviations, " "), AssistantPrefillOmitted) {
			t.Fatal(v)
		}
		responses = append(responses, v)
	}
	if calls.Load() != 3 || rejected.Load() != 1 {
		t.Fatal(calls.Load(), rejected.Load())
	}
	srv.Close()
	t.Setenv("MEMEVAL_ANTHROPIC_API_KEY", "")
	cfg.Mode = "replay"
	replay := engine(t, cfg)
	for i, r := range rs {
		v, err := replay.Complete(t.Context(), r)
		if err != nil || !v.CassetteHit || v.Text != responses[i].Text || !reflect.DeepEqual(v.Deviations, responses[i].Deviations) {
			t.Fatal(v, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("replay HTTP")
	}
}
