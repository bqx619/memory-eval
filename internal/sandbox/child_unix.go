//go:build darwin || linux

package sandbox

import (
	"context"
	"errors"
	"io"
	"memeval/internal/procexec"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type child struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	out, errout *os.File
	done        chan struct{}
	status      ExitStatus
	err         error
	killMu      sync.Mutex
	killed      bool
}

func (p *child) Stdin() io.WriteCloser { return p.in }
func (p *child) Stdout() io.Reader     { return p.out }
func (p *child) Stderr() io.Reader     { return p.errout }
func (p *child) PID() int              { return p.cmd.Process.Pid }
func (p *child) Wait(ctx context.Context) (ExitStatus, error) {
	select {
	case <-p.done:
		return p.status, p.err
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	}
}
func (p *child) Kill(ctx context.Context) error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.killed {
		return nil
	}
	pg := -p.PID()
	e := syscall.Kill(pg, syscall.SIGTERM)
	if errors.Is(e, syscall.ESRCH) {
		p.killed = true
		return nil
	}
	if e != nil {
		return e
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if errors.Is(syscall.Kill(pg, 0), syscall.ESRCH) {
				p.killed = true
				return nil
			}
		case <-deadline.C:
			e = syscall.Kill(pg, syscall.SIGKILL)
			if e != nil && !errors.Is(e, syscall.ESRCH) {
				return e
			}
			p.killed = true
			// Reap the leader before callers remove its working directory.
			select {
			case <-p.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
				return errors.New("process leader did not exit after SIGKILL")
			}
		case <-ctx.Done():
			_ = syscall.Kill(pg, syscall.SIGKILL)
			p.killed = true
			return ctx.Err()
		}
	}
}
func start(ctx context.Context, s Spec, exe string, args []string) (Process, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = s.Cwd
	cmd.Env = Environment(s.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	inR, inW, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	outR, outW, e := os.Pipe()
	if e != nil {
		inR.Close()
		inW.Close()
		return nil, e
	}
	errR, errW, e := os.Pipe()
	if e != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, e
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = errW
	if e = procexec.Start(cmd); e != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		return nil, e
	}
	inR.Close()
	outW.Close()
	errW.Close()
	p := &child{cmd: cmd, in: inW, out: outR, errout: errR, done: make(chan struct{})}
	go func() {
		e := cmd.Wait()
		p.status.Code = cmd.ProcessState.ExitCode()
		if w, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && w.Signaled() {
			p.status.Signal = w.Signal().String()
		}
		var ee *exec.ExitError
		if e != nil && !errors.As(e, &ee) {
			p.err = e
		}
		close(p.done)
	}()
	go func() {
		var timeout <-chan time.Time
		var timer *time.Timer
		if s.Limits.Timeout > 0 {
			timer = time.NewTimer(s.Limits.Timeout)
			timeout = timer.C
			defer timer.Stop()
		}
		select {
		case <-p.done:
		case <-ctx.Done():
			_ = p.Kill(context.Background())
		case <-timeout:
			_ = p.Kill(context.Background())
		}
	}()
	return p, nil
}

// CloseReaders is an optional process capability, used after stream consumers stop.
func (c *child) CloseReaders() {
	c.out.Close()
	c.errout.Close()
	c.in.Close()
}
