package main

import (
	"bytes"
	"encoding/json"
	"memeval/internal/benchmark"
	"memeval/internal/registry"
	"memeval/internal/suite"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemasUpToDate(t *testing.T) {
	for _, test := range []struct {
		kind, name string
		generate   func() ([]byte, error)
	}{{"suite", "suite.v3.json", suite.Schema}, {"bundle-header", "suite-bundle-header.v1.json", suite.BundleHeaderSchema}, {"episode", "suite-episode.v1.json", suite.EpisodeSchema}, {"benchmark", "benchmark-protocol.v1.json", benchmark.Schema}, {"registry", "registry.v1.json", registry.Schema}, {"sut-manifest", "sut-manifest.v3.json", sut.Schema}, {"sut-upstream", "sut-upstream.v1.json", sut.UpstreamSchema}} {
		t.Run(test.kind, func(t *testing.T) {
			want, err := test.generate()
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join("../../schemas", test.name))
			if err != nil || !bytes.Equal(want, got) {
				t.Fatalf("schema drift: run memeval suite schema --kind %s --out schemas/%s (%v)", test.kind, test.name, err)
			}
		})
	}
}

func TestDatasetCLIUpgradeAndLeakSkip(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "v2.json")
	output := filepath.Join(dir, "v3.json")
	raw := []byte(`{"schema_version":"2.0","suite_id":"cli-fixture","track":"qa","provenance":{"source_url":"fixture://local"},"limitations":[],"protocol":{"ingestion":"per_event"},"episodes":[{"id":"private-episode","cluster_id":"private-episode","events":[],"cases":[{"id":"private-case","kind":"qa","query":"private-case","scope":[],"cluster_id":"private-episode","budget":{},"cutoff":0,"gold":{"answers":["a"],"evidence_ids":null}}]}]}`)
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if err := run(t.Context(), []string{"validate", input}, &out, &errs); err == nil {
		t.Fatal("leaky query accepted")
	}
	out.Reset()
	if err := run(t.Context(), []string{"validate", input, "--no-leak-scan"}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	json.Unmarshal(out.Bytes(), &result)
	if result["leak_scan"] != "skipped" {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := run(t.Context(), []string{"suite", "upgrade", input, "--out", output}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, bytes.Replace(raw, []byte(`"2.0"`), []byte(`"3.0"`), 1)) {
		t.Fatal("upgrade rewrote source", err)
	}
	if !strings.Contains(out.String(), "suite_sha256 changed; existing reports/cassettes") {
		t.Fatal("missing upgrade warning")
	}
}
