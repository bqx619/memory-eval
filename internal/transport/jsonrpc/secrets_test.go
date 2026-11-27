package jsonrpc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSecretValuesLengthOrderAndDiagnosticRedaction(t *testing.T) {
	input := []string{"1", "none", "local", "1234567", "凭据值", "12345678", "abcdefgh", "abcdefgh-suffix", "abcdefgh", ""}
	want := []string{"abcdefgh-suffix", "12345678", "abcdefgh"}
	if got := SecretValues(input); !slices.Equal(got, want) {
		t.Fatal(got)
	}
	original := "/home/example/env/1.0.1 none local 1234567 凭据值 abcdefgh-suffix 12345678 abcdefgh"
	got := Redact(original, input)
	if got != "/home/example/env/1.0.1 none local 1234567 凭据值 [REDACTED] [REDACTED] [REDACTED]" {
		t.Fatal(got)
	}
	raw := redactData(json.RawMessage(`{"path":"/home/example/v1/local","error":"abcdefgh-suffix","token":"short"}`), input)
	if !strings.Contains(string(raw), "/home/example/v1/local") || strings.Contains(string(raw), "abcdefgh") || strings.Contains(string(raw), "short") {
		t.Fatal(string(raw))
	}
	// Explicitly labelled credentials still use the existing token/JSON-field
	// protections. The short-value rule concerns the substring replacement set.
	if strings.Contains(Redact("token=abc", input), "abc") {
		t.Fatal("labelled credential protection regressed")
	}
}

func TestRedactPreservesJSONEscapesAndNonCredentials(t *testing.T) {
	key := "sk-proj-0123456789abcdefghijklmnopqrstuvwxyz"
	raw, err := json.Marshal(map[string]string{"text": `password=abc" after; Bearer xyz" after; Flask-Login; mask-abcdefghijklmnop; (` + key + ")"})
	if err != nil {
		t.Fatal(err)
	}
	got := Redact(string(raw), nil)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal("redaction corrupted JSON", err, got)
	}
	for _, secret := range []string{"password=abc", "Bearer xyz", key} {
		if strings.Contains(got, secret) {
			t.Fatal("credential retained", got)
		}
	}
	if want := `[REDACTED]" after; [REDACTED]" after; Flask-Login; mask-abcdefghijklmnop; ([REDACTED])`; decoded["text"] != want {
		t.Fatalf("got %q, want %q", decoded["text"], want)
	}
	if matches := CredentialMatches(string(raw)); len(matches) != 3 {
		t.Fatal("streaming matcher differs", matches)
	}
}

func TestCredentialContinuationPreservesJSONDelimiters(t *testing.T) {
	for _, kind := range []string{"assignment", "bearer"} {
		for _, c := range []byte{'\\', '"', '\'', ',', ';', ' ', '\n'} {
			if CredentialContinues(kind, c) {
				t.Fatalf("%s consumed delimiter %q", kind, c)
			}
		}
	}
	for _, s := range []string{"Flask-Login", "mask-abcdefghijklmnop", "sk-short"} {
		if got := Redact(s, nil); got != s {
			t.Fatalf("non-credential modified: %q -> %q", s, got)
		}
	}
}
