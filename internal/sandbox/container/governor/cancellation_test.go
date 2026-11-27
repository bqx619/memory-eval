package governor

import (
	"bytes"
	"context"
	"encoding/json"
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

func meterRows(t *testing.T, s *Server) []Attempt {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.cfg.MeterDir, MeterFile))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ReadAttempts(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func assertBillingParity(t *testing.T, s *Server) {
	t.Helper()
	st := s.Snapshot()
	for _, b := range []Billing{Aggregate(meterRows(t, s), nil, st.Billing.CircuitOpens), AggregateAttributed(meterRows(t, s), st.Billing.CircuitOpens)} {
		b.ByEpisode = []EpisodeBilling{}
		if !reflect.DeepEqual(b, st.Billing) {
			t.Fatalf("aggregate=%+v status=%+v", b, st.Billing)
		}
	}
	w := httptest.NewRecorder()
	s.StatusHandler(w, httptest.NewRequest("GET", "/memeval/status", nil))
	var got Status
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Billing.ClientDisconnects != st.Billing.ClientDisconnects {
		t.Fatal(w.Body.String(), err)
	}
}
func request(ctx context.Context) *http.Request {
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fixture"}`)).WithContext(ctx)
	r.RemoteAddr = "172.20.0.2:12"
	return r
}
func await(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture stalled")
	}
}

// Observe entry into the semaphore select without wall-clock sleeps.
type observedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestClientCancellationQueued(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	s := proxy(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("queued request reached upstream") }), func(c *Config) { c.Concurrency = 1 }, clock.hooks())
	s.failures = 3
	s.sem <- struct{}{} // an existing request owns the only slot
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &observedContext{Context: ctx, entered: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), request(observed)) }()
	await(t, observed.entered)
	clock.Sleep(t.Context(), 28*time.Second)
	cancel()
	await(t, done)
	<-s.sem
	rows := meterRows(t, s)
	b := s.Snapshot().Billing
	if len(rows) != 1 || rows[0].Error != clientDisconnected || rows[0].Status != 0 || !rows[0].NotForwarded || rows[0].QueueWaitMS != 28000 || b.Calls != 0 || b.ClientDisconnects != 1 || b.Unmetered != 0 || b.Retries != 0 || s.failures != 3 {
		t.Fatal(rows, b, s.failures)
	}
	assertBillingParity(t, s)
}

func TestClientCancellationInflight(t *testing.T) {
	for _, mode := range []string{"pending-429", "reading-429", "reading-json", "reading-stream"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if mode != "pending-429" {
					w.Header().Set("Content-Length", "10000")
					if mode == "reading-429" {
						w.WriteHeader(429)
					} else if mode == "reading-stream" {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					w.Write([]byte("partial"))
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
				if mode == "pending-429" {
					w.WriteHeader(429)
				}
			}), nil, Hooks{})
			s.failures = 4
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), request(ctx)) }()
			await(t, entered)
			cancel()
			await(t, done)
			rows := meterRows(t, s)
			b := s.Snapshot().Billing
			if len(rows) != 1 || rows[0].Error != clientDisconnected || rows[0].Status != 0 || rows[0].NotForwarded || b.Calls != 1 || b.ClientDisconnects != 1 || b.Unmetered != 0 || b.Retries != 0 || b.RateLimited != 0 || b.CircuitOpens != 0 || s.failures != 4 {
				t.Fatal(rows, b, s.failures)
			}
			assertBillingParity(t, s)
		})
	}
}

func TestCancelledCandidateDoesNotTakeHalfOpenProbe(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	var calls atomic.Int64
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(usageJSON)) }), nil, clock.hooks())
	s.state = "open"
	s.cooldown = 120 * time.Second
	s.opened = clock.Now().Add(-120 * time.Second)
	s.until = clock.Now()
	s.failures = 5
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.ServeHTTP(httptest.NewRecorder(), request(ctx))
	if calls.Load() != 0 || s.halfBusy || s.cooldown != 120*time.Second || s.failures != 5 {
		t.Fatal(s.Snapshot())
	}
	if w := call(s, `{}`); w.Code != 200 || calls.Load() != 1 || s.Snapshot().State != "closed" {
		t.Fatal(w, s.Snapshot())
	}
	if rows := meterRows(t, s); len(rows) != 2 || rows[0].Error != clientDisconnected || !rows[0].NotForwarded {
		t.Fatal(rows)
	}
	assertBillingParity(t, s)
}

