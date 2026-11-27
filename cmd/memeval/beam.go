package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers/beam"
)

func newBEAMCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "beam", Short: "stream a locked BEAM parquet variant to a bundle", Args: cobra.NoArgs}
	var o beam.Options
	c.Flags().StringVar(&o.Input, "input", "", "locked upstream parquet file")
	c.Flags().StringVar(&o.Variant, "variant", "100K", "100K|500K|1M")
	c.Flags().StringVar(&o.Out, "out", "", "new bundle directory")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if o.Input == "" || o.Out == "" {
			return fmt.Errorf("import beam requires --input and --out")
		}
		r, e := beam.Import(ctx, o)
		if e != nil {
			return e
		}
		if e = pinRegisteredImport(ctx, benchmark.Root(), r.Path, r.SuiteID); e != nil {
			return e
		}
		return emit(out, r)
	}
	return c
}
