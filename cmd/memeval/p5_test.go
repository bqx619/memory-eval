package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJevOnlyRejectsLegacyCLI(t *testing.T) {
	for _, name := range []string{"judge-kind", "judge-model", "judge-provider", "judge-gate", "judge-thresholds", "judge-agreement", "judge-cross-model"} {
		var out, errOut bytes.Buffer
		e := runCommand(t.Context(), []string{"run", "--" + name, "legacy"}, &out, &errOut)
		if e == nil || !strings.Contains(e.Error(), "unknown flag") {
			t.Fatalf("%s: %v", name, e)
		}
	}
	var out, errOut bytes.Buffer
	if e := runCommand(t.Context(), []string{"judge-agreement"}, &out, &errOut); e == nil {
		t.Fatal("removed command accepted")
	}
}
func TestJevOnlyResumeRejectsLegacyJudgeBeforeArtifacts(t *testing.T) {
	for _, kind := range []string{"llm", "gated", ""} {
		dir := t.TempDir()
		b, _ := json.Marshal(map[string]any{"run_id": "legacy", "suite_sha256": "hash", "cases": []any{map[string]any{"case_id": "case"}}, "protocol": map[string]any{"version": "paeval-v2", "judge": map[string]any{"kind": kind}}})
		os.WriteFile(filepath.Join(dir, "report.json"), b, 0600)
		var out, errOut bytes.Buffer
		e := runCommand(t.Context(), []string{"run", "--resume", dir}, &out, &errOut)
		if e == nil || !strings.Contains(e.Error(), "resume rejects legacy judge") {
			t.Fatal(kind, e)
		}
		after, _ := os.ReadFile(filepath.Join(dir, "report.json"))
		if !bytes.Equal(b, after) {
			t.Fatal("rejection modified original")
		}
	}
}
