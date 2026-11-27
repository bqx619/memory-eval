package selftest

import (
	"encoding/json"
	"errors"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var _ Selftester = (*container.Backend)(nil)

func TestContainerSelftestRelayDestroyedDocker29(t *testing.T) {
	for _, remains := range []bool{false, true} {
		name := "removed"
		if remains {
			name = "remains"
		}
		t.Run(name, func(t *testing.T) {
			f, m, l, b := containerFixture(t, sandbox.NetworkGateway)
			if remains {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_RELAY": "1"})
			}
			r, _, err := Run(t.Context(), b, m, l, t.TempDir())
			if r == nil || (err != nil) != remains || r.Checks["relay_destroyed"].Passed != !remains || r.Passed != !remains {
				t.Fatal(r, err)
			}
			var relay string
			for _, args := range f.Calls(t) {
				if len(args) > 2 && args[2] == "inspect" {
					t.Fatal("untyped inspect in lifecycle", args)
				}
				if slices.Contains(args, "create") && slices.Contains(args, "memeval.role=governor") {
					relay = args[slices.Index(args, "--name")+1]
				}
			}
			if relay == "" {
				t.Fatal("relay was not created")
			}
			_, err = b.ImageCommand(t.Context(), "container", "inspect", relay)
			if remains {
				if err != nil {
					t.Fatal("residual relay must still exist", err)
				}
			} else {
				var diagnostic *container.CommandError
				if !errors.As(err, &diagnostic) || diagnostic.Stderr != "Error response from daemon: No such container: "+relay+"\n" {
					t.Fatal("unexpected typed inspect diagnostic", err)
				}
				t.Logf("absent relay stderr=%q", diagnostic.Stderr)
			}
			t.Logf("remains=%v passed=%v relay_destroyed=%v checks=%d", remains, r.Passed, r.Checks["relay_destroyed"].Passed, len(r.Checks))
		})
	}
}

func containerFixture(t *testing.T, mode sandbox.NetworkMode) (testoracle.DockerFixture, *sut.Manifest, *sut.Lock, *container.Backend) {
	t.Helper()
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	m := &sut.Manifest{Name: "selftest-fixture", Dir: t.TempDir(), DefaultEnvFile: filepath.Join(t.TempDir(), "no.env")}
	m.Sandbox.Backend = "container"
	m.Sandbox.Network = mode
	m.Sandbox.Limits.PidsMax = 73
	m.Sandbox.Limits.TimeoutS = 30
	m.Sandbox.Limits.MaxRSS = 64 << 20
	m.Process.Command = []string{"/opt/memeval/env/.venv/bin/python", "/opt/memeval/env/project/echo.py"}
	m.Process.Env = map[string]string{"PUBLIC_CONFIG": "fixture"}
	m.Gateway, _ = sut.ParseLLMGateway("http://127.0.0.1:8317/v1", "fake-gateway-key", "fixture-model", "low")
	if mode != sandbox.NetworkNone {
		m.LLM = sut.LLM{Required: true, Env: map[string]string{"OPENAI_BASE_URL": "{llm.base_url}", "OPENAI_API_KEY": "{llm.api_key}"}}
	}
	l := &sut.Lock{Name: m.Name, Version: 3, ImageRoot: sut.ImageRoot, Executable: "/opt/memeval/env/.venv/bin/python", VenvRealPath: "/opt/memeval/env/.venv", Image: sandbox.Image{ID: "fixture:image"}}
	return f, m, l, container.New(container.DefaultConfig())
}

func TestContainerSelftestModesAndTemplate(t *testing.T) {
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkOpen, sandbox.NetworkGateway} {
		t.Run(string(mode), func(t *testing.T) {
			f, m, l, b := containerFixture(t, mode)
			defer func() {
				if err := b.FinishRun(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			dir := t.TempDir()
			r, path, e := Run(t.Context(), b, m, l, dir)
			if e != nil || r == nil || !r.Passed || r.Version != 3 {
				t.Fatalf("%+v %v", r, e)
			}
			if _, e = Recent(dir, r.Signature, time.Now()); e != nil {
				t.Fatal(e)
			}
			raw, _ := os.ReadFile(path)
			if strings.Contains(string(raw), "fake-gateway-key") {
				t.Fatal("credential in record")
			}
			if len(r.Checks) != len(ContainerExpectations(mode, nil)) {
				t.Fatal(r.Checks)
			}
			for k, c := range r.Checks {
				if !c.Passed || !c.ControlPassed {
					t.Fatal(k, c)
				}
			}
			h, e := m.Prepare(t.Context(), b, l, "normal", false)
			if e != nil {
				t.Fatal(e)
			}
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Fatal(e)
			}
			var probe, normal []string
			for _, a := range f.Calls(t) {
				if slices.Contains(a, "create") && slices.Contains(a, "fixture:image") {
					if slices.Contains(a, "__container-probe") {
						probe = a
					} else {
						normal = a
					}
				}
				if strings.Contains(strings.Join(a, " "), "fake-gateway-key") {
					t.Fatal("credential in argv")
				}
			}
			if probe == nil || normal == nil {
				t.Fatal("missing create calls")
			}
			for _, key := range []string{"--platform", "--user", "--cap-drop", "--security-opt", "--pids-limit", "--shm-size", "--tmpfs", "--stop-timeout"} {
				p, n := slices.Index(probe, key), slices.Index(normal, key)
				if p < 0 || n < 0 || probe[p+1] != normal[n+1] {
					t.Fatal(key, probe, normal)
				}
			}
			for _, flag := range []string{"--read-only", "--init", "--interactive"} {
				if !slices.Contains(probe, flag) || !slices.Contains(normal, flag) {
					t.Fatal(flag)
				}
			}
			binaryPath, e := filepath.EvalSymlinks(f.LinuxBinary)
			if e != nil {
				t.Fatal(e)
			}
			binary := false
			for _, v := range probe {
				binary = binary || v == binaryPath+":/memeval:ro"
			}
			if !binary {
				t.Fatal(probe)
			}
			normalize := func(args []string) []string {
				args = append([]string(nil), args[:slices.Index(args, "fixture:image")+1]...)
				envFile := args[slices.Index(args, "--env-file")+1]
				ep := filepath.Dir(filepath.Dir(envFile))
				var out []string
				for i := 0; i < len(args); i++ {
					if args[i] == "-v" && args[i+1] == binaryPath+":/memeval:ro" {
						i++
						continue
					}
					v := strings.ReplaceAll(args[i], ep, "{ep}")
					if i > 0 && args[i-1] == "--name" {
						v = "{name}"
					}
					if i > 0 && args[i-1] == "--network" && v != "none" {
						v = "{network}"
					}
					if strings.HasPrefix(v, "memeval.episode=") {
						v = "memeval.episode={episode}"
					}
					out = append(out, v)
				}
				return out
			}
			if a, b := normalize(probe), normalize(normal); !reflect.DeepEqual(a, b) {
				t.Fatalf("probe and SUT parameter templates differ:\n%v\n%v", a, b)
			}
			if mode == sandbox.NetworkGateway {
				if !r.Checks["gateway_connect"].Passed || !r.Checks["relay_destroyed"].Passed {
					t.Fatal(r.Checks)
				}
			}
			t.Logf("mode=%s checks=%d record=%s", mode, len(r.Checks), path)
		})
	}
}

func TestContainerSelftestProbeFailures(t *testing.T) {
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkOpen, sandbox.NetworkGateway} {
		t.Run(string(mode), func(t *testing.T) {
			f, m, l, b := containerFixture(t, mode)
			for _, name := range []string{"gold_absent", "write_rootfs_fails", "environment", "tcp4_control", "udp_control", "dns_external", "https_external", "pids_limit", "mounts_expected", "gateway_connect"} {
				if name == "gateway_connect" && mode != sandbox.NetworkGateway {
					continue
				}
				t.Run(name, func(t *testing.T) {
					f.Control(t, map[string]string{"FAKE_DOCKER_PROBE_FAIL": name})
					r, path, e := Run(t.Context(), b, m, l, t.TempDir())
					if e == nil || !strings.Contains(e.Error(), name) || r == nil || r.Passed || r.Checks[name].Passed {
						t.Fatalf("%+v %v", r, e)
					}
					if _, e = os.Stat(path); e != nil {
						t.Fatal(e)
					}
				})
			}
		})
	}
}

