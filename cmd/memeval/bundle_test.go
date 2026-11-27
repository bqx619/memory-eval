package main

import (
	"bytes"
	"encoding/json"
	"memeval/internal/importers/locomo"
	"memeval/internal/registry"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleCLIImportAndTrust(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "empty-config")
	if err := os.WriteFile(env, nil, 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) (string, error) {
		var out, errs bytes.Buffer
		err := run(t.Context(), append([]string{"--env-file", env}, args...), &out, &errs)
		return out.String(), err
	}
	input := "../../internal/importers/locomo/testdata/synthetic_locomo.json"
	bundle := filepath.Join(dir, "default-bundle")
	if _, err := call("import", "locomo", "--input", input, "--out", bundle); err != nil {
		t.Fatal(err)
	}
	b, err := suite.Open(t.Context(), bundle)
	if err != nil || b.Container() != "bundle" {
		t.Fatal(b, err)
	}
	root, _ := suite.SHA256(b)
	if _, err = call("validate", bundle, "--expected-sha256", root); err != nil {
		t.Fatal(err)
	}
	if _, err = call("validate", bundle, "--expected-sha256", strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong root accepted")
	}
	single := filepath.Join(dir, "single.json")
	if _, err = call("import", "locomo", "--input", input, "--out", single, "--format", "single"); err != nil {
		t.Fatal(err)
	}
	s, err := suite.Open(t.Context(), single)
	if err != nil || s.Container() != "single" {
		t.Fatal(s, err)
	}
	a, _ := os.ReadFile(filepath.Join(bundle, "import.json"))
	c, _ := os.ReadFile(filepath.Join(dir, "single.import.json"))
	if !bytes.Equal(a, c) {
		t.Fatal("format changed import statistics")
	}
	packed := filepath.Join(dir, "packed")
	output, err := call("suite", "pack", single, "--out", packed)
	if err != nil || !strings.Contains(output, rootChangeWarning) {
		t.Fatal(output, err)
	}
	if _, err = call("suite", "index", packed); err != nil {
		t.Fatal(err)
	}
}

func TestImportPinsOnlyRegisteredDestination(t *testing.T) {
	root := t.TempDir()
	s, _, err := locomo.Import(t.Context(), "../../internal/importers/locomo/testdata/synthetic_locomo.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "data/registry")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	entry := registry.Entry{ID: "fixture", ImplementationStatus: "importer", Suites: []registry.SuiteRef{{SuiteID: s.SuiteID, Path: "bundle"}}}
	raw, _ := json.Marshal(entry)
	file := filepath.Join(dir, "fixture.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = pinRegisteredImport(t.Context(), root, filepath.Join(root, "scratch"), s.SuiteID); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(file)
	if !bytes.Equal(raw, unchanged) {
		t.Fatal("scratch import changed trust anchor")
	}
	path := filepath.Join(root, "bundle")
	if err = suite.Pack(t.Context(), s, path); err != nil {
		t.Fatal(err)
	}
	if err = pinRegisteredImport(t.Context(), root, path, s.SuiteID); err != nil {
		t.Fatal(err)
	}
	r, err := registry.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ref, ok := r.LookupSuite(s.SuiteID)
	want, _ := importedRoot(t.Context(), path)
	if !ok || ref.SuiteSHA256 != want || ref.ImportStatsPath != "bundle/import.json" {
		t.Fatal(ref)
	}
}
