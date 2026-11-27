package main

import (
	"bytes"
	"context"
	"errors"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sandbox/none"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if handled, code := internalCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// echoEnvironment installs only the repository's standard-library fixture in a
// test-owned cache. It never depends on a real SUT's retired environment lock.
func echoEnvironment(t *testing.T) (*sut.Manifest, *sut.Lock) {
	t.Helper()
	testoracle.FakeDocker(t).Configure(t)
	dir := t.TempDir()
	raw, err := os.ReadFile("../../suts/echo/echo_bridge.py")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"echo.py":               raw,
		"requirements.in":       nil,
		"requirements.lock.txt": nil,
		"manifest.json":         []byte(`{"schema_version":"3","name":"echo-transport-fixture","adapter":{"kind":"generic","transport":"jsonrpc-stdio"},"runtime":{"kind":"uv-python","python":"3.12","requirements":"requirements.in","requirements_lock":"requirements.lock.txt"},"process":{"command":["{venv}/bin/python","{project}/echo.py"]},"sandbox":{"backend":"container","network":"none"},"ready":{"strategy":"sync"}}`),
	} {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := sut.Load(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.CacheDir = t.TempDir()
	l, err := sut.Install(t.Context(), m, sut.InstallOptions{Cache: m.CacheDir, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sut.Destroy(context.Background(), m.CacheDir, m.Name, false); err != nil {
			t.Error(err)
		}
	})
	return m, l
}

func echoClient(t *testing.T, b sandbox.Sandbox, network sandbox.NetworkMode, options jsonrpc.Options) (*jsonrpc.Client, sandbox.Handle) {
	t.Helper()
	m, l := echoEnvironment(t)
	b = container.New(container.DefaultConfig())
	m.Sandbox.Network = network
	h, e := m.Prepare(context.Background(), b, l, "test", false)
	if e != nil {
		t.Fatal(e)
	}
	p, e := b.Spawn(context.Background(), h)
	if e != nil {
		b.Destroy(context.Background(), h)
		t.Fatal(e)
	}
	c, e := jsonrpc.New(p, h.Dir(), options)
	if e != nil {
		b.Destroy(context.Background(), h)
		t.Fatal(e)
	}
	t.Cleanup(func() {
		c.Close()
		if e := b.Destroy(context.Background(), h); e != nil {
			t.Error(e)
		}
	})
	return c, h
}
func TestEchoTransportIntegration(t *testing.T) {
	t.Run("echo", func(t *testing.T) {
		c, _ := echoClient(t, none.Backend{}, sandbox.NetworkNone, jsonrpc.Options{})
		var got map[string]string
		if e := c.Call(context.Background(), "echo", map[string]string{"text": "你好"}, &got); e != nil || got["text"] != "你好" {
			t.Fatalf("%v %v", got, e)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		c, _ := echoClient(t, none.Backend{}, sandbox.NetworkNone, jsonrpc.Options{RequestTimeout: 150 * time.Millisecond})
		var timeout *jsonrpc.Timeout
		if e := c.Call(context.Background(), "sleep", map[string]int{"ms": 1000}, nil); !errors.As(e, &timeout) {
			t.Fatal(e)
		}
	})
	t.Run("large", func(t *testing.T) {
		c, _ := echoClient(t, none.Backend{}, sandbox.NetworkNone, jsonrpc.Options{MaxFrameBytes: 1024})
		var large *jsonrpc.FrameTooLarge
		if e := c.Call(context.Background(), "big", map[string]int{"bytes": 4096}, nil); !errors.As(e, &large) {
			t.Fatal(e)
		}
	})
	t.Run("crash", func(t *testing.T) {
		c, _ := echoClient(t, none.Backend{}, sandbox.NetworkNone, jsonrpc.Options{})
		var exit *jsonrpc.ProcessExited
		if e := c.Call(context.Background(), "crash", nil, nil); !errors.As(e, &exit) || exit.Code != 17 || bytes.Contains([]byte(exit.Detail), []byte("fixture-secret")) {
			t.Fatalf("%+v %v", exit, e)
		}
	})
}
func TestFailingSelftestRefusesScoring(t *testing.T) {
	m, _ := echoEnvironment(t)
	if e := os.WriteFile(filepath.Join(filepath.Dir(os.Getenv("MEMEVAL_DOCKER_CLI")), "control.json"), []byte(`{"FAKE_DOCKER_PROBE_FAIL":"gold_absent"}`), 0600); e != nil {
		t.Fatal(e)
	}
	outdir := t.TempDir()
	out := &bytes.Buffer{}
	e := runCommand(t.Context(), []string{"run", "--cache-dir", m.CacheDir, "--sut", m.Path, "--suite", "../../docs/data/pmb_projection.json", "--limit", "1", "--out", outdir}, out, out)
	var denied *isolationExit
	if !errors.As(e, &denied) {
		t.Fatalf("expected exit 3: %v %s", e, out)
	}
	if exitCode(e) != 3 {
		t.Fatal("wrong exit code")
	}
	data, e := os.ReadFile(filepath.Join(outdir, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, []byte(`"cases"`)) || bytes.Contains(data, []byte(`"scores"`)) {
		t.Fatal("refused run contains scoring")
	}
}
