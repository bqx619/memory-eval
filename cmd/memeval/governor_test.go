package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/config/dotenv"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var governorEvidence = flag.String("governor-evidence", "", "retain offline governor CLI evidence in scratch")

func TestGovernorCLIStopAndResume(t *testing.T) {
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	root := t.TempDir()
	if *governorEvidence != "" {
		root = *governorEvidence
		os.MkdirAll(root, 0700)
	}
	project := filepath.Join(root, "project")
	os.MkdirAll(project, 0700)
	put := func(name string, b []byte) {
		t.Helper()
		if e := os.WriteFile(name, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	echo, e := os.ReadFile("../../suts/echo/echo_bridge.py")
	if e != nil {
		t.Fatal(e)
	}
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	m := map[string]any{"schema_version": "3", "name": "governor-fixture", "adapter": map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"}, "runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"}, "process": map[string]any{"command": []string{"{venv}/bin/python", "{project}/echo.py"}}, "sandbox": map[string]any{"backend": "container", "network": "gateway"}, "ready": map[string]any{"strategy": "sync"}}
	m["llm"] = map[string]any{"required": false, "concurrency": 32}
	raw, _ := json.Marshal(m)
	manifest := filepath.Join(project, "manifest.json")
	put(manifest, raw)
	t.Setenv("MEMEVAL_SUT_LLM_BASE_URL", "http://127.0.0.1:8317/v1")
	t.Setenv("MEMEVAL_SUT_LLM_API_KEY", "fake-gateway-key")
	t.Setenv("MEMEVAL_CACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("MEMEVAL_EP_ROOT", filepath.Join(root, "ep"))
	for _, k := range []string{"MEMEVAL_SUT_LLM_CONCURRENCY", "MEMEVAL_SUT_LLM_RPM", "MEMEVAL_SUT_LLM_BUDGET_TOKENS", "MEMEVAL_SUT_LLM_MAX_PAUSE"} {
		t.Setenv(k, "")
	}
	var lastStderr string
	cli := func(args ...string) error {
		t.Helper()
		var out, errOut bytes.Buffer
		ctx := dotenv.With(t.Context(), &dotenv.Settings{Sources: map[string]string{}})
		e := runCommand(ctx, args, &out, &errOut)
		lastStderr = errOut.String()
		if strings.Contains(out.String()+errOut.String(), "fake-gateway-key") {
			t.Fatal("key in output")
		}
		return e
	}
	if e = cli("env", "install", manifest, "--offline"); e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(root, "suite.json")
	put(source, []byte(`{"schema_version":"2.0","suite_id":"governor-fixture","provenance":{"source_url":"fixture://synthetic"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","content":"blue"}],"cases":[{"id":"q1","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]},{"id":"ep2","events":[{"source_id":"s","owner":"u","content":"blue"}],"cases":[{"id":"q2","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`))
	dest := filepath.Join(root, "run")
	f.Control(t, map[string]string{"FAKE_DOCKER_GOVERNOR_AFTER_SUTS": "2", "FAKE_DOCKER_GOVERNOR_METER": "1", "FAKE_DOCKER_GOVERNOR_DISCONNECT": "1"})
	e = cli("run", "--sut", manifest, "--suite", source, "--out", dest, "--sut-llm-budget-tokens", "120")
	if e == nil || exitCode(e) != 4 || !strings.Contains(e.Error(), "--resume "+dest) {
		t.Fatal(e)
	}
	r, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Cases) != 1 || r.Interruption == nil || r.Cases[0].Status != result.Scored {
		t.Fatal(r.Interruption, len(r.Cases))
	}
	warning := "governor: 2 SUT connections closed while queued/retrying; raise --sut-llm-concurrency (now 32) or the SUT's HTTP timeout"
	if !strings.Contains(lastStderr, warning) || !strings.Contains(strings.Join(r.Warnings, "\n"), warning) {
		t.Fatal(lastStderr, r.Warnings)
	}
	oldID := r.RunID
	wire, _ := json.Marshal(r)
	for _, want := range []string{`"client_disconnects":2`, `"calls":2`, `"prompt_tokens":200`, `"ns":"` + r.Cases[0].Namespace + `"`, `"run_clean":true`, `"metered":true`, `"budget_tokens":120`, `"concurrency":32`, `"concurrency_source":"manifest"`} {
		if !bytes.Contains(wire, []byte(want)) {
			t.Fatal("missing", want, string(wire))
		}
	}
	found := false
	for _, args := range f.Calls(t) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "__container-governor --listen") && strings.Contains(joined, filepath.Join(dest, "governor")+":/meter") {
			found = true
			if !strings.Contains(joined, "--concurrency 32 ") {
				t.Fatal("manifest concurrency absent from run governor argv", args)
			}
		}
	}
	if !found {
		t.Fatal("run governor create call missing")
	}
	// Force fresh selftest evidence into the resume stage, as after a binary
	// change or expired cached record. Only this fixture's cache is removed.
	if e = os.RemoveAll(filepath.Join(root, "cache", "selftests")); e != nil {
		t.Fatal(e)
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_GOVERNOR_METER": "1"})
	if e = cli("run", "--resume", dest, "--sut", manifest, "--suite", source, "--sut-llm-budget-tokens", "1000"); e != nil {
		t.Fatal(e)
	}
	next, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Cases) != 2 || next.Interruption != nil || next.RunID != oldID || next.ResumeCount != 1 {
		t.Fatal(len(next.Cases), next.Interruption)
	}
	var evidence struct {
		Path string `json:"path"`
	}
	raw, _ = json.Marshal(next.Isolation.Selftest)
	if e = json.Unmarshal(raw, &evidence); e != nil {
		t.Fatal(e)
	}
	if filepath.Dir(evidence.Path) != filepath.Join(dest, "selftest") {
		t.Fatal("selftest still points at staging", evidence.Path)
	}
	if _, e = os.Stat(evidence.Path); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dest, ".resume.lock")); !os.IsNotExist(e) {
		t.Fatal("stale resume lock", e)
	}
	if next.Cases[0].PublicInputSHA256 != r.Cases[0].PublicInputSHA256 {
		t.Fatal("public input changed")
	}
	raw, _ = json.Marshal(next.Billing.SUT)
	if !bytes.Contains(raw, []byte(`"prompt_tokens":400`)) || !bytes.Contains(raw, []byte(`"client_disconnects":2`)) || !bytes.Contains(raw, []byte(`"calls":4`)) {
		t.Fatal(string(raw))
	}
	for _, p := range []string{"governor/attempts.jsonl", "governor/resume-1/attempts.jsonl", "resume-history/0001/report.json"} {
		if _, e = os.Stat(filepath.Join(dest, p)); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("partial: cases=1 exit=4; resume: cases=2 same run_id, cumulative prompt=400; evidence=%s", dest)
	// A Hindsight-style per-episode declaration must scale at the CLI boundary,
	// even when fewer jobs than the configured parallelism are selected.
	parallelDest := filepath.Join(root, "parallel")
	if e = cli("run", "--sut", manifest, "--suite", source, "--out", parallelDest, "--parallel", "4"); e != nil {
		t.Fatal(e)
	}
	parallelReport, e := report.Load(t.Context(), filepath.Join(parallelDest, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	wire, _ = json.Marshal(parallelReport.Isolation.Container)
	for _, want := range []string{`"concurrency":128`, `"concurrency_source":"manifest"`, `"concurrency_per_episode":32`, `"parallel":4`} {
		if !bytes.Contains(wire, []byte(want)) {
			t.Fatal("missing scaled governor disclosure", want, string(wire))
		}
	}
	found = false
	for _, args := range f.Calls(t) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "__container-governor --listen") && strings.Contains(joined, filepath.Join(parallelDest, "governor")+":/meter") {
			found = true
			if !strings.Contains(joined, "--concurrency 128 ") {
				t.Fatal("per-episode fanout not scaled in argv", args)
			}
		}
	}
	if !found {
		t.Fatal("parallel run governor create call missing")
	}
	t.Logf("manifest fanout=32, parallel=4: governor argv/disclosure=128; evidence=%s", parallelDest)

}
func TestGovernorFlagDefaultsAndValidation(t *testing.T) {
	for _, b := range dotenv.Bindings {
		if strings.HasPrefix(b.Flag, "sut-llm-") {
			t.Setenv(b.Env, "")
		}
	}
	c, e := container.GovernorConfig(t.Context(), nil, 1)
	if e != nil || c.Concurrency != 8 || c.RPM != 0 || c.BudgetTokens != 0 || c.MaxPause.String() != "30m0s" {
		t.Fatal(c, e)
	}
	ctx := dotenv.With(t.Context(), &dotenv.Settings{Sources: map[string]string{}})
	root := newRootCommand(ctx, io.Discard, io.Discard)
	cmd, _, _ := root.Find([]string{"sandbox", "caps"})
	cmd.RunE = func(_ *cobra.Command, _ []string) error {
		var err error
		c, err = container.GovernorConfig(ctx, new(32), 4)
		return err
	}
	root.SetArgs([]string{"sandbox", "caps", "--sut-llm-concurrency", "2", "--sut-llm-rpm", "30", "--sut-llm-budget-tokens", "1234", "--sut-llm-max-pause", "2m"})
	if e = root.ExecuteContext(t.Context()); e != nil {
		t.Fatal(e)
	}
	if c.Concurrency != 2 || c.ConcurrencySource != "flag" || c.ConcurrencyPerEpisode != 32 || c.Parallel != 4 || c.RPM != 30 || c.BudgetTokens != 1234 || c.MaxPause.String() != "2m0s" {
		t.Fatal(c)
	}
	t.Setenv("MEMEVAL_SUT_LLM_CONCURRENCY", "-1")
	if _, e = container.GovernorConfig(t.Context(), nil, 1); e == nil {
		t.Fatal("invalid concurrency accepted")
	}
}
