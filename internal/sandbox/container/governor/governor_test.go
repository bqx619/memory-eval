package governor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	delays []time.Duration
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delays = append(c.delays, d)
	c.now = c.now.Add(d)
	return nil
}
func (c *fakeClock) hooks() Hooks {
	return Hooks{Now: c.Now, Sleep: c.Sleep, Jitter: func(d time.Duration) time.Duration { return d }}
}
func proxy(t *testing.T, up http.Handler, cfg func(*Config), h Hooks) *Server {
	t.Helper()
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	c := Defaults()
	c.Target = strings.TrimPrefix(upstream.URL, "http://")
	c.MeterDir = t.TempDir()
	if cfg != nil {
		cfg(&c)
	}
	s, e := New(c, h)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return s
}

const usageJSON = `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":7}}}`

func call(s *Server, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.RemoteAddr = "172.20.0.2:1234"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestRetriesMeterAndHeaders(t *testing.T) {
	for _, mode := range []string{"429-seconds", "429-date", "503", "502", "504", "disconnect", "EOF"} {
		t.Run(mode, func(t *testing.T) {
			clock := &fakeClock{now: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
			n := 0
			s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				if r.Header.Get("Authorization") != "Bearer secret-key" {
					t.Error("auth not forwarded")
				}
				b, _ := io.ReadAll(r.Body)
				if string(b) != `{"model":"fixture"}` {
					t.Error(string(b))
				}
				if n <= 2 {
					switch mode {
					case "disconnect":
						h, _, _ := w.(http.Hijacker).Hijack()
						h.Close()
						return
					case "EOF":
						w.Header().Set("Content-Length", "99")
						w.Write([]byte("short"))
						return
					case "429-seconds":
						w.Header().Set("Retry-After", "3")
						w.WriteHeader(429)
					case "429-date":
						w.Header().Set("Retry-After", clock.Now().Add(4*time.Second).Format(http.TimeFormat))
						w.WriteHeader(429)
					default:
						var code int
						fmt.Sscanf(mode, "%d", &code)
						w.WriteHeader(code)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(usageJSON))
			}), nil, clock.hooks())
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fixture"}`))
			r.Header.Set("Authorization", "Bearer secret-key")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 200 || w.Body.String() != usageJSON || n != 3 {
				t.Fatal(w.Code, w.Body.String(), n)
			}
			st := s.Snapshot()
			if st.Billing.Calls != 3 || st.Billing.Retries != 2 || st.Billing.Prompt != 100 || st.Billing.Cached != 40 || st.Billing.Reasoning != 7 {
				t.Fatal(st)
			}
			want := []time.Duration{time.Second, 2 * time.Second}
			if mode == "429-seconds" {
				want = []time.Duration{3 * time.Second, 3 * time.Second}
			}
			if mode == "429-date" {
				want = []time.Duration{4 * time.Second, 4 * time.Second}
			}
			if !reflect.DeepEqual(clock.delays, want) {
				t.Fatal(clock.delays, want)
			}
			raw, e := os.ReadFile(filepath.Join(s.cfg.MeterDir, MeterFile))
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(raw, []byte("secret-key")) {
				t.Fatal("credential leaked")
			}
			rows, e := ReadAttempts(bytes.NewReader(raw))
			if e != nil || len(rows) != 3 || rows[2].Attempt != 3 {
				t.Fatal(rows, e)
			}
			for _, p := range []string{s.cfg.MeterDir, filepath.Join(s.cfg.MeterDir, MeterFile)} {
				i, _ := os.Stat(p)
				want := os.FileMode(0600)
				if i.IsDir() {
					want = 0700
				}
				if i.Mode().Perm() != want {
					t.Fatal(i.Mode())
				}
			}
		})
	}
}
func TestStreamingUnchangedAndInjection(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\ndata: " + usageJSON + "\n\ndata: [DONE]\n\n"
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		if v["stream_options"].(map[string]any)["include_usage"] != true || v["stream_options"].(map[string]any)["keep"] != true {
			t.Error(v)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, b := range []byte(stream) {
			w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}), nil, Hooks{})
	w := call(s, `{"stream":true,"stream_options":{"keep":true}}`)
	if w.Body.String() != stream || !w.Flushed {
		t.Fatal(w.Body.String())
	}
	if s.Snapshot().Billing.Prompt != 100 {
		t.Fatal(s.Snapshot())
	}
}
func TestConcurrencyLimit(t *testing.T) {
	var active, peak atomic.Int64
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		w.Write([]byte(usageJSON))
	}), func(c *Config) { c.Concurrency = 3 }, Hooks{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call(s, `{}`).Code != 200 {
				t.Error("failed")
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 3 || s.Snapshot().Billing.Calls != 20 {
		t.Fatal(peak.Load(), s.Snapshot())
	}
}
func TestTokenBucket(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(usageJSON)) }), func(c *Config) { c.RPM = 2 }, clock.hooks())
	for i := 0; i < 4; i++ {
		call(s, `{}`)
	}
	if !reflect.DeepEqual(clock.delays, []time.Duration{30 * time.Second, 30 * time.Second}) {
		t.Fatal(clock.delays)
	}
}
func TestCircuitAndPause(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }), func(c *Config) { c.MaxPause = 2 * time.Minute }, clock.hooks())
	w := call(s, `{}`)
	if w.Code != 503 || w.Header().Get("X-Memeval-Governor") != "exhausted" {
		t.Fatal(w)
	}
	st := s.Snapshot()
	if st.Billing.Calls != 6 || st.Billing.CircuitOpens != 2 || st.State != "exhausted" {
		t.Fatal(st)
	}
	cools := []float64{}
	for _, e := range st.Events {
		if e.State == "open" {
			cools = append(cools, e.Cooldown)
		}
	}
	if !reflect.DeepEqual(cools, []float64{60, 120}) {
		t.Fatal(cools)
	}
	before := st.Billing.Calls
	call(s, `{}`)
	if s.Snapshot().Billing.Calls != before {
		t.Fatal("forward after exhausted")
	}
}
func TestHalfOpenSuccessAndLongRetryAfter(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	n := 0
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "75")
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(usageJSON))
	}), nil, clock.hooks())
	if call(s, `{}`).Code != 200 {
		t.Fatal("failed")
	}
	st := s.Snapshot()
	if st.State != "closed" || st.Billing.CircuitOpens != 1 || !reflect.DeepEqual(clock.delays, []time.Duration{75 * time.Second}) {
		t.Fatal(st, clock.delays)
	}
}
func TestBudgetAndUnmetered(t *testing.T) {
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(usageJSON)) }), func(c *Config) { c.BudgetTokens = 120 }, Hooks{})
	if call(s, `{}`).Code != 200 {
		t.Fatal("budget crossing response lost")
	}
	w := call(s, `{}`)
	if w.Code != 503 || w.Header().Get("X-Memeval-Governor") != "budget" || s.Snapshot().Billing.Calls != 1 {
		t.Fatal(w, s.Snapshot())
	}
	s2 := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not JSON")) }), nil, Hooks{})
	if call(s2, `{}`).Body.String() != "not JSON" || s2.Snapshot().Billing.Unmetered != 1 {
		t.Fatal(s2.Snapshot())
	}
}
func TestBoundariesTimeoutAndNoLargeRetry(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(503) }), nil, clock.hooks())
	w := call(s, strings.Repeat("x", BodyLimit+1))
	if w.Code != 503 || w.Header().Get("X-Memeval-Governor") != "" || s.Snapshot().Billing.Calls != 1 {
		t.Fatal(w.Code, s.Snapshot())
	}
	for _, url := range []string{"http://elsewhere:80/x", "/memeval/status"} {
		r := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 && w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("CONNECT", "http://"+s.cfg.Target, nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	slow := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }), func(c *Config) { c.UpstreamTimeout = 5 * time.Millisecond; c.MaxPause = time.Millisecond }, clock.hooks())
	w = call(slow, `{}`)
	if w.Header().Get("X-Memeval-Governor") != "exhausted" {
		t.Fatal(w)
	}
}
func TestHalfOpenSingleProbe(t *testing.T) {
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(usageJSON)) }), nil, Hooks{})
	s.mu.Lock()
	s.state = "half_open"
	s.mu.Unlock()
	if _, _, e := s.acquire(t.Context()); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		_, _, e := s.acquire(t.Context())
		if e != nil {
			t.Error(e)
		}
		close(done)
		<-s.sem
	}()
	select {
	case <-done:
		t.Fatal("two half-open probes")
	case <-time.After(20 * time.Millisecond):
	}
	s.record(Attempt{}, false, 0)
	<-s.sem
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued probe stuck")
	}
}

