package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestRetryAfterAndBackoffClock(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-key")
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("Retry-After", "3")
		case 2:
			w.Header().Set("Retry-After", now.Add(7*time.Second).Format(http.TimeFormat))
		case 3:
			w.Header().Set("Retry-After", "malformed")
		case 4:
			w.Header().Set("Retry-After", "999999999999")
		case 5:
			reply(w)
			return
		}
		w.WriteHeader(429)
	}))
	defer server.Close()
	cfg := config(t, server.URL)
	cfg.Mode, cfg.RetryDelay, cfg.RetryMaxWait = "record-once", time.Second, 10*time.Second
	waits := []time.Duration{}
	cfg.Now = func() time.Time { return now }
	cfg.Sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		now = now.Add(d)
		return ctx.Err()
	}
	e := engine(t, cfg)
	r, err := e.Complete(t.Context(), request())
	if err != nil || r.Text != "yes" {
		t.Fatal(r, err)
	}
	if !reflect.DeepEqual(waits, []time.Duration{3 * time.Second, 7 * time.Second, 4 * time.Second, 10 * time.Second}) {
		t.Fatal(waits)
	}
	if b := e.Billing(); b.Calls != 5 || b.HTTP429 != 4 || b.HTTP4xx != 4 || b.HTTP5xx != 0 {
		t.Fatal(b)
	}
	for i, row := range e.Entries() {
		want := 429
		if i == 4 {
			want = 200
		}
		if row.HTTPStatus != want {
			t.Fatal(row)
		}
	}
	cfg.Mode = "replay"
	cfg.Sleep = func(context.Context, time.Duration) error { t.Fatal("replay slept"); return nil }
	server.Close()
	replay := engine(t, cfg)
	x, err := replay.Complete(t.Context(), request())
	if err != nil || x.RequestSHA != r.RequestSHA || !x.CassetteHit || calls != 5 || replay.Billing().HTTP429 != 0 {
		t.Fatal(x, err, calls, replay.Billing())
	}
}

func TestRetryLimitsExponentialAndCancellation(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	cfg := config(t, server.URL)
	cfg.Mode, cfg.MaxRetries, cfg.RetryDelay = "live", 10, time.Second
	waits := []time.Duration{}
	cfg.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	e := engine(t, cfg)
	if _, err := e.Complete(t.Context(), request()); err == nil {
		t.Fatal("expected exhaustion")
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second}
	if !reflect.DeepEqual(waits, want) || e.Billing().Calls != 11 || e.Billing().HTTP5xx != 11 {
		t.Fatal(waits, e.Billing())
	}
	cfg.MaxRetries = 11
	if _, err := New(cfg); err == nil {
		t.Fatal("accepted >10 retries")
	}
	cfg.MaxRetries = 10
	cfg.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if _, err := engine(t, cfg).Complete(t.Context(), request()); err == nil || err.Error() != "LLMCanceled" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepContext(ctx, time.Hour); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestRetryAfterEdgeCasesAndLegacyHTTPCounts(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{{"0", 0}, {"-1", 2 * time.Second}, {"garbage", 2 * time.Second}, {now.Add(-time.Second).Format(http.TimeFormat), 0}, {now.Add(time.Hour).Format(http.TimeFormat), 60 * time.Second}} {
		if got := retryWait(tc.header, now, time.Second, 1, 60*time.Second); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	b := BillingEntries([]LedgerEntry{{Error: "LLMHTTPError (HTTP 429)"}, {HTTPStatus: 400}, {HTTPStatus: 503}, {HTTPStatus: 429, CassetteHit: true}}, "")
	if b.HTTP429 != 1 || b.HTTP4xx != 2 || b.HTTP5xx != 1 {
		t.Fatal(b)
	}
}
