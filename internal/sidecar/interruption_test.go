package sidecar

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/sandbox"
	"testing"
	"time"
)

type cancellationProofBackend struct{ diagnosticBackend }

func (cancellationProofBackend) WaitService(ctx context.Context, _ sandbox.Service, _ sandbox.Process) error {
	<-ctx.Done()
	return ctx.Err()
}

func detachedDeadline(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
		return fmt.Errorf("cleanup/audit must have an independent bounded deadline")
	}
	return nil
}
func (cancellationProofBackend) ServiceFacts(ctx context.Context, _ sandbox.Process) (sandbox.ServiceEvidence, error) {
	return sandbox.ServiceEvidence{}, detachedDeadline(ctx)
}
func (cancellationProofBackend) ServiceStopped(ctx context.Context, _ sandbox.Process) error {
	return detachedDeadline(ctx)
}

type cancellationProofProcess struct{ diagnosticProcess }

func (*cancellationProofProcess) Kill(ctx context.Context) error { return detachedDeadline(ctx) }

func TestCanceledContextStillAuditsAndCleansWithDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	item := &Instance{Process: &cancellationProofProcess{}, Evidence: &Evidence{Name: "worker", Ready: true}}
	g := &Group{Backend: cancellationProofBackend{}, Items: []*Instance{item}}
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"audit", g.Audit}, {"stop", g.Stop}, {"verify", g.VerifyStopped},
	} {
		t.Run(step.name, func(t *testing.T) {
			if err := step.run(ctx); err != nil {
				t.Fatal(err)
			}
			if item.Evidence.Error != "" {
				t.Fatal(item.Evidence.Error)
			}
		})
	}
	if !item.Evidence.Destroyed {
		t.Fatal("cleanup not verified")
	}
}

func TestInterruptedReadinessStillRequiresCleanTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	b := cancellationProofBackend{diagnosticBackend{process: &diagnosticProcess{}}}
	h := &sandbox.Prepared{Spec: sandbox.Spec{EpisodeDir: t.TempDir(), Services: []sandbox.Service{{Name: "worker", TimeoutMS: 1000}}}}
	g, err := Start(ctx, b, h, nil)
	if !errors.Is(err, context.Canceled) || g.Items[0].Evidence.Error != "" {
		t.Fatal("interruption misreported as isolation failure", err, g.Evidence())
	}
	if err = g.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err = g.VerifyStopped(ctx); err != nil {
		t.Fatal(err)
	}
	if !g.Items[0].Evidence.Destroyed {
		t.Fatal("cleanup not proved")
	}
}
