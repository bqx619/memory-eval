package sidecar

import (
	"context"
	"fmt"
	"memeval/internal/episodelog"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sandbox/none"
	"memeval/internal/testoracle"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListenerAudit(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{{"p123\nn127.0.0.1:12345\n", true}, {"p12\nn[::1]:12345\n", true}, {"p12\nn*:12345\n", false}, {"p12\nn0.0.0.0:12345\n", false}, {"p12\nn[::]:12345\n", false}, {"p12\nn192.168.1.2:12345\n", false}, {"", false}, {"p1\nn127.0.0.1:99\n", false}} {
		_, _, err := ParseListeners([]byte(tc.raw), 12345)
		if (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.raw, err)
		}
	}
}
func TestSidecarHelper(t *testing.T) {
	args := os.Args
	idx := -1
	for i, a := range args {
		if a == "--service-fixture" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	mode, address := args[idx+1], args[idx+2]
	if strings.HasPrefix(mode, "ready-") {
		c, e := net.Dial(strings.TrimPrefix(mode, "ready-"), address)
		if e != nil {
			os.Exit(1)
		}
		c.Close()
		os.Exit(0)
	}
	fmt.Fprintln(os.Stdout, mode+" fixture stdout "+strings.Repeat("x", 20000))
	fmt.Fprintln(os.Stderr, mode+" fixture stderr")
	if mode == "init" {
		if err := os.WriteFile(address, []byte("initialized"), 0600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	l, err := net.Listen(mode, address)
	if err != nil {
		os.Exit(3)
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			os.Exit(0)
		}
		conn.Close()
	}
}
func TestSandboxedServicesOrderAuditAndCleanup(t *testing.T) { testServices(t, false) }
func TestContainerServicesOrderAuditAndCleanup(t *testing.T) { testServices(t, true) }
func testServices(t *testing.T, fakeContainer bool) {
	exe, _ := os.Executable()
	exe, _ = sandbox.Real(exe)
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "loopback", true: "wildcard"}[bad], func(t *testing.T) {
			var b sandbox.Sandbox = none.Backend{}
			if fakeContainer {
				f := testoracle.FakeDocker(t)
				if bad {
					f.Control(t, map[string]string{"FAKE_DOCKER_PUBLISHED_PORT": "1"})
				}
				b = container.New(container.Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Platform: f.Platform})
			}
			ep, err := sandbox.NewEpisode()
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(ep)
			reserve, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := uint16(reserve.Addr().(*net.TCPAddr).Port)
			address := reserve.Addr().String()
			reserve.Close()
			listenAddress := address
			if bad {
				listenAddress = strings.Replace(address, "127.0.0.1", "0.0.0.0", 1)
			}
			args := func(mode, address string) []string {
				return []string{exe, "-test.run=^TestSidecarHelper$", "--", "--service-fixture", mode, address}
			}
			env := map[string]string{"PATH": "/usr/bin:/bin", "HOME": filepath.Join(ep, "home")}
			services := []sandbox.Service{{Name: "postgres", Kind: "postgres", Transport: "unix", Command: args("unix", filepath.Join(ep, "sock/pg.sock")), Init: [][]string{args("init", filepath.Join(ep, "init-proof"))}, Ready: sandbox.ServiceReady{Unix: filepath.Join(ep, "sock/pg.sock")}, Env: env, TimeoutMS: 3000}, {Name: "qdrant", Kind: "qdrant", Transport: "tcp", Command: args("tcp4", listenAddress), Ready: sandbox.ServiceReady{TCP: address}, Port: port, Env: env, TimeoutMS: 3000}}
			s := sandbox.Spec{EpisodeDir: ep, Command: []string{exe}, EnvRoot: filepath.Dir(exe), Env: env, Network: sandbox.NetworkNone, Services: services}
			if fakeContainer {
				s.Container = &sandbox.ContainerSpec{Image: sandbox.Image{ID: "fixture:image"}, EnvID: "sidecars"}
				for i := range s.Services {
					v := &s.Services[i]
					mode, address := "unix", v.Ready.Unix
					if v.Transport == "tcp" {
						mode, address = "tcp4", v.Ready.TCP
					}
					v.Ready.Exec = args("ready-"+mode, address)
				}
			}
			h, err := b.Prepare(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Destroy(context.Background(), h)
			var sinks Sinks
			var logs *episodelog.Run
			var episode *episodelog.Episode
			if !bad {
				logs, err = episodelog.New(t.Context(), t.TempDir(), episodelog.Options{})
				if err != nil {
					t.Fatal(err)
				}
				defer logs.Close()
				episode, err = logs.Episode("services")
				if err != nil {
					t.Fatal(err)
				}
				sinks = episode
			}
			group, err := Start(t.Context(), b, h, sinks)
			if (err != nil) != bad {
				t.Fatalf("bad=%v: %v", bad, err)
			}
			if _, e := os.Stat(filepath.Join(ep, "init-proof")); e != nil && !(fakeContainer && bad) {
				t.Fatal("init not executed", e)
			}
			if (!fakeContainer || !bad) && (len(group.Items) != 2 || !group.Items[0].Evidence.Ready) {
				t.Fatal(group.Evidence())
			}
			if err == nil && !fakeContainer && !group.Items[1].Evidence.LoopbackOnly {
				t.Fatal("missing audit")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := group.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if err := b.Destroy(ctx, h); err != nil {
				t.Fatal(err)
			}
			for _, item := range group.Items {
				if !item.Evidence.Destroyed || !fakeContainer && sandbox.Alive(item.Evidence.PID) || fakeContainer && item.Evidence.PID != 0 {
					t.Fatal("residual service")
				}
			}
			if !bad {
				if err = logs.Close(); err != nil {
					t.Fatal(err)
				}
				for name, mode := range map[string]string{"init-postgres-1.log": "init", "sidecar-postgres.log": "unix", "sidecar-qdrant.log": "tcp4"} {
					data, e := os.ReadFile(filepath.Join(episode.Dir(), name))
					if e != nil {
						t.Fatal(e)
					}
					for _, stream := range []string{"stdout", "stderr"} {
						if !strings.Contains(string(data), mode+" fixture "+stream) {
							t.Fatalf("%s lost %s", name, stream)
						}
					}
				}
				for _, item := range group.Items {
					if len(item.tail.String()) != 16384 {
						t.Fatal("ring capacity changed")
					}
				}
			}
		})
	}
}
func TestReadyRejectsRemoteHTTP(t *testing.T) {
	if err := Ready(t.Context(), sandbox.ServiceReady{HTTP: &sandbox.HTTPReady{URL: "http://192.0.2.1:8080"}}); err == nil {
		t.Fatal("external readiness allowed")
	}
}

