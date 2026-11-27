package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/config/redact"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	vcr "gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
)

func transportRequest(provider string) Request {
	r := request()
	r.Provider = provider
	if provider == "typesafe" {
		r.Operation = &Operation{
			APIType: "systemone",
			Body:    json.RawMessage(`{"model":"fixture","state":{"response":"yes"},"questions":[]}`),
			Parse: func(raw []byte, resp Response) (Response, error) {
				var result struct{ Answer string }
				if err := json.Unmarshal(raw, &result); err != nil {
					return resp, &Error{Kind: "LLMResponseInvalid"}
				}
				resp.Text = result.Answer
				return resp, nil
			},
		}
	}
	return r
}

func assertTransportLedger(t *testing.T, e *Engine, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(want) || len(e.Entries()) != len(want) {
		t.Fatalf("ledger rows = %d, memory rows = %d; want %d", len(lines), len(e.Entries()), len(want))
	}
	for i, line := range lines {
		var row LedgerEntry
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row.Error != want[i] || (want[i] != "" && row.HTTPStatus != 0) {
			t.Fatalf("attempt %d: error=%q status=%d", i+1, row.Error, row.HTTPStatus)
		}
	}
	for _, private := range []string{"synthetic-transport-secret", "EOF", "redirect refused", "http://", "retryTransport"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("ledger exposes transport detail %q", private)
		}
	}
}

func TestTransportRetriesAndOfflineReplay(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-transport-secret")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "synthetic-transport-secret")
	for _, provider := range []string{"openai-compat", "typesafe"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) <= 2 {
					_, _ = io.Copy(io.Discard, r.Body)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				if provider == "typesafe" {
					if r.URL.Path != "/v1/systemone" {
						t.Error("wrong typed API path", r.URL.Path)
					}
					_, _ = io.WriteString(w, `{"answer":"yes"}`)
					return
				}
				reply(w)
			}))
			defer server.Close()
			cfg := config(t, server.URL)
			cfg.Mode, cfg.MaxRetries, cfg.RetryDelay = "record-once", 2, time.Second
			cfg.Redactor = redact.New([]string{"synthetic-transport-secret"})
			var waits []time.Duration
			cfg.Sleep = func(ctx context.Context, d time.Duration) error {
				waits = append(waits, d)
				return ctx.Err()
			}
			e := engine(t, cfg)
			r := transportRequest(provider)
			resp, err := e.Complete(t.Context(), r)
			if err != nil || resp.Text != "yes" || calls.Load() != 3 {
				t.Fatalf("text=%q calls=%d err=%v", resp.Text, calls.Load(), err)
			}
			if !reflect.DeepEqual(waits, []time.Duration{time.Second, 2 * time.Second}) {
				t.Fatal(waits)
			}
			assertTransportLedger(t, e, cfg.LedgerPath, []string{"LLMTransportError", "LLMTransportError", ""})
			files, err := filepath.Glob(filepath.Join(cfg.CassetteDir, provider, "*", "*.yaml"))
			if err != nil || len(files) != 1 {
				t.Fatalf("cassettes=%v err=%v", files, err)
			}
			cassette, err := vcr.Load(strings.TrimSuffix(files[0], ".yaml"))
			if err != nil || len(cassette.Interactions) != 1 {
				t.Fatalf("expected one saved successful interaction: %v", err)
			}
			server.Close()
			cfg.Mode = "replay"
			cfg.LedgerPath = filepath.Join(t.TempDir(), "replay.jsonl")
			cfg.Sleep = func(context.Context, time.Duration) error { t.Fatal("replay slept"); return nil }
			replay := engine(t, cfg)
			hit, err := replay.Complete(t.Context(), r)
			if err != nil || !hit.CassetteHit || hit.Text != resp.Text || hit.RequestSHA != resp.RequestSHA || calls.Load() != 3 {
				t.Fatalf("replay=%+v calls=%d err=%v", hit, calls.Load(), err)
			}
			assertTransportLedger(t, replay, cfg.LedgerPath, []string{""})
		})
	}
}

func TestTransportRetryStops(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-transport-secret")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "synthetic-transport-secret")
	for _, provider := range []string{"openai-compat", "typesafe"} {
		for _, scenario := range []string{"exhausted", "canceled", "redirect"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					switch scenario {
					case "redirect":
						http.Redirect(w, r, "/never-follow", http.StatusTemporaryRedirect)
					case "canceled":
						cancel()
						<-r.Context().Done()
					default:
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
					}
				}))
				defer server.Close()
				cfg := config(t, server.URL)
				cfg.Mode, cfg.MaxRetries = "record-once", 2
				waits := 0
				cfg.Sleep = func(ctx context.Context, _ time.Duration) error { waits++; return ctx.Err() }
				e := engine(t, cfg)
				_, err := e.Complete(ctx, transportRequest(provider))
				want, attempts := "LLMTransportError", 1
				if scenario == "canceled" {
					want = "LLMCanceled"
				} else if scenario == "exhausted" {
					attempts = cfg.MaxRetries + 1
				}
				if err == nil || err.Error() != want || int(calls.Load()) != attempts || waits != attempts-1 {
					t.Fatalf("err=%v calls=%d waits=%d; want %s, %d calls", err, calls.Load(), waits, want, attempts)
				}
				rows := make([]string, attempts)
				for i := range rows {
					rows[i] = want
				}
				assertTransportLedger(t, e, cfg.LedgerPath, rows)
				files, _ := filepath.Glob(filepath.Join(cfg.CassetteDir, provider, "*", "*.yaml"))
				if len(files) != 0 {
					t.Fatalf("failed request saved a cassette: %v", files)
				}
			})
		}
	}
}

func TestProviderTransportPreservesURLError(t *testing.T) {
	for _, provider := range []string{"openai-compat", "typesafe"} {
		t.Run(provider, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.EOF })}
			wire, err := providerTransport(provider).Do(t.Context(), client, "http://fixture.invalid/v1/chat/completions", "synthetic-transport-secret", transportRequest(provider), []byte(`{}`))
			var urlErr *url.Error
			if wire != nil || !errors.As(err, &urlErr) || !errors.Is(err, io.EOF) {
				t.Fatalf("provider must preserve *url.Error wrapping EOF: response=%v error type=%T", wire, err)
			}
		})
	}
}

func TestTransientTransportErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"eof", io.EOF, true},
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"timeout", context.DeadlineExceeded, true},
		{"net_error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("transport failure")}, true},
		{"reset", syscall.ECONNRESET, true},
		{"pipe", syscall.EPIPE, true},
		{"refused", syscall.ECONNREFUSED, true},
		{"redirect", errors.New("redirect refused"), false},
		{"other", errors.New("non-transient request failure"), false},
		{"canceled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &url.Error{Op: "Post", URL: "https://fixture.invalid/private", Err: fmt.Errorf("cause: %w", tc.err)})
			if got := transientTransportError(err); got != tc.want {
				t.Fatalf("retryable = %v, want %v", got, tc.want)
			}
		})
	}
	if transientTransportError(io.EOF) || transientTransportError(nil) {
		t.Fatal("errors without *url.Error must not be treated as transport failures")
	}
}
