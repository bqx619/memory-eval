package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers/prefeval"
)

func newPrefEvalCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "prefeval", Short: "import locked PrefEval preference tasks as a bundle", Args: cobra.NoArgs}
	var o prefeval.Options
	c.Flags().StringVar(&o.Input, "input", "", "locked benchmark_dataset directory")
	c.Flags().StringVar(&o.Form, "form", "explicit", "explicit|implicit-choice|implicit-persona")
	c.Flags().StringVar(&o.Task, "task", "generation", "generation|mcq")
	c.Flags().IntVar(&o.InterTurns, "inter-turns", 10, "number of filler user/assistant pairs")
	c.Flags().StringVar(&o.Out, "out", "", "new bundle directory")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if o.Input == "" || o.Out == "" {
			return fmt.Errorf("import prefeval requires --input and --out")
		}
		r, e := prefeval.Import(ctx, o)
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
