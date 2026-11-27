package selftest

import (
	"context"
	"encoding/json"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sandbox/none"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecentBindsTimeConfigAndHash(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	r := Record{Version: 3, Passed: true, Status: "passed", Signature: "config", CreatedAt: now.Add(-time.Hour)}
	write := func() {
		r.SHA256 = r.hash()
		b, _ := json.Marshal(r)
		os.WriteFile(filepath.Join(dir, "selftest-unit.json"), b, 0600)
	}
	write()
	if _, e := Recent(dir, "config", now); e != nil {
		t.Fatal(e)
	}
	if _, e := Recent(dir, "changed", now); e == nil {
		t.Fatal("accepted changed config")
	}
	r.CreatedAt = now.Add(-25 * time.Hour)
	write()
	if _, e := Recent(dir, "config", now); e == nil {
		t.Fatal("accepted stale evidence")
	}
	r.CreatedAt = now.Add(time.Hour)
	write()
	if _, e := Recent(dir, "config", now); e == nil {
		t.Fatal("accepted future evidence")
	}
	r.CreatedAt = now
	r.Passed = false
	write()
	if _, e := Recent(dir, "config", now); e == nil {
		t.Fatal("accepted failed evidence")
	}
}

type fixtureTester struct {
	none.Backend
	pass bool
}

func (b fixtureTester) Selftest(_ context.Context, s sandbox.Spec, _ container.ProbeOptions) (*container.SelftestResult, error) {
	r := &container.SelftestResult{Probe: &ProbeResult{Items: map[string]Observation{}}}
	for k, v := range ContainerExpectations(s.Network, s.Services) {
		r.Probe.Items[k] = Observation{Success: v}
	}
	if !b.pass {
		r.Probe.Items["gold_absent"] = Observation{Success: false}
	}
	return r, nil
}

func TestOptionalSelftesterAndNotApplicableCache(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := &sut.Manifest{Name: "local-fixture"}
	m.Sandbox.Backend = "none"
	m.Sandbox.Network = sandbox.NetworkNone
	m.Process.Command = []string{exe}
	l := &sut.Lock{Executable: exe}
	for _, tc := range []struct {
		name    string
		b       sandbox.Sandbox
		status  string
		pass    bool
		failure bool
	}{
		{"none", none.Backend{}, "not_applicable", false, false},
		{"tested", fixtureTester{pass: true}, "passed", true, false},
		{"failed", fixtureTester{pass: false}, "failed", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r, path, err := Run(t.Context(), tc.b, m, l, dir)
			if (err != nil) != tc.failure || r == nil || r.Status != tc.status || r.Passed != tc.pass {
				t.Fatal(r, err)
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatal(err)
			}
			_, err = Recent(dir, r.Signature, time.Now())
			if (err == nil) != tc.pass {
				t.Fatal("incorrect cache eligibility", r, err)
			}
		})
	}
}
