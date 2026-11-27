package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers/memoryagentbench"
)

func newMABCommand(ctx context.Context, out io.Writer) *cobra.Command {
	c := &cobra.Command{Use: "memoryagentbench", Args: cobra.NoArgs, Short: "stream a locked MemoryAgentBench split to bundle(s)"}
	var o memoryagentbench.Options
	c.Flags().StringVar(&o.Input, "input", "", "upstream directory containing source.lock.json and data/*.parquet")
	c.Flags().StringVar(&o.Split, "split", "", "accurate_retrieval|conflict_resolution|long_range_understanding|test_time_learning")
	c.Flags().IntVar(&o.ChunkTokens, "chunk-tokens", 4096, "4096|512; official sentence-token greedy budget")
	c.Flags().StringVar(&o.Out, "out", "", "new bundle directory; AR overlap is a sibling bundle")
	c.RunE = func(_ *cobra.Command, _ []string) error {
		if o.Input == "" || o.Split == "" || o.Out == "" {
			return fmt.Errorf("import memoryagentbench requires --input, --split and --out")
		}
		results, e := memoryagentbench.Import(ctx, o)
		if e != nil {
			return e
		}
		for _, r := range results {
			if e = pinRegisteredImport(ctx, benchmark.Root(), r.Path, r.SuiteID); e != nil {
				return e
			}
		}
		return emit(out, results)
	}
	return c
}
