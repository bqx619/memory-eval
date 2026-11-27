package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers/halumem"
)

func newHaluMemCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "halumem", Short: "import locked HaluMem as a local-only bundle", Args: cobra.NoArgs}
	var o halumem.Options
	c.Flags().StringVar(&o.Input, "input", "", "locked upstream JSONL")
	c.Flags().StringVar(&o.Variant, "variant", "medium", "medium|long")
	c.Flags().StringVar(&o.Out, "out", "", "new local-only bundle directory")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if o.Input == "" || o.Out == "" {
			return fmt.Errorf("import halumem requires --input and --out")
		}
		v, e := halumem.Import(ctx, o)
		if e != nil {
			return e
		}
		if e = pinRegisteredImport(ctx, benchmark.Root(), v.Path, v.SuiteID); e != nil {
			return e
		}
		return emit(out, v)
	}
	return c
}
