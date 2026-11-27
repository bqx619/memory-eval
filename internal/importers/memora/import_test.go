package memora

import (
	"context"
	"flag"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update reviewed Memora conformance goldens")

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Spec{Name: "Memora", Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
		r, e := ImportPeriod(ctx, Options{"testdata/data", "weekly", filepath.Join(t.TempDir(), "bundle")}, "fixture", true)
		if e != nil {
			return nil, importers.Stats{}, e
		}
		s, e := suite.Open(ctx, r.Path)
		return s, r.Statistics, e
	}, GoldenSuite: "testdata/suite.golden.json", GoldenStats: "testdata/stats.golden.json", Update: *update})
}
func TestPrivateAnnotationsAndEvidence(t *testing.T) {
	r, e := ImportPeriod(t.Context(), Options{"testdata/data", "weekly", filepath.Join(t.TempDir(), "bundle")}, "fixture", true)
	if e != nil {
		t.Fatal(e)
	}
	s, e := suite.Open(t.Context(), r.Path)
	if e != nil {
		t.Fatal(e)
	}
	e = s.Iter(t.Context(), func(ep *suite.Episode) error {
		if len(ep.Cases) != 1 {
			t.Fatal("case count")
		}
		c := ep.Cases[0]
		if c.QueryTime != "2025-06-07T00:00:00Z" || !*c.InjectCurrentDate || c.Cutoff != len(ep.Events) || len(c.Gold.Rubric.Items) != 13 || c.Gold.Rubric.Items[5].Group != "forgetting_absence" {
			t.Fatal("rubric/date/cutoff")
		}
		n := 0
		for _, ref := range c.Gold.EvidenceRefs {
			if ref.Kind == "event" {
				n++
			}
		}
		if n != 0 {
			t.Fatal("fixture paraphrases must remain text-only")
		}
		refs, err := evidence(map[string]any{"value": "needle", "session_id": float64(1)}, []suite.Event{{SourceID: "event1", Session: "s1", Content: "a needle"}})
		if err != nil || len(refs) != 2 || refs[1].Kind != "event" {
			t.Fatal(refs, err)
		}
		for _, ev := range ep.Events {
			if len(ev.Attributes) != 0 || ev.Session == "" || ev.TimeRaw == "" || ev.Timestamp == "" {
				t.Fatal("event whitelist")
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	leak, e := suite.LeakScan(s)
	if e != nil || len(leak.Problems) > 0 {
		t.Fatal(leak, e)
	}
}
func TestRejectedIdentityAndGroup(t *testing.T) {
	ss := []Session{{ID: 1, Persona: "a", Date: "2025-01-01", Conversation: []Turn{{1, "unknown", "x"}}}}
	q := Questions{Persona: "a", Questions: map[string][]Question{}}
	if _, e := Episode("a", "weekly", ss, q); e == nil {
		t.Fatal("unknown speaker")
	}
	ss[0].Conversation[0].Speaker = "user_agent"
	ss = append(ss, ss[0])
	if _, e := Episode("a", "weekly", ss, q); e == nil {
		t.Fatal("duplicate session")
	}
}

func TestPrivilegedLeakKeys(t *testing.T) {
	r, e := ImportPeriod(t.Context(), Options{"testdata/data", "weekly", filepath.Join(t.TempDir(), "bundle")}, "fixture", true)
	if e != nil {
		t.Fatal(e)
	}
	s, e := suite.Open(t.Context(), r.Path)
	if e != nil {
		t.Fatal(e)
	}
	var ep *suite.Episode
	if e = s.Iter(t.Context(), func(x *suite.Episode) error { ep = x; return nil }); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"share_memory", "session_type", "operation", "operation_details", "evaluation", "evaluation_questions", "evaluation_type", "expected_answer", "memory_evidence", "forgetting_evidence", "date_range"} {
		clone := *ep
		clone.Events = append([]suite.Event{}, ep.Events...)
		clone.Events[0].Attributes = map[string]any{key: "privileged"}
		single := &suite.Suite{SchemaVersion: "3.0", SuiteID: "fixture", Protocol: s.Protocol, Episodes: []suite.Episode{clone}}
		leak, e := suite.LeakScan(single)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, problem := range leak.Problems {
			found = found || strings.Contains(problem, key+": gold key")
		}
		if !found {
			t.Fatal("privileged key escaped", key, leak)
		}
	}
}
