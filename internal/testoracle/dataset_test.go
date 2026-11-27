package testoracle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataset(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"go.mod", "data/upstream/fixture/source.lock.json",
		"data/upstream/fixture/data/rows.csv", "data/normalized/single.json",
		"data/normalized/bundle/import.json", "data/normalized/bundle/suite.json",
		"data/normalized/bundle/episodes.index.jsonl", "data/normalized/bundle/episodes.jsonl",
		"data/normalized/metadata-only/import.json", "data/upstream/metadata-only/source.lock.json",
		"data/upstream/metadata-only/files.sha256.jsonl",
	}
	for _, name := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("present; deliberately not parsed by the helper"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(root, "data"))
	for _, rel := range []string{"upstream/fixture", "upstream/fixture/data/rows.csv", "normalized/single.json", "normalized/bundle", "upstream/fixture/source.lock.json", "normalized/bundle/import.json"} {
		want := filepath.Join(root, "data", rel)
		for _, input := range []string{rel, want} {
			got := Dataset(t, input)
			actual, err := os.Stat(got)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.Stat(want)
			if !filepath.IsAbs(got) || err != nil || !os.SameFile(actual, expected) {
				t.Fatalf("wrong reference: %s (%v)", got, err)
			}
		}
	}
	for _, rel := range []string{"upstream/missing.json", "normalized/missing", "normalized/metadata-only", "upstream/metadata-only"} {
		assertDatasetSkip(t, rel)
	}
	for _, name := range []string{"suite.json", "episodes.index.jsonl", "episodes.jsonl"} {
		file := filepath.Join(root, "data/normalized/bundle", name)
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		t.Run("missing-"+name, func(t *testing.T) { assertDatasetSkip(t, "normalized/bundle") })
		if err := os.WriteFile(file, []byte("present"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertDatasetSkip(t *testing.T, path string) {
	t.Helper()
	skipped := false
	t.Run(path, func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		Dataset(t, path)
		t.Fatal("missing dataset content did not skip")
	})
	if !skipped {
		t.Errorf("%s was not skipped", path)
	}
}
