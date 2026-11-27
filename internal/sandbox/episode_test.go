package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestShortEpisodeRoot(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MEMEVAL_EP_ROOT", root)
	a, e := NewEpisode()
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(a)
	b, e := NewEpisode()
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(b)
	r, _ := Real(root)
	st, _ := os.Stat(a)
	if filepath.Dir(a) != r || a == b || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(filepath.Base(a)) || st.Mode().Perm() != 0700 {
		t.Fatal(a, b, st.Mode())
	}
	t.Setenv("MEMEVAL_EP_ROOT", "relative")
	if _, e = NewEpisode(); e == nil {
		t.Fatal("relative root accepted")
	}
}
