//go:build darwin || linux

package none

import (
	"bufio"
	"context"
	"memeval/internal/sandbox"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDestroyProcessGroupAndKeep(t *testing.T) {
	b := Backend{}
	ctx := context.Background()
	h, e := b.Prepare(ctx, sandbox.Spec{Command: []string{"/bin/sh", "-c", `sleep 30 & echo $!; wait`}, Env: map[string]string{"PATH": "/usr/bin:/bin"}, Network: sandbox.NetworkNone, KeepDir: true})
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(h.Dir())
	p, e := b.Spawn(ctx, h)
	if e != nil {
		t.Fatal(e)
	}
	defer sandbox.CloseReaders(p)
	line, e := bufio.NewReader(p.Stdout()).ReadString('\n')
	if e != nil {
		t.Fatal(e)
	}
	pid, e := strconv.Atoi(strings.TrimSpace(line))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Destroy(ctx, h); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for sandbox.Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sandbox.Alive(pid) {
		t.Fatal("descendant survived Destroy")
	}
	if _, e = os.Stat(h.Dir()); e != nil {
		t.Fatal("KeepDir ignored")
	}
	if e = b.Destroy(ctx, h); e != nil {
		t.Fatal(e)
	}
}
func TestWholeProcessDeadline(t *testing.T) {
	b := Backend{}
	h, e := b.Prepare(context.Background(), sandbox.Spec{Command: []string{"/bin/sleep", "30"}, Env: map[string]string{}, Network: sandbox.NetworkNone, Limits: sandbox.Limits{Timeout: 30 * time.Millisecond}})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Destroy(context.Background(), h)
	p, e := b.Spawn(context.Background(), h)
	if e != nil {
		t.Fatal(e)
	}
	defer sandbox.CloseReaders(p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, e := p.Wait(ctx)
	if e != nil || status.Signal == "" {
		t.Fatalf("timeout did not terminate child: %+v %v", status, e)
	}
}
