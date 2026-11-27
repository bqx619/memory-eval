//go:build darwin || linux

package selftest

import (
	"encoding/json"
	"fmt"
	"memeval/internal/procexec"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__probe" {
		if err := Probe(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "--fd-snapshot" {
		fds, all, err := openFDs() // Observe before ANY cleanup or external spawn.
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		json.NewEncoder(os.Stdout).Encode(ProbeResult{ExtraFDs: fds, ObservedFDs: all, Items: map[string]Observation{"inherited_fds": {Success: len(fds) == 0}}})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProbeStillDetectsInheritedFD(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "leak-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, inject := range []bool{false, true} {
		cmd := exec.Command(os.Args[0], "--fd-snapshot")
		if inject {
			cmd.ExtraFiles = []*os.File{f}
		}
		b, err := procexec.Output(cmd)
		if err != nil {
			t.Fatal(err)
		}
		var r ProbeResult
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if r.Items["inherited_fds"].Success == inject || slices.Contains(r.ExtraFDs, 3) != inject {
			t.Fatalf("inject=%v: %s", inject, b)
		}
		for _, d := range r.ObservedFDs {
			if slices.Contains(r.ExtraFDs, d.FD) == d.CloseOnExec {
				t.Fatalf("descriptor lost/misclassified: %s", b)
			}
		}
		t.Logf("inject=%v: %s", inject, b)
	}
}
