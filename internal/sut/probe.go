package sut

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/sandbox"
	"memeval/internal/transport/jsonrpc"
	"strings"
	"time"
)

// Probe runs a bounded, non-RPC preflight in the same clean environment and OS
// backend as the bridge. Its temporary episode is always removed.
func probeCommand(ctx context.Context, m *Manifest, l *Lock, b sandbox.Sandbox, command []string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := *m
	probe.Process.Command = command
	h, err := probe.Prepare(ctx, b, l, "probe", false)
	if err != nil {
		return nil, err
	}
	defer b.Destroy(context.WithoutCancel(ctx), h)
	p, err := b.Spawn(ctx, h)
	if err != nil {
		return nil, err
	}
	_ = p.Stdin().Close()
	drained := make(chan struct{})
	var stderr bytes.Buffer
	go func() { _, _ = io.Copy(&stderr, p.Stderr()); close(drained) }()
	failure := func(message string) error {
		return fmt.Errorf("%s; stderr:\n%s", message, jsonrpc.Redact(stderr.String(), m.Secrets()))
	}
	data, err := io.ReadAll(io.LimitReader(p.Stdout(), 65537))
	if err != nil || len(data) > 65536 {
		_ = p.Kill(context.WithoutCancel(ctx))
		<-drained
		return nil, failure("invalid probe output")
	}
	status, waitErr := p.Wait(ctx)
	<-drained
	if waitErr != nil || status.Code != 0 {
		return nil, failure(fmt.Sprintf("probe process failed (exit %d)", status.Code))
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, failure("invalid probe JSON")
	}

	return result, nil
}

func Probe(ctx context.Context, m *Manifest, l *Lock, b sandbox.Sandbox, command []string) (map[string]any, error) {
	result, e := probeCommand(ctx, m, l, b, command)
	if e != nil {
		return nil, e
	}
	if result["ok"] != true || result["fts5"] != true {
		return nil, fmt.Errorf("bridge probe failed")
	}
	return result, nil
}
func probeRuntime(ctx context.Context, m *Manifest, l *Lock, b sandbox.Sandbox) error {
	command := []string{l.Executable, "-c", "# memeval-runtime-probe\nimport json,os,sys; print(json.dumps(dict(version=sys.version.split()[0],executable=os.path.realpath(sys.executable),base_prefix=sys.base_prefix)))"}
	if m.Runtime.Kind == "node" {
		command = []string{l.Executable, "-e", "// memeval-runtime-probe\nconsole.log(JSON.stringify({version:process.versions.node,executable:process.execPath,base_prefix:'/usr/local'}))"}
	}
	r, e := probeCommand(ctx, m, l, b, command)
	if e != nil {
		return e
	}
	version, _ := r["version"].(string)
	exe, _ := r["executable"].(string)
	root, _ := r["base_prefix"].(string)
	if version == "" || !strings.HasPrefix(exe, "/") || !strings.HasPrefix(root, "/") {
		return fmt.Errorf("invalid image runtime probe")
	}
	l.RuntimeActualVersion = version
	if m.Runtime.Kind == "uv-python" {
		l.PythonVersion = version
		l.PythonRealPath = exe
		l.PyRoot = root
	}
	return nil
}
