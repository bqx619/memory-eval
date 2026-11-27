// Package testoracle locates the opt-in, preinstalled offline Python oracle.
package testoracle

import (
	"bytes"
	"encoding/json"
	"memeval/internal/procexec"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func Python(t *testing.T) string {
	t.Helper()
	p := os.Getenv("MEMEVAL_ORACLE_PYTHON")
	if p == "" {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, ".cache/memeval/longmemeval-oracle/.venv/bin/python")
	}
	if _, e := os.Stat(p); e != nil {
		t.Skip("oracle Python unavailable")
	}
	return p
}
func Run(t *testing.T, script string, input, out any, args ...string) {
	t.Helper()
	b, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(Python(t), append([]string{"-B", "-c", script}, args...)...)
	cmd.Stdin = bytes.NewReader(b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, e := procexec.Output(cmd)
	if e != nil {
		t.Fatalf("oracle: %v: %s", e, stderr.String())
	}
	if e = json.Unmarshal(data, out); e != nil {
		t.Fatalf("oracle JSON: %v: %.500s", e, data)
	}
}
