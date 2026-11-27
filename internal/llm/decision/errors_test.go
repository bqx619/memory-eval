package decision

import (
	"encoding/json"
	"memeval/internal/llm"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestTypeSafeHTTPFailuresAndInvalidLedger(t *testing.T) {
	for _, status := range []int{401, 422, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "private-test-token")
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				if status == 200 {
					json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": map[string]any{"correct": map[string]any{"type": "noul", "noul": 1.2}}, "usage": map[string]int{"input_tokens": 17, "output_tokens": 0}})
				} else {
					w.Write([]byte("untrusted response body"))
				}
			}))
			defer s.Close()
			cfg := config(t, s.URL)
			cfg.MaxRetries = 0
			c := client(t, cfg)
			_, e := c.Decide(t.Context(), DecisionRequest{Purpose: "judge-decision", Provider: "typesafe", Model: "jev-1.14.0", State: "{}", Questions: questions()[:1], SampleID: "case", TemplateID: "test-v2"})
			if e == nil || calls != 1 {
				t.Fatal(e, calls)
			}
			if status == 200 && e.Error() != "DecisionResponseInvalid" {
				t.Fatal(e)
			}
			b, e := os.ReadFile(cfg.LedgerPath)
			if e != nil {
				t.Fatal(e)
			}
			var row llm.LedgerEntry
			if json.Unmarshal(b, &row) != nil || row.Purpose != "judge-decision" || row.Error == "" {
				t.Fatal(string(b))
			}
			if strings.Contains(string(b), "private-test-token") || strings.Contains(string(b), "untrusted response body") {
				t.Fatal("sensitive response leaked")
			}
			if status == 200 && row.PromptTokens != 17 {
				t.Fatal(row)
			}
		})
	}
}
func TestOnlyTypeSafeProvider(t *testing.T) {
	for _, p := range []string{"emulated", "openai-decisions", "openai-compat"} {
		_, e := Normalize(DecisionRequest{Purpose: "decision", Provider: p, Model: "jev-1.14.0", SampleID: "s", TemplateID: "t", Questions: questions()})
		if e == nil {
			t.Fatal(p)
		}
	}
}
