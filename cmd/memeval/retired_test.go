package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"memeval/internal/sandbox"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredTier(t *testing.T) {
	for _, tier := range []string{"process", "microvm"} {
		t.Run(tier, func(t *testing.T) { testRetiredTier(t, tier) })
	}
}
func testRetiredTier(t *testing.T, tier string) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	raw := `{"schema_version":"2","name":"retired-fixture","adapter":{"kind":"generic","transport":"jsonrpc-stdio"},"runtime":{"kind":"uv-python","python":"3.12","requirements":"requirements.in"},"process":{"command":["{venv}/bin/python","{project}/fixture.py"]},"sandbox":{"tier":"microvm","network":"none"},"ready":{"strategy":"sync"}}`
	if err := os.WriteFile(manifest, []byte(strings.Replace(raw, "microvm", tier, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := sut.Load(manifest)
	assertRetired := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "migrate") || exitCode(err) != 2 {
			t.Fatalf("expected v3 migration error, got %v", err)
		}
	}
	assertRetired(err)
	suitePath := filepath.Join(dir, "suite.json")
	fixture := `{"schema_version":"2.0","suite_id":"fixture","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`
	if err = os.WriteFile(suitePath, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache")
	t.Setenv("MEMEVAL_CACHE_DIR", cache)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"run", []string{"run", "--suite", suitePath, "--sut", manifest, "--out", filepath.Join(dir, "out")}},
		{"env-install", []string{"env", "install", manifest, "--offline"}},
		{"sut-setup", []string{"sut", "setup", "--sut", manifest, "--offline"}},
		{"selftest", []string{"sandbox", "selftest", "--sut", manifest, "--out", filepath.Join(dir, "selftest")}},
		{"caps", []string{"sandbox", "caps", "--sut", manifest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			assertRetired(runCommand(t.Context(), tc.args, &out, &out))
		})
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("retired operation touched environment cache", err)
	}

}

func TestRetiredTierFlagsAndHiddenCommands(t *testing.T) {
	for _, name := range []string{"target", "runtime-archive", "runtime-sha256"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := runCommand(t.Context(), []string{"env", "install", "fixture.json", "--" + name, "fixture"}, &out, &out)
			if err == nil || !strings.Contains(err.Error(), "unknown flag: --"+name) || exitCode(err) != 2 {
				t.Fatal("removed flag accepted", err)
			}
		})
	}
	for _, command := range []string{"__sandbox-helper", "__microvm-exec", "__guest-process-probe", "__microvm-probe", "__microvm-install", "__sandbox-guest-helper"} {
		if handled, _ := internalCommand([]string{command}); handled {
			t.Fatalf("retired internal command still handled: %s", command)
		}
	}
}

func TestContainerUnavailableCLI(t *testing.T) {
	m, err := sut.Load("../../suts/mem0/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.Sandbox.Backend != "container" {
		t.Fatal(m.Sandbox.Backend)
	}
	dir := t.TempDir()
	suitePath := filepath.Join(dir, "suite.json")
	fixture := `{"schema_version":"2.0","suite_id":"fixture","provenance":{"source_url":"fixture://local"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","role":"user","content":"blue"}],"cases":[{"id":"q","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`
	if err = os.WriteFile(suitePath, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMEVAL_CACHE_DIR", filepath.Join(dir, "unused-cache"))
	t.Setenv("MEMEVAL_DOCKER_CLI", filepath.Join(dir, "no-docker"))
	for _, args := range [][]string{
		{"run", "--suite", suitePath, "--sut", m.Path, "--out", filepath.Join(dir, "run"), "--allow-unenforced"},
		{"env", "install", m.Path, "--offline"},
		{"sandbox", "selftest", "--sut", m.Path, "--out", filepath.Join(dir, "selftest")},
	} {
		var out bytes.Buffer
		err := runCommand(t.Context(), args, &out, &out)
		var unavailable *sandbox.Unenforceable
		if !errors.As(err, &unavailable) || (!strings.Contains(unavailable.Reason, "unreachable") && !strings.Contains(unavailable.Reason, "P8 K3")) || exitCode(err) != 2 {
			t.Fatal(args, err)
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "unused-cache")); !os.IsNotExist(err) {
		t.Fatal("container fallback touched host cache", err)
	}
	for _, args := range [][]string{{"sandbox", "caps"}, {"sandbox", "caps", "--sut", m.Path}} {
		var out bytes.Buffer
		if err = runCommand(t.Context(), args, &out, &out); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Backend string       `json:"backend"`
			Caps    sandbox.Caps `json:"caps"`
		}
		if err = json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err, out.String())
		}
		if got.Caps.Enforced {
			t.Fatal(got)
		}
		if got.Backend != "container/docker" || !strings.Contains(got.Caps.Reason, "unreachable") {
			t.Fatal(got)
		}
	}
}
