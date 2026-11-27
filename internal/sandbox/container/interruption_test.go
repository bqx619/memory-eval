package container

import (
	"context"
	"errors"
	"memeval/internal/sandbox"
	"testing"
)

func TestCommandCancellationRetainsCauseWithoutHidingDaemonErrors(t *testing.T) {
	b, f := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := b.call(ctx, "version", "--format", "json")
	var command *CommandError
	var isolation *sandbox.Unenforceable
	if !errors.As(err, &command) || command.Stderr == "" || !errors.Is(err, context.Canceled) {
		t.Fatal("lost command diagnostic/cancellation cause", err)
	}
	if errors.As(unavailable(err), &isolation) {
		t.Fatal("cancellation treated as isolation failure")
	}
	if !errors.As(unavailable(context.DeadlineExceeded), &isolation) {
		t.Fatal("internal readiness/audit deadline must still fail closed")
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "version"})
	_, err = b.call(t.Context(), "version", "--format", "json")
	if err == nil || errors.Is(err, context.Canceled) || !errors.As(unavailable(err), &isolation) {
		t.Fatal("actual daemon error ignored", err)
	}
}
