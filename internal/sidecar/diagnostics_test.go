package sidecar

import (
	"context"
	"errors"
	"io"
	"memeval/internal/config/redact"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/none"
	"strings"
	"testing"
)

// Exercise Start/Audit/Stop error preservation without launching a runtime.
type diagnosticBackend struct {
	none.Backend
	process              *diagnosticProcess
	factsErr, stoppedErr error
}

func (b diagnosticBackend) AuditService(context.Context, int) error { return b.factsErr }

var _ ServiceBackend = diagnosticBackend{}

func (b diagnosticBackend) PrepareServiceCommand(_ context.Context, h sandbox.Handle, _ sandbox.Service, _ []string, _ map[string]string, _ string) (sandbox.Handle, error) {
	return h, nil
}
func (b diagnosticBackend) Spawn(context.Context, sandbox.Handle) (sandbox.Process, error) {
	return b.process, nil
}
func (b diagnosticBackend) WaitService(ctx context.Context, _ sandbox.Service, _ sandbox.Process) error {
	<-ctx.Done()
	return errors.New("docker start: signal: killed")
}
func (b diagnosticBackend) ExecService(context.Context, sandbox.Process, []string, io.Writer) error {
	return nil
}
func (b diagnosticBackend) ServiceFacts(context.Context, sandbox.Process) (sandbox.ServiceEvidence, error) {
	return sandbox.ServiceEvidence{}, b.factsErr
}
func (b diagnosticBackend) ServiceStopped(context.Context, sandbox.Process) error {
	return b.stoppedErr
}

type diagnosticProcess struct {
	sandbox.Process
	killErr error
}

func (p *diagnosticProcess) Stdin() io.WriteCloser { return discardCloser{} }
func (p *diagnosticProcess) Stdout() io.Reader {
	return strings.NewReader("\x1b[32mVerifying connection\x1b[0m\n")
}
func (p *diagnosticProcess) Stderr() io.Reader          { return strings.NewReader("") }
func (p *diagnosticProcess) PID() int                   { return 0 }
func (p *diagnosticProcess) Kill(context.Context) error { return p.killErr }

type discardCloser struct{}

func (discardCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardCloser) Close() error                { return nil }

func TestReadinessTimeoutKeepsSanitizedTail(t *testing.T) {
	p := &diagnosticProcess{}
	b := diagnosticBackend{process: p}
	h := &sandbox.Prepared{Spec: sandbox.Spec{EpisodeDir: t.TempDir(), Services: []sandbox.Service{{Name: "api", TimeoutMS: 50}}}}
	g, err := Start(t.Context(), b, h, nil)
	if err == nil || !strings.Contains(err.Error(), "api readiness timeout after 0.05s") || strings.Contains(err.Error(), "signal: killed") {
		t.Fatal(err)
	}
	if err := g.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	tail := g.Items[0].tail.String()
	if strings.Contains(tail, "\x1b") || !strings.Contains(tail, "Verifying connection") {
		t.Fatal(tail)
	}
	if strings.Contains(g.Items[0].Evidence.Error, "\x1b") {
		t.Fatal(g.Evidence())
	}
}
func TestAuditStopAndVerifyEvidence(t *testing.T) {
	for _, kind := range []string{"audit", "stop", "verify"} {
		t.Run(kind, func(t *testing.T) {
			ctx := redact.With(t.Context(), redact.New([]string{"credential-value"}))
			p := &diagnosticProcess{}
			b := diagnosticBackend{process: p}
			err := errors.New("\x1b[31mfirst credential-value\x1b[0m\nsecond line")
			switch kind {
			case "audit":
				b.factsErr = err
			case "stop":
				p.killErr = err
			case "verify":
				b.stoppedErr = err
			}
			item := &Instance{Spec: sandbox.Service{Name: "api"}, Process: p, Evidence: &Evidence{Name: "api", Ready: true}}
			g := &Group{Backend: b, Items: []*Instance{item}}
			switch kind {
			case "audit":
				err = g.Audit(ctx)
			case "stop":
				err = g.Stop(ctx)
			case "verify":
				err = g.VerifyStopped(ctx)
			}
			if err == nil || item.Evidence.Error != "first [REDACTED]\nsecond line" {
				t.Fatal(err, item.Evidence.Error)
			}
			item.failure(ctx, errors.New("cleanup failure"))
			if item.Evidence.Error != "first [REDACTED]\nsecond line" {
				t.Fatal("initial trigger overwritten")
			}
		})
	}
}
