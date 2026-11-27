package main

import (
	"bytes"
	"encoding/json"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/selftest"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerCapsCLIAndSelftestGate(t *testing.T) {
	f := testoracle.FakeDocker(t)
	t.Setenv("MEMEVAL_DOCKER_CLI", filepath.Join(t.TempDir(), "must-be-overridden"))
	t.Setenv("MEMEVAL_DOCKER_CONTEXT", "wrong-context")
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	raw := map[string]any{"schema_version": "3", "name": "caps-fixture", "adapter": map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"}, "runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in"}, "process": map[string]any{"command": []string{f.Python, "-c", "pass"}}, "sandbox": map[string]any{"backend": "container", "network": "none"}, "ready": map[string]any{"strategy": "sync"}}
	data, _ := json.Marshal(raw)
	os.WriteFile(path, data, 0600)
	var out bytes.Buffer
	err := runCommand(t.Context(), []string{"sandbox", "caps", "--sut", path, "--docker-cli", f.CLI, "--docker-context", "orbstack", "--linux-binary", f.LinuxBinary, "--container-tmpfs-size", "64m", "--container-shm-size", "32m"}, &out, &out)
	if err != nil {
		t.Fatal(err, out.String())
	}
	var got struct {
		Backend string
		Caps    sandbox.Caps
	}
	if err = json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	if got.Backend != "container/docker" || !got.Caps.Enforced || got.Caps.UserMode != "uid-remapped" || got.Caps.Runtime["context"] != "orbstack" {
		t.Fatal(got)
	}
	found := false
	for _, args := range f.Calls(t) {
		text := strings.Join(args, " ")
		if strings.Contains(text, " create ") {
			if !strings.Contains(text, "size=64m") || !strings.Contains(text, "--shm-size 32m") {
				t.Fatal(args)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no caps probe")
	}
	t.Setenv("MEMEVAL_DOCKER_CLI", f.CLI)
	t.Setenv("MEMEVAL_DOCKER_CONTEXT", "orbstack")
	t.Setenv("MEMEVAL_LINUX_BINARY", f.LinuxBinary)
	m, err := sut.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	l := &sut.Lock{ImageRoot: sut.ImageRoot, EnvDir: dir, Executable: f.Python, Image: sandbox.Image{ID: "fixture:image"}}
	f.Control(t, map[string]string{"FAKE_DOCKER_PROBE_FAIL": "gold_absent"})
	_, err = ensureSelftest(t.Context(), m, l, filepath.Join(dir, "selftest"))
	if err == nil || !strings.Contains(err.Error(), "gold_absent") {
		t.Fatal("caps must not substitute full selftest", err)
	}
	t.Log(out.String())
}

func TestContainerSelftestNetworkOverrideIdentity(t *testing.T) {
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	project := t.TempDir()
	raw, err := os.ReadFile("../../suts/echo/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["runtime"].(map[string]any)["requirements_lock"] = "requirements.lock.txt"
	manifest["sandbox"].(map[string]any)["network"] = "none"
	raw, _ = json.Marshal(manifest)
	path := filepath.Join(project, "manifest.json")
	for name, data := range map[string][]byte{"manifest.json": raw, "requirements.in": nil, "requirements.lock.txt": nil} {
		if err = os.WriteFile(filepath.Join(project, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bridge, err := os.ReadFile("../../suts/echo/echo_bridge.py")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project, "echo_bridge.py"), bridge, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := sut.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	l, err := sut.Install(t.Context(), m, sut.InstallOptions{Cache: cache, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	signatures := map[string]bool{}
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkOpen, sandbox.NetworkGateway} {
		var out, stderr bytes.Buffer
		err = runCommand(t.Context(), []string{"sandbox", "selftest", "--sut", path, "--network", string(mode), "--cache-dir", cache, "--out", t.TempDir(), "--sut-llm-base-url", "http://127.0.0.1:8317/v1"}, &out, &stderr)
		if err != nil {
			t.Fatalf("mode=%s err=%v stdout=%s stderr=%s", mode, err, &out, &stderr)
		}
		var summary struct{ Report string }
		if err = json.Unmarshal(out.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(summary.Report)
		if err != nil {
			t.Fatal(err)
		}
		var record selftest.Record
		if err = json.Unmarshal(data, &record); err != nil || !record.Passed || record.Network != mode || signatures[record.Signature] {
			t.Fatal("network signature/expectation mismatch", record, err)
		}
		signatures[record.Signature] = true
		m.Sandbox.Network = mode
		m.CacheDir = cache
		reused, err := sut.CheckLock(m)
		if err != nil || reused.InputSHA256 != l.InputSHA256 || reused.LockSHA256 != l.LockSHA256 || reused.Image.ID != l.Image.ID {
			t.Fatalf("mode=%s lock not reused: %+v %v", mode, reused, err)
		}
		t.Logf("mode=%s input_sha256=%s signature=%s checks=%d", mode, reused.InputSHA256, record.Signature, len(record.Checks))
	}
}
