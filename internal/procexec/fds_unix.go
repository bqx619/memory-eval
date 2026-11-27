//go:build darwin || linux

package procexec

import (
	"fmt"
	"syscall"
)

const Policy = "cloexec_all"

func fcntl(fd int, op int, value uintptr) (uintptr, syscall.Errno) {
	for {
		v, _, err := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(op), value)
		if err != syscall.EINTR {
			return v, err
		}
	}
}

// scan uses the same fd range and F_GETFD operation as the selftest. EBADF is
// normal (including a descriptor concurrently closed by another goroutine);
// all other errors fail closed. Do not enumerate /dev/fd: that opens an fd.
func scan(visit func(int, uintptr) error) error {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return fmt.Errorf("fd hygiene: getrlimit: %w", err)
	}
	if limit.Cur > uint64(^uint(0)>>1) {
		return fmt.Errorf("fd hygiene: unrepresentable RLIMIT_NOFILE %d", limit.Cur)
	}
	for fd := 3; fd < int(limit.Cur); fd++ {
		flags, err := fcntl(fd, syscall.F_GETFD, 0)
		if err == syscall.EBADF {
			continue
		}
		if err != 0 {
			return fmt.Errorf("fd hygiene: F_GETFD(%d): %w", fd, err)
		}
		if err := visit(fd, flags); err != nil {
			return err
		}
	}
	return nil
}

func cloexecAll() error {
	return scan(func(fd int, flags uintptr) error {
		if flags&syscall.FD_CLOEXEC != 0 {
			return nil
		}
		_, err := fcntl(fd, syscall.F_SETFD, flags|syscall.FD_CLOEXEC)
		if err != 0 && err != syscall.EBADF {
			return fmt.Errorf("fd hygiene: F_SETFD(%d): %w", fd, err)
		}
		return nil
	})
}

// Snapshot only observes: it MUST NOT sanitize a probe before checking for leaks.
func Snapshot() ([]Descriptor, error) {
	fds := []Descriptor{}
	err := scan(func(fd int, flags uintptr) error {
		fds = append(fds, Descriptor{FD: fd, Flags: int(flags), CloseOnExec: flags&syscall.FD_CLOEXEC != 0})
		return nil
	})
	return fds, err
}

// Exec applies the same descriptor hygiene to direct executable replacement.
// No ExtraFiles are used at this boundary; stdin/stdout/stderr are kept intact.
func Exec(path string, argv, env []string) error {
	spawnMu.Lock()
	defer spawnMu.Unlock()
	if err := cloexecAll(); err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}
