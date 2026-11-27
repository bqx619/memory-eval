package testoracle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpstream(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"internal/example/nested", "design/upstream/fixture"} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"go.mod", "design/upstream/fixture/oracle.py"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(root, "internal/example/nested"))
	for _, rel := range []string{"fixture", "fixture/oracle.py"} {
		got := Upstream(t, rel)
		if !filepath.IsAbs(got) {
			t.Fatalf("not absolute: %s", got)
		}
		actual, err := os.Stat(got)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.Stat(filepath.Join(root, "design/upstream", rel))
		if err != nil || !os.SameFile(actual, want) {
			t.Fatalf("wrong reference: %s (%v)", got, err)
		}
	}
	for _, rel := range []string{"missing/oracle.py", "fixture/missing.py"} {
		skipped := false
		t.Run(rel, func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			Upstream(t, rel)
			t.Fatal("missing upstream reference did not skip")
		})
		if !skipped {
			t.Errorf("%s was not skipped", rel)
		}
	}
}

func TestProtocolSources(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"protocols", "design/upstream/fixture"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"go.mod":                            "module fixture\n",
		"protocols/fixture.json":            `{"scoring":{"primary":"judge","judge":{"file":"design/upstream/fixture/judge.py"},"reader":{"file":"design/upstream/fixture/reader.py"}}}`,
		"design/upstream/fixture/judge.py":  "judge",
		"design/upstream/fixture/reader.py": "reader",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	ProtocolSources(t, "fixture")
	if err := os.Remove(filepath.Join(root, "design/upstream/fixture/reader.py")); err != nil {
		t.Fatal(err)
	}
	skipped := false
	t.Run("missing-reader", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		ProtocolSources(t, "fixture")
		t.Fatal("missing protocol reference did not skip")
	})
	if !skipped {
		t.Fatal("missing protocol reference did not skip")
	}
}
