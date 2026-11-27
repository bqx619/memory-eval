package convomem

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update reviewed synthetic ConvoMem goldens")

func TestConformance(t *testing.T) {
	for _, typ := range Types {
		t.Run(typ, func(t *testing.T) {
			conformance.Run(t, conformance.Spec{Name: typ, Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
				r, e := ImportData(ctx, Options{Input: "testdata/input", Type: typ, Out: filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
				if e != nil {
					return nil, importers.Stats{}, e
				}
				s, e := suite.Open(ctx, r.Path)
				return s, r.Statistics, e
			}, GoldenSuite: "testdata/" + typ + ".suite.golden.json", GoldenStats: "testdata/" + typ + ".stats.golden.json", Update: *update})
		})
	}
}
func TestEvidenceAndPrivateFields(t *testing.T) {
	a, lit, e := Evidence(json.RawMessage(`[{"speaker":"User","text":"literal $question"}]`))
	if e != nil || lit {
		t.Fatal(e)
	}
	b, lit, e := Evidence(json.RawMessage(`"[{'speaker':'User','text':'literal $question'}]"`))
	if e != nil || !lit || !reflect.DeepEqual(a, b) {
		t.Fatal(a, b, e)
	}
	r, e := ImportData(t.Context(), Options{Input: "testdata/input", Type: "implicit_connection", Out: filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
	if e != nil {
		t.Fatal(e)
	}
	d := r.Statistics.Diagnostics
	if d["evidence_exact"] != 2 || d["evidence_multiple"] != 1 || d["evidence_unmatched"] != 1 {
		t.Fatal(d)
	}
	s, e := suite.Open(t.Context(), r.Path)
	if e != nil {
		t.Fatal(e)
	}
	e = s.Iter(t.Context(), func(ep *suite.Episode) error {
		c := ep.Cases[0]
		if c.Kind != "qa" || string(c.Gold.Checker.Params) != `{"template":"rubric"}` || c.Gold.EvidenceRefs[0].Ref != ep.Events[0].SourceID {
			t.Fatal(c)
		}
		for _, ev := range ep.Events {
			if len(ev.Attributes) != 0 || ev.Timestamp != "" || ev.TimeRaw != "" {
				t.Fatal(ev)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestUnknownSpeakerRejected(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "assistant_facts_evidence/2_evidence")
	os.MkdirAll(dest, 0700)
	b, e := os.ReadFile("testdata/input/assistant_facts_evidence/2_evidence/synthetic-person.json")
	if e != nil {
		t.Fatal(e)
	}
	b = []byte(strings.Replace(string(b), `"User"`, `"Tool"`, 1))
	os.WriteFile(filepath.Join(dest, "x.json"), b, 0600)
	_, e = ImportData(t.Context(), Options{Input: dir, Type: "assistant_facts", Out: filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
	if e == nil || !strings.Contains(e.Error(), "unsupported speaker") {
		t.Fatal(e)
	}
}
func TestRealImportStatistics(t *testing.T) {
	input := os.Getenv("MEMEVAL_CONVOMEM_INPUT")
	if input == "" {
		t.Skip("MEMEVAL_CONVOMEM_INPUT gates CC BY-NC real input; fixtures are synthetic")
	}
	absolute, err := filepath.Abs(input)
	if err != nil {
		t.Fatal(err)
	}
	input = testoracle.Dataset(t, absolute)
	h, e := Verify(t.Context(), input)
	if e != nil {
		t.Fatal(e)
	}
	expected := map[string]int{"abstention": 14910, "assistant_facts": 12745, "changing": 18323, "implicit_connection": 7546, "preference": 2749}
	questions, files, events, sessions, exact, missing := 0, 0, 0, 0, 0, 0
	for _, typ := range Types {
		r, e := ImportData(t.Context(), Options{Input: input, Type: typ, Out: filepath.Join(t.TempDir(), "bundle")}, h, false)
		if e != nil {
			t.Fatal(e)
		}
		if r.Statistics.Questions != expected[typ] {
			t.Fatal(r.Statistics)
		}
		d := r.Statistics.Diagnostics
		questions += r.Statistics.Questions
		files += d["files"].(int)
		events += r.Statistics.Events
		sessions += r.Statistics.Sessions
		exact += d["evidence_exact"].(int)
		missing += d["evidence_unmatched"].(int)
		s, e := suite.Open(t.Context(), r.Path)
		if e != nil {
			t.Fatal(e)
		}
		scan, e := suite.LeakScan(s)
		if e != nil || len(scan.Problems) > 0 {
			t.Fatal(e, scan)
		}
		t.Logf("%s files=%v items=%d messages=%d conversations=%d exact=%v unmatched=%v leak=0", typ, d["files"], r.Statistics.Questions, r.Statistics.Events, r.Statistics.Sessions, d["evidence_exact"], d["evidence_unmatched"])
	}
	if questions != 56273 || files != 906 || events != 3801631 || sessions != 106989 || exact != 77261 || missing != 29780 {
		t.Fatal(questions, files, events, sessions, exact, missing)
	}
}
