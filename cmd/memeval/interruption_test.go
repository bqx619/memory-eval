package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/sidecar"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInterruptedContainerRunPublishesPartialResults(t *testing.T) {
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
	echo = bytes.Replace(echo, []byte("        request = params[\"request\"]\n"), []byte(`        request = params["request"]
        if request["query"] == "interrupt":
            with open(os.environ["FIXTURE_FAULT"], encoding="utf-8") as stream:
                fault = stream.read().strip()
            control = {"FAKE_DOCKER_LEAVE_SIDECAR": "1"} if fault == "residue" else {}
            path = os.environ["FIXTURE_CONTROL"]
            with open(path + ".tmp", "w", encoding="utf-8") as stream:
                json.dump(control, stream)
            os.replace(path + ".tmp", path)
            with open(os.environ["FIXTURE_STARTED"], "w") as stream:
                stream.write("in retrieve")
            time.sleep(60)
`), 1)
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	fault, started := filepath.Join(root, "fault"), filepath.Join(root, "started")
	put(fault, nil)
	raw := map[string]any{
		"schema_version": "3", "name": "interrupt-echo-fixture",
		"adapter":  map[string]any{"kind": "generic", "transport": "jsonrpc-stdio", "request_timeout_s": 30},
		"runtime":  map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"},
		"process":  map[string]any{"command": []string{"{venv}/bin/python", "{project}/echo.py"}, "env": map[string]string{"FIXTURE_CONTROL": filepath.Join(f.State, "control.json"), "FIXTURE_FAULT": fault, "FIXTURE_STARTED": started}},
		"sandbox":  map[string]any{"backend": "container", "network": "none"},
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
		sut.Destroy(context.Background(), cache, "interrupt-echo-fixture", false)
	})
	suitePath := filepath.Join(root, "suite.json")
	put(suitePath, []byte(`{"schema_version":"2.0","suite_id":"interrupt-fixture","provenance":{"source_url":"fixture://local"},"episodes":[
{"id":"fast","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q1","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]},
{"id":"interrupted","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q2","kind":"qa","query":"interrupt","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"q3","kind":"qa","query":"unscheduled","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]},
{"id":"later","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q4","kind":"qa","query":"unscheduled","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"q5","kind":"qa","query":"unscheduled","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}
]}`))
	for _, mode := range []string{"clean", "resume", "residue"} {
		t.Run(mode, func(t *testing.T) {
			f.Control(t, nil)
			put(fault, []byte(mode))
			os.Remove(started)
			dest := filepath.Join(root, mode)
			if mode == "resume" {
				dest = filepath.Join(root, "clean")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			observed := make(chan bool, 1)
			go func() {
				timer := time.NewTimer(30 * time.Second)
				defer timer.Stop()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						observed <- false
						return
					case <-timer.C:
						observed <- false
						cancel()
						return
					case <-tick.C:
						if _, e := os.Stat(started); e == nil {
							observed <- true
							cancel()
							return
						}
					}
				}
			}()
			stdout.Reset()
			stderr.Reset()
			args := []string{"run", "--sut", manifest, "--suite", suitePath, "--out", dest, "--cache-dir", cache, "--reset-policy", "incremental_per_episode"}
			if mode == "resume" {
				args = append(args, "--resume", dest, "--resume-statuses", "error")
			}
			err := runCommand(ctx, args, &stdout, &stderr)
			cancel()
			if !<-observed {
				t.Fatalf("fixture never reached retrieval: %v\n%s", err, &stderr)
			}
			content, e := os.ReadFile(filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatalf("partial report missing: %v; CLI: %v", e, err)
			}
			var raw map[string]any
			if e = json.Unmarshal(content, &raw); e != nil {
				t.Fatal(e)
			}
			if mode == "residue" {
				var rejected *isolationExit
				if !errors.As(err, &rejected) || raw["summary"] != nil || !strings.Contains(rejected.reason, "no scored results published") {
					t.Fatalf("real residue was accepted: %v\n%s", err, content)
				}
				iso := raw["isolation"].(map[string]any)["container"].(map[string]any)
				if iso["residue"].(map[string]any)["run_clean"] != false {
					t.Fatal(iso)
				}
				return
			}
			var rejected *isolationExit
			if errors.As(err, &rejected) || raw["reason"] != nil {
				t.Fatalf("cancellation rejected safe results: %v", err)
			}
			r, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			if len(r.Cases) != 2 || r.Cases[0].Status != result.Scored || r.Cases[1].Status != result.Error || r.Summary.StatusCounts[result.Scored] != 1 {
				t.Fatalf("lost results: %+v", r.Summary)
			}
			if r.Interruption == nil || r.Interruption.Reason != "interrupted" || r.Interruption.Unscheduled != 3 {
				t.Fatal(r.Interruption)
			}
			if mode == "resume" && (r.ResumeCount != 1 || len(r.ResumeHistory) != 1) {
				t.Fatal("interrupted resume was not committed", r.ResumeCount, r.ResumeHistory)
			}
			var iso struct {
				Container map[string]any
				Sidecars  []sidecar.Evidence
			}
			encoded, _ := json.Marshal(r.Isolation)
			if e = json.Unmarshal(encoded, &iso); e != nil {
				t.Fatal(e)
			}
			if iso.Container["residue"].(map[string]any)["run_clean"] != true {
				t.Fatal(iso.Container)
			}
			for _, ev := range iso.Sidecars {
				if !ev.Destroyed || ev.Error != "" {
					t.Fatalf("cancellation became audit failure: %+v", ev)
				}
			}
			t.Logf("scored=%d error=%d unscheduled=%d; run_clean=true", r.Summary.StatusCounts[result.Scored], r.Summary.StatusCounts[result.Error], r.Interruption.Unscheduled)
		})
	}
}
