package none

import (
	"context"
	"fmt"
	"memeval/internal/sandbox"
	"os"
)

type Backend struct{}

func (Backend) Name() string { return "none" }
func (Backend) Capabilities(ctx context.Context) (sandbox.Caps, error) {
	return sandbox.Caps{Mechanism: "no OS confinement; process-group cleanup only", Host: sandbox.Host(ctx)}, nil
}
func (Backend) Prepare(ctx context.Context, s sandbox.Spec) (sandbox.Handle, error) {
	// Host path resolution belongs only to this explicit fixture backend.
	// Container paths may not exist on the evaluator host.
	if len(s.Command) == 0 {
		return nil, fmt.Errorf("absolute executable required")
	}
	exe, e := sandbox.Real(s.Command[0])
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(exe)
	if e != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("host executable must be a regular file: %s", exe)
	}
	for _, root := range []*string{&s.EnvRoot, &s.PythonRoot, &s.Venv} {
		if *root != "" {
			*root, e = sandbox.Real(*root)
			if e != nil {
				return nil, e
			}
		}
	}
	if s.EnvRoot != "" && !sandbox.Within(exe, s.EnvRoot) {
		return nil, fmt.Errorf("host executable must resolve inside env")
	}
	for _, target := range s.CacheLinks {
		real, e := sandbox.Real(target)
		if e != nil || s.EnvRoot == "" || !sandbox.Within(real, s.EnvRoot) {
			return nil, fmt.Errorf("model cache missing or outside env: %s", target)
		}
	}
	h, e := sandbox.PrepareEpisode(ctx, s)
	if e == nil {
		h.SHA = sandbox.Hash("none-v1")
	}
	return h, e
}
func (Backend) Spawn(ctx context.Context, h sandbox.Handle) (sandbox.Process, error) {
	p, ok := h.(*sandbox.Prepared)
	if !ok {
		return nil, fmt.Errorf("foreign handle")
	}
	return sandbox.Spawn(ctx, p, p.Spec.Command[0], p.Spec.Command[1:])
}
func (Backend) Destroy(ctx context.Context, h sandbox.Handle) error { return sandbox.Destroy(ctx, h) }

func (Backend) PrepareCommand(ctx context.Context, parent sandbox.Handle, command []string, env map[string]string) (sandbox.Handle, error) {
	return sandbox.DeriveCommand(parent, command, env)
}
