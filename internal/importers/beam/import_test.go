package beam

import (
	"context"
	"flag"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "update reviewed BEAM conformance goldens")

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Spec{Name: "BEAM", Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
		r, e := ImportFile(ctx, Options{Input: "testdata/fixture.json", Variant: "100K", Out: filepath.Join(t.TempDir(), "bundle")}, true)
		if e != nil {
			return nil, importers.Stats{}, e
		}
		s, e := suite.Open(ctx, r.Path)
		return s, r.Statistics, e
	}, GoldenSuite: "testdata/suite.golden.json", GoldenStats: "testdata/stats.golden.json", Update: *update})
}
func TestTurnAndGoldMapping(t *testing.T) {
	e := ReadRows(t.Context(), "testdata/fixture.json", func(_ int, row Row) error {
		ep, n, e := Episode(row)
		if e != nil {
			return e
		}
		if n != 1 || len(ep.Events) != 60 || len(ep.Cases) != 10 {
			t.Fatal(n, len(ep.Events), len(ep.Cases))
		}
		a := ep.Events[0]
		if a.Timestamp != "2024-03-15T00:00:00Z" || a.TimeRaw != "March-15-2024" || a.TurnGroup != "batch0" || len(a.Attributes) != 0 {
			t.Fatal(a)
		}
		for _, c := range ep.Cases {
			if c.Kind != "rubric" || c.Gold.Rubric.Items[0].Polarity != "as_written" || c.Cutoff != 60 {
				t.Fatal(c.ID)
			}
			if c.Category == "event_ordering" && len(c.Gold.Sequence) != len(c.Gold.Rubric.Items) {
				t.Fatal("order")
			}
		}
		row.Chat[0][1].ID = row.Chat[0][0].ID
		dup, _, err := Episode(row)
		if err != nil {
			t.Fatal(err)
		}
		if dup.Events[1].SourceID != dup.Events[0].SourceID+":occ2" {
			t.Fatal("duplicate occurrence not retained")
		}
		row.Chat[0][1].ID = -1
		if _, _, e = Episode(row); e == nil {
			t.Fatal("negative id accepted")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestNestedEvidence(t *testing.T) {
	ids, e := sourceIDs(map[string]any{"second": []any{float64(5)}, "first": []any{float64(2), float64(5)}})
	if e != nil || len(ids) != 2 || ids[0] != 2 || ids[1] != 5 {
		t.Fatal(ids, e)
	}
}
