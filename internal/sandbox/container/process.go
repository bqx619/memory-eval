package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"memeval/internal/sandbox"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type containerProcess struct {
	backend  *Backend
	episode  *episode
	cli      sandbox.Process
	done     chan struct{}
	mu       sync.Mutex
	killMu   sync.Mutex
	killErr  error // protected by killMu; retain asynchronous timeout teardown failures
	status   sandbox.ExitStatus
	err      error
	captured bool
}

func (p *containerProcess) Stdin() io.WriteCloser { return p.cli.Stdin() }
func (p *containerProcess) Stdout() io.Reader     { return p.cli.Stdout() }
func (p *containerProcess) Stderr() io.Reader     { return p.cli.Stderr() }
func (p *containerProcess) PID() int              { return p.cli.PID() }
func (p *containerProcess) CloseReaders()         { sandbox.CloseReaders(p.cli) }
func (p *containerProcess) Wait(ctx context.Context) (sandbox.ExitStatus, error) {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.status, p.err
	case <-ctx.Done():
		return sandbox.ExitStatus{}, ctx.Err()
	}
}
func (p *containerProcess) capture(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.captured {
		return p.err
	}
	raw, err := p.backend.call(ctx, "wait", p.episode.name)
	if err != nil {
		return err
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("invalid docker wait exit code")
	}
	v, err := p.backend.inspectContainer(ctx, p.episode.name)
	if err != nil {
		return err
	}
	p.status = sandbox.ExitStatus{Code: code, OOM: v.State.OOMKilled}
	p.captured = true
	return nil
}
func (p *containerProcess) TimedOut() bool { return p.episode.timedOut.Load() }
func (p *containerProcess) Kill(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	p.killMu.Lock()
	defer p.killMu.Unlock()
	defer func() {
		if p.TimedOut() && p.killErr == nil {
			p.killErr = err
		}
		if err == nil {
			err = p.killErr
		}
	}()
	p.episode.mu.Lock()
	removed := p.episode.removed
	p.episode.mu.Unlock()
	var waitErr error
	if !removed {
		v, e := p.backend.inspectContainer(ctx, p.episode.name)
		if e != nil {
			return unavailable(e)
		}
		if v.State.Running {
			if _, e = p.backend.call(ctx, "kill", "--signal", "TERM", p.episode.name); e != nil {
				return unavailable(e)
			}
		}
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		waitErr = p.capture(wait)
		if errors.Is(wait.Err(), context.DeadlineExceeded) {
			waitErr = nil
		}
		cancel()
		if e = p.backend.removeContainerLocked(ctx, p.episode); e != nil {
			return unavailable(e)
		}
	}
	return unavailable(errors.Join(waitErr, p.cli.Kill(ctx)))
}
func (b *Backend) Spawn(ctx context.Context, h sandbox.Handle) (sandbox.Process, error) {
	e, err := b.episode(h)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.removed || e.handle.Child != nil {
		return nil, fmt.Errorf("container handle already used")
	}
	t := time.Now()
	args := []string{"--context", b.cfg.Context, "start", "--attach"}
	if e.role != "sidecar" {
		args = append(args, "--interactive")
	}
	args = append(args, e.name)
	cli, err := sandbox.SpawnWithEnv(context.WithoutCancel(ctx), e.handle, b.cfg.CLI, args, b.cliEnv())
	if err != nil {
		return nil, err
	}
	p := &containerProcess{backend: b, episode: e, cli: cli, done: make(chan struct{})}
	e.handle.Child = p
	// Confirm the daemon started the container before unlinking the credential file.
	started := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, x := b.inspectContainer(ctx, e.name)
		if x != nil {
			err = x
			break
		}
		if v.State.Running || v.State.StartedAt != "" && !strings.HasPrefix(v.State.StartedAt, "0001-") {
			started = true
			b.recordIPs(e, v)
			break
		}
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if removeErr := os.Remove(e.envFile); removeErr != nil && !os.IsNotExist(removeErr) {
		started = false
		err = removeErr
	}
	if !started {
		_ = cli.Kill(context.Background())
		sandbox.CloseReaders(cli)
		e.handle.Child = nil
		if err == nil {
			err = fmt.Errorf("container failed to start")
		}
		return nil, unavailable(err)
	}
	e.handle.Extra["start_ms"] = float64(time.Since(t).Microseconds()) / 1000
	go func() {
		_, x := cli.Wait(context.Background())
		wait, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if x == nil {
			x = p.capture(wait)
		}
		p.mu.Lock()
		if !p.captured {
			p.err = x
		}
		p.mu.Unlock()
		close(p.done)
	}()
	go func() {
		var timer *time.Timer
		var timeout <-chan time.Time
		if h := e.handle; h.Spec.Limits.Timeout > 0 {
			timer = time.NewTimer(h.Spec.Limits.Timeout)
			timeout = timer.C
			defer timer.Stop()
		}
		select {
		case <-p.done:
		case <-ctx.Done():
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_ = p.Kill(cleanup)
		case <-timeout:
			e.timedOut.Store(true)
			e.root.timedOut.Store(true)
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_ = p.Kill(cleanup)
		}
	}()
	return p, nil
}
