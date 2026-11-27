//go:build darwin || linux

package sandbox

import (
	"context"
	"fmt"
	"memeval/internal/procexec"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A detached service may have been reparented and may have changed its process
// title. Match both bounded command paths and kernel-reported cwd/open files.
func pathMention(text, root string) bool {
	for start := 0; start < len(text); {
		i := strings.Index(text[start:], root)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(root)
		before := i == 0 || strings.ContainsRune(" \t\n\"'=:([", rune(text[i-1]))
		after := end == len(text) || strings.ContainsRune("/ \t\n\"'):]\x00", rune(text[end]))
		if before && after {
			return true
		}
		start = end
	}
	return false
}
func pathProcesses(ctx context.Context, roots []string) (map[int]string, error) {
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,uid=,lstart=,command=")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	b, err := procexec.Output(cmd)
	if err != nil {
		return nil, err
	}
	starts, parents, commands := map[int]string{}, map[int]int{}, map[int]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		uid, _ := strconv.Atoi(f[2])
		parents[pid] = ppid
		if uid == os.Getuid() {
			starts[pid] = strings.Join(f[3:8], " ")
			commands[pid] = strings.Join(f[8:], " ")
		}
	}
	excluded := map[int]bool{}
	for pid := os.Getpid(); pid > 0 && !excluded[pid]; pid = parents[pid] {
		excluded[pid] = true
	}
	matched := map[int]string{}
	match := func(pid int, text string) {
		if excluded[pid] || starts[pid] == "" {
			return
		}
		for _, root := range roots {
			if pathMention(text, root) {
				matched[pid] = starts[pid]
			}
		}
	}
	for pid, text := range commands {
		match(pid, text)
	}
	if runtime.GOOS == "darwin" {
		cmd = exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-Fpn", "-a", "-u", strconv.Itoa(os.Getuid()))
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		b, e := procexec.Output(cmd)
		if e != nil {
			if exit, ok := e.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				return nil, fmt.Errorf("process file inventory: %w", e)
			}
		}
		pid := 0
		for _, line := range strings.Split(string(b), "\n") {
			if len(line) < 2 {
				continue
			}
			switch line[0] {
			case 'p':
				pid, _ = strconv.Atoi(line[1:])
			case 'n':
				match(pid, line[1:])
			}
		}
	} else {
		for pid := range starts {
			base := filepath.Join("/proc", strconv.Itoa(pid))
			for _, name := range []string{"cwd", "exe"} {
				if p, e := os.Readlink(filepath.Join(base, name)); e == nil {
					match(pid, p)
				}
			}
			fds, _ := os.ReadDir(filepath.Join(base, "fd"))
			for _, f := range fds {
				if p, e := os.Readlink(filepath.Join(base, "fd", f.Name())); e == nil {
					match(pid, p)
				}
			}
		}
	}
	return matched, nil
}

func OwnsProcess(ctx context.Context, pid int, root string) (bool, error) {
	processes, e := pathProcesses(ctx, []string{root})
	if e != nil {
		return false, e
	}
	_, ok := processes[pid]
	return ok, nil
}
func CleanupPaths(ctx context.Context, roots ...string) error {
	roots = append([]string(nil), roots...)
	for i, root := range roots {
		if !filepath.IsAbs(root) || len(filepath.Clean(root)) < 8 {
			return fmt.Errorf("unsafe process cleanup root")
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		if len(real) < 8 {
			return fmt.Errorf("unsafe process cleanup root")
		}
		roots[i] = real
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		matches, err := pathProcesses(ctx, roots)
		if err != nil {
			return err
		}
		live := false
		for pid := range matches {
			if Alive(pid) {
				live = true
				signal := syscall.SIGTERM
				if time.Until(deadline) < 3*time.Second {
					signal = syscall.SIGKILL
				}
				if e := syscall.Kill(pid, signal); e != nil && e != syscall.ESRCH {
					return e
				}
			}
		}
		if !live {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("path-owned processes remain after cleanup")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
}
