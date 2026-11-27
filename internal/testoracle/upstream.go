package testoracle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Upstream locates an optional reference under design/upstream relative to the
// repository root. Missing references skip the calling test, not the whole package.
func Upstream(t *testing.T, rel string) string {
	t.Helper()
	if !filepath.IsLocal(rel) {
		t.Fatalf("upstream reference must be a local relative path: %q", rel)
	}
	path := filepath.Join(repositoryRoot(t), "design", "upstream", filepath.FromSlash(rel))
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("upstream reference unavailable: %s; restore with python3 -B scripts/fetch_upstream_refs.py", path)
	} else if err != nil {
		t.Fatal(err)
	}
	return path
}

// ProtocolSources checks the optional inputs that benchmark.Resolve will hash.
// It does not replace Resolve or suppress malformed protocols or hash failures.
func ProtocolSources(t *testing.T, id string) {
	t.Helper()
	if !filepath.IsLocal(id) || filepath.Base(id) != id {
		t.Fatalf("invalid protocol id: %q", id)
	}
	b, err := os.ReadFile(filepath.Join(repositoryRoot(t), "protocols", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	type reference struct{ File string }
	var p struct {
		Scoring struct{ Judge, Reader *reference }
	}
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []*reference{p.Scoring.Judge, p.Scoring.Reader} {
		if ref != nil {
			if rel, ok := strings.CutPrefix(filepath.ToSlash(ref.File), "design/upstream/"); ok {
				Upstream(t, rel)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := os.Stat(filepath.Join(root, "go.mod"))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("cannot locate repository root (go.mod)")
		}
		root = parent
	}
	return root
}
