package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChatCompatibilityRecordReplay(t *testing.T) {
	for _, provider := range []string{"openai-compat"} {
		for _, order := range [][]string{{}, {"temperature"}, {"max_completion_tokens"}, {"temperature", "max_completion_tokens"}, {"max_completion_tokens", "temperature"}} {
			t.Run(provider+"/"+strings.Join(order, "+"), func(t *testing.T) {
				t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-only")
				var calls atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					i := int(calls.Add(1)) - 1
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("bad JSON")
					}
					wantTokens, wantTemperature := "max_completion_tokens", true
					for _, field := range order[:min(i, len(order))] {
						if field == "temperature" {
							wantTemperature = false
						} else {
							wantTokens = "max_tokens"
						}
					}
					_, hasTemperature := body["temperature"]
					otherTokens := "max_tokens"
					if wantTokens == otherTokens {
						otherTokens = "max_completion_tokens"
					}
					if body[wantTokens] != float64(10) || body[otherTokens] != nil || hasTemperature != wantTemperature || body["n"] != float64(1) {
						t.Errorf("unexpected request %d: %#v", i, body)
					}
					if i < len(order) {
						code := "unsupported_parameter"
						if order[i] == "temperature" {
							code = "unsupported_value"
						}
						w.WriteHeader(400)
						json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "param": order[i], "message": "fixture rejection"}})
						return
					}
					fmt.Fprint(w, `{"model":"resolved","system_fingerprint":null,"choices":[{"message":{"content":"yes"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":7,"completion_tokens_details":{"reasoning_tokens":5}}}`)
				}))
				defer s.Close()
				cfg := config(t, s.URL+"/v1")
				cfg.Mode, cfg.MaxRetries = "record", 0
				e := engine(t, cfg)
				r := request()
				r.Provider = provider
				originalSHA, _, _ := e.key(r)
				a, err := e.Complete(context.Background(), r)
				if err != nil || int(calls.Load()) != len(order)+1 || a.RequestSHA != originalSHA || a.Usage.ReasoningTokens != 5 || len(a.Deviations) != len(order) {
					t.Fatal(a, err, calls.Load())
				}
				entries := e.Entries()
				if len(entries) != len(order)+1 || entries[len(order)].ReasoningTokens != 5 || e.Billing().ReasoningTokens != 5 {
					t.Fatal(entries)
				}
				if provider == "openai-compat" && *e.Billing().CostNew != .000024 {
					t.Fatal("reasoning double charged", e.Billing())
				}
				for i, field := range order {
					want := "max_completion_tokens unavailable; using max_tokens"
					if field == "temperature" {
						want = "temperature unavailable; provider default"
					}
					if a.Deviations[i] != want || entries[i].Deviations[i] != want {
						t.Fatal("missing rejection disclosure")
					}
				}
				s.Close()
				t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
				cfg.Mode = "replay"
				replay := engine(t, cfg)
				b, err := replay.Complete(context.Background(), r)
				if err != nil || !b.CassetteHit || *replay.Billing().CostNew != 0 || replay.Entries()[0].ReasoningTokens != 5 {
					t.Fatal(b, err)
				}
				b.CassetteHit = false
				if !reflect.DeepEqual(a, b) || int(calls.Load()) != len(order)+1 {
					t.Fatal("replay mismatch")
				}
			})
		}
	}
}

func TestChatCompatibilityRetryIsBoundedAndTargeted(t *testing.T) {
	for _, tc := range []struct {
		name, code, param string
		status, calls     int
	}{
		{"repeat temperature rejection", "unsupported_value", "temperature", 400, 2},
		{"repeat token rejection", "unsupported_parameter", "max_completion_tokens", 400, 2},
		{"wrong code", "invalid_request_error", "temperature", 400, 1},
		{"other parameter", "unsupported_parameter", "n", 400, 1},
		{"temperature parameter not value", "unsupported_parameter", "temperature", 400, 1},
		{"wrong status", "unsupported_value", "temperature", 422, 1},
		{"missing parameter", "unsupported_value", "", 400, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture")
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": tc.code, "param": tc.param}})
			}))
			defer s.Close()
			cfg := config(t, s.URL)
			cfg.Mode, cfg.MaxRetries = "live", 0
			e := engine(t, cfg)
			if _, err := e.Complete(context.Background(), request()); err == nil || int(calls.Load()) != tc.calls || len(e.Entries()) != tc.calls {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func TestCompatibilityDoesNotConsumeTransientRetry(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"unsupported_value","param":"temperature"}}`)
		case 2:
			w.WriteHeader(503)
		default:
			reply(w)
		}
	}))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode, cfg.MaxRetries = "live", 1
	if _, err := engine(t, cfg).Complete(context.Background(), request()); err != nil || calls.Load() != 3 {
		t.Fatal(err, calls.Load())
	}
}