func TestContainerSelftestHostResidueAndPortAudit(t *testing.T) {
	for _, key := range []string{"FAKE_DOCKER_LEAVE_VOLUME", "FAKE_DOCKER_PUBLISHED_PORT"} {
		t.Run(key, func(t *testing.T) {
			f, m, l, b := containerFixture(t, sandbox.NetworkNone)
			if caps, e := b.Capabilities(t.Context()); e != nil || !caps.Enforced {
				t.Fatal(caps, e)
			}
			f.Control(t, map[string]string{key: "1"})
			r, _, e := Run(t.Context(), b, m, l, t.TempDir())
			if e == nil || r.Passed {
				t.Fatal(r, e)
			}
			name := "volumes_none"
			if key == "FAKE_DOCKER_PUBLISHED_PORT" {
				name = "no_published_ports"
			}
			if r.Checks[name].Passed {
				t.Fatal(name, r.Checks)
			}
		})
	}
}

func TestContainerSelftestDockerInitMount(t *testing.T) {
	for _, key := range []string{"", "FAKE_DOCKER_INIT_MOUNT_RW", "FAKE_DOCKER_INIT_INSPECT_RW"} {
		name := key
		if name == "" {
			name = "readonly init"
		}
		t.Run(name, func(t *testing.T) {
			f, m, l, b := containerFixture(t, sandbox.NetworkNone)
			if key != "" {
				f.Control(t, map[string]string{key: "1"})
			}
			r, _, err := Run(t.Context(), b, m, l, t.TempDir())
			if key == "" {
				if err != nil || !r.Passed || !strings.Contains(r.Sandboxed.Facts["mountinfo"].(string), "/usr/sbin/docker-init ro,") {
					t.Fatal(r, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "mounts_expected") || r.Checks["mounts_expected"].Passed {
				t.Fatal("writable init accepted", r, err)
			}
		})
	}
}

func TestContainerSelftestRelayAudit(t *testing.T) {
	for _, key := range []string{"FAKE_DOCKER_RELAY_EXTRA_TARGET", "FAKE_DOCKER_LEAVE_RELAY"} {
		t.Run(key, func(t *testing.T) {
			f, m, l, b := containerFixture(t, sandbox.NetworkGateway)
			f.Control(t, map[string]string{key: "1"})
			r, _, e := Run(t.Context(), b, m, l, t.TempDir())
			if e == nil || r == nil || r.Passed {
				t.Fatal(r, e)
			}
			name := "relay_single_target"
			if key == "FAKE_DOCKER_LEAVE_RELAY" {
				name = "relay_destroyed"
			}
			if r.Checks[name].Passed {
				t.Fatal(name, r.Checks)
			}
		})
	}
}

func TestContainerSignatureLinuxBinaryAndOldRecords(t *testing.T) {
	f, m, l, b := containerFixture(t, sandbox.NetworkNone)
	caps, e := b.Capabilities(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	a, e := Signature(m, l, caps)
	if e != nil {
		t.Fatal(e)
	}
	file, e := os.OpenFile(f.LinuxBinary, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	file.Write([]byte("changed"))
	file.Close()
	next, e := Signature(m, l, caps)
	if e != nil || a == next {
		t.Fatal(a, next, e)
	}
	spec, e := m.Spec(l, t.TempDir(), "changed", false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Selftest(t.Context(), spec, container.ProbeOptions{LinuxBinarySHA256: "old"}); e == nil || !strings.Contains(e.Error(), "Signature") {
		t.Fatal(e)
	}
	os.Remove(f.LinuxBinary)
	if _, e = Signature(m, l, caps); e == nil || !strings.Contains(e.Error(), "scripts/build_linux_memeval.sh") {
		t.Fatal(e)
	}
	for _, v := range []int{0, 1, 2} {
		r := Record{Version: v, Passed: true, Status: "passed", Signature: "old", CreatedAt: time.Now()}
		r.SHA256 = r.hash()
		raw, _ := json.Marshal(r)
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "selftest-old.json"), raw, 0600)
		if _, e = Recent(dir, "old", time.Now()); e == nil {
			t.Fatal("old record accepted", v)
		}
	}
}

func TestContainerSelftestStartsSidecars(t *testing.T) {
	f, m, _, b := containerFixture(t, sandbox.NetworkNone)
	dir := m.Dir
	for n, v := range map[string]string{"requirements.in": "", "requirements.lock.txt": "", "server.py": "import socket,sys\ns=socket.socket();s.bind(('0.0.0.0',int(sys.argv[1])));s.listen()\nwhile True:\n c,a=s.accept();c.close()\n"} {
		if e := os.WriteFile(filepath.Join(dir, n), []byte(v), 0600); e != nil {
			t.Fatal(e)
		}
	}
	echo, e := os.ReadFile(filepath.Join(dotenv.Root("."), "suts/echo/echo_bridge.py"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "echo.py"), echo, 0600)
	manifest := `{"schema_version":"3","name":"selftest-sidecar","adapter":{"kind":"generic","transport":"jsonrpc-stdio"},"runtime":{"kind":"uv-python","python":"3.12","requirements":"requirements.in","requirements_lock":"requirements.lock.txt"},"process":{"command":["{venv}/bin/python","{project}/echo.py"]},"sandbox":{"backend":"container","network":"none"},"ready":{"strategy":"sync"}}`
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0600)
	m, e = sut.Load(filepath.Join(dir, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	m.CacheDir = t.TempDir()
	l, e := sut.Install(t.Context(), m, sut.InstallOptions{Cache: m.CacheDir, Offline: true})
	if e != nil {
		t.Fatal(e)
	}
	m.Sidecars = []sut.Sidecar{{Name: "fixture", Kind: "generic", Port: 18888, Command: []string{"{venv}/bin/python", "{project}/server.py", "{port}"}, Stop: "SIGTERM", Ready: sandbox.ServiceReady{TCP: "127.0.0.1:{port}", TimeoutS: 5}}}
	r, _, e := Run(t.Context(), b, m, l, t.TempDir())
	if e != nil || !r.Passed {
		t.Fatal(r, e)
	}
	for _, k := range []string{"sidecar_fixture_connect", "sidecar_fixture_dns", "network_internal", "container_destroyed"} {
		if !r.Checks[k].Passed {
			t.Fatal(k, r.Checks)
		}
	}
	ready := false
	for _, a := range f.Calls(t) {
		ready = ready || slices.Contains(a, "__container-ready") && slices.Contains(a, "--service")
	}
	if !ready {
		t.Fatal("missing container readiness")
	}
}
