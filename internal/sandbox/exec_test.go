package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandsAndCacheStayInsideEnv(t *testing.T) {
	root, err := Real(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(root, "env")
	if err = os.Mkdir(env, 0700); err != nil {
		t.Fatal(err)
	}
	inside, outside := filepath.Join(env, "tool"), filepath.Join(root, "outside")
	for _, p := range []string{inside, outside} {
		if err = os.WriteFile(p, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	parent := &Prepared{Spec: Spec{EnvRoot: env}}
	if _, err = DeriveCommand(parent, []string{outside}, nil); err == nil {
		t.Fatal("outside executable accepted")
	}
	h, err := DeriveCommand(parent, []string{inside}, map[string]string{"A": "b"})
	if err != nil || !h.Derived {
		t.Fatal(h, err)
	}
	alias := filepath.Join(env, "escape")
	if err = os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = DeriveCommand(parent, []string{alias}, nil); err == nil {
		t.Fatal("symlink escape accepted")
	}
	_, err = PrepareEpisode(t.Context(), Spec{Command: []string{inside}, EnvRoot: env, CacheLinks: map[string]string{"../../escape": env}, Network: NetworkNone})
	if err == nil {
		t.Fatal("cache escape accepted")
	}
}
