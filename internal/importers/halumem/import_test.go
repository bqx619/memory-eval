package halumem

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/config/dotenv"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update synthetic HaluMem conformance goldens")

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Spec{Name: "HaluMem synthetic", Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
		r, e := ImportFile(ctx, Options{"testdata/synthetic.jsonl", "medium", filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
		if e != nil {
			return nil, importers.Stats{}, e
		}
		s, e := suite.Open(ctx, r.Path)
		return s, r.Statistics, e
	}, GoldenSuite: "testdata/suite.golden.json", GoldenStats: "testdata/stats.golden.json", Update: *update})
}
func TestRealImportStatistics(t *testing.T) {
	input := dotenv.Value("MEMEVAL_HALUMEM_INPUT")
	if input == "" {
		t.Skip("MEMEVAL_HALUMEM_INPUT unset; ND real data never embedded")
	}
	absolute, err := filepath.Abs(input)
	if err != nil {
		t.Fatal(err)
	}
	input = testoracle.Dataset(t, absolute)
	variant := "medium"
	sessions, events, cases := 1387, 60146, 4854
	if strings.Contains(strings.ToLower(filepath.Base(input)), "long") {
		variant = "long"
		sessions, events, cases = 2417, 107032, 5884
	}
	r, e := Import(t.Context(), Options{input, variant, filepath.Join(t.TempDir(), "bundle")})
	if e != nil {
		t.Fatal(e)
	}
	if r.Statistics.Counts.Episodes != 20 || r.Statistics.Sessions != sessions || r.Statistics.Events != events || r.Statistics.Counts.Cases != cases || r.Statistics.Questions != 3467 || r.Statistics.Diagnostics["memory_points"] != 14948 || r.Statistics.Diagnostics["updates"] != 3122 {
		t.Fatal(r.Statistics)
	}
	s, e := suite.Open(t.Context(), r.Path)
	if e != nil {
		t.Fatal(e)
	}
	l, e := suite.LeakScan(s)
	if e != nil || len(l.Problems) > 0 {
		t.Fatal(l, e)
	}
	t.Logf("users=20 sessions=%d turns=%d audit=%d qa=3467 memory_points=14948 updates=3122 leak=0", sessions, events, sessions)
}

func TestGeneratedEmptyAuditAndPrivateFields(t *testing.T) {
	raw := "Sep 01, 2025, 18:00:00"
	u := User{UUID: "synthetic", Sessions: []Session{{Start: raw, End: raw, Generated: true, Dialogue: []Turn{{Role: "user", Content: "synthetic", Timestamp: raw, DialogueTurn: 1}}, Points: []Point{}}}}
	ep, e := Episode(u)
	if e != nil {
		t.Fatal(e)
	}
	b, e := suite.CanonicalJSON(ep)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `"memory_points":[]`) {
		t.Fatal("explicit empty audit lost")
	}
	var round suite.Episode
	if e = json.Unmarshal(b, &round); e != nil || round.Cases[0].Gold.MemoryPoints == nil {
		t.Fatal("empty audit round trip", e)
	}
	s := &suite.Suite{SchemaVersion: "3.0", SuiteID: "synthetic", Provenance: map[string]any{"source_url": "synthetic"}, Protocol: suite.SuiteProtocol{Ingestion: "per_session", ResetPolicyAllowed: []string{"incremental_per_episode"}}, Episodes: []suite.Episode{round}}
	if _, e = suite.Validate(t.Context(), s, suite.ValidateOptions{}); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"persona_info", "memory_points", "importance", "memory_source", "evidence", "original_memories", "is_update", "reason", "is_generated_qa_session"} {
		s.Episodes[0].Events[0].Attributes = map[string]any{key: "private"}
		r, e := suite.LeakScan(s)
		if e != nil || len(r.Problems) == 0 {
			t.Fatal("privileged key escaped", key, e)
		}
	}
	u.Sessions[0].Dialogue[0].Timestamp = "bad"
	if _, e = Episode(u); e == nil {
		t.Fatal("timestamp parse failure ignored")
	}
}