func TestUnmanagedListenerAuditAndCleanup(t *testing.T) {
	b := none.Backend{}
	exe, _ := os.Executable()
	exe, _ = sandbox.Real(exe)
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "loopback", true: "wildcard"}[bad], func(t *testing.T) {
			ep, err := sandbox.NewEpisode()
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(ep)
			reserve, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := uint16(reserve.Addr().(*net.TCPAddr).Port)
			address := reserve.Addr().String()
			reserve.Close()
			listen := address
			if bad {
				listen = strings.Replace(address, "127.0.0.1", "0.0.0.0", 1)
			}
			service := sandbox.Service{Name: "unmanaged", Unmanaged: true, Transport: "tcp", Port: port, Ready: sandbox.ServiceReady{TCP: address}, TimeoutMS: 3000}
			spec := sandbox.Spec{EpisodeDir: ep, Command: []string{exe, "-test.run=^TestSidecarHelper$", "--", "--service-fixture", "tcp4", listen}, EnvRoot: filepath.Dir(exe), Env: map[string]string{"HOME": ep}, Network: sandbox.NetworkNone, Services: []sandbox.Service{service}}
			h, err := b.Prepare(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Destroy(context.Background(), h)
			g, err := Start(t.Context(), b, h, nil)
			if err != nil {
				t.Fatal(err)
			}
			if g.Items[0].Process != nil || g.Items[0].Evidence.Ready {
				t.Fatal("unmanaged service spawned/waited before SUT")
			}
			child, err := b.Spawn(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.CloseReaders(child)
			err = g.AwaitUnmanaged(t.Context())
			if (err != nil) != bad {
				t.Fatalf("bad=%v error=%v", bad, err)
			}
			if !bad {
				if err = g.Audit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if g.VerifyStopped(t.Context()) == nil {
					t.Fatal("live listener reported destroyed")
				}
			}
			if err = b.Destroy(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			if err = g.VerifyStopped(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !g.Items[0].Evidence.Destroyed {
				t.Fatal(g.Evidence())
			}
			if !bad && g.Audit(t.Context()) == nil {
				t.Fatal("RPC audit accepted vanished listener")
			}
		})
	}
}
