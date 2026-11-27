// migrate-cassettes copies legacy response snapshots into go-vcr v4 cassettes.
package main

import (
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/config/redact"
	"memeval/internal/llm"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	var source, dest, envFile string
	cmd := &cobra.Command{Use: "migrate-cassettes", Short: "offline copy of response-only JSON snapshots to go-vcr v4", SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().StringVar(&source, "input", "reports/live/cassettes", "legacy cassette directory (read only)")
	cmd.Flags().StringVar(&dest, "out", "", "new output directory (required)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "configuration file used only for credential redaction")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 || dest == "" {
			return fmt.Errorf("requires --out and no positional arguments")
		}
		s, undo, err := dotenv.Load(envFile, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		defer undo()
		r := redact.New(s.Secrets())
		count, err := llm.MigrateCassettes(source, dest, r)
		if err != nil {
			return fmt.Errorf("migration stopped after %d files: %s", count, r.Text(err.Error()))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "migrated=%d format=go-vcr-v4 response_only=true source_unchanged=true\n", count)
		return nil
	}
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
