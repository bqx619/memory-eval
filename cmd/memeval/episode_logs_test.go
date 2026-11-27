package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io/fs"
	"memeval/internal/config/redact"
	"memeval/internal/episodelog"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/suite"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Optional test evidence destination, outside the repository. No new env key.
var episodeLogEvidence = flag.String("episode-log-evidence", "", "retain offline episode log test evidence in a scratch directory")

func TestEpisodeLogsCLI(t *testing.T) {
	var e error
	testoracle.FakeDocker(t).Configure(t)
	root := t.TempDir()
	if *episodeLogEvidence != "" {
		root = *episodeLogEvidence
		if e = os.MkdirAll(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	project := filepath.Join(root, "echo-project")
	if e = os.Mkdir(project, 0700); e != nil {
		t.Fatal(e)
	}
	echo, e := os.ReadFile("../../suts/echo/echo_bridge.py")
	if e != nil {
		t.Fatal(e)
	}
	// The real echo protocol fixture, with diagnostics and a synthetic RPC error.
	// Neither the repository fixture nor any real SUT manifest is modified.
	echo = bytes.Replace(echo, []byte("    if method == \"retrieve\":\n"), []byte("    if method == \"retrieve\":\n        print('dataset text stays: ' + params['request']['query'], file=sys.stderr, flush=True)\n        if params['request']['query'] == 'fail':\n            raise ValueError('dataset error ' + os.environ['OPENAI_API_KEY'])\n"), 1)
	echo = bytes.Replace(echo, []byte("events = []\n"), []byte("print('echo diagnostic ' + os.environ.get('OPENAI_API_KEY', 'offline-install-probe'), file=sys.stderr, flush=True)\nevents = []\n"), 1)
	put := func(p string, b []byte) {
		t.Helper()
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	key := "fixture-openai-credential-123456789"
	credentialFile := filepath.Join(root, "fixture-credentials")
	put(credentialFile, []byte("OPENAI_API_KEY="+key+"\n"))
	raw := map[string]any{"schema_version": "3", "name": "c1-echo-fixture", "adapter": map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"}, "runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"}, "process": map[string]any{"command": []string{"{venv}/bin/python", "{project}/echo.py"}, "env_file": credentialFile, "env_passthrough": []string{"OPENAI_API_KEY"}}, "sandbox": map[string]any{"backend": "container", "network": "none"}, "ready": map[string]any{"strategy": "sync"}}
	b, _ := json.Marshal(raw)
	manifest := filepath.Join(project, "manifest.json")
	put(manifest, b)
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("UV_OFFLINE", "1")
	t.Setenv("MEMEVAL_EPISODE_LOGS", "")
	t.Setenv("MEMEVAL_LOG_FILE_CAP_BYTES", "")
	cache := filepath.Join(root, "cache")
	t.Setenv("MEMEVAL_CACHE_DIR", cache)
	ctx := redact.With(t.Context(), redact.New([]string{"private-config-value-123456789"}))
	cli := func(t *testing.T, failure bool, args ...string) {
		t.Helper()
		var out, errOut bytes.Buffer
		if e := runCommand(ctx, args, &out, &errOut); (e != nil) != failure {
			t.Fatalf("%v: %v\n%s\n%s", args, e, out.String(), errOut.String())
		} else if failure {
			t.Logf("expected CLI failure: %v", e)
		}
	}
	cli(t, false, "env", "install", manifest, "--offline", "--cache-dir", cache)
	// Installation now records the complete selftest against the offline daemon fixture.
	defer sut.Destroy(context.Background(), cache, "c1-echo-fixture", false)
	suitePath := filepath.Join(root, "suite.json")
	suiteJSON := `{"schema_version":"2.0","suite_id":"log-fixture","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`
	put(suitePath, []byte(suiteJSON))
	for _, mode := range []string{"default", "disabled", "keep", "error"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "error" {
				put(suitePath, []byte(strings.Replace(suiteJSON, "color?", "fail", 1)))
			}
			dest := filepath.Join(root, mode)
			args := []string{"run", "--allow-unenforced", "--sut", manifest, "--suite", suitePath, "--out", dest, "--cache-dir", cache, "--limit", "1"}
			if mode == "disabled" {
				args = append(args, "--no-logs")
			}
			if mode == "keep" {
				args = append(args, "--keep")
			}
			cli(t, mode == "error", args...)
			r, e := report.Load(ctx, filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			if len(r.Cases) != 1 || r.Cases[0].Namespace == "" {
				t.Fatal("namespace missing")
			}
			if r.DataFlow == nil || r.DataFlow.Summary != "zero_egress" || r.DataFlow.ZeroEgressProof == nil || !r.DataFlow.ZeroEgressProof.Verified {
				t.Fatalf("runExternal did not publish verified runtime data flow: %+v", r.DataFlow)
			}
			if mode == "disabled" {
				if r.Logs == nil || !r.Logs.Disabled {
					t.Fatal(r.Logs)
				}
				if _, e = os.Stat(filepath.Join(dest, "logs")); !os.IsNotExist(e) {
					t.Fatal("--no-logs created logs")
				}
				return
			}
			if r.Logs == nil || r.Logs.Episodes != 2 || r.Logs.Disabled {
				t.Fatal(r.Logs)
			}
			if _, e = os.Stat(filepath.Join(dest, "logs/0001-describe/sut.stderr.log")); e != nil {
				t.Fatal(e)
			}
			casesLog := filepath.Join(dest, "logs", "0002-"+r.Cases[0].Namespace)
			b, e = os.ReadFile(filepath.Join(casesLog, "sut.stderr.log"))
			if e != nil || !bytes.Contains(b, []byte("dataset text stays")) {
				t.Fatal("dataset text was masked or mirror absent", e)
			}
			var meta episodelog.Record
			b, e = os.ReadFile(filepath.Join(casesLog, "episode.json"))
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(b, &meta); e != nil {
				t.Fatal(e)
			}
			if meta.Keep != (mode == "keep") {
				t.Fatal(meta)
			}
			if mode == "keep" {
				if r.Logs.KeptEPSUnredactedStderr != 2 {
					t.Fatal(r.Logs)
				}
				// Keep intentionally leaves the EP raw stderr; remove only these test-owned EPs after asserting it.
				index, e := os.ReadFile(filepath.Join(dest, "logs/index.jsonl"))
				if e != nil {
					t.Fatal(e)
				}
				for _, line := range bytes.Split(bytes.TrimSpace(index), []byte{'\n'}) {
					var rec episodelog.Record
					if e = json.Unmarshal(line, &rec); e != nil {
						t.Fatal(e)
					}
					defer os.RemoveAll(rec.EpisodeDir)
					raw, e := os.ReadFile(filepath.Join(rec.EpisodeDir, "stderr.log"))
					if e != nil || !bytes.Contains(raw, []byte(key)) {
						t.Fatal("EP raw log compatibility changed", e)
					}
				}
			} else if _, e = os.Stat(meta.EpisodeDir); !os.IsNotExist(e) {
				t.Fatal("EP not removed")
			}
			if mode == "error" {
				if r.Cases[0].Status != result.Error || r.Cases[0].Note == nil || !strings.Contains(*r.Cases[0].Note, "dataset text is not redacted") {
					t.Fatal(r.Cases[0])
				}
				rpc, e := os.ReadFile(filepath.Join(casesLog, "rpc-errors.jsonl"))
				if e != nil || !bytes.Contains(rpc, []byte("dataset error")) || !bytes.Contains(rpc, []byte(`"method":"retrieve"`)) {
					t.Fatal("missing RPC original error", e, string(rpc))
				}
			}
			e = filepath.WalkDir(filepath.Join(dest, "logs"), func(p string, d fs.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if d.IsDir() {
					return nil
				}
				raw, e := os.ReadFile(p)
				if e == nil && bytes.Contains(raw, []byte(key)) {
					t.Errorf("credential leaked into %s", p)
				}
				return e
			})
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("evidence %s: episodes=%d files=%d bytes=%d", dest, r.Logs.Episodes, r.Logs.Files, r.Logs.Bytes)
			if mode == "error" {
				old, e := os.ReadFile(filepath.Join(casesLog, "rpc-errors.jsonl"))
				if e != nil {
					t.Fatal(e)
				}
				cli(t, true, "run", "--resume", dest, "--suite", suitePath, "--sut", manifest, "--cache-dir", cache, "--allow-unenforced")
				resumed, e := report.Load(ctx, filepath.Join(dest, "report.json"))
				if e != nil {
					t.Fatal(e)
				}
				if resumed.ResumeCount != 1 || resumed.Logs.Episodes != 4 || resumed.RunID != r.RunID || resumed.Cases[0].PreviousStatus != result.Error {
					t.Fatal("resume log merge failed", resumed.Logs, resumed.ResumeCount)
				}
				after, e := os.ReadFile(filepath.Join(casesLog, "rpc-errors.jsonl"))
				if e != nil || !bytes.Equal(old, after) {
					t.Fatal("resume overwrote old error log", e)
				}
				if _, e = os.Stat(filepath.Join(dest, "logs/resume-1/0001-describe/sut.stderr.log")); e != nil {
					t.Fatal(e)
				}
				t.Logf("resume evidence %s: episodes=%d files=%d", dest, resumed.Logs.Episodes, resumed.Logs.Files)
			}
		})
	}
}

// Exercise the adapter/runner/log pipeline with a fake daemon. These records
// describe fixture observations, not a real container isolation measurement.
func TestEpisodeLogsFakeContainer(t *testing.T) {
	f := testoracle.FakeDocker(t)
	root := t.TempDir()
	key := "fixture-container-credential-123456789"
	t.Setenv("OPENAI_API_KEY", key)
	envFile := filepath.Join(root, "fixture.env")
	if e := os.WriteFile(envFile, []byte("OPENAI_API_KEY="+key+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	bridge, e := os.ReadFile("../../suts/echo/echo_bridge.py")
	if e != nil {
		t.Fatal(e)
	}
	bridge = bytes.Replace(bridge, []byte("events = []\n"), []byte("print('container log ' + os.environ['OPENAI_API_KEY'], file=sys.stderr, flush=True)\nevents = []\n"), 1)
	script := filepath.Join(root, "echo.py")
	if e = os.WriteFile(script, bridge, 0600); e != nil {
		t.Fatal(e)
	}
	manifest := filepath.Join(root, "manifest.json")
	raw := map[string]any{"schema_version": "3", "name": "container-echo-fixture", "adapter": map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"}, "runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"}, "process": map[string]any{"command": []string{f.Python, script}, "env_passthrough": []string{"OPENAI_API_KEY"}}, "sandbox": map[string]any{"backend": "container", "network": "none"}, "ready": map[string]any{"strategy": "sync"}}
	b, _ := json.Marshal(raw)
	if e = os.WriteFile(manifest, b, 0600); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(root, "requirements.in"), nil, 0600)
	m, e := sut.LoadWithEnv(manifest, envFile)
	if e != nil {
		t.Fatal(e)
	}
	l := &sut.Lock{Name: m.Name, EnvDir: root, Executable: f.Python, Image: sandbox.Image{ID: "fixture:image"}}
	suitePath := filepath.Join(root, "suite.json")
	os.WriteFile(suitePath, []byte(`{"schema_version":"2.0","suite_id":"container-log","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`), 0600)
	s, e := suite.Open(t.Context(), suitePath)
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"default", "disabled", "keep", "residue"} {
		t.Run(mode, func(t *testing.T) {
			f.Control(t, nil)
			if mode == "residue" {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_VOLUME": "1"})
			}
			backend := container.New(container.Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Platform: f.Platform})
			dest := filepath.Join(root, mode)
			logs, e := episodelog.New(t.Context(), dest, episodelog.Options{Disabled: mode == "disabled", Secrets: []string{key}})
			if e != nil {
				t.Fatal(e)
			}
			a := &sut.Adapter{Manifest: m, Lock: l, Backend: backend, Keep: mode == "keep", Logs: logs}
			if mode == "residue" {
				if e = a.Reset(t.Context(), "residue"); e != nil {
					t.Fatal(e)
				}
				h := a.Handle
				e = a.Close(t.Context())
				if !a.Unenforceable || e == nil {
					t.Fatal("residue not propagated to report rejection", e)
				}
				f.Control(t, nil)
				if e = backend.Destroy(t.Context(), h); e != nil {
					t.Fatal(e)
				}
				logs.Close()
				return
			}
			opts := runner.DefaultOptions()
			opts.RunSeed = strings.Repeat("41", 32)
			opts.RecordNamespace = true
			r, e := runner.Run(t.Context(), s, a, opts)
			if e != nil {
				t.Fatal(e)
			}
			if e = logs.Close(); e != nil {
				t.Fatal(e)
			}
			summary := logs.Summary()
			if mode == "disabled" {
				if !summary.Disabled {
					t.Fatal(summary)
				}
				if _, e = os.Stat(filepath.Join(dest, "logs")); !os.IsNotExist(e) {
					t.Fatal("disabled logs exist")
				}
				return
			}
			if summary.Episodes != 2 || len(r.Cases) != 1 || r.Cases[0].Namespace == "" {
				t.Fatal(summary, r.Cases)
			}
			if _, e = os.Stat(filepath.Join(dest, "logs/0001-describe/sut.stderr.log")); e != nil {
				t.Fatal(e)
			}
			e = filepath.WalkDir(filepath.Join(dest, "logs"), func(p string, d fs.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if d.IsDir() {
					return nil
				}
				v, e := os.ReadFile(p)
				if bytes.Contains(v, []byte(key)) {
					t.Error("credential in log", p)
				}
				if d.Name() == "episode.json" {
					var rec episodelog.Record
					if e = json.Unmarshal(v, &rec); e != nil {
						return e
					}
					if rec.Keep != (mode == "keep") {
						t.Error(rec)
					}
					if rec.Keep {
						defer os.RemoveAll(rec.EpisodeDir)
						if _, statErr := os.Stat(filepath.Join(rec.EpisodeDir, ".memeval/env.list")); !os.IsNotExist(statErr) {
							t.Error("kept credential file")
						}
					}
				}
				return e
			})
			if e != nil {
				t.Fatal(e)
			}
			disclosure := backend.Report(t.Context())
			if disclosure["residue"].(map[string]any)["run_clean"] != true {
				t.Fatal(disclosure)
			}
			for _, ep := range a.Episodes {
				if ep["container"].(map[string]any)["run_id"] != r.RunID {
					t.Fatal("describe/case episode not bound to report run_id", ep, r.RunID)
				}
				if ep["destroyed"] != true {
					t.Fatal(ep)
				}
			}
			for _, args := range f.Calls(t) {
				if strings.Contains(strings.Join(args, " "), key) {
					t.Fatal("credential in argv")
				}
			}
			t.Logf("%s: episodes=%d logs=%d container residue=%v", mode, summary.Episodes, summary.Files, disclosure["residue"])
		})
	}
}
