package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultsCLI(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reports", "fixture")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../internal/results/testdata/bm25-pmb.report.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	call := func(args ...string) ([]byte, string, error) {
		var out, errs bytes.Buffer
		err := run(t.Context(), args, &out, &errs)
		return out.Bytes(), errs.String(), err
	}
	a, _, e := call("results", "card", dir)
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := call("results", "card", dir)
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal(e, "card nondeterminism")
	}
	var card map[string]any
	if e = json.Unmarshal(a, &card); e != nil {
		t.Fatal(e)
	}
	if card["dataset"].(map[string]any)["cases_with_gold"] != float64(43) {
		t.Fatal(card)
	}
	target := filepath.Join(root, "card.json")
	if _, _, e = call("results", "card", dir, "--out", target); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(target)
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal(e, "file/stdout mismatch")
	}

	other := filepath.Join(root, "rejected")
	if e = os.MkdirAll(other, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(other, "report.json"), []byte(`{"reason":"rejected"}`), 0600); e != nil {
		t.Fatal(e)
	}
	outdir := filepath.Join(root, "index")
	_, stderr, e := call("results", "build", "--reports", filepath.Dir(dir), "--reports", other, "--out", outdir)
	if e != nil || !strings.Contains(stderr, "skipping rejected") {
		t.Fatal(e, stderr)
	}
	entries, e := os.ReadDir(outdir)
	if e != nil || len(entries) != 2 {
		t.Fatal(e, entries)
	}
	for _, name := range []string{"index.json", "index.js"} {
		if _, e = os.Stat(filepath.Join(outdir, name)); e != nil {
			t.Fatal(e)
		}
	}
	glossary, _, e := call("results", "glossary")
	if e != nil {
		t.Fatal(e)
	}
	var defs []map[string]any
	if e = json.Unmarshal(glossary, &defs); e != nil || len(defs) < 100 {
		t.Fatal(e, len(defs))
	}
	gpath := filepath.Join(root, "glossary.json")
	if _, _, e = call("results", "glossary", "--out", gpath); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(gpath)
	if e != nil || !bytes.Equal(b, glossary) {
		t.Fatal(e, "glossary differs")
	}
	for _, args := range [][]string{{"results", "card"}, {"results", "build", "--out", outdir}, {"results", "build", "--reports", dir}, {"results", "glossary", "extra"}} {
		if _, _, e = call(args...); e == nil {
			t.Fatal("invalid args accepted", args)
		}
	}
}
