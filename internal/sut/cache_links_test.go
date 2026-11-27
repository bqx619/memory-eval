package sut

import (
	"context"
	"maps"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageCacheLinks(t *testing.T) {
	m, root := fixtureManifest(t, "python")
	m.Runtime.Prepare = [][]string{{"{python}", "-c", "from pathlib import Path; Path('/opt/memeval/env/models/cache').mkdir()"}}
	before, err := inputHash(m)
	if err != nil {
		t.Fatal(err)
	}
	m.Runtime.CacheLinks = map[string]string{"home/.cache/model": "{env}/models/cache"}
	after, err := inputHash(m)
	if err != nil || before == after {
		t.Fatal("cache_links missing from input identity", err)
	}
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	l, err := Install(t.Context(), m, InstallOptions{Cache: filepath.Join(root, "cache"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := os.ReadFile(filepath.Join(l.EnvDir, "Containerfile"))
	if err != nil || !strings.Contains(string(recipe), "RUN test -e '/opt/memeval/env/models/cache'") {
		t.Fatal("missing build target check", err)
	}
	b := container.New(container.DefaultConfig())
	h, err := m.Prepare(t.Context(), b, l, "cache-test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Destroy(context.Background(), h)
	link := filepath.Join(h.Dir(), "home/.cache/model")
	target, err := os.Readlink(link)
	if err != nil || target != ImageRoot+"/models/cache" {
		t.Fatal(target, err)
	}
	if _, err = os.Stat(link); !os.IsNotExist(err) {
		t.Fatal("fixture link should be dangling on host", err)
	}
	got, ok := h.EffectiveConfig()["cache_links"].(map[string]string)
	if !ok || !maps.Equal(got, map[string]string{"home/.cache/model": target}) {
		t.Fatal("cache links not disclosed", h.EffectiveConfig())
	}
	old := l.InputSHA256
	m.Runtime.CacheLinks["home/.cache/model"] = "{env}/models/other"
	if now, err := inputHash(m); err != nil || old == now {
		t.Fatal("target change lost", err)
	}
}

func TestCacheLinksValidation(t *testing.T) {
	for _, links := range []map[string]string{
		{"../escape": "{env}/models"}, {"/absolute": "{env}/models"}, {".": "{env}/models"},
		{"home/cache": "/host/models"}, {"home/cache": "{env}/../outside"}, {"home/cache": "{env}"},
		{"home/cache": "{unknown}/models"}, {"home/cache": "{env}/models\ninvalid"},
		{"home/cache": "{env}/models", "home/cache/nested": "{env}/other"},
	} {
		m := &Manifest{Runtime: Runtime{CacheLinks: links}}
		if err := m.validateRuntime(); err == nil || !strings.Contains(err.Error(), "cache link") && !strings.Contains(err.Error(), "cache_links") {
			t.Fatal(links, err)
		}
	}
}

func TestMastraAndHindsightModelCacheManifests(t *testing.T) {
	m, err := Load("../../suts/mastra/manifest-semantic.json")
	if err != nil {
		t.Fatal(err)
	}
	links, err := m.cacheLinks()
	if err != nil || links["home/.cache/mastra"] != ImageRoot+"/models-home/.cache/mastra" || len(m.Runtime.Prepare) == 0 {
		t.Fatal(links, err)
	}
	for _, name := range []string{"manifest.json", "manifest-open.json"} {
		m, err := Load("../../suts/hindsight/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Runtime.CacheLinks) != 0 || m.Process.Env["HF_HOME"] != "{env}/models/hf" {
			t.Fatal("Hindsight should use existing HF_HOME", name)
		}
	}
	// Lexical validation is shared with PrepareEpisode; no host stat of targets.
	if err := sandbox.ValidateCacheLinks(ImageRoot, map[string]string{"home/cache": ImageRoot + "/models"}); err != nil {
		t.Fatal(err)
	}
}
