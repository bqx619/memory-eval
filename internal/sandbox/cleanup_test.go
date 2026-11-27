//go:build darwin || linux

package sandbox

import (
	"context"
	"memeval/internal/procexec"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCleanupDetachedCwdAndBoundaries(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e := procexec.Start(cmd); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	defer cmd.Process.Kill()
	if e := CleanupPaths(context.Background(), root); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("detached child survived")
	}
	if pathMention("unix:"+root+"-other/a", root) || !pathMention("unix:"+root+"/db.sock", root) {
		t.Fatal("path boundary match")
	}
}
