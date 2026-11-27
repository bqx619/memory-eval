package main

import (
	"bytes"
	"errors"
	"memeval/internal/config/redact"
	"memeval/internal/sandbox/container/governor"
	"memeval/internal/sidecar"
	"strings"
	"testing"
)

func TestGovernorDisconnectWarning(t *testing.T) {
	var stderr bytes.Buffer
	if got := governorDisconnectWarnings(governor.Billing{}, 8, &stderr); len(got) != 0 || stderr.Len() != 0 {
		t.Fatal(got, &stderr)
	}
	want := "governor: 4 SUT connections closed while queued/retrying; raise --sut-llm-concurrency (now 8) or the SUT's HTTP timeout"
	got := governorDisconnectWarnings(governor.Billing{ClientDisconnects: 4}, 8, &stderr)
	if len(got) != 1 || got[0] != want || stderr.String() != want+"\n" {
		t.Fatal(got, &stderr)
	}
}
func TestIsolationFailureReason(t *testing.T) {
	ctx := redact.With(t.Context(), redact.New([]string{"private-config"}))
	ev := []*sidecar.Evidence{{Name: "healthy"}, {Name: "api", Error: "\x1b[31m\n  readiness timeout after 120s: manifest-secret private-config\x1b[0m\ntraceback"}, {Name: "later", Error: "later error"}}
	got := isolationFailureReason(ctx, ev, errors.New("cleanup failed"), []string{"manifest-secret"})
	want := "backend or sidecar isolation audit failed; no scored results published: api: readiness timeout after 120s: [REDACTED] [REDACTED]"
	if got != want {
		t.Fatal(got)
	}
	ev[1].Error = strings.Repeat("界", 300)
	got = isolationFailureReason(ctx, ev, nil, nil)
	detail := strings.TrimPrefix(got, "backend or sidecar isolation audit failed; no scored results published: ")
	if len([]rune(detail)) != 200 || !strings.HasSuffix(detail, "...") {
		t.Fatal(detail)
	}
	got = isolationFailureReason(ctx, nil, errors.New("\x1b[1mcleanup\x1b[0m\nsecond"), nil)
	if !strings.HasSuffix(got, ": cleanup") {
		t.Fatal(got)
	}
}
