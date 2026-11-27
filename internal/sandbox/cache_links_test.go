package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareEpisodeCacheLinks(t *testing.T) {
	root := "/opt/memeval/env"
	links := map[string]string{"home/.cache/mastra": root + "/models-home/.cache/mastra"}
	h, err := PrepareEpisode(t.Context(), Spec{Command: []string{root + "/bin/sut"}, EnvRoot: root, Network: NetworkNone, CacheLinks: links})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(h.Dir()) })
	if got, err := os.Readlink(filepath.Join(h.Dir(), "home/.cache/mastra")); err != nil || got != links["home/.cache/mastra"] {
		t.Fatal(got, err)
	}
	for _, bad := range []map[string]string{
		{"../escape": root + "/models"}, {"/absolute": root + "/models"},
		{"a": "/outside"}, {"a": root + "-other/models"}, {"a": root},
		{"a": root + "/models", "a/b": root + "/other"},
	} {
		ep := t.TempDir()
		_, err := PrepareEpisode(t.Context(), Spec{EpisodeDir: ep, Command: []string{root + "/sut"}, EnvRoot: root, Network: NetworkNone, CacheLinks: bad})
		if err == nil {
			t.Fatal("invalid link accepted", bad)
		}
		files, _ := os.ReadDir(ep)
		if len(files) != 0 {
			t.Fatal("validation must precede filesystem mutations", bad)
		}
	}
}
