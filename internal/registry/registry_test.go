package registry

import (
	"context"
	"encoding/json"
	"memeval/internal/benchmark"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryRegistry(t *testing.T) {
	root := benchmark.Root()
	r, err := LoadDir(filepath.Join(root, "data/registry"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 12 {
		t.Fatal(len(r))
	}
	for _, entry := range r {
		if len(entry.Suites) > 0 {
			testoracle.ProtocolSources(t, entry.ProtocolID)
			for _, ref := range entry.Suites {
				testoracle.Dataset(t, filepath.Join(root, ref.Path))
			}
		}
	}
	out := r.Check(t.Context(), root, false)
	if !out.OK {
		t.Fatal(out)
	}
	e, s, ok := r.LookupSuite("precisionmembench-projection-v1")
	if !ok || e.ID != "precisionmembench" || s.ProtocolRef != "protocols/precisionmembench.json#typed_context" {
		t.Fatal(e, s, ok)
	}
}
func testRegistry(t *testing.T) (Registry, string) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "protocols"), 0755)
	p := benchmark.Protocol{ProtocolID: "test", Version: "1", Variants: map[string]benchmark.Variant{"s": {Description: "fixture"}}, Scoring: benchmark.Scoring{Primary: "retrieval"}}
	b, _ := json.Marshal(p)
	os.WriteFile(filepath.Join(root, "protocols/test.json"), b, 0600)
	s := suite.Suite{SchemaVersion: "2.0", SuiteID: "fixture-suite", Provenance: map[string]any{"source_url": "fixture://local"}, Episodes: []suite.Episode{{ID: "ep", Cases: []suite.Case{{ID: "case", Kind: "qa", Gold: suite.Gold{Answers: []string{"a"}}}}}}}
	b, _ = json.Marshal(s)
	os.WriteFile(filepath.Join(root, "suite.json"), b, 0600)
	return Registry{{ID: "test", ProtocolID: "test", ImplementationStatus: "importer", Variants: []string{"s"}, Suites: []SuiteRef{{SuiteID: "fixture-suite", Path: "suite.json", ProtocolRef: "protocols/test.json#s"}}}}, root
}
func TestRegistryCheckProblems(t *testing.T) {
	for _, tt := range []struct {
		name, contains string
		mutate         func(Registry, string)
	}{
		{"missing protocol", "no such file", func(r Registry, root string) {
			r[0].Suites = nil
			os.Remove(filepath.Join(root, "protocols/test.json"))
		}},
		{"suite id", "suite_id differs", func(r Registry, _ string) { r[0].Suites[0].SuiteID = "wrong" }},
		{"overlap", "overlaps not symmetric", func(r Registry, _ string) { r[0].Overlaps = []string{"missing"} }},
		{"scale", "scale:", func(r Registry, _ string) { r[0].ReportedScale = map[string]any{"questions": 2} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, root := testRegistry(t)
			tt.mutate(r, root)
			result := r.Check(context.Background(), root, false)
			if result.OK || len(result.Problems) != 1 || !strings.Contains(result.Problems[0], tt.contains) {
				t.Fatal(result)
			}
		})
	}
	r, root := testRegistry(t)
	r[0].ReportedScale = map[string]any{"questions": 2}
	r[0].Notes = []string{"scale: released fixture is a one-question subset"}
	if out := r.Check(t.Context(), root, false); !out.OK {
		t.Fatal(out)
	}
}
func TestRegistryStrictAndLock(t *testing.T) {
	r, root := testRegistry(t)
	dir := filepath.Join(root, "data/registry")
	os.MkdirAll(dir, 0755)
	b, _ := json.Marshal(r[0])
	b = []byte(strings.Replace(string(b), `"suite_id":`, `"unknown":1,"suite_id":`, 1))
	os.WriteFile(filepath.Join(dir, "test.json"), b, 0600)
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "suites[0].unknown") {
		t.Fatal(err)
	}
	r[0].ImplementationStatus = "run"
	if out := r.Check(t.Context(), root, false); out.OK {
		t.Fatal("missing lock accepted")
	}
	r[0].LockPath = "source.lock.json"
	os.WriteFile(filepath.Join(root, r[0].LockPath), []byte(`{"dataset_id":"test","source_url":"fixture://local","revision":"pinned","files":[{"path":"missing","bytes":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}]}`), 0600)
	if out := r.Check(t.Context(), root, false); !out.OK {
		t.Fatal(out)
	}
	if out := r.Check(t.Context(), root, true); out.OK {
		t.Fatal("missing locked bytes accepted")
	}
}
func TestDatasetAndProtocolVariantAxes(t *testing.T) {
	p := &benchmark.Protocol{Variants: map[string]benchmark.Variant{"official": {Params: map[string]any{"dataset_variants": []any{"32k"}}}, "memory_only": {Params: map[string]any{"dataset_variants": []any{"32k"}}}}}
	if !declaredVariant(p, "32k") || declaredVariant(p, "128k") || !registeredProtocolVariant(p, "official", []string{"32k"}) || registeredProtocolVariant(p, "official", []string{"128k"}) {
		t.Fatal("variant axes validation")
	}
}

func TestBundleTrustedRoot(t *testing.T) {
	r, root := testRegistry(t)
	s, err := suite.Open(t.Context(), filepath.Join(root, "suite.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "bundle")
	if err = suite.Pack(t.Context(), s, dir); err != nil {
		t.Fatal(err)
	}
	b, err := suite.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := &r[0].Suites[0]
	ref.Path = "bundle"
	if got := r.Check(t.Context(), root, false); got.OK || !strings.Contains(strings.Join(got.Problems, ";"), "requires trusted") {
		t.Fatal("missing trust accepted", got)
	}
	ref.SuiteSHA256, err = suite.SHA256(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Check(t.Context(), root, false); !got.OK {
		t.Fatal(got)
	}
	path := filepath.Join(dir, "suite.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err = json.Unmarshal(data, &header); err != nil {
		t.Fatal(err)
	}
	header["track"] = "tampered-but-valid-track"
	data, _ = json.Marshal(header)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := r.Check(t.Context(), root, false); got.OK || !strings.Contains(strings.Join(got.Problems, ";"), "expected suite_sha256") {
		t.Fatal("changed header accepted", got)
	}
}
