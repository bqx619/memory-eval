package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/registry"
	"memeval/internal/report"
	"memeval/internal/suite"
	"os"
	"path/filepath"
)

const rootChangeWarning = "suite_sha256 changed; existing reports/cassettes for the old file are not pairable with runs on the new file"

func bundleCommands(ctx context.Context, out io.Writer) []*cobra.Command {
	pack := &cobra.Command{Use: "pack <single.json>", Args: cobra.ExactArgs(1)}
	dest := pack.Flags().String("out", "", "new bundle directory")
	pack.RunE = func(_ *cobra.Command, args []string) error {
		if *dest == "" {
			return fmt.Errorf("suite pack requires --out")
		}
		s, err := suite.Open(ctx, args[0])
		if err != nil {
			return err
		}
		if s.Container() != "single" {
			return fmt.Errorf("suite pack input must be single")
		}
		old, err := suite.SHA256(s)
		if err != nil {
			return err
		}
		if err = suite.Pack(ctx, s, *dest); err != nil {
			return err
		}
		b, err := suite.Open(ctx, *dest)
		if err != nil {
			return err
		}
		root, err := suite.SHA256(b)
		if err != nil {
			return err
		}
		return emit(out, map[string]any{"bundle": *dest, "old_suite_sha256": old, "new_suite_sha256": root, "warning": rootChangeWarning})
	}
	index := &cobra.Command{Use: "index <dir>", Args: cobra.ExactArgs(1)}
	index.RunE = func(_ *cobra.Command, args []string) error {
		s, err := suite.Reindex(ctx, args[0])
		if err != nil {
			return err
		}
		counts, err := suite.Validate(ctx, s, suite.ValidateOptions{})
		if err != nil {
			return err
		}
		root, err := suite.SHA256(s)
		if err != nil {
			return err
		}
		return emit(out, map[string]any{"suite_sha256": root, "counts": counts, "warning": "Index repaired; review the new root before updating registry trust. Existing reports and cassettes remain bound to their original root."})
	}
	return []*cobra.Command{pack, index}
}

func importedRoot(ctx context.Context, path string) (string, error) {
	s, err := suite.Open(ctx, path)
	if err != nil {
		return "", err
	}
	return suite.SHA256(s)
}

// Only the registered destination can replace a persistent trust anchor. A
// scratch import of the same suite must not redirect the repository registry.
func pinRegisteredImport(ctx context.Context, root, path, id string) error {
	dir := filepath.Join(root, "data/registry")
	r, err := registry.LoadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	entry, ref, ok := r.LookupSuite(id)
	if !ok {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	registered, err := filepath.Abs(filepath.Join(root, ref.Path))
	if err != nil {
		return err
	}
	if absolute != registered {
		return nil
	}
	ref.SuiteSHA256, err = importedRoot(ctx, path)
	if err != nil {
		return err
	}
	ref.ImportStatsPath = filepath.ToSlash(filepath.Join(ref.Path, "import.json"))
	return report.WriteJSON(ctx, filepath.Join(dir, entry.ID+".json"), entry)
}
