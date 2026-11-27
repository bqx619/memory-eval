package main

import (
	"bytes"
	"encoding/json"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsRunScopedResidue(t *testing.T) {
	for _, mode := range []string{"recover", "stuck"} {
		t.Run(mode, func(t *testing.T) {
			f := testoracle.FakeDocker(t)
			f.Configure(t)
			root := t.TempDir()
			cache := filepath.Join(root, "cache")
			t.Setenv("MEMEVAL_CACHE_DIR", cache)
			t.Setenv("MEMEVAL_EP_ROOT", filepath.Join(root, "eps"))
			project := filepath.Join(root, "project")
			os.Mkdir(project, 0700)
			body, e := os.ReadFile("../../suts/echo/echo_bridge.py")
			if e != nil {
				t.Fatal(e)
			}
			for n, b := range map[string][]byte{"echo.py": body, "requirements.in": nil, "requirements.lock.txt": nil} {
				os.WriteFile(filepath.Join(project, n), b, 0600)
			}
			manifest := filepath.Join(project, "manifest.json")
			os.WriteFile(manifest, []byte(`{"schema_version":"3","name":"k6-echo","adapter":{"kind":"generic","transport":"jsonrpc-stdio"},"runtime":{"kind":"uv-python","python":"3.12","requirements":"requirements.in","requirements_lock":"requirements.lock.txt"},"process":{"command":["{venv}/bin/python","{project}/echo.py"]},"sandbox":{"backend":"container","network":"none"},"ready":{"strategy":"sync"}}`), 0600)
			var out bytes.Buffer
			if e = runCommand(t.Context(), []string{"env", "install", manifest, "--offline"}, &out, &out); e != nil {
				t.Fatal(e, out.String())
			}
			suite := filepath.Join(root, "suite.json")
			os.WriteFile(suite, []byte(`{"schema_version":"2.0","suite_id":"cleanup-fixture","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`), 0600)
			f.Control(t, map[string]string{"FAKE_DOCKER_RUN_ORPHAN": mode})
			dest := filepath.Join(root, "out")
			e = runCommand(t.Context(), []string{"run", "--sut", manifest, "--suite", suite, "--out", dest, "--limit", "1"}, &out, &out)
			raw, readErr := os.ReadFile(filepath.Join(dest, "report.json"))
			if readErr != nil {
				t.Fatal(e, readErr)
			}
			var r map[string]any
			if readErr = json.Unmarshal(raw, &r); readErr != nil {
				t.Fatal(readErr)
			}
			iso := r["isolation"].(map[string]any)
			residue := iso["container"].(map[string]any)["residue"].(map[string]any)
			if mode == "stuck" {
				if e == nil || exitCode(e) != 3 || !strings.Contains(e.Error(), "memeval-run-orphan") {
					t.Fatal(e)
				}
				if r["scores"] != nil || residue["run_clean"] != false {
					t.Fatal(r)
				}
				if _, err := os.Stat(filepath.Join(dest, "cases.jsonl")); !os.IsNotExist(err) {
					t.Fatal("published scored cases despite cleanup failure")
				}
			} else {
				if e != nil || residue["run_clean"] != true {
					t.Fatal(e, r)
				}
			}
			t.Logf("%s: run_clean=%v, error=%v", mode, residue["run_clean"], e)
		})
	}
}
func TestGCCLIFlags(t *testing.T) {
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	t.Setenv("MEMEVAL_CACHE_DIR", t.TempDir())
	t.Setenv("MEMEVAL_EP_ROOT", t.TempDir())
	var out bytes.Buffer
	if e := runCommand(t.Context(), []string{"gc", "--json"}, &out, &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"apply": false`) {
		t.Fatal(out.String())
	}
	for _, args := range [][]string{{"gc", "--build-cache"}, {"gc", "--older-than", "-1s"}} {
		if e := runCommand(t.Context(), args, &out, &out); e == nil {
			t.Fatal(args)
		}
	}
}
