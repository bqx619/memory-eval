package sut

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpstreamValidation(t *testing.T) {
	good := `{"project":"Example","repo":"https://github.com/example/project","package":"example","version":"1.0","license":"MIT"}`
	path := filepath.Join(t.TempDir(), "UPSTREAM.json")
	for _, raw := range []string{good, strings.Replace(good, `"1.0"`, `""`, 1), strings.Replace(good, "https://", "http://", 1), strings.Replace(good, "github.com", "user:secret@github.com", 1), strings.Replace(good, `"license":"MIT"`, `"other":"MIT"`, 1)} {
		if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		u, e := LoadUpstream(path)
		if (e == nil) != (raw == good) {
			t.Fatal(raw, u, e)
		}
	}
	if runtimeInput("UPSTREAM.json", "manifest.json") {
		t.Fatal("upstream presentation metadata must not change install identity")
	}
}
func TestInstallUpstreamMissingWarningAndInvalidFails(t *testing.T) {
	m := &Manifest{Dir: t.TempDir(), Name: "fixture", Status: "deferred", DeferredReason: "test"}
	var warnings []string
	_, e := Install(context.Background(), m, InstallOptions{Warn: func(s string) { warnings = append(warnings, s) }})
	if e == nil || !strings.Contains(e.Error(), "deferred") || len(warnings) != 1 || !strings.Contains(warnings[0], "upstream: null") {
		t.Fatal(e, warnings)
	}
	if e = os.WriteFile(filepath.Join(m.Dir, "UPSTREAM.json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	_, e = Install(context.Background(), m, InstallOptions{})
	if e == nil || !strings.Contains(e.Error(), "project must be non-empty") {
		t.Fatal(e)
	}
}
