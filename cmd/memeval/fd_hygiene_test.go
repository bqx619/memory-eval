//go:build darwin || linux

package main

import (
	"encoding/json"
	"io"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sandbox/none"
	"memeval/internal/sandbox/selftest"
	"memeval/internal/testoracle"
	"os"
	"syscall"
	"testing"
)

func TestBackendSpawnFDHygiene(t *testing.T) {
	f := testoracle.FakeDocker(t)
	for _, b := range []sandbox.Sandbox{none.Backend{}, container.New(container.Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Platform: f.Platform})} {
		t.Run(b.Name(), func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			h, err := b.Prepare(t.Context(), sandbox.Spec{Command: []string{exe, "__probe"}, Network: sandbox.NetworkNone, Container: &sandbox.ContainerSpec{Image: sandbox.Image{ID: "fixture:image"}, EnvID: "fd-fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Destroy(t.Context(), h)
			// Deliberately leak AFTER capability probes and Prepare, immediately
			// before the production Spawn boundary, in the fixture backend.
			fd, err := syscall.Open(os.DevNull, syscall.O_RDONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Close(fd)
			before, err := procexec.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range before {
				if d.FD == fd && !d.CloseOnExec {
					found = true
				}
			}
			if !found {
				t.Fatal("fixture is not an inheritable fd")
			}
			p, err := b.Spawn(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.CloseReaders(p)
			stderr := make(chan []byte, 1)
			go func() { data, _ := io.ReadAll(p.Stderr()); stderr <- data }()
			var r selftest.ProbeResult
			err = json.NewDecoder(p.Stdout()).Decode(&r)
			status, waitErr := p.Wait(t.Context())
			if err != nil || waitErr != nil || status.Code != 0 {
				t.Fatalf("probe %v %v %+v %s", err, waitErr, status, <-stderr)
			}
			if len(r.ExtraFDs) != 0 || !r.Items["inherited_fds"].Success {
				t.Fatalf("leaked: %+v", r)
			}
			t.Logf("%s: injected parent fd %d; child extra_fds=%v", b.Name(), fd, r.ExtraFDs)
		})
	}
}
