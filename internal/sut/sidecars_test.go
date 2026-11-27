package sut

import (
	"context"
	"encoding/json"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestP6bProjectContracts(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), pythonPath(t), "-B", "../../suts/_bridge/python/test_p6b_contracts.py")
	root, _ := filepath.Abs("../../suts/_bridge/python")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1", "PYTHONPATH=" + root, "EP=" + t.TempDir()}
	if out, e := procexec.CombinedOutput(cmd); e != nil {
		t.Fatalf("%v: %s", e, out)
	}
	for _, name := range []string{"hindsight", "honcho", "cognee"} {
		t.Run(name+"/pending_fixture", func(t *testing.T) {
			// Only the manifest shape comes from the repository. Never execute a
			// real project's dependency lock, prepare step or SDK in this test.
			dir := t.TempDir()
			path := filepath.Join(dir, "manifest.json")
			if e := copyFile("../../suts/"+name+"/manifest.json", path); e != nil {
				t.Fatal(e)
			}
			m, e := Load(path)
			if e != nil {
				t.Fatal(name, e)
			}
			m.Sandbox.Backend = "none" // Test only the local pending-lock fixture.
			if m.Runtime.RequirementsLock == "" {
				t.Fatal("project must declare its dependency lock")
			}
			writeFixture(t, m.Resolve(m.Runtime.RequirementsLock), "# pending fixture: no verified hashes\n")
			want := "lock is not verified"
			if name == "honcho" {
				want = "env install deferred: P6c"
			}
			cache := filepath.Join(dir, "unused-cache")
			if _, e = Install(t.Context(), m, InstallOptions{Offline: true, Cache: cache}); e == nil || !strings.Contains(e.Error(), want) {
				t.Fatal("pending fixture lock must fail before subprocess/download", name, e)
			}
			if _, e = os.Stat(cache); !os.IsNotExist(e) {
				t.Fatal("pending lock started installation", e)
			}
		})
	}
}

func TestP6bVerifiedLockLocalWheel(t *testing.T) {
	pythonPath(t)
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "verified", false: "hash_mismatch"}[valid], func(t *testing.T) {
			m, root := fixtureManifest(t, "python")
			links := wheelFixture(t, root)
			sha, err := FileHash(filepath.Join(links, "fixture_sdk-1.0.0-py3-none-any.whl"))
			if err != nil {
				t.Fatal(err)
			}
			if !valid {
				sha = strings.Repeat("0", 64)
			}
			m.Runtime.RequirementsLock = "requirements.lock.txt"
			writeFixture(t, m.Resolve(m.Runtime.RequirementsLock), "fixture-sdk==1.0.0 --hash=sha256:"+sha+"\n")
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, m.Path, string(raw))
			m, err = Load(m.Path)
			if err != nil {
				t.Fatal(err)
			}
			o := InstallOptions{Offline: true, Cache: filepath.Join(root, "cache"), FindLinks: links}
			l, err := Install(t.Context(), m, o)
			t.Cleanup(func() { _ = Destroy(context.Background(), o.Cache, m.Name, false) })
			if !valid {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "hash mismatch") {
					t.Fatal("wrong wheel hash was not rejected", err)
				}
				if _, err = CheckLock(m); err == nil {
					t.Fatal("failed hash created usable lock")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, _ := FileHash(m.Resolve(m.Runtime.RequirementsLock))
			if l.RequirementsLockSHA256 != want || l.Probe["ok"] != true || l.Probe["describe"] != "fixture" {
				t.Fatal("verified lock/SDK probe not exercised", l.Probe)
			}
			if err = CheckEnv(l); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestP6bProjectEnvironmentPlans(t *testing.T) {
	for _, name := range []string{"hindsight", "cognee"} {
		m, err := Load("../../suts/" + name + "/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		ep := t.TempDir()
		env, err := m.Environment(ep, "/fixture")
		if err != nil {
			t.Fatal(name, err)
		}
		if !sandbox.Within(env["HOME"], ep) {
			t.Fatal("project HOME must remain within EP", env["HOME"])
		}
		if name == "hindsight" {
			s := sandbox.Spec{EpisodeDir: ep, Env: env, Command: []string{"/fixture", "{sidecar.api.port}"}}
			if err = m.PlanServices(planningLock(t, m), &s); err != nil {
				t.Fatal(err)
			}
			// P8: Postgres uses an immutable image and an episode-local DNS alias.
			if len(s.Services) != 2 {
				t.Fatal("expected database then API", len(s.Services))
			}
			db, api := s.Services[0], s.Services[1]
			if db.Unmanaged || db.Kind != "postgres" || db.Transport != "tcp" || db.Port != 5432 ||
				len(db.Init) == 0 || len(db.Ready.Exec) == 0 || db.Image == "" ||
				db.Exports["HINDSIGHT_API_DATABASE_URL"] != s.Env["HINDSIGHT_API_DATABASE_URL"] ||
				!strings.HasPrefix(s.Env["HINDSIGHT_API_DATABASE_URL"], "postgresql://") ||
				api.Env["HINDSIGHT_API_DATABASE_URL"] != s.Env["HINDSIGHT_API_DATABASE_URL"] || api.Env["HOME"] != env["HOME"] {
				t.Fatal(s)
			}
		}
		m.Process.Env = map[string]string{"HOME": "/outside"}
		m.TelemetryEnv = nil
		if _, err = m.Environment(ep, "/fixture"); err == nil {
			t.Fatal("HOME escaped EP")
		}
	}
}
