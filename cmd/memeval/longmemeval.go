package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/fetch"
	"memeval/internal/importers/longmemeval"
)

func newFetchCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "fetch"}
	f := cmd.Flags()
	var o fetch.Options
	f.StringVar(&o.Out, "out", "data/upstream/longmemeval-s", "upstream directory")
	f.StringVar(&o.Revision, "revision", "", "immutable upstream revision")
	f.BoolVar(&o.AckLicense, "ack-license", false, "acknowledge reviewing data card (not commercial clearance)")
	f.BoolVar(&o.VerifyOnly, "verify-only", false, "verify local lock and files without network")
	f.BoolVar(&o.Force, "force", false, "replace existing lock/content")
	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if f.NArg() != 1 {
			return fmt.Errorf("fetch requires longmemeval-s")
		}
		l, err := (fetch.Client{}).Fetch(ctx, f.Arg(0), o)
		if err != nil {
			return err
		}
		return emit(out, l)

	}
	return cmd
}

func newLongMemEvalCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "longmemeval"}
	f := cmd.Flags()
	input := f.String("input", "", "upstream JSON")
	variant := f.String("variant", "s", "s|oracle")
	revision := f.String("revision", "", "immutable revision (otherwise source.lock.json)")
	dest := f.String("out", "", "output suite")
	format := f.String("format", "bundle", "bundle|single")
	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if *input == "" || *dest == "" || f.NArg() != 0 {
			return fmt.Errorf("import longmemeval requires --input and --out")
		}
		s, counts, err := longmemeval.Import(ctx, *input, *variant, *revision)
		if err != nil {
			return err
		}
		if err = saveImport(ctx, *dest, s, counts, *format); err != nil {
			return err
		}
		root, err := importedRoot(ctx, *dest)
		if err != nil {
			return err
		}
		return emit(out, map[string]any{"suite_sha256": root, "suite": *dest, "statistics": counts, "provenance": s.Provenance})

	}
	return cmd
}
