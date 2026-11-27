package container

import (
	"encoding/json"
	"errors"
	"io"
	"memeval/internal/sandbox"
	"memeval/internal/testoracle"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*Backend, testoracle.DockerFixture) {
	t.Helper()
	f := testoracle.FakeDocker(t)
	b := New(Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Platform: f.Platform})
	return b, f
}
func spec(f testoracle.DockerFixture) sandbox.Spec {
	return sandbox.Spec{Command: []string{f.Python, "-u", "-c", "import sys,json; print('diagnostic',file=sys.stderr); [print(x.strip(),flush=True) for x in sys.stdin]"}, EnvRoot: filepath.Dir(f.Python), Env: map[string]string{"OPENAI_API_KEY": "fixture-secret-123456789", "MEMEVAL_OPENAI_API_KEY": "evaluator-must-not-enter", "PATH": "/usr/bin:/bin"}, Network: sandbox.NetworkNone, Container: &sandbox.ContainerSpec{Image: sandbox.Image{ID: "fixture:image"}, EnvID: "fixture@lock"}}
}
func TestCapabilities(t *testing.T) {
	for _, tc := range []string{"remapped", "preserved", "unreachable", "uid_nonroot", "caps_dropped", "no_new_privs", "rootfs_readonly", "probe_writable", "bad-platform", "bad-elf"} {
		t.Run(tc, func(t *testing.T) {
			b, f := fixture(t)
			switch tc {
			case "preserved":
				calls := 0
				b.fileOwner = func(p string) (int, int, error) {
					if _, e := os.Stat(p); e != nil {
						return 0, 0, e
					}
					calls++
					if calls == 1 {
						return 65532, 65532, nil
					}
					return os.Getuid(), os.Getgid(), nil
				}
			case "unreachable":
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "version"})
			case "uid_nonroot":
				f.Control(t, map[string]string{"FAKE_DOCKER_CAPS_FALSE": "uid"})
			case "caps_dropped":
				f.Control(t, map[string]string{"FAKE_DOCKER_CAPS_FALSE": "cap_eff_hex"})
			case "no_new_privs", "rootfs_readonly", "probe_writable":
				f.Control(t, map[string]string{"FAKE_DOCKER_CAPS_FALSE": tc})
			case "bad-platform":
				if b.cfg.Platform == "linux/arm64" {
					b.cfg.Platform = "linux/amd64"
				} else {
					b.cfg.Platform = "linux/arm64"
				}
			case "bad-elf":
				os.WriteFile(f.LinuxBinary, []byte("not ELF"), 0600)
			}
			c, e := b.Capabilities(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			if tc == "preserved" && os.Getuid() == 0 {
				if c.Enforced || !strings.Contains(c.Reason, "host root") {
					t.Fatal(c)
				}
				return
			}
			if tc == "remapped" || tc == "preserved" {
				if !c.Enforced || c.UserMode != "uid-"+tc {
					t.Fatal(c)
				}
				t.Log(c.Runtime)
				before := len(f.Calls(t))
				_, e = b.Capabilities(t.Context())
				if e != nil || len(f.Calls(t)) != before {
					t.Fatal("caps cache missed", e)
				}
			} else {
				if c.Enforced || c.Reason == "" {
					t.Fatal(c)
				}
				if tc == "unreachable" && !strings.Contains(c.Reason, `docker context "orbstack" unreachable; start OrbStack and retry`) {
					t.Fatal(c.Reason)
				}
			}
		})
	}
}
func TestLifecycleArgsSecretsAndOwnership(t *testing.T) {
	for _, mode := range []string{"none", "internal", "open"} {
		t.Run(mode, func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			if mode == "internal" {
				s.Services = []sandbox.Service{{Name: "declared"}}
			}
			if mode == "open" {
				s.Network = sandbox.NetworkOpen
			}
			s.Limits.MaxRSS = 64 << 20
			h, e := b.Prepare(t.Context(), s)
			if e != nil {
				t.Fatal(e)
			}
			p := h.(*sandbox.Prepared)
			raw, e := os.ReadFile(filepath.Join(h.Dir(), ".memeval/env.list"))
			if e != nil || strings.Contains(string(raw), "MEMEVAL_") || !strings.Contains(string(raw), "TMPDIR=/tmp") {
				t.Fatal(string(raw), e)
			}
			child, e := b.Spawn(t.Context(), h)
			if e != nil {
				t.Fatal(e)
			}
			defer sandbox.CloseReaders(child)
			if _, e = os.Stat(filepath.Join(h.Dir(), ".memeval/env.list")); !os.IsNotExist(e) {
				t.Fatal("credential file retained", e)
			}
			_, _ = io.WriteString(child.Stdin(), "hello\n")
			child.Stdin().Close()
			got, _ := io.ReadAll(child.Stdout())
			stderr, _ := io.ReadAll(child.Stderr())
			st, e := child.Wait(t.Context())
			if e != nil || st.Code != 0 || string(got) != "hello\n" || !strings.Contains(string(stderr), "diagnostic") {
				t.Fatal(st, e, string(got), string(stderr))
			}
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Fatal(e)
			}
			n := len(f.Calls(t))
			if e = b.Destroy(t.Context(), h); e != nil || len(f.Calls(t)) != n {
				t.Fatal("destroy not idempotent", e)
			}
			if p.Extra["destroyed"] != true {
				t.Fatal(p.Extra)
			}
			calls := f.Calls(t)
			found := false
			for _, a := range calls {
				joined := strings.Join(a, " ")
				if !strings.HasPrefix(joined, "--context orbstack ") || strings.Contains(joined, "fixture-secret") || strings.Contains(joined, "evaluator-must") {
					t.Fatal(a)
				}
				if contains(a, "-p") || contains(a, "--rm") || contains(a, "-t") && a[2] != "build" {
					t.Fatal(a)
				}
				if a[2] == "create" && contains(a, "fixture:image") {
					found = true
					for _, flag := range []string{"--read-only", "--cap-drop", "--security-opt", "--pids-limit", "--memory", "--memory-swap", "--init", "--interactive", "--stop-signal", "--stop-timeout"} {
						if !contains(a, flag) {
							t.Fatal("missing", flag, a)
						}
					}
					for _, key := range []string{"run", "episode", "env", "role", "owner_pid", "owner_start"} {
						if !strings.Contains(joined, "memeval."+key+"=") {
							t.Fatal(a)
						}
					}
				}
			}
			if !found {
				t.Fatal("no SUT create")
			}
			state, _ := os.ReadFile(filepath.Join(f.State, "state.json"))
			var v map[string]map[string]any
			json.Unmarshal(state, &v)
			for _, k := range []string{"container", "network", "volume", "image"} {
				if _, ok := v[k+"s"]["other-"+k]; !ok {
					t.Fatal("unrelated resource removed", k)
				}
			}
		})
	}
}
func TestResidueAndFailureRetry(t *testing.T) {
	for _, op := range []string{"create", "container inspect", "start", "rm", "network rm", "volume", "image inspect"} {
		t.Run(op, func(t *testing.T) {
			b, f := fixture(t)
			if _, e := b.Capabilities(t.Context()); e != nil {
				t.Fatal(e)
			}
			s := spec(f)
			s.Network = sandbox.NetworkOpen
			if op == "create" || op == "container inspect" || op == "image inspect" {
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": op})
			}
			if op == "volume" {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_VOLUME": "1"})
			}
			h, e := b.Prepare(t.Context(), s)
			if op == "create" || op == "container inspect" || op == "image inspect" {
				var u *sandbox.Unenforceable
				if !errors.As(e, &u) {
					t.Fatal(e)
				}
				f.Control(t, nil)
				for p, ep := range b.episodes {
					if ep.root == ep {
						if e = b.Destroy(t.Context(), p); e != nil {
							t.Fatal(e)
						}
					}
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if op == "start" {
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "start"})
				_, e = b.Spawn(t.Context(), h)
				if e == nil {
					t.Fatal("start failure accepted")
				}
				f.Control(t, nil)
			} else if op != "volume" {
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": op})
			}
			e = b.Destroy(t.Context(), h)
			if op != "start" {
				var u *sandbox.Unenforceable
				if !errors.As(e, &u) {
					t.Fatal("cleanup must reject scoring", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			f.Control(t, nil)
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Fatal("retry", e)
			}
		})
	}
}
func TestProfileVolumesAndMissingImage(t *testing.T) {
	b, f := fixture(t)
	s := spec(f)
	s.Container = nil
	if _, e := b.Prepare(t.Context(), s); e == nil || !strings.Contains(e.Error(), "environment image missing") {
		t.Fatal(e)
	}
	r := runConfig{Name: "one", Network: "a", Alias: "sut", Image: "fixture:image", User: "65532:65532", EnvFile: "/one/env", EP: "/one", Cwd: "/one/work", Command: []string{"/image/python", "/one/script"}}
	a, e := b.profile(r, nil, sandbox.NetworkNone)
	if e != nil {
		t.Fatal(e)
	}
	r.Name = "two"
	r.Network = "b"
	r.EP = "/two"
	r.Cwd = "/two/work"
	r.EnvFile = "/two/env"
	r.Command[1] = "/two/script"
	z, _ := b.profile(r, nil, sandbox.NetworkNone)
	if a != z {
		t.Fatal("instance paths changed policy hash")
	}
	for _, change := range []string{"image", "network", "flags"} {
		q := r
		mode := sandbox.NetworkNone
		switch change {
		case "image":
			q.Image = "different"
		case "network":
			mode = sandbox.NetworkOpen
		case "flags":
			q.Limits.PidsMax = 42
		}
		z, _ = b.profile(q, nil, mode)
		if a == z {
			t.Fatal(change)
		}
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_IMAGE_VOLUMES": "[\"/db\"]"})
	s = spec(f)
	if _, e = b.Prepare(t.Context(), s); e == nil || !strings.Contains(e.Error(), "VOLUME") {
		t.Fatal(e)
	}
	s.Container.Tmpfs = []string{"/db:rw,size=16m"}
	h, e := b.Prepare(t.Context(), s)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Destroy(t.Context(), h); e != nil {
		t.Fatal(e)
	}
}
func TestKillAndOOM(t *testing.T) {
	for _, oom := range []bool{false, true} {
		t.Run(map[bool]string{false: "kill", true: "oom"}[oom], func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			s.Command = []string{f.Python, "-c", "import time; time.sleep(60)"}
			if oom {
				f.Control(t, map[string]string{"FAKE_DOCKER_OOM": "1"})
				s.Command = []string{f.Python, "-c", "import time;time.sleep(.1);raise SystemExit(137)"}
			}
			h, e := b.Prepare(t.Context(), s)
			if e != nil {
				t.Fatal(e)
			}
			p, e := b.Spawn(t.Context(), h)
			if e != nil {
				t.Fatal(e)
			}
			defer sandbox.CloseReaders(p)
			if oom {
				c, e := jsonrpc.New(p, h.Dir(), jsonrpc.Options{})
				if e != nil {
					t.Fatal(e)
				}
				e = c.Call(t.Context(), "echo", nil, nil)
				var exited *jsonrpc.ProcessExited
				if !errors.As(e, &exited) || !exited.OOM || exited.Code != 137 {
					t.Fatal(e, exited)
				}
				c.Close()
			} else {
				if e = p.Kill(t.Context()); e != nil {
					t.Fatal(e)
				}
			}
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestDestroyOrderAndPrivateCredentialCleanup(t *testing.T) {
	b, f := fixture(t)
	s := spec(f)
	s.Services = []sandbox.Service{{Name: "fixture"}}
	s.KeepDir = true
	f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_VOLUME": "1"})
	h, e := b.Prepare(t.Context(), s)
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(h.Dir())
	root, _ := b.episode(h)
	names := []string{root.containerID}
	for _, role := range []string{"init", "sidecar", "sidecar"} {
		h2, e := b.PrepareServiceCommand(t.Context(), h, sandbox.Service{Name: role}, s.Command, s.Env, role)
		if e != nil {
			t.Fatal(e)
		}
		ep, _ := b.episode(h2)
		names = append(names, ep.containerID)
	}
	before := len(f.Calls(t))
	f.Control(t, nil)
	if e = b.Destroy(t.Context(), h); e != nil {
		t.Fatal(e)
	}
	ops := []string{}
	for _, a := range f.Calls(t)[before:] {
		if a[2] == "rm" {
			ops = append(ops, a[len(a)-1])
		}
		if a[2] == "network" && a[3] == "rm" {
			ops = append(ops, "network")
		}
		if a[2] == "volume" && a[3] == "rm" {
			ops = append(ops, "volume")
		}
	}
	want := []string{names[0], names[3], names[2], names[1], "network", "volume", "volume", "volume", "volume"}
	if strings.Join(ops, ",") != strings.Join(want, ",") {
		t.Fatal("destroy order", ops, want)
	}
	files, _ := filepath.Glob(filepath.Join(h.Dir(), ".memeval", "*.list"))
	if len(files) != 0 {
		t.Fatal("kept credential files", files)
	}
	report := b.Report(t.Context())
	if report["residue"].(map[string]any)["run_clean"] != true {
		t.Fatal(report)
	}
	df := report["system_df"].(map[string]any)
	if df["before"] == nil || df["after"] == nil {
		t.Fatal(df)
	}
	t.Log("destroy: SUT, sidecars reverse, network, recorded volumes, verify; unstarted env.list removed")
}

func TestServiceInspectRejectsPortPublication(t *testing.T) {
	b, f := fixture(t)
	s := spec(f)
	s.Services = []sandbox.Service{{Name: "fixture"}}
	h, e := b.Prepare(t.Context(), s)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Destroy(t.Context(), h)
	f.Control(t, map[string]string{"FAKE_DOCKER_PUBLISHED_PORT": "1"})
	c, e := b.PrepareCommand(t.Context(), h, []string{f.Python, "-c", "import time;time.sleep(60)"}, s.Env)
	if e != nil {
		t.Fatal(e)
	}
	p, e := b.Spawn(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	defer sandbox.CloseReaders(p)
	if e = b.AuditService(t.Context(), p.PID()); e == nil {
		t.Fatal("published ports accepted")
	}
}

func TestProbeImageCachedByStaticBinaryHash(t *testing.T) {
	b, f := fixture(t)
	c, e := b.Capabilities(t.Context())
	if e != nil || !c.Enforced {
		t.Fatal(c, e)
	}
	again := New(b.cfg)
	d, e := again.Capabilities(t.Context())
	if e != nil || !d.Enforced {
		t.Fatal(d, e)
	}
	if sandbox.Hash(c) != sandbox.Hash(d) {
		t.Fatal("caps signature includes per-probe identity")
	}
	builds := 0
	for _, a := range f.Calls(t) {
		if a[2] == "build" {
			builds++
			if !contains(a, "--pull=false") || !contains(a, "none") {
				t.Fatal(a)
			}
		}
	}
	if builds != 1 {
		t.Fatal("probe image rebuilt", builds)
	}
	broken := New(b.cfg)
	broken.cfg.LinuxBinarySHA256 = strings.Repeat("0", 64)
	c, e = broken.Capabilities(t.Context())
	if e != nil || c.Enforced || !strings.Contains(c.Reason, "SHA256 mismatch") {
		t.Fatal(c, e)
	}
}

func TestKillFailureStillRefusesAndCanBeCleaned(t *testing.T) {
	for _, op := range []string{"kill", "wait", "rm"} {
		t.Run(op, func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			s.Command = []string{f.Python, "-c", "import time;time.sleep(60)"}
			h, e := b.Prepare(t.Context(), s)
			if e != nil {
				t.Fatal(e)
			}
			p, e := b.Spawn(t.Context(), h)
			if e != nil {
				t.Fatal(e)
			}
			defer sandbox.CloseReaders(p)
			f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": op})
			e = p.Kill(t.Context())
			var u *sandbox.Unenforceable
			if !errors.As(e, &u) {
				t.Fatal(op, e)
			}
			f.Control(t, nil)
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Fatal(e)
			}
		})
	}
}
