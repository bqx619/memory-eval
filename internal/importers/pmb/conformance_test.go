package pmb

import (
	"context"
	"flag"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update importer golden fixtures after review")

func TestConformance(t *testing.T) {
	for _, kind := range []string{"typed_context", "retrieval"} {
		t.Run(kind, func(t *testing.T) {
			conformance.Run(t, conformance.Spec{Name: "pmb-" + kind, Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
				return ImportAs(ctx, "../../../docs/data/pmb_projection.json", kind)
			}, GoldenSuite: "testdata/" + kind + ".suite.golden.json", GoldenStats: "testdata/" + kind + ".stats.golden.json", Update: *updateGolden})
		})
	}
}
