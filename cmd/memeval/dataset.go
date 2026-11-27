package main

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers"
	"memeval/internal/registry"
	"memeval/internal/report"
	"memeval/internal/suite"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

func saveImport(ctx context.Context, path string, s *suite.Suite, stats importers.Stats, formats ...string) error {
	format := "single" // internal fixtures retain their explicitly single representation
	if len(formats) > 0 {
		format = formats[0]
	}
	if format != "single" && format != "bundle" {
		return fmt.Errorf("format must be bundle or single")
	}
	if err := importers.Check(s, stats); err != nil {
		return err
	}
	b, err := suite.CanonicalJSON(stats)
	if err != nil {
		return err
	}
	if format == "bundle" {
		if err = suite.Pack(ctx, s, path); err != nil {
			return err
		}
		if err = report.Write(ctx, filepath.Join(path, "import.json"), append(b, '\n')); err != nil {
			return err
		}
		return pinRegisteredImport(ctx, benchmark.Root(), path, s.SuiteID)
	}
	if err = report.WriteJSON(ctx, path, s); err != nil {
		return err
	}
	return report.Write(ctx, strings.TrimSuffix(path, filepath.Ext(path))+".import.json", append(b, '\n'))
}
func newSuiteCommand(ctx context.Context, out io.Writer) *cobra.Command {
	parent := &cobra.Command{Use: "suite", Short: "upgrade suites or generate protocol schemas"}
	upgrade := &cobra.Command{Use: "upgrade <in>", Args: cobra.ExactArgs(1)}
	dest := upgrade.Flags().String("out", "", "output upgraded suite")
	upgrade.RunE = func(_ *cobra.Command, args []string) error {
		if *dest == "" {
			return fmt.Errorf("suite upgrade requires --out")
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		old, err := suite.Load(ctx, args[0])
		if err != nil {
			return err
		}
		oldSHA, err := suite.SHA256(old)
		if err != nil {
			return err
		}
		upgraded, err := suite.UpgradeBytes(data)
		if err != nil {
			return err
		}
		s, err := suite.Upgrade(data)
		if err != nil {
			return err
		}
		newSHA, err := suite.SHA256(s)
		if err != nil {
			return err
		}
		if err = report.Write(ctx, *dest, upgraded); err != nil {
			return err
		}
		return emit(out, map[string]any{"old_suite_sha256": oldSHA, "new_suite_sha256": newSHA, "warning": "suite_sha256 changed; existing reports/cassettes for the old file are not pairable with runs on the new file"})
	}
	schema := &cobra.Command{Use: "schema", Args: cobra.NoArgs}
	kind := schema.Flags().String("kind", "suite", "suite|bundle-header|episode|benchmark|registry|sut-manifest|sut-upstream")
	path := schema.Flags().String("out", "", "output path (default: schemas/<kind version>.json)")
	schema.RunE = func(_ *cobra.Command, _ []string) error {
		var b []byte
		var err error
		name := ""
		switch *kind {
		case "suite":
			b, err = suite.Schema()
			name = "suite.v3.json"
		case "bundle-header":
			b, err = suite.BundleHeaderSchema()
			name = "suite-bundle-header.v1.json"
		case "episode":
			b, err = suite.EpisodeSchema()
			name = "suite-episode.v1.json"
		case "benchmark":
			b, err = benchmark.Schema()
			name = "benchmark-protocol.v1.json"
		case "sut-upstream":
			b, err = sut.UpstreamSchema()
			name = "sut-upstream.v1.json"
		case "sut-manifest":
			b, err = sut.Schema()
			name = "sut-manifest.v3.json"
		case "registry":
			b, err = registry.Schema()
			name = "registry.v1.json"
		default:
			return fmt.Errorf("unknown schema kind")
		}
		if err != nil {
			return err
		}
		dest := *path
		if dest == "" {
			dest = filepath.Join("schemas", name)
		}
		if err = report.Write(ctx, dest, b); err != nil {
			return err
		}
		return emit(out, map[string]string{"schema": dest})
	}
	parent.AddCommand(upgrade, schema)
	parent.AddCommand(bundleCommands(ctx, out)...)
	return parent
}
func newRegistryCommand(ctx context.Context, out io.Writer) *cobra.Command {
	parent := &cobra.Command{Use: "registry", Short: "list and verify dataset registrations"}
	dir := parent.PersistentFlags().String("registry", "data/registry", "dataset registry directory")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs}
	asJSON := list.Flags().Bool("json", false, "output full entries as JSON")
	list.RunE = func(_ *cobra.Command, _ []string) error {
		r, err := registry.LoadDir(*dir)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(out, r)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "id\tstatus\tlicense.id\tprotocol_id\tsuites\tverification_date")
		for _, e := range r {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", e.ID, e.ImplementationStatus, e.License.ID, e.ProtocolID, len(e.Suites), e.VerificationDate)
		}
		return w.Flush()
	}
	check := &cobra.Command{Use: "check", Args: cobra.NoArgs}
	verify := check.Flags().Bool("verify-files", false, "verify source lock bytes and sha256")
	check.RunE = func(_ *cobra.Command, _ []string) error {
		r, err := registry.LoadDir(*dir)
		if err != nil {
			_ = emit(out, registry.CheckResult{Problems: []string{err.Error()}})
			return err
		}
		result := r.Check(ctx, benchmark.Root(), *verify)
		if err = emit(out, result); err != nil {
			return err
		}
		if !result.OK {
			return fmt.Errorf("registry check failed with %d problems", len(result.Problems))
		}
		return nil
	}
	parent.AddCommand(list, check)
	return parent
}
