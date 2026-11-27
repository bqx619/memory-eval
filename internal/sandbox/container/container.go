// Package container executes SUTs through an explicitly selected Docker context.
package container

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container/governor"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Config struct {
	CLI, Context, Platform, LinuxBinary, LinuxBinarySHA256, ProbeRepository string
	DefaultPidsLimit                                                        int
	DefaultShmSize, TmpfsSize                                               string
	// CLIEnv is for injected CLI implementations; it is never forwarded to a SUT.
	CLIEnv map[string]string
}

func DefaultConfig() Config {
	c := Config{CLI: "docker", Context: "orbstack", Platform: "linux/" + runtime.GOARCH, LinuxBinary: filepath.Join("bin", "memeval-linux-"+runtime.GOARCH), ProbeRepository: "memeval/probe", DefaultPidsLimit: 2048, DefaultShmSize: "512m", TmpfsSize: "1g"}
	for k, p := range map[string]*string{"MEMEVAL_DOCKER_CLI": &c.CLI, "MEMEVAL_DOCKER_CONTEXT": &c.Context, "MEMEVAL_LINUX_BINARY": &c.LinuxBinary, "MEMEVAL_CONTAINER_TMPFS_SIZE": &c.TmpfsSize, "MEMEVAL_CONTAINER_SHM_SIZE": &c.DefaultShmSize} {
		if v := dotenv.Value(k); v != "" {
			*p = v
		}
	}
	return c
}

type Backend struct {
	govMu             sync.Mutex
	gov               *runGovernor
	govConfig         governor.Config
	meterDir          string
	cfg               Config
	mu                sync.Mutex
	capsMu            sync.Mutex
	destroyMu         sync.Mutex
	caps              *sandbox.Caps
	runID, ownerStart string
	episodes          map[*sandbox.Prepared]*episode
	order             []*episode
	dfBefore          any
	runResidue        map[string]any
	dfStarted         bool
	fileOwner         func(string) (int, int, error)
}

func New(c Config) *Backend {
	d := DefaultConfig()
	for _, p := range []struct {
		v *string
		d string
	}{{&c.CLI, d.CLI}, {&c.Context, d.Context}, {&c.Platform, d.Platform}, {&c.LinuxBinary, d.LinuxBinary}, {&c.ProbeRepository, d.ProbeRepository}, {&c.DefaultShmSize, d.DefaultShmSize}, {&c.TmpfsSize, d.TmpfsSize}} {
		if *p.v == "" {
			*p.v = p.d
		}
	}
	if c.DefaultPidsLimit == 0 {
		c.DefaultPidsLimit = d.DefaultPidsLimit
	}
	return &Backend{cfg: c, runID: nonce(), ownerStart: currentOwnerStart(), episodes: map[*sandbox.Prepared]*episode{}, fileOwner: fileOwner}
}
func (*Backend) Name() string         { return "container/docker" }
func (b *Backend) BeginRun(id string) { b.mu.Lock(); defer b.mu.Unlock(); b.runID = id }
func (b *Backend) episode(h sandbox.Handle) (*episode, error) {
	p, ok := h.(*sandbox.Prepared)
	if !ok {
		return nil, fmt.Errorf("foreign container handle")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.episodes[p]
	if e == nil {
		return nil, fmt.Errorf("unknown container handle")
	}
	return e, nil
}
func unavailable(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, context.Canceled) {
		return e // Callers must still verify teardown; cancellation alone is not lost isolation.
	}
	return &sandbox.Unenforceable{Reason: e.Error()}
}
func repositoryPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	wd, e := os.Getwd()
	if e != nil {
		return "", e
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, e := os.Stat(filepath.Join(d, "go.mod")); e == nil {
			return filepath.Join(d, p), nil
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return filepath.Abs(p)
}

var _ sandbox.Sandbox = (*Backend)(nil)
var _ sandbox.CommandBackend = (*Backend)(nil)