func TestResumeBudgetAndCooldownCap(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(usageJSON)) }), func(c *Config) { c.ConsumedTokens = 100; c.BudgetTokens = 220 }, clock.hooks())
	if call(s, `{}`).Code != 200 || s.Snapshot().State != "budget" {
		t.Fatal(s.Snapshot())
	}
	s2 := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil, clock.hooks())
	s2.mu.Lock()
	s2.state = "half_open"
	s2.cooldown = 480 * time.Second
	s2.mu.Unlock()
	s2.record(Attempt{}, true, 0)
	st := s2.Snapshot()
	if st.Events[len(st.Events)-1].Cooldown != 600 {
		t.Fatal(st)
	}
}
func TestPartialStreamEOFIsNotReplayed(t *testing.T) {
	var calls atomic.Int64
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "999")
		w.Write([]byte("data: {\"choices\":[]}\n\n"))
		w.(http.Flusher).Flush()
	}), nil, Hooks{})
	w := call(s, `{"stream":true}`)
	if calls.Load() != 1 || !strings.Contains(w.Body.String(), "choices") || s.Snapshot().Billing.Unmetered != 1 {
		t.Fatal(calls.Load(), s.Snapshot())
	}
}
func TestAttributionWithReusedIP(t *testing.T) {
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	rows := []Attempt{{Time: at.Format(time.RFC3339Nano), EpisodeIP: "1", Attempt: 1, Usage: &Usage{Prompt: 2}}, {Time: at.Add(time.Hour).Format(time.RFC3339Nano), EpisodeIP: "1", Attempt: 2, Usage: &Usage{Prompt: 3}}}
	bindings := []Attribution{{IP: "1", NS: "old", Until: at.Add(time.Minute)}, {IP: "1", NS: "new", From: at.Add(time.Minute)}}
	b := AggregateAttributed(Attribute(rows, bindings), 0)
	if b.Prompt != 5 || len(b.ByEpisode) != 2 || b.ByEpisode[0].NS != "new" || b.ByEpisode[0].Prompt != 3 {
		t.Fatal(b)
	}
}
