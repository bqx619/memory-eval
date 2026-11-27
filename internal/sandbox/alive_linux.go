//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func Alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// An orphan zombie is dead, although kill(pid,0) succeeds until PID 1 reaps it.
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return true
	}
	_, state, ok := strings.Cut(string(b), ") ")
	return !ok || !strings.HasPrefix(state, "Z ")
}
