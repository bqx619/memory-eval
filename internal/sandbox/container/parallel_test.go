package container

import (
	"context"
	"fmt"
	"io"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container/governor"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParallelGatewayLifecycle(t *testing.T) {
	b, f := fixture(t)
	f.Control(t, map[string]string{"FAKE_DOCKER_GOVERNOR_METER": "1"})
	b.BeginRun("parallel-fixture")
	if e := b.ConfigureGovernor(t.TempDir(), governor.Defaults()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Concurrent polling exercises the same state read as runExternal's watcher.
	var polling sync.WaitGroup
	polling.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
				if _, e := b.PollGovernor(ctx); e != nil && ctx.Err() == nil {
					t.Error(e)
				}
			}
		}
	})
	var workers sync.WaitGroup
	for i := range 4 {
		workers.Go(func() {
			s := spec(f)
			s.Network = sandbox.NetworkGateway
			s.GatewayURL, _ = url.Parse("http://127.0.0.1:8317/v1")
			s.Services = []sandbox.Service{{Name: "test"}}
			s.Limits.CPUs = new(.5)
			h, e := b.Prepare(t.Context(), s)
			if e != nil {
				t.Error(e)
				return
			}
			defer b.Destroy(context.Background(), h)
			b.BindNamespace(h, fmt.Sprintf("worker-%d", i))
			svc := sandbox.Service{Name: "test", Transport: "tcp", Port: 9999, TimeoutMS: 5000, Env: s.Env, Command: []string{f.Python, "-c", "import time;time.sleep(100)"}, Ready: sandbox.ServiceReady{}}
			svc.Ready.Exec = []string{f.Python, "-c", "pass"}
			child, e := b.PrepareServiceCommand(t.Context(), h, svc, svc.Command, svc.Env, "sidecar")
			if e != nil {
				t.Error(e)
				return
			}
			service, e := b.Spawn(t.Context(), child)
			if e != nil {
				t.Error(e)
				return
			}
			defer sandbox.CloseReaders(service)
			go io.Copy(io.Discard, service.Stdout())
			go io.Copy(io.Discard, service.Stderr())
			if e = b.WaitService(t.Context(), svc, service); e != nil {
				t.Error(e)
				return
			}
			p, e := b.Spawn(t.Context(), h)
			if e != nil {
				t.Error(e)
				return
			}
			defer sandbox.CloseReaders(p)
			go io.Copy(io.Discard, p.Stdout())
			go io.Copy(io.Discard, p.Stderr())
			if _, e = io.WriteString(p.Stdin(), "request\n"); e != nil {
				t.Error(e)
			}
			if e = b.AuditService(t.Context(), service.PID()); e != nil {
				t.Error(e)
			}
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Error(e)
			}
			ep, _ := b.episode(h)
			if r, e := b.verifyEpisodeEmpty(t.Context(), ep); e != nil || r["run_clean"] != true {
				t.Error(r, e)
			}
		})
	}
	workers.Wait()
	cancel()
	polling.Wait()
	if e := b.FinishRun(t.Context()); e != nil {
		t.Fatal(e)
	}
	billing, err := b.GovernorBilling()
	if err != nil || billing.Calls != 4 || billing.Prompt != 400 || len(billing.ByEpisode) != 4 {
		t.Fatal("concurrent IP attribution lost usage", billing, err)
	}
	for _, ep := range billing.ByEpisode {
		if !strings.HasPrefix(ep.NS, "worker-") || ep.Calls != 1 || ep.Prompt != 100 {
			t.Fatal("incorrect episode attribution", ep)
		}
	}
	created, connected, disconnected := 0, 0, 0
	for _, args := range f.Calls(t) {
		text := strings.Join(args, " ")
		if args[2] == "create" && contains(args, "__container-governor") {
			created++
		}
		if strings.Contains(text, "network connect --alias llm-gateway") {
			connected++
		}
		if strings.Contains(text, "network disconnect") {
			disconnected++
		}
		if args[2] == "create" && contains(args, "memeval.role=sut") && !strings.Contains(text, "--cpus 0.5") {
			t.Fatal(args)
		}
	}
	if created != 1 || connected != 4 || disconnected != 4 {
		t.Fatal(created, connected, disconnected)
	}
	if got := b.Report(t.Context())["residue"].(map[string]any)["run_clean"]; got != true {
		t.Fatal(got)
	}
}

func TestCPUArgsAndProfile(t *testing.T) {
	b := New(Config{})
	r := runConfig{Image: "fixture:image", User: "65532:65532", Network: "none"}
	want, e := b.profile(r, nil, sandbox.NetworkNone)
	if e != nil {
		t.Fatal(e)
	}
	for _, value := range []float64{.25, 2.5, 0, -1} {
		r.Limits.CPUs = new(value)
		args, e := b.runArgs(r)
		if value <= 0 {
			if e == nil {
				t.Fatal("invalid cpus accepted")
			}
			continue
		}
		if e != nil || !contains(args, "--cpus") {
			t.Fatal(args, e)
		}
		if got, e := b.profile(r, nil, sandbox.NetworkNone); e != nil || got == want {
			t.Fatal("cpus absent from policy hash", e)
		}
	}
}
