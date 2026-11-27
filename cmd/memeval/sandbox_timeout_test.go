package main

import (
	"bytes"
	"context"
	"encoding/json"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/sidecar"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real CLI publication gate, runner, adapter, sidecar audits and
// timeout teardown together. Only the daemon and Python SUT are local fixtures.
func TestSandboxTimeoutPublishesScoredEpisodes(t *testing.T) {
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	put := func(path string, raw []byte) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	echo, err := os.ReadFile("../../suts/echo/echo_bridge.py")
	if err != nil {
		t.Fatal(err)
	}
	// Block ingestion, so every case in this incremental episode fails together.
	// Inject removal faults only once the failing episode is already running;
	// installation, selftest, describe and the scored episode stay unaffected.
	echo = bytes.Replace(echo, []byte("        batch = params[\"events\"]\n"), []byte(`        batch = params["events"]
        if any(e["content"] == "slow" for e in batch):
            with open(os.environ["FIXTURE_FAULT"], encoding="utf-8") as stream:
                fault = stream.read().strip()
            control = {"FAKE_DOCKER_LEAVE_SIDECAR": "1"} if fault == "residue" else {"FAKE_DOCKER_FAIL": "rm"} if fault == "remove_error" else {}
            path = os.environ["FIXTURE_CONTROL"]
            with open(path + ".tmp", "w", encoding="utf-8") as stream:
                json.dump(control, stream)
            os.replace(path + ".tmp", path)
            time.sleep(60)
`), 1)
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	faultFile := filepath.Join(root, "fault")
	put(faultFile, nil)
	raw := map[string]any{
		"schema_version": "3", "name": "timeout-echo-fixture",
		"adapter":  map[string]any{"kind": "generic", "transport": "jsonrpc-stdio", "request_timeout_s": 30},
		"runtime":  map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"},
		"process":  map[string]any{"command": []string{"{venv}/bin/python", "{project}/echo.py"}, "env": map[string]string{"FIXTURE_CONTROL": filepath.Join(f.State, "control.json"), "FIXTURE_FAULT": faultFile}},
		"sandbox":  map[string]any{"backend": "container", "network": "none", "limits": map[string]any{"timeout_s": 5}},
		"ready":    map[string]any{"strategy": "sync"},
		"sidecars": []any{map[string]any{"name": "worker", "kind": "generic", "port": 9999, "command": []string{"{venv}/bin/python", "-c", "import time;time.sleep(60)"}, "ready": map[string]any{"exec": []string{"{venv}/bin/python", "-c", "pass"}, "timeout_s": 5}}},
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(project, "manifest.json")
	put(manifest, encoded)
	cache := filepath.Join(root, "cache")
	t.Setenv("MEMEVAL_CACHE_DIR", cache)
	t.Setenv("MEMEVAL_EPISODE_LOGS", "true")
	t.Setenv("MEMEVAL_PARALLEL", "1")
	t.Setenv("UV_OFFLINE", "1")
	var stdout, stderr bytes.Buffer
	if err = runCommand(t.Context(), []string{"env", "install", manifest, "--offline", "--cache-dir", cache}, &stdout, &stderr); err != nil {
		t.Fatalf("install: %v\n%s\n%s", err, &stdout, &stderr)
	}
	t.Cleanup(func() {
		f.Control(t, nil)
		sut.Destroy(context.Background(), cache, "timeout-echo-fixture", false)
	})
	suitePath := filepath.Join(root, "suite.json")
	put(suitePath, []byte(`{"schema_version":"2.0","suite_id":"timeout-fixture","provenance":{"source_url":"fixture://local"},"episodes":[
{"id":"fast","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q1","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]},
{"id":"slow","events":[{"source_id":"s","owner":"u","role":"user","content":"slow"}],"cases":[{"id":"q2","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"q3","kind":"qa","query":"again?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}
]}`))
	for _, mode := range []string{"clean", "residue", "remove_error"} {
		t.Run(mode, func(t *testing.T) {
			f.Control(t, nil)
			put(faultFile, []byte(mode))
			dest := filepath.Join(root, mode)
			stdout.Reset()
			stderr.Reset()
			err := runCommand(t.Context(), []string{"run", "--sut", manifest, "--suite", suitePath, "--out", dest, "--cache-dir", cache, "--reset-policy", "incremental_per_episode"}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected case-error or rejection exit")
			}
			t.Logf("CLI result: %v", err)
			content, readErr := os.ReadFile(filepath.Join(dest, "report.json"))
			if readErr != nil {
				t.Fatalf("no report: %v; CLI %v\n%s", readErr, err, &stderr)
			}
			if mode != "clean" {
				var rejected map[string]any
				if err := json.Unmarshal(content, &rejected); err != nil {
					t.Fatal(err)
				}
				if rejected["summary"] != nil || !strings.Contains(rejected["reason"].(string), "no scored results published") {
					t.Fatalf("unsafe results published: %s", content)
				}
				return
			}
			r, err := report.Load(t.Context(), filepath.Join(dest, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Cases) != 3 || r.Summary.StatusCounts[result.Scored] != 1 || r.Summary.StatusCounts[result.Error] != 2 {
				t.Fatalf("lost scored cases: %s", content)
			}
			for _, c := range r.Cases[1:] {
				if c.ErrorType != "sandbox_timeout" || !strings.Contains(c.ErrorDetail, "process_exited") {
					t.Fatalf("timeout classification/detail lost: %+v", c)
				}
			}
			var isolation struct {
				Container       map[string]any     `json:"container"`
				EffectiveConfig []map[string]any   `json:"effective_config"`
				Sidecars        []sidecar.Evidence `json:"sidecars"`
			}
			isoRaw, err := json.Marshal(r.Isolation)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(isoRaw, &isolation); err != nil {
				t.Fatal(err)
			}
			if isolation.Container["residue"].(map[string]any)["run_clean"] != true {
				t.Fatal("run residue not verified")
			}
			timed := 0
			for _, cfg := range isolation.EffectiveConfig {
				if cfg["timeout"] == true {
					timed++
					if cfg["destroyed"] != true {
						t.Fatal(cfg)
					}
				}
			}
			if timed != 1 {
				t.Fatalf("timeout episodes: %d", timed)
			}
			sidecarTimeouts := 0
			for _, ev := range isolation.Sidecars {
				if ev.Timeout {
					sidecarTimeouts++
					if !ev.Destroyed || ev.Error != "" {
						t.Fatalf("timeout misreported as isolation failure: %+v", ev)
					}
				}
			}
			if sidecarTimeouts != 1 {
				t.Fatalf("timeout sidecars: %d", sidecarTimeouts)
			}
		})
	}
}
