package main

import (
	"encoding/json"
	"memeval/internal/benchmark"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Existing isolated CLI fixtures now declare the reader required by P2.
func stageReaderProtocol(t *testing.T, root string) {
	t.Helper()
	testoracle.ProtocolSources(t, "longmemeval")
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	data, e := os.ReadFile(filepath.Join(repo, "protocols/longmemeval.json"))
	if e != nil {
		t.Fatal(e)
	}
	var p benchmark.Protocol
	if e = json.Unmarshal(data, &p); e != nil {
		t.Fatal(e)
	}
	paths := []string{"protocols/longmemeval.json", p.Scoring.Reader.File, p.Scoring.Judge.File}
	for _, name := range paths {
		b, e := os.ReadFile(filepath.Join(repo, name))
		if e != nil {
			t.Fatal(e)
		}
		dest := filepath.Join(root, name)
		if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
