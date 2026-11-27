package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers/convomem"
)

func newConvoMemCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "convomem", Short: "import locked evidence-only ConvoMem bundles", Args: cobra.NoArgs}
	var o convomem.Options
	c.Flags().StringVar(&o.Input, "input", "", "locked core_benchmark/evidence_questions directory")
	c.Flags().StringVar(&o.Type, "type", "all", "abstention|assistant_facts|changing|implicit_connection|preference|all")
	c.Flags().StringVar(&o.Out, "out", "", "new bundle directory (all: parent of five bundles)")
	c.RunE = func(*cobra.Command, []string) error {
		if o.Input == "" || o.Out == "" {
			return fmt.Errorf("import convomem requires --input and --out")
		}
		rs, e := convomem.Import(ctx, o)
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
