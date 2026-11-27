package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestModelsPinnedObservedAndReplay(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-key")
	t.Setenv("MEMEVAL_ANTHROPIC_API_KEY", "fixture-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			fmt.Fprint(w, `{"model":"claude-fixed","content":[{"type":"text","text":"yes"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		reply(w)
	}))
	defer server.Close()
	cfg := config(t, server.URL+"/v1")
	cfg.Mode = "record"
	e := engine(t, cfg)
	before, err := e.ReportMetadata()
	if err != nil || len(before["models_pinned"].([]ModelPin)) != 0 || before["cassette_files"] != 0 {
		t.Fatal(before, err)
	}
	a, b := request(), request()
	a.Provider = "anthropic"
	b.Model = "other-alias"
	requests := []Request{b, request(), a, request()}
	for _, req := range requests {
		if _, err = e.Complete(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	want := []ModelPin{{"anthropic", "fixture", "claude-fixed"}, {"openai-compat", "fixture", "fixture-returned"}, {"openai-compat", "other-alias", "fixture-returned"}}
	metadata, err := e.ReportMetadata()
	if err != nil || !reflect.DeepEqual(metadata["models_pinned"], want) || metadata["cassette_files"] != 3 {
		t.Fatal(metadata, err)
	}
	server.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	t.Setenv("MEMEVAL_ANTHROPIC_API_KEY", "")
	missing := request()
	missing.Model = "never-called"
	if _, err = e.Complete(context.Background(), missing); err == nil {
		t.Fatal("missing key succeeded")
	}
	after, err := e.ReportMetadata()
	if err != nil || !reflect.DeepEqual(metadata, after) {
		t.Fatal("preflight failure became a model pin", after, err)
	}
	cfg.Mode = "replay"
	replay := engine(t, cfg)
	for _, req := range requests {
		if _, err = replay.Complete(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = replay.Complete(context.Background(), missing); err == nil {
		t.Fatal("miss succeeded")
	}
	replayed, err := replay.ReportMetadata()
	if err != nil || !reflect.DeepEqual(replayed["models_pinned"], want) || replayed["cassette_set"] != metadata["cassette_set"] {
		t.Fatal(replayed, err)
	}
}

func TestCassetteSetContentHash(t *testing.T) {
	root := t.TempDir()
	put := func(dir, name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, b := filepath.Join(root, "first"), filepath.Join(root, "relocated")
	// A directory and a sibling filename deliberately test global path ordering.
	put(a, "a/z.json", "first")
	put(a, "a.json", "second")
	put(b, "a.json", "second")
	put(b, "a/z.json", "first")
	ha, n, err := cassetteSet(a)
	hb, _, errB := cassetteSet(b)
	x, y := sha256.Sum256([]byte("second")), sha256.Sum256([]byte("first"))
	manifest := fmt.Sprintf("{\"path\":\"a.json\",\"sha256\":\"%x\"}\n{\"path\":\"a/z.json\",\"sha256\":\"%x\"}\n", x, y)
	expected := sha256.Sum256([]byte(manifest))
	if err != nil || errB != nil || n != 2 || ha != hb || ha != hex.EncodeToString(expected[:]) {
		t.Fatal(ha, hb, n, err, errB)
	}
	for _, mutate := range []func(){
		func() { put(b, "a/z.json", "changed") },
		func() { put(b, "extra.txt", "non-json content also included") },
		func() {
			if err := os.Rename(filepath.Join(b, "extra.txt"), filepath.Join(b, "renamed.txt")); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			if err := os.Remove(filepath.Join(b, "renamed.txt")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		old, _, _ := cassetteSet(b)
		mutate()
		next, _, err := cassetteSet(b)
		if err != nil || old == next {
			t.Fatal("mutation did not change directory hash", err)
		}
	}
	empty := sha256.Sum256(nil)
	h, n, err := cassetteSet(filepath.Join(root, "missing"))
	if err != nil || n != 0 || h != hex.EncodeToString(empty[:]) {
		t.Fatal(h, n, err)
	}
	if err := os.Symlink(filepath.Join(a, "a.json"), filepath.Join(b, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cassetteSet(b); err == nil {
		t.Fatal("symlink silently omitted/followed")
	}
}
