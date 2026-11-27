package testoracle

import (
	"encoding/binary"
	"encoding/json"
	"memeval/internal/config/dotenv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type DockerFixture struct{ CLI, LinuxBinary, State, Python, Platform string }

// FakeDocker uses only a stdlib simulator and a non-executable ELF header. It
// never resolves the host docker command or contacts a daemon.
func FakeDocker(t *testing.T) DockerFixture {
	t.Helper()
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python 3 required for offline Docker fixture")
	}
	dir := t.TempDir()
	script := filepath.Join(dotenv.Root("."), "internal/sandbox/container/testdata/fake_docker.py")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	cli := filepath.Join(dir, "docker-fixture")
	body := "#!/bin/sh\nexport FAKE_DOCKER_STATE=" + quote(dir) + "\nexport FAKE_DOCKER_UID_MAP=host\nexport FAKE_DOCKER_ARCH=" + runtime.GOARCH + "\nexec " + quote(python) + " " + quote(script) + " \"$@\"\n"
	if e = os.WriteFile(cli, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	elf := make([]byte, 64)
	copy(elf, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(elf[16:], 2)
	machine := map[string]uint16{"arm64": 183, "amd64": 62}[runtime.GOARCH]
	if machine == 0 {
		t.Skip("unsupported fixture ELF architecture", runtime.GOARCH)
	}
	binary.LittleEndian.PutUint16(elf[18:], machine)
	binary.LittleEndian.PutUint32(elf[20:], 1)
	binary.LittleEndian.PutUint16(elf[52:], 64)
	path := filepath.Join(dir, "probe-linux-"+runtime.GOARCH)
	if e = os.WriteFile(path, elf, 0600); e != nil {
		t.Fatal(e)
	}
	return DockerFixture{CLI: cli, LinuxBinary: path, State: dir, Python: python, Platform: "linux/" + runtime.GOARCH}
}
func (f DockerFixture) Control(t *testing.T, values map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(values)
	if e := os.WriteFile(filepath.Join(f.State, "control.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func (f DockerFixture) Calls(t *testing.T) [][]string {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(f.State, "calls.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	out := [][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if e = json.Unmarshal([]byte(line), &args); e != nil {
			t.Fatal(e)
		}
		out = append(out, args)
	}
	return out
}

// Configure makes production entry points use this offline fixture, never a host daemon.
func (f DockerFixture) Configure(t *testing.T) {
	t.Helper()
	t.Setenv("MEMEVAL_DOCKER_CLI", f.CLI)
	t.Setenv("MEMEVAL_DOCKER_CONTEXT", "orbstack")
	t.Setenv("MEMEVAL_LINUX_BINARY", f.LinuxBinary)
}
