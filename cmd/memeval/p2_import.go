package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/importers/locomo"
	"memeval/internal/importers/personamem"
)

func newLocomoCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "locomo", Args: cobra.NoArgs}
	input := c.Flags().String("input", "", "upstream LoCoMo JSON")
	dest := c.Flags().String("out", "", "output suite")
	format := c.Flags().String("format", "bundle", "bundle|single")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if *input == "" || *dest == "" {
			return fmt.Errorf("import locomo requires --input and --out")
		}
		s, stats, e := locomo.Import(ctx, *input)
		if e != nil {
			return e
		}
		if e = saveImport(ctx, *dest, s, stats, *format); e != nil {
			return e
		}
		root, e := importedRoot(ctx, *dest)
		if e != nil {
			return e
		}
		return emit(out, map[string]any{"suite_sha256": root, "suite": *dest, "statistics": stats})
	}
	return c
}
func newPersonaMemCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "personamem", Args: cobra.NoArgs}
	q := c.Flags().String("questions", "", "upstream questions CSV")
	h := c.Flags().String("contexts", "", "upstream shared contexts JSONL")
	v := c.Flags().String("variant", "32k", "32k")
	p := c.Flags().String("system-persona", "include", "include|exclude")
	dest := c.Flags().String("out", "", "output suite")
	format := c.Flags().String("format", "bundle", "bundle|single")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if *q == "" || *h == "" || *dest == "" {
			return fmt.Errorf("import personamem requires --questions, --contexts and --out")
		}
		s, stats, e := personamem.Import(ctx, *q, *h, *v, *p)
		if e != nil {
			return e
		}
		if e = saveImport(ctx, *dest, s, stats, *format); e != nil {
			return e
		}
		root, e := importedRoot(ctx, *dest)
		if e != nil {
			return e
		}
		return emit(out, map[string]any{"suite_sha256": root, "suite": *dest, "statistics": stats})
	}
	return c
}
