//go:build darwin

package sandbox

import "syscall"

func Alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
