package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNestedJSONAndEncodedPrivateValues(t *testing.T) {
	secret := "credential-\"<&\\\nprivate"
	r := New([]string{secret, "https://private.invalid/v1", "/private/cassette/path"})
	nested, _ := json.Marshal(map[string]string{"echo": secret})
	wire, err := r.Marshal(map[string]any{"raw": string(nested), "text": secret, "url": "https://private.invalid/v1", "path": "/private/cassette/path", "model": "jev-1.14.0", "p": .4})
	if err != nil || !json.Valid(wire) {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(wire, &got)
	if got["text"] != "[REDACTED]" || got["url"] != "[REDACTED]" || got["path"] != "[REDACTED]" || got["model"] != "jev-1.14.0" || got["p"] != .4 || strings.Contains(got["raw"].(string), "credential-") {
		t.Fatal("private values leaked or public protocol changed")
	}
	if strings.Contains(string(r.Encoded(wire)), "credential-") {
		t.Fatal("encoded secret leaked")
	}
}

func TestStripANSI(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x1b[31mERROR\x1b[0m\nnext", "ERROR\nnext"},
		{"\x1b]8;;https://example.invalid\aapi\x1b]8;;\x1b\\", "api"},
		{"\u009b31m中文\u009b0m", "中文"},
		{"\x1b[2K\rfailed\x1b(B\x1b[0m", "\rfailed"},
		{"prefix\x1b[", "prefix"},
		{"\x1bPdiscard\x1b\\plain", "plain"},
	} {
		if got := StripANSI(tc.in); got != tc.want {
			t.Errorf("%q => %q, want %q", tc.in, got, tc.want)
		}
	}
}
