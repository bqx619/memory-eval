package main

import (
	"context"
	"fmt"
	"io"
	"memeval/internal/config/dotenv"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const usage = "usage: memeval [--env-file path] env | gc | suite | registry | validate | fetch | import | sut | sandbox | run | decide | compare | report | results"

func newRootCommand(ctx context.Context, out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "memeval", Long: usage, SilenceErrors: true, SilenceUsage: true}
	root.SetOut(out)
	root.SetErr(errOut)
	root.CompletionOptions.DisableDefaultCmd = true
	root.RunE = func(*cobra.Command, []string) error { return fmt.Errorf("%s", usage) }
	// run loads this before parsing, so file selection also controls flag defaults.
	root.PersistentFlags().String("env-file", "", "configuration file (default: repository .env)")
	root.PersistentFlags().String("ep-root", "", "private episode directory root")
	for _, b := range dotenv.Bindings {
		if strings.HasPrefix(b.Flag, "docker-") || strings.HasPrefix(b.Flag, "container-") || b.Flag == "linux-binary" || strings.HasPrefix(b.Flag, "sut-llm-") {
			root.PersistentFlags().String(b.Flag, dotenv.Default(b), "container backend "+b.Flag)
		}
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		explicit := []string{}
		cmd.Flags().Visit(func(f *pflag.Flag) { explicit = append(explicit, "--"+f.Name) })
		if err := dotenv.ApplyFlags(cmd.Flags(), explicit); err != nil {
			return err
		}
		for _, b := range dotenv.Bindings {
			if strings.HasPrefix(b.Flag, "docker-") || strings.HasPrefix(b.Flag, "container-") || b.Flag == "linux-binary" || strings.HasPrefix(b.Flag, "sut-llm-") {
				if f := cmd.Flags().Lookup(b.Flag); f != nil {
					if settings := dotenv.From(ctx); settings != nil {
						if f.Changed {
							settings.Sources[b.Env] = "flag"
						} else if os.Getenv(b.Env) == "" {
							settings.Sources[b.Env] = "default"
						}
					}
					if e := os.Setenv(b.Env, f.Value.String()); e != nil {
						return e
					}
				}
			}
		}
		if f := cmd.Flags().Lookup("ep-root"); f != nil && f.Value.String() != "" {
			return os.Setenv("MEMEVAL_EP_ROOT", f.Value.String())
		}
		return nil
	}
	imports := &cobra.Command{Use: "import", Short: "import pmb, longmemeval, locomo, personamem, memoryagentbench, beam, memora, halumem, prefeval or convomem"}
	imports.AddCommand(newPmbCommand(ctx, out), newLongMemEvalCommand(ctx, out), newLocomoCommand(ctx, out), newPersonaMemCommand(ctx, out), newMABCommand(ctx, out), newBEAMCommand(ctx, out), newMemoraCommand(ctx, out), newHaluMemCommand(ctx, out), newPrefEvalCommand(ctx, out), newConvoMemCommand(ctx, out))
	root.AddCommand(newGCCommand(ctx, out), newSuiteCommand(ctx, out), newRegistryCommand(ctx, out), newEnvCommand(ctx, out), newValidateCommand(ctx, out), newFetchCommand(ctx, out), imports,
		newResultsCommand(ctx, out), newRunCommand(ctx, out), newDecideCommand(ctx, out), newCompareCommand(ctx, out), newReportCommand(ctx, out))
	for _, group := range []struct {
		name     string
		children []string
	}{
		{"sut", []string{"setup", "check"}}, {"sandbox", []string{"caps", "selftest"}},
	} {
		parent := &cobra.Command{Use: group.name}
		for _, name := range group.children {
			parent.AddCommand(newSandboxCommand(ctx, out, group.name, name))
		}
		root.AddCommand(parent)
	}
	return root
}

func runCommand(ctx context.Context, args []string, out, errOut io.Writer) error {
	keys := []string{"MEMEVAL_SUT_LLM_CONCURRENCY", "MEMEVAL_SUT_LLM_RPM", "MEMEVAL_SUT_LLM_BUDGET_TOKENS", "MEMEVAL_SUT_LLM_MAX_PAUSE", "MEMEVAL_SUT_LLM_BASE_URL", "MEMEVAL_SUT_LLM_MODEL", "MEMEVAL_SUT_LLM_REASONING_EFFORT", "MEMEVAL_EP_ROOT", "MEMEVAL_DOCKER_CONTEXT", "MEMEVAL_DOCKER_CLI", "MEMEVAL_LINUX_BINARY", "MEMEVAL_CONTAINER_TMPFS_SIZE", "MEMEVAL_CONTAINER_SHM_SIZE"}
	old := map[string]*string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			old[k] = &v
		}
	}
	defer func() {
		for _, k := range keys {
			if v := old[k]; v != nil {
				_ = os.Setenv(k, *v)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}()
	root := newRootCommand(ctx, out, errOut)
	root.SetArgs(legacyArgs(root, args))
	return root.ExecuteContext(ctx)
}

func legacyArgs(root *cobra.Command, args []string) []string {
	// Retain stdlib flag's historical single-dash long spellings too. Recognize
	// only registered names, never rewrite negative numeric values or after --.
	names := map[string]*pflag.Flag{}
	var collect func(*cobra.Command)
	collect = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) { names[f.Name] = f })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) { names[f.Name] = f })
		for _, child := range c.Commands() {
			collect(child)
		}
	}
	collect(root)
	args = append([]string(nil), args...)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, _, equal := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := names[name]
		if strings.HasPrefix(arg, "-") && f != nil {
			if !strings.HasPrefix(arg, "--") {
				args[i] = "-" + arg
			}
			if !equal && f.NoOptDefVal == "" {
				i++
			}
		}
	}
	return args
}
