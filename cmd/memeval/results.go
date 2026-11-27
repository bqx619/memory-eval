package main

import (
	"context"
	"fmt"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/results"
	"memeval/internal/scoring"

	"github.com/spf13/cobra"
)

func newResultsCommand(ctx context.Context, out io.Writer) *cobra.Command {
	parent := &cobra.Command{Use: "results", Short: "extract result cards, build an index, or export the metric glossary"}
	card := &cobra.Command{Use: "card <report-dir>", Args: cobra.ExactArgs(1), Short: "extract a deterministic result card"}
	dest := card.Flags().String("out", "", "output JSON file (default: stdout)")
	card.RunE = func(cmd *cobra.Command, args []string) error {
		c, e := results.LoadCard(args[0], results.IndexOptions{RepoRoot: benchmark.Root(), Stderr: cmd.ErrOrStderr()})
		if e != nil {
			return e
		}
		return resultOutput(ctx, out, *dest, c)
	}
	build := &cobra.Command{Use: "build", Args: cobra.NoArgs, Short: "build index.json and index.js from report directories"}
	dirs := build.Flags().StringArray("reports", nil, "reports directory (repeatable; searched recursively)")
	dir := build.Flags().String("out", "", "output directory for index.json and index.js")
	build.RunE = func(cmd *cobra.Command, _ []string) error {
		if *dir == "" {
			return fmt.Errorf("results build requires --out directory")
		}
		index, e := results.BuildIndex(results.IndexOptions{RepoRoot: benchmark.Root(), Stderr: cmd.ErrOrStderr(), Commit: results.BuiltCommit()}, (*dirs)...)
		if e != nil {
			return e
		}
		return results.WriteIndex(ctx, *dir, index)
	}
	glossary := &cobra.Command{Use: "glossary", Args: cobra.NoArgs, Short: "export the metric glossary"}
	glossaryOut := glossary.Flags().String("out", "", "output JSON file (default: stdout)")
	glossary.RunE = func(_ *cobra.Command, _ []string) error {
		return resultOutput(ctx, out, *glossaryOut, scoring.Glossary())
	}
	parent.AddCommand(card, build, glossary)
	return parent
}
func resultOutput(ctx context.Context, out io.Writer, path string, v any) error {
	b, e := results.JSON(v)
	if e != nil {
		return e
	}
	if path != "" {
		return results.WriteFile(ctx, path, b)
	}
	_, e = out.Write(b)
	return e
}
