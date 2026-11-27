package runner_test

import (
	"context"
	"encoding/json"
	"memeval/internal/adapter"
	"memeval/internal/registry"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchmarkBindingPrecedence(t *testing.T) {
	s := toy()
	s.ProtocolRef = "protocols/precisionmembench.json#typed_context"
	s.SchemaVersion = "3.0"
	s.Episodes[0].Cases[0].Cutoff = 2
	o := opts()
	o.RegistryDir = filepath.Join(t.TempDir(), "missing")
	r := run(t, s, newSpy(), o)
	if r.Protocol.Benchmark == nil || r.Protocol.Benchmark.Source != "suite" {
		t.Fatal(r.Protocol.Benchmark)
	}
	s.ProtocolRef = "protocols/missing.json#variant"
	if _, err := runner.Run(t.Context(), s, newSpy(), o); err == nil {
		t.Fatal("bad suite ref fell back")
	}
	s = toy()
	o.RegistryDir = t.TempDir()
	entry := registry.Entry{ID: "fixture", ProtocolID: "precisionmembench", ImplementationStatus: "importer", Suites: []registry.SuiteRef{{SuiteID: s.SuiteID, ProtocolRef: "protocols/precisionmembench.json#retrieval"}}}
	b, _ := json.Marshal(entry)
	os.WriteFile(filepath.Join(o.RegistryDir, "fixture.json"), b, 0600)
	r = run(t, s, newSpy(), o)
	if r.Protocol.Benchmark == nil || r.Protocol.Benchmark.Source != "registry" || r.Protocol.Benchmark.Variant != "retrieval" {
		t.Fatal(r.Protocol.Benchmark)
	}
	s.SuiteID = "unknown"
	r = run(t, s, newSpy(), o)
	if r.Protocol.Benchmark != nil || !strings.Contains(strings.Join(r.Warnings, " "), "no benchmark protocol declaration bound") {
		t.Fatal(r.Protocol.Benchmark, r.Warnings)
	}
}
func TestV3UnsupportedAndExecutionGuards(t *testing.T) {
	for _, kind := range []string{"rubric", "set_match", "ordering", "summary", "recsys", "agentic"} {
		t.Run(kind, func(t *testing.T) {
			s := toy()
			s.SchemaVersion = "3.0"
			c := &s.Episodes[0].Cases[0]
			c.Kind = kind
			c.Cutoff = 2
			c.Gold = suite.Gold{}
			switch kind {
			case "rubric":
				c.Gold.Rubric = &suite.Rubric{Items: []suite.RubricItem{{Text: "nugget", Polarity: "must_contain"}}}
			case "set_match":
				c.Gold.PhraseSet = &suite.PhraseSet{Items: []string{"phrase"}}
			case "ordering":
				c.Gold.Sequence = []string{"a", "b"}
			case "summary":
				c.Gold.Reference = "summary"
			case "recsys":
				c.Gold.ItemIDs = []string{"item"}
			case "agentic":
				c.Gold.Checker = &suite.Checker{Type: "environment"}
			}
			a := newSpy()
			r := run(t, s, a, opts())
			if r.Cases[0].Status != result.Unsupported || len(a.names) > 0 || !strings.Contains(strings.Join(r.Warnings, " "), "marked unsupported") {
				t.Fatal(r.Cases[0], r.Warnings)
			}
		})
	}
	for _, mode := range []string{"per_turn_batch:20", "per_document", "per_trajectory", "per_file", "as_of", "after_session_k"} {
		t.Run(mode, func(t *testing.T) {
			s := toy()
			s.SchemaVersion = "3.0"
			s.Episodes[0].Cases[0].Cutoff = 2
			switch mode {
			case "as_of":
				s.Protocol.QueryTimePolicy = mode
			case "at_index", "after_session_k":
				s.Episodes[0].Cases[0].QueryPoint = mode
				s.Episodes[0].Cases[0].Cutoff = 1
				s.Episodes[0].Cases[0].SessionCheckpoint = "PRIVATE-SESSION"
			default:
				s.Protocol.Ingestion = mode
			}
			a, _ := adapter.NewBaseline("null")
			_, err := runner.Run(t.Context(), s, a, opts())
			want := "not implemented in this version"
			if mode == "after_session_k" {
				want = "after_session_k requires incremental_per_episode"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal(err)
			}
		})
	}
	t.Run("memory audit execution guard takes precedence", func(t *testing.T) {
		s := toy()
		s.SchemaVersion = "3.0"
		c := &s.Episodes[0].Cases[0]
		c.Kind = "memory_audit"
		c.QueryPoint = "after_session_k"
		c.SessionCheckpoint = "PRIVATE-SESSION"
		c.Gold = suite.Gold{MemoryPoints: []suite.MemoryPoint{{ID: "m", Content: "fact", Polarity: "should_exist"}}}
		if _, err := suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true}); err != nil {
			t.Fatal(err)
		}
		_, err := runner.Run(t.Context(), s, newSpy(), opts())
		if err == nil || !strings.Contains(err.Error(), "after_session_k requires incremental_per_episode") {
			t.Fatal(err)
		}
	})
}
