package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/sandbox/container"
	"memeval/internal/sut"
	"text/tabwriter"
	"time"
)

func newGCCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "gc", Short: "list or remove only abandoned memeval resources", Args: cobra.NoArgs}
	apply := cmd.Flags().Bool("apply", false, "apply deletions (default dry-run)")
	age := cmd.Flags().Duration("older-than", 24*time.Hour, "minimum age for expired resources")
	bases := cmd.Flags().Bool("base-images", false, "include unused digest-pinned base and sidecar images")
	jsonOut := cmd.Flags().Bool("json", false, "emit cleanup plan and results as JSON")
	cache := cmd.Flags().String("cache-dir", "", "environment cache root")
	cmd.RunE = func(_ *cobra.Command, _ []string) error {
		r, e := sut.GarbageCollect(ctx, container.New(container.DefaultConfig()), sut.GCOptions{Apply: *apply, OlderThan: *age, BaseImages: *bases, Cache: *cache})
		if *jsonOut {
			if err := emit(out, r); err != nil {
				return err
			}
		} else {
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ACTION\tKIND\tSIZE\tNAME\tREASON")
			for _, v := range r.Items {
				reason := v.Reason
				if v.Result != "" {
					reason += "; " + v.Result
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", v.Action, v.Kind, v.Size, v.Name, reason)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if *apply {
				fmt.Fprintf(out, "remaining: %v\n", r.Remaining)
			}
		}
		return e
	}
	return cmd
}
