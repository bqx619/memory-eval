package sut

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"memeval/internal/config/redact"
	"memeval/internal/episodelog"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var buildStep = regexp.MustCompile(`^(#[0-9]+) (.*)$`)
var prepareStep = regexp.MustCompile(`^\[env [0-9]+/[0-9]+\] RUN HOME=/opt/memeval/env/models-home `)
var prepareNetworkError = regexp.MustCompile(`(?i)fetch failed|ECONNRESET|socket disconnected|\bTLS\b|ETIMEDOUT|Temporary failure in name resolution|Connection reset`)

// Only retry a failed prepare RUN with a network diagnostic on that same
// BuildKit step. Network errors from apt, lock resolution or pip are excluded.
func retryablePrepareLog(r io.Reader) bool {
	type step struct{ prepare, network, failed bool }
	steps := map[string]*step{}
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 64<<10), 2<<20)
	for scan.Scan() {
		parts := buildStep.FindStringSubmatch(scan.Text())
		if parts == nil {
			continue
		}
		id, text := parts[1], parts[2]
		if prepareStep.MatchString(text) {
			steps[id] = &step{prepare: true}
			continue
		}
		if s := steps[id]; s != nil {
			s.network = s.network || prepareNetworkError.MatchString(text)
			s.failed = s.failed || strings.HasPrefix(text, "ERROR:")
		}
	}
	if scan.Err() != nil {
		return false
	}
	for _, s := range steps {
		if s.prepare && s.network && s.failed {
			return true
		}
	}
	return false
}

// buildEnvWithRetry leaves diagnostics even when a retry recovers. Each attempt
// has its own redacted log so a previous failure cannot qualify a later one.
func buildEnvWithRetry(ctx context.Context, b *container.Backend, m *Manifest, l *Lock, stage string, log io.Writer, args []string) (retried bool, err error) {
	for attempt := 1; attempt <= 3; attempt++ {
		path := filepath.Join(stage, fmt.Sprintf("build-attempt-%d.log", attempt))
		capture := episodelog.NewFileWriter(path, math.MaxInt64, m.Secrets(), redact.From(ctx))
		err = b.Build(ctx, io.MultiWriter(log, capture), args...)
		if closeErr := capture.Close(); closeErr != nil {
			return retried, errors.Join(err, closeErr)
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return retried, errors.Join(err, openErr)
		}
		retry := err != nil && attempt < 3 && ctx.Err() == nil && retryablePrepareLog(f)
		if retry {
			message := fmt.Sprintf("prepare network failure: retry %d/2 (build attempt %d/3)\n", attempt, attempt+1)
			_, writeErr := io.WriteString(log, message)
			flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
			if !retried {
				flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			}
			diagnostics, e := os.OpenFile(filepath.Join(l.EnvDir, "install-failure.log"), flags, 0600)
			if e == nil {
				_, e = f.Seek(0, io.SeekStart)
				if e == nil {
					_, e = io.Copy(diagnostics, f) // Already credential-redacted.
				}
				if e == nil {
					_, e = io.WriteString(diagnostics, message)
				}
				e = errors.Join(e, diagnostics.Close())
			}
			writeErr = errors.Join(writeErr, e)
			if writeErr != nil {
				f.Close()
				return retried, errors.Join(err, writeErr)
			}
			retried = true
		}
		f.Close()
		os.Remove(path)
		if !retry {
			return retried, err
		}
	}
	return retried, err
}
