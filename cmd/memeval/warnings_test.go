package main

import (
	"bytes"
	"memeval/internal/report"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWarningsMatchExecution(t *testing.T) {
	t.Setenv("MEMEVAL_SUT_LLM_BASE_URL", "http://127.0.0.1:8317/v1")
	t.Setenv("MEMEVAL_SUT_LLM_API_KEY", "fixture-gateway-credential")
	t.Setenv("MEMEVAL_EP_ROOT", filepath.Join(t.TempDir(), "ep"))
	m, _ := echoEnvironment(t) // Fake Docker only; no preinstalled environment.
	source := filepath.Join(t.TempDir(), "suite.json")
	if e := os.WriteFile(source, []byte(`{"schema_version":"2.0","suite_id":"warnings","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"baseline", "none", "gateway", "open"} {
		t.Run(mode, func(t *testing.T) {
			dest := t.TempDir()
			args := []string{"run", "--suite", source, "--out", dest, "--run-seed", strings.Repeat("42", 32)}
			if mode == "baseline" {
				args = append(args, "--sut", "bm25")
			} else {
				args = append(args, "--sut", m.Path, "--cache-dir", m.CacheDir, "--network", mode)
			}
			var out, errs bytes.Buffer
			if e := runCommand(t.Context(), args, &out, &errs); e != nil {
				t.Fatalf("%v\n%s\n%s", e, &out, &errs)
			}
			r, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			warnings := strings.Join(r.Warnings, "\n")
			for text, want := range map[string]bool{
				"In-process trusted baselines":                            mode == "baseline",
				"SUT provider calls are direct and unmetered":             mode == "open",
				"SUT gateway usage is metered":                            mode == "gateway",
				"Source-specific diagnostic":                              true,
				"Repeated samples do not add independent source clusters": true,
				"Echo is a transport fixture":                             mode != "baseline",
			} {
				if strings.Contains(warnings, text) != want {
					t.Errorf("%s presence=%t: %s", text, want, warnings)
				}
			}
			if mode != "baseline" && r.Isolation.Backend != "container/docker" {
				t.Fatal(r.Isolation)
			}
			t.Logf("%s warnings: %s", mode, warnings)
		})
	}
}
