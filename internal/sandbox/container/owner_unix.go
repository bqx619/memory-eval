//go:build darwin || linux

package container

import (
	"os"
	"syscall"
)

func fileOwner(p string) (int, int, error) {
	s, e := os.Stat(p)
	if e != nil {
		return 0, 0, e
	}
	v := s.Sys().(*syscall.Stat_t)
	return int(v.Uid), int(v.Gid), nil
}
