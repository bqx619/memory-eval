package container

import (
	"context"
	"io"
	"memeval/internal/sandbox"
	"testing"
	"time"
)

func TestServiceAuditAfterSandboxTimeout(t *testing.T) {
	b, f := fixture(t)
	s := spec(f)
	s.Limits.Timeout = 150 * time.Millisecond
	s.Services = []sandbox.Service{{Name: "worker"}}
	h, err := b.Prepare(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Destroy(context.Background(), h) })
	svc := sandbox.Service{Name: "worker", Command: []string{f.Python, "-c", "import time;time.sleep(100)"}, Env: s.Env}
	child, err := b.PrepareServiceCommand(t.Context(), h, svc, svc.Command, svc.Env, "sidecar")
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.Spawn(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.CloseReaders(p)
	go io.Copy(io.Discard, p.Stdout())
	go io.Copy(io.Discard, p.Stderr())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err = p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	// Wait and the post-RPC audit can race the timeout's removeContainer.
	facts, err := b.ServiceFacts(ctx, p)
	if err != nil {
		t.Fatalf("our own timeout teardown became an isolation failure: %v", err)
	}
	if !facts.Stopped || !facts.Timeout || !b.TimedOut(h) {
		t.Fatalf("missing timeout teardown facts: %+v", facts)
	}
	if err = b.Destroy(ctx, h); err != nil {
		t.Fatal(err)
	}
	if h.EffectiveConfig()["timeout"] != true || h.EffectiveConfig()["destroyed"] != true {
		t.Fatal(h.EffectiveConfig())
	}
}

func TestServiceAuditStillRejectsUnprovenTeardown(t *testing.T) {
	for _, mode := range []string{"unexpected_disappearance", "residue", "remove_error", "timeout_remove_error", "inspect_error"} {
		t.Run(mode, func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			s.Services = []sandbox.Service{{Name: "worker"}}
			h, err := b.Prepare(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				f.Control(t, nil)
				b.Destroy(context.Background(), h)
			})
			svc := sandbox.Service{Name: "worker", Command: []string{f.Python, "-c", "import time;time.sleep(100)"}, Env: s.Env}
			child, err := b.PrepareServiceCommand(t.Context(), h, svc, svc.Command, svc.Env, "sidecar")
			if err != nil {
				t.Fatal(err)
			}
			p, err := b.Spawn(t.Context(), child)
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.CloseReaders(p)
			go io.Copy(io.Discard, p.Stdout())
			go io.Copy(io.Discard, p.Stderr())
			switch mode {
			case "unexpected_disappearance":
				ep, _ := b.episode(child)
				if _, err = b.call(t.Context(), "rm", "-f", ep.name); err != nil {
					t.Fatal(err)
				}
			case "residue":
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_SIDECAR": "1"})
				if err = p.Kill(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "remove_error", "timeout_remove_error":
				if mode == "timeout_remove_error" {
					ep, _ := b.episode(child)
					ep.timedOut.Store(true)
					ep.root.timedOut.Store(true)
				}
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "rm"})
				if err = p.Kill(t.Context()); err == nil {
					t.Fatal("failed removal accepted")
				}
				if mode == "timeout_remove_error" {
					// Even if a retry successfully removes it, the background
					// deadline failure must reach the publication gate.
					f.Control(t, nil)
					if err = b.Destroy(t.Context(), h); err == nil {
						t.Fatal("asynchronous cleanup failure was forgotten")
					}
				}
			case "inspect_error":
				if err = p.Kill(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "container inspect"})
			}
			if _, err = b.ServiceFacts(t.Context(), p); err == nil {
				t.Fatal("unknown/residual service accepted as destroyed")
			}
		})
	}
}
