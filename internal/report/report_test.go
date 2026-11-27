package report

import (
	"context"
	"encoding/json"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactsPermissionsAndEscaping(t *testing.T) {
	zero := 0.
	empty := true
	r := &result.Report{SuiteSHA256: "hash", SuiteID: "unit", Protocol: result.Protocol{Version: "paeval-v2"}, SUT: result.SUT{Name: "<script>alert(1)</script>"}, Source: map[string]any{"source_url": "unit-test://fixture"}, Cases: []result.Case{{CaseID: "one", Status: result.Scored, Metrics: map[string]*float64{"recall": nil, "empty_result_correct": &zero}, EmptyExpected: &empty, Timings: map[string]float64{}}}}
	dir := t.TempDir()
	if err := Save(context.Background(), r, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.json", "cases.jsonl", "cases.csv", "report.html"} {
		s, err := os.Stat(filepath.Join(dir, name))
		if err != nil || s.Mode().Perm() != 0600 {
			t.Fatal(name, s, err)
		}
	}
	html, _ := os.ReadFile(filepath.Join(dir, "report.html"))
	if strings.Contains(string(html), "<script>alert(1)</script>") || !strings.Contains(string(html), `<script>alert(1)<\/script>`) {
		t.Fatal("unescaped HTML")
	}
	loaded, err := Load(context.Background(), filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cases[0].EmptyExpected == nil || !*loaded.Cases[0].EmptyExpected || loaded.Cases[0].Metrics["recall"] != nil {
		t.Fatal("lost null/boolean")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	var obj map[string]any
	_ = json.Unmarshal(data, &obj)
	for _, key := range []string{"reader", "evaluator_llm", "billing", "scores", "isolation", "repeats", "warnings"} {
		if _, ok := obj[key]; !ok {
			t.Fatal("omitted default", key)
		}
	}
}
