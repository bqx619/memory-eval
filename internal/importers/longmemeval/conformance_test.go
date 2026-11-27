package longmemeval

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
	conformance.Run(t, conformance.Spec{Name: "longmemeval", Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
		return Import(ctx, "testdata/fixture.json", "s", "98d7416c24c778c2fee6e6f3006e7a073259d48f")
	}, GoldenSuite: "testdata/suite.golden.json", GoldenStats: "testdata/stats.golden.json", Update: *updateGolden})
}
