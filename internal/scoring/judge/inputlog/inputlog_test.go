package inputlog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"memeval/internal/config/redact"
	"memeval/internal/llm/decision"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactAuthenticationPrivacyAndFailure(t *testing.T) {
	dir := t.TempDir()
	ctx := redact.With(context.Background(), redact.New([]string{"private-test-key", "https://private.endpoint"}))
	w, e := New(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	ctx = WithRecorder(WithCase(ctx, "case", "ep", "cluster", 1), w)
	ctx = WithFields(ctx, map[string]any{"query": "private-test-key", "endpoint_echo": "https://private.endpoint"})
	p := Recipe{"binary", `{"response":"private-test-key"}`, []decision.Question{{Name: "correct", Type: "noul"}}, "fixture/v1"}
	v := Verdict{Kind: "decision", Provider: "typesafe", Model: "jev-1.14.0", Valid: true, Raw: []byte(`{"echo":"private-test-key"}`)}
	yes := true
	v.Label = &yes
	if e = Append(ctx, "sample", "memora_eval", p, v); e != nil {
		t.Fatal(e)
	}
	meta, e := w.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, Filename))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "private-test-key") || strings.Contains(string(b), "https://private.endpoint") || meta.SHA256 != fmt.Sprintf("%x", sha256.Sum256(b)) {
		t.Fatal("privacy/hash")
	}
	st, _ := os.Stat(filepath.Join(dir, Filename))
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	rows, e := Read(dir, *meta)
	if e != nil || len(rows) != 1 || rows[0].CaseID != "case" || rows[0].RepeatIndex != 1 || rows[0].Fields["query"] != "[REDACTED]" {
		t.Fatal(rows, e)
	}
	if e = Append(ctx, "sample", "memora_eval", p, v); e == nil {
		t.Fatal("duplicate accepted")
	}
	if _, e = w.Snapshot(); e == nil {
		t.Fatal("write failure swallowed")
	}
	os.WriteFile(filepath.Join(dir, Filename), append(b, ' '), 0600)
	if _, e = Read(dir, *meta); e == nil {
		t.Fatal("tamper accepted")
	}
}

func TestOutReuseReplacesRatherThanAppendsAndKeepsPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	if e := os.WriteFile(path, []byte("old run"), 0644); e != nil {
		t.Fatal(e)
	}
	w, e := New(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	meta, e := w.Snapshot()
	if e != nil || meta.Rows != 0 {
		t.Fatal(meta, e)
	}
	b, e := os.ReadFile(path)
	if e != nil || len(b) != 0 {
		t.Fatal(string(b), e)
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, e)
	}
}
