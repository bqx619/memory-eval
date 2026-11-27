package testoracle

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dataset returns an absolute path beneath the repository's data directory.
// Absolute paths are also accepted for explicit, environment-gated local inputs.
// Only unavailable content is skipped; present but corrupt data remains a failure
// in the caller's existing validation. Tracked metadata alone is not a dataset.
func Dataset(t *testing.T, rel string) string {
	t.Helper()
	root := repositoryRoot(t)
	path := rel
	if !filepath.IsAbs(path) {
		if !filepath.IsLocal(rel) {
			t.Fatalf("dataset reference must be a local relative or absolute path: %q", rel)
		}
		path = filepath.Join(root, "data", filepath.FromSlash(rel))
	}
	path = filepath.Clean(path)
	missing := func(path string) {
		t.Helper()
		t.Skipf("dataset content unavailable: %s; rebuild with memeval fetch and memeval import using data/registry and source.lock.json", path)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		missing(path)
	} else if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		return path
	}
	dataRel, err := filepath.Rel(filepath.Join(root, "data"), path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(filepath.ToSlash(dataRel), "normalized/") {
		// Bundle directories can survive a clean checkout with only import.json.
		for _, name := range []string{"suite.json", "episodes.index.jsonl", "episodes.jsonl"} {
			file := filepath.Join(path, name)
			if _, err := os.Stat(file); os.IsNotExist(err) {
				missing(file)
			} else if err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	content := false
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch entry.Name() {
		case "import.json", "source.lock.json", "files.sha256.jsonl":
			return nil
		}
		content = true
		return fs.SkipAll
	})
	if err != nil {
		t.Fatal(err)
	}
	if !content {
		missing(path)
	}
	return path
}
