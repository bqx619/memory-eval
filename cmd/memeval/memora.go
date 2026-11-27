package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers/memora"
)

func newMemoraCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "memora", Short: "import locked Memora periods as bundles", Args: cobra.NoArgs}
	var o memora.Options
	c.Flags().StringVar(&o.Input, "input", "", "locked upstream data directory")
	c.Flags().StringVar(&o.Period, "period", "all", "weekly|monthly|quarterly|all (all creates three child bundles)")
	c.Flags().StringVar(&o.Out, "out", "", "new bundle directory or parent for all")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if o.Input == "" || o.Out == "" {
			return fmt.Errorf("import memora requires --input and --out")
		}
		rs, e := memora.Import(ctx, o)
		if e != nil {
			return e
		}
		for _, r := range rs {
			if e = pinRegisteredImport(ctx, benchmark.Root(), r.Path, r.SuiteID); e != nil {
				return e
			}
		}
		return emit(out, rs)
	}
	return c
}
