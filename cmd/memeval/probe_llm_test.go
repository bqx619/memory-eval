package main

import (
	"bytes"
	"encoding/json"
	"memeval/internal/report"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeLLMAndDescribeNotesCLI(t *testing.T) {
	testoracle.FakeDocker(t).Configure(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	put := func(p string, b []byte) {
		t.Helper()
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	echo, err := os.ReadFile("../../suts/echo/echo_bridge.py")
	if err != nil {
		t.Fatal(err)
	}
	echo = bytes.Replace(echo, []byte("events = []\n"), []byte(`assert os.environ['SDK_MODEL'] == 'flag-model'
assert os.environ['SDK_EFFORT'] == 'medium'
assert os.environ['OPENAI_BASE_URL'] == 'http://gateway.invalid:8317/v1'
assert os.environ['OPENAI_API_KEY'] in ('memeval-probe-placeholder', 'gateway-fixture-secret')
assert not any(k.startswith('MEMEVAL_') for k in os.environ)
events = []
`), 1)
	echo = bytes.Replace(echo, []byte(`"config": {}`), []byte(`"config": {"llm_notes": ["fixture SDK forwards model and effort", "credential echo: " + os.environ['OPENAI_API_KEY']]}`), 1)
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	raw := map[string]any{
		"schema_version": "3", "name": "probe-llm-fixture", "variant": "gateway",
		"adapter": map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"},
		"runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"},
		"process": map[string]any{"command": []string{"{venv}/bin/python", "{project}/echo.py"}},
		"sandbox": map[string]any{"backend": "container", "network": "open"},
		"ready":   map[string]any{"strategy": "sync"},
		"llm": map[string]any{"required": true, "model": "manifest-model", "reasoning_effort": "medium", "env": map[string]string{
			"OPENAI_BASE_URL": "{llm.base_url}", "OPENAI_API_KEY": "{llm.api_key}", "SDK_MODEL": "{llm.model}", "SDK_EFFORT": "{llm.reasoning_effort}",
		}},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(project, "manifest.json")
	put(manifest, b)
	envFile := filepath.Join(root, "fixture.env")
	put(envFile, []byte("MEMEVAL_SUT_LLM_BASE_URL=http://gateway.invalid:8317/v1\nMEMEVAL_SUT_LLM_API_KEY=gateway-fixture-secret\nMEMEVAL_SUT_LLM_MODEL=env-model\nMEMEVAL_SUT_LLM_REASONING_EFFORT=low\n"))
	// Load only the synthetic file, never the developer's private .env.
	for _, k := range []string{"MEMEVAL_SUT_LLM_BASE_URL", "MEMEVAL_SUT_LLM_API_KEY", "MEMEVAL_SUT_LLM_MODEL", "MEMEVAL_SUT_LLM_REASONING_EFFORT"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(root, "cache")
	invoke := func(args ...string) {
		t.Helper()
		var out, stderr bytes.Buffer
		argv := append([]string{"--env-file", envFile, "--sut-llm-model", "flag-model"}, args...)
		if err := run(t.Context(), argv, &out, &stderr); err != nil {
			t.Fatalf("CLI failed: %v\n%s\n%s", err, &out, &stderr)
		}
		if strings.Contains(out.String()+stderr.String(), "gateway-fixture-secret") {
			t.Fatal("credential in CLI output")
		}
	}
	invoke("env", "install", manifest, "--offline", "--cache-dir", cache)
	suite := filepath.Join(root, "suite.json")
	put(suite, []byte(`{"schema_version":"2.0","suite_id":"probe-llm","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`))
	dest := filepath.Join(root, "out")
	invoke("run", "--suite", suite, "--sut", manifest, "--out", dest, "--cache-dir", cache, "--limit", "1")
	r, err := report.Load(t.Context(), filepath.Join(dest, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if r.SUT.LLM["model"] != "flag-model" || r.SUT.LLM["reasoning_effort"] != "medium" {
		t.Fatal(r.SUT.LLM)
	}
	notes, err := json.Marshal(r.SUT.LLM["notes"])
	if err != nil {
		t.Fatal(err)
	}
	described, err := json.Marshal(r.SUT.Describe.Config["llm_notes"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(notes, described) || !bytes.Contains(notes, []byte("fixture SDK forwards model and effort")) || !bytes.Contains(notes, []byte("[REDACTED]")) || bytes.Contains(notes, []byte("TODO(K7)")) {
		t.Fatalf("SDK notes not copied/redacted: %s", notes)
	}
	b, err = os.ReadFile(filepath.Join(dest, "report.json"))
	if err != nil || bytes.Contains(b, []byte("gateway-fixture-secret")) {
		t.Fatal("credential leaked in report", err)
	}
	t.Logf("fake CLI install + run: offline describe succeeds, effective model=flag-model effort=medium; report.sut.llm.notes=%s", notes)
}
