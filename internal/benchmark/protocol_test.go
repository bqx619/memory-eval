package benchmark

import (
	"encoding/json"
	"errors"
	judge "memeval/internal/scoring/judge/longmemeval"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUpstreamTemplateErrors(t *testing.T) {
	for _, role := range []string{"judge", "reader"} {
		t.Run(role, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"protocols", "design/upstream/fixture/nested"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := "design/upstream/fixture/nested/template.py"
			ref := &TemplateRef{File: path, SHA256: digest([]byte("original"))}
			p := Protocol{ProtocolID: "fixture", Version: "1", Variants: map[string]Variant{"default": {}}, Scoring: Scoring{Primary: "judge"}}
			if role == "judge" {
				p.Scoring.Judge = ref
			} else {
				p.Scoring.Reader = ref
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "protocols/fixture.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			_, _, _, err = ResolveAt(root, "protocols/fixture.json#default")
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			for _, want := range []string{"protocols reference an unavailable upstream copy", path, "python3 -B scripts/fetch_upstream_refs.py --only fixture"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q in %v", want, err)
				}
			}
			if err = os.WriteFile(filepath.Join(root, path), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, _, err = ResolveAt(root, "protocols/fixture.json#default")
			if err == nil || err.Error() != "template sha256 mismatch: "+path {
				t.Fatalf("hash mismatch message changed: %v", err)
			}
		})
	}
}

func TestResolveTemplateHashes(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	p, v, h, err := Resolve("protocols/longmemeval.json#s")
	if err != nil || v != "s" || len(h) != 64 {
		t.Fatal(v, h, err)
	}
	if p.Scoring.Judge.Params["template_set_sha256"] != judge.VendoredTemplateSHA256 {
		t.Fatal("template set sha differs from vendored literal source")
	}
	if p.Scoring.Judge.File != "design/upstream/longmemeval/src/evaluation/evaluate_qa.py" {
		t.Fatal("judge source file differs")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "protocols"), 0755)
	file := filepath.Join(root, "template.py")
	os.WriteFile(file, []byte("original"), 0600)
	p.Scoring.Judge.File = "template.py"
	p.Scoring.Judge.SHA256 = digest([]byte("original"))
	p.Scoring.Reader = nil
	raw, _ := json.Marshal(p)
	os.WriteFile(filepath.Join(root, "protocols/longmemeval.json"), raw, 0600)
	if _, _, _, err = ResolveAt(root, "protocols/longmemeval.json#s"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte("changed"), 0600)
	if _, _, _, err = ResolveAt(root, "protocols/longmemeval.json#s"); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatal(err)
	}
	os.Remove(file)
	if _, _, _, err = ResolveAt(root, "protocols/longmemeval.json#s"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, _, _, err = Resolve("protocols/longmemeval.json#missing"); err == nil {
		t.Fatal("unknown variant accepted")
	}
	raw = []byte(strings.Replace(string(raw), `"primary":`, `"typo":true,"primary":`, 1))
	if _, err = decode(raw); err == nil || !strings.Contains(err.Error(), "scoring.typo") {
		t.Fatal(err)
	}
}
