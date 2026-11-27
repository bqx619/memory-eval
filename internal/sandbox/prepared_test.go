package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type cleanupProcess struct {
	Process
	steps *[]string
}

func (p *cleanupProcess) Kill(context.Context) error { *p.steps = append(*p.steps, "kill"); return nil }
func (p *cleanupProcess) CloseReaders()              { *p.steps = append(*p.steps, "readers") }

func TestExternalPreparedTeardownAndOptionalReaders(t *testing.T) {
	// These executable/runtime paths intentionally do not exist on the host.
	h, err := PrepareEpisode(t.Context(), Spec{Command: []string{"/image/env/bin/sut"}, EnvRoot: "/image/env", Network: NetworkNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(h.Dir()) })
	h.External = true
	derived, err := DeriveCommand(h, []string{"/image/env/bin/sidecar"}, nil)
	if err != nil || !derived.External || !derived.Derived {
		t.Fatal(derived, err)
	}
	steps := []string{}
	p := &cleanupProcess{steps: &steps}
	h.Child = p
	h.Teardown = func(context.Context) error { steps = append(steps, "teardown"); return nil }
	derived.Teardown = func(context.Context) error { steps = append(steps, "derived"); return nil }
	// Cancelled host commands must not run: External skips host ps/path cleanup.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = Destroy(ctx, h); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"teardown", "kill", "derived"}) {
		t.Fatal(steps)
	}
	if _, err = os.Stat(h.Dir()); !os.IsNotExist(err) {
		t.Fatal("episode retained", err)
	}
	if err = Destroy(ctx, h); err != nil || len(steps) != 3 {
		t.Fatal("destroy not idempotent", err, steps)
	}
	CloseReaders(p)
	CloseReaders(nil)
	if steps[3] != "readers" {
		t.Fatal(steps)
	}
}

func TestTeardownFailurePreservesHandleForRetry(t *testing.T) {
	h, err := PrepareEpisode(t.Context(), Spec{Command: []string{"/image/sut"}, Network: NetworkNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(h.Dir()) })
	h.External = true
	steps := []string{}
	h.Child = &cleanupProcess{steps: &steps}
	h.Teardown = func(context.Context) error { return errors.New("fixture cleanup failed") }
	if err = Destroy(t.Context(), h); err == nil || len(steps) != 0 {
		t.Fatal("child killed before boundary teardown", err, steps)
	}
	if _, err = os.Stat(filepath.Join(h.Dir(), "work")); err != nil {
		t.Fatal(err)
	}
	h.Teardown = nil
	if err = Destroy(t.Context(), h); err != nil {
		t.Fatal(err)
	}
}
