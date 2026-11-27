package sut

import (
	"errors"
	"fmt"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"strings"
)

type InstallFailure struct {
	Path, Preview string
	Cause         error
}

func (e *InstallFailure) Error() string {
	return fmt.Sprintf("install failed; diagnostics: %s\n%s", e.Path, e.Preview)
}
func (e *InstallFailure) Unwrap() error { return e.Cause }

func RecordInstallFailure(m *Manifest, l *Lock, cause error, stderr string) error {
	text := cause.Error() + "\n"
	var rpc *jsonrpc.RPCError
	if errors.As(cause, &rpc) {
		text += "JSON-RPC message: " + rpc.Message + "\nJSON-RPC data: " + string(rpc.Data) + "\n"
	}
	text += "stderr:\n" + stderr
	text = jsonrpc.Redact(text, m.Secrets())
	path := filepath.Join(l.EnvDir, "install-failure.log")
	_ = os.Remove(filepath.Join(l.EnvDir, "env.lock.json"))
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		return errors.Join(cause, err)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[:20]
	}
	return &InstallFailure{Path: path, Preview: strings.Join(lines, "\n"), Cause: cause}
}

type EnvSummary struct {
	Orphan         bool   `json:"orphan,omitempty"`
	Name           string `json:"name"`
	LockSHA        string `json:"lock_sha"`
	RuntimeKind    string `json:"runtime_kind"`
	RuntimeVersion string `json:"runtime_version"`
	Platform       string `json:"platform"`
	InstalledAt    string `json:"installed_at"`
	Size           int64  `json:"size_bytes"`
}

func Summarize(l Lock) (EnvSummary, error) {
	s := EnvSummary{Orphan: l.Orphan, Name: l.Name, LockSHA: l.LockSHA256, RuntimeKind: l.RuntimeKind, RuntimeVersion: l.RuntimeVersion, Platform: l.Platform.OS + "/" + l.Platform.Arch, InstalledAt: l.CreatedAt, Size: l.Image.SizeBytes}
	if l.Orphan {
		s.Size = l.Image.SizeBytes
		return s, nil
	}
	err := filepath.Walk(l.EnvDir, func(_ string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.Mode().IsRegular() {
			s.Size += i.Size()
		}
		return nil
	})
	return s, err
}
