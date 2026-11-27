package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvCheckJSONImageErrors(t *testing.T) {
	m, l := echoEnvironment(t)
	if err := os.WriteFile(filepath.Join(l.EnvDir, "unexpected"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runCommand(t.Context(), []string{"env", "check", m.Path, "--cache-dir", m.CacheDir, "--json"}, &out, &out)
	var missing *sut.NotInstalled
	if !errors.As(err, &missing) || missing.Reason != "files_changed" || exitCode(err) != 3 {
		t.Fatal(err)
	}
	var response sut.NotInstalled
	if e := json.Unmarshal(out.Bytes(), &response); e != nil || response.Reason != "files_changed" || !strings.Contains(response.Detail, "unexpected") {
		t.Fatal(out.String(), e)
	}
}
