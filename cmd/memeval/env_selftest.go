package main

import (
	"context"
	"fmt"
	"memeval/internal/sandbox/selftest"
	"memeval/internal/sut"
	"path/filepath"
	"time"
)

func selftestDir(l *sut.Lock) string {
	return filepath.Join(filepath.Dir(filepath.Dir(l.EnvDir)), "selftests")
}
func ensureSelftest(ctx context.Context, m *sut.Manifest, l *sut.Lock, dir string) (*selftest.Evidence, error) {
	b, e := backend(m)
	if e != nil {
		return nil, e
	}
	caps, e := b.Capabilities(ctx)
	if e != nil {
		return nil, e
	}
	sig, e := selftest.Signature(m, l, caps)
	if e != nil {
		return nil, e
	}
	if evidence, e := selftest.Recent(dir, sig, time.Now()); e == nil {
		return evidence, nil
	}
	r, path, e := selftest.Run(ctx, b, m, l, dir)
	if e != nil {
		return nil, e
	}
	if caps.Enforced && !r.Passed {
		return nil, fmt.Errorf("container selftest failed; no passing isolation evidence")
	}
	return r.Evidence(path), nil
}
func installEnvironment(ctx context.Context, m *sut.Manifest, o sut.InstallOptions) (*sut.Lock, error) {
	l, e := sut.Install(ctx, m, o)
	if e != nil {
		return nil, e
	}
	b, e := backend(m)
	if e != nil {
		return nil, e
	}
	if _, ok := b.(selftest.Selftester); !ok {
		_, _, e = selftest.Run(ctx, b, m, l, selftestDir(l))
		return l, e
	}
	if _, e = ensureSelftest(ctx, m, l, selftestDir(l)); e != nil {
		return nil, sut.RecordInstallFailure(m, l, e, "")
	}
	return l, nil
}