func TestCancelledProbeReleasesWithoutDoublingCooldown(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		w.Write([]byte(usageJSON))
	}), nil, Hooks{})
	s.state = "half_open"
	s.cooldown = 120 * time.Second
	s.failures = 5
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), request(ctx)) }()
	await(t, entered)
	// A cancelled waiter cannot release the live probe's slot.
	dead, stop := context.WithCancel(t.Context())
	stop()
	s.ServeHTTP(httptest.NewRecorder(), request(dead))
	s.mu.Lock()
	busy := s.halfBusy
	s.mu.Unlock()
	if !busy {
		t.Fatal("cancelled waiter consumed another request's probe")
	}
	cancel()
	await(t, done)
	if s.state != "half_open" || s.halfBusy || s.cooldown != 120*time.Second || s.failures != 5 || s.Snapshot().Billing.CircuitOpens != 0 {
		t.Fatal(s.Snapshot(), s.halfBusy, s.cooldown, s.failures)
	}
	if w := call(s, `{}`); w.Code != 200 || s.Snapshot().State != "closed" || calls.Load() != 2 {
		t.Fatal(w, s.Snapshot())
	}
	assertBillingParity(t, s)
}

func TestFiveUpstreamFailuresStillOpen(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var s *Server
	hooks := clock.hooks()
	hooks.Sleep = func(ctx context.Context, d time.Duration) error {
		if d == 60*time.Second {
			st := s.Snapshot()
			if st.State != "open" || st.Billing.Calls != 5 || st.Billing.RateLimited != 5 || st.Billing.CircuitOpens != 1 {
				t.Error(st)
			}
			cancel()
			return ctx.Err()
		}
		return clock.Sleep(ctx, d)
	}
	s = proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }), nil, hooks)
	s.ServeHTTP(httptest.NewRecorder(), request(ctx))
	st := s.Snapshot()
	if st.State != "open" || s.failures != 5 || st.Billing.Calls != 5 || st.Billing.Retries != 4 || st.Billing.ClientDisconnects != 1 || st.Billing.Unmetered != 5 {
		t.Fatal(st, s.failures)
	}
	if rows := meterRows(t, s); len(rows) != 6 || !rows[5].NotForwarded || rows[5].Error != clientDisconnected {
		t.Fatal(rows)
	}
	assertBillingParity(t, s)
}

func TestCancelledRetryWaitIsNotAnotherCall(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hooks := clock.hooks()
	hooks.Sleep = func(ctx context.Context, d time.Duration) error { clock.Sleep(ctx, d); cancel(); return ctx.Err() }
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }), nil, hooks)
	s.ServeHTTP(httptest.NewRecorder(), request(ctx))
	rows := meterRows(t, s)
	b := s.Snapshot().Billing
	if len(rows) != 2 || rows[1].QueueWaitMS != 1000 || rows[1].Error != clientDisconnected || !rows[1].NotForwarded || b.Calls != 1 || b.Retries != 0 || b.ClientDisconnects != 1 || s.failures != 1 {
		t.Fatal(rows, b)
	}
	assertBillingParity(t, s)
}

type blockedTransport struct{ entered chan struct{} }

func (tr blockedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	close(tr.entered)
	<-r.Context().Done()
	return nil, r.Context().Err()
}
func TestCancellationBeforeUpstreamWrite(t *testing.T) {
	s := proxy(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream call") }), nil, Hooks{})
	entered := make(chan struct{})
	s.client.Transport = blockedTransport{entered: entered}
	s.state = "half_open"
	s.cooldown = 60 * time.Second
	s.failures = 5
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), request(ctx)) }()
	await(t, entered)
	cancel()
	await(t, done)
	b := s.Snapshot().Billing
	if b.Calls != 0 || b.ClientDisconnects != 1 || b.Retries != 0 || b.Unmetered != 0 || s.state != "half_open" || s.halfBusy || s.cooldown != 60*time.Second {
		t.Fatal(b, s.Snapshot())
	}
	if rows := meterRows(t, s); len(rows) != 1 || !rows[0].NotForwarded {
		t.Fatal(rows)
	}
	assertBillingParity(t, s)
}
func TestCancelledRetryIsNotBilledAsRetry(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	var n atomic.Int64
	entered := make(chan struct{})
	s := proxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if n.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		close(entered)
		<-r.Context().Done()
	}), nil, clock.hooks())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), request(ctx)) }()
	await(t, entered)
	cancel()
	await(t, done)
	b := s.Snapshot().Billing
	if b.Calls != 2 || b.Retries != 0 || b.ClientDisconnects != 1 || b.Unmetered != 1 || b.RateLimited != 1 || s.failures != 1 {
		t.Fatal(b, s.failures)
	}
	rows := meterRows(t, s)
	if len(rows) != 2 || rows[1].Attempt != 2 || rows[1].Error != clientDisconnected {
		t.Fatal(rows)
	}
	assertBillingParity(t, s)
}
