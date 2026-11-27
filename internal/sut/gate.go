//go:build darwin || linux

package sut

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func fileGate(path string, exclusive bool) (func(), error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if e = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("environment gate busy %s: %w", path, e)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// The catalog gate protects lock-file unlink against concurrent openers. Its
// inode is permanent and lives outside env-gates, which destroy may remove.
func cacheGate(root string, exclusive bool) (func(), error) {
	return fileGate(filepath.Join(root, ".env-lifecycle.lock"), exclusive)
}
func environmentGate(env string, exclusive bool) (func(), error) {
	root := filepath.Dir(filepath.Dir(env))
	catalog, e := cacheGate(root, false)
	if e != nil {
		return nil, e
	}
	gate, e := fileGate(filepath.Join(root, "env-gates", filepath.Base(env)+".lock"), exclusive)
	if e != nil {
		catalog()
		return nil, e
	}
	return func() { gate(); catalog() }, nil
}
