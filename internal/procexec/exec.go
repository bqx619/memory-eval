// Package procexec applies descriptor hygiene at every evaluator exec boundary.
// Build commands with os/exec, but launch them through this package.
package procexec

import (
	"bytes"
	"errors"
	"os/exec"
	"sync"
)

// Serialize the sweep and Start, not the lifetime of the subprocess. Parent
// descriptors stay open; only their inheritance flags change. Explicit stdio
// and ExtraFiles still work: os/exec duplicates them into the child's fd table.
var spawnMu sync.Mutex

type Descriptor struct {
	FD          int  `json:"fd"`
	Flags       int  `json:"flags"`
	CloseOnExec bool `json:"close_on_exec"`
}

func Start(cmd *exec.Cmd) error {
	spawnMu.Lock()
	defer spawnMu.Unlock()
	if err := cloexecAll(); err != nil {
		return err
	}
	return cmd.Start()
}

func Run(cmd *exec.Cmd) error {
	if err := Start(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}

func Output(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr errorOutput
	capture := cmd.Stderr == nil
	if capture {
		cmd.Stderr = &stderr
	}
	err := Run(cmd)
	if ee, ok := err.(*exec.ExitError); ok && capture {
		ee.Stderr = stderr.Bytes()
	}
	return out.Bytes(), err
}

func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := Run(cmd)
	return out.Bytes(), err
}
