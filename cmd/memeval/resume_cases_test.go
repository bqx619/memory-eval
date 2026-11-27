package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeNewFlagsRequireResume(t *testing.T) {
	for _, args := range [][]string{{"--resume-cases", "q1"}, {"--resume-cases", ""}, {"--resume-sut-change"}, {"--resume-sut-change=false"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			cmd := newRunCommand(t.Context(), &out)
			if err := cmd.Flags().Parse(args); err != nil {
				t.Fatal(err)
			}
			if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "requires --resume") {
				t.Fatal(err)
			}
		})
	}
}

func TestResumeCaseListAndSUTChangeFakeContainer(t *testing.T) {
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	put := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	echo := readFile(t, "../../suts/echo/echo_bridge.py")
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	snapshot := strings.Repeat("a", 64)
	bridgeConfig := map[string]any{"source_snapshot_sha256": snapshot}
	m := map[string]any{"schema_version": "3", "name": "resume-echo-fixture", "variant": "old",
		"adapter": map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"},
		"runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"},
		"process": map[string]any{"command": []string{"{venv}/bin/python", "{project}/echo.py"}},
		"bridge":  map[string]any{"config": bridgeConfig},
		"sandbox": map[string]any{"backend": "container", "network": "none"}, "ready": map[string]any{"strategy": "sync"}}
	manifest := filepath.Join(project, "manifest.json")
	saveManifest := func() {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		put(manifest, raw)
	}
	saveManifest()
	t.Setenv("MEMEVAL_CACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("MEMEVAL_EP_ROOT", filepath.Join(root, "ep"))
	t.Setenv("MEMEVAL_PARALLEL", "1")
	t.Setenv("MEMEVAL_EPISODE_LOGS", "true")
	ctx := dotenv.With(t.Context(), &dotenv.Settings{Sources: map[string]string{}})
	cli := func(args ...string) error {
		t.Helper()
		var out, stderr bytes.Buffer
		err := runCommand(ctx, args, &out, &stderr)
		if err != nil {
			t.Logf("CLI: %v; %s", err, &stderr)
		}
		return err
	}
	if err := cli("env", "install", manifest, "--offline"); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "suite.json")
	put(source, []byte(`{"schema_version":"2.0","suite_id":"resume-fixture","provenance":{"source_url":"fixture://synthetic"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q1","kind":"qa","query":"first?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"q2","kind":"qa","query":"second?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"q3","kind":"qa","query":"third?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`))
	dest := filepath.Join(root, "run")
	if err := cli("run", "--sut", manifest, "--suite", source, "--out", dest, "--repeats", "2"); err != nil {
		t.Fatal(err)
	}
	old := readRun(t, dest)
	if len(old.Cases) != 6 || old.Summary.StatusCounts[result.Scored] != 6 {
		t.Fatal(old.Summary)
	}
	// Simulate the pre-flag report shape: snapshot only exposed by describe.
	old.SUT.SourceSnapshotSHA256 = nil
	old.SUT.Describe.Config = map[string]any{"source_snapshot_sha256": snapshot}
	if err := report.Save(context.Background(), old, dest); err != nil {
		t.Fatal(err)
	}
	original := readFile(t, filepath.Join(dest, "report.json"))
	rows := bytes.Split(bytes.TrimSpace(readFile(t, filepath.Join(dest, "cases.jsonl"))), []byte("\n"))
	m["variant"] = "new"
	bridgeConfig["source_snapshot_sha256"] = strings.Repeat("b", 64)
	saveManifest()
	// Both preflights must reject before contacting even the fake daemon.
	base := []string{"run", "--resume", dest, "--sut", manifest, "--suite", source, "--resume-statuses", "scored"}
	beforeCalls := len(f.Calls(t))
	if err := cli(base...); err == nil || !strings.Contains(err.Error(), "resume SUT manifest mismatch") {
		t.Fatal(err)
	}
	if err := cli(append(append([]string{}, base...), "--resume-cases", "unknown", "--resume-sut-change")...); err == nil || !strings.Contains(err.Error(), "unknown resume") {
		t.Fatal(err)
	}
	if len(f.Calls(t)) != beforeCalls {
		t.Fatal("preflight contacted daemon")
	}
	if !bytes.Equal(original, readFile(t, filepath.Join(dest, "report.json"))) {
		t.Fatal("preflight changed report")
	}
	if err := cli("env", "install", manifest, "--offline"); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(root, "cases.txt")
	put(list, []byte("# redo\n\nq2\nq3#1\n"))
	if err := cli(append(append([]string{}, base...), "--resume-cases", "@"+list, "--resume-sut-change")...); err != nil {
		t.Fatal(err)
	}
	next := readRun(t, dest)
	if next.ResumeCount != 1 || len(next.Cases) != 6 || len(next.ResumeHistory) != 1 {
		t.Fatal(next.ResumeCount, len(next.Cases))
	}
	attempt := next.ResumeHistory[0]
	if attempt.Cases == nil || *attempt.Cases != 2 || attempt.RedoneCases != 3 || attempt.SUTChange == nil {
		t.Fatalf("%+v", attempt)
	}
	change := attempt.SUTChange
	if change.Previous.ManifestSHA256 != old.SUT.ManifestSHA256 || change.Current.ManifestSHA256 != next.SUT.ManifestSHA256 || change.Previous.EnvLockSHA256 != old.SUT.EnvLockSHA256 || change.Current.EnvLockSHA256 != next.SUT.EnvLockSHA256 || change.Previous.Variant != "old" || change.Current.Variant != "new" {
		t.Fatalf("%+v", change)
	}
	if change.Previous.SourceSnapshotSHA256 == nil || *change.Previous.SourceSnapshotSHA256 != snapshot || change.Current.SourceSnapshotSHA256 == nil || *change.Current.SourceSnapshotSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("%+v", change)
	}
	warning := fmt.Sprintf("Resume 1 replaced the SUT identity for 3 cases: %.12s → %.12s; preserved rows keep the previous identity (resume-history/0001)", old.SUT.ManifestSHA256, next.SUT.ManifestSHA256)
	if !strings.Contains(strings.Join(next.Warnings, "\n"), warning) {
		t.Fatal(next.Warnings)
	}
	if !bytes.Equal(original, readFile(t, filepath.Join(dest, "resume-history/0001/report.json"))) {
		t.Fatal("history changed")
	}
	fresh := bytes.Split(bytes.TrimSpace(readFile(t, filepath.Join(dest, "cases.jsonl"))), []byte("\n"))
	for _, i := range []int{0, 1, 4} {
		if !bytes.Equal(rows[i], fresh[i]) {
			t.Fatal("preserved row changed", i)
		}
	}
	// Future normal resumes use the newly committed identity, no override/list.
	if err := cli(base...); err != nil {
		t.Fatal(err)
	}
	final := readRun(t, dest)
	if final.ResumeCount != 2 || final.ResumeHistory[1].Cases != nil || final.ResumeHistory[1].SUTChange != nil {
		t.Fatal(final.ResumeHistory)
	}
	if !strings.Contains(strings.Join(final.Warnings, "\n"), warning) {
		t.Fatal("later resume lost identity-change disclosure", final.Warnings)
	}
	t.Logf("6 samples, 3 replaced, 3 byte-preserved; identity history and default guard verified")
}
