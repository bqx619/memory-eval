package sut

import (
	"context"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"testing"
)

// Only synthetic installed SDKs use this adapter. Runtime readiness is exec'd
// through the fake CLI, never dialed from the evaluator's host-side checker.
type fixtureContainer struct{ *container.Backend }

func newFixtureContainer(t *testing.T) *fixtureContainer {
	f := testoracle.FakeDocker(t)
	return &fixtureContainer{container.New(container.Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Context: "orbstack", Platform: f.Platform})}
}
func (b *fixtureContainer) Prepare(ctx context.Context, s sandbox.Spec) (sandbox.Handle, error) {
	for i := range s.Services {
		v := &s.Services[i]
		v.Ready.Exec = []string{v.Command[0], "-c", `import socket,sys
s=socket.create_connection(('127.0.0.1',int(sys.argv[1])),timeout=0.5);s.close()`, v.Command[len(v.Command)-1]}
	}
	return b.Backend.Prepare(ctx, s)
}
func withFixtureContainer(t *testing.T, a *Adapter) {
	m := *a.Manifest
	l := *a.Lock
	m.Sandbox.Backend = "container"
	if l.Image.ID == "" {
		l.Image = sandbox.Image{ID: "fixture:image", Ref: "fixture:image"}
	}
	a.Manifest = &m
	a.Lock = &l
	if l.ImageRoot != "" {
		a.Backend = &fixtureContainer{container.New(container.DefaultConfig())}
	} else {
		a.Backend = newFixtureContainer(t)
	}
}
