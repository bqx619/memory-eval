package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	"io"
	"memeval/internal/adapter"
	"memeval/internal/benchmark"
	"memeval/internal/config/dotenv"
	"memeval/internal/episodelog"
	"memeval/internal/importers/pmb"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/scoring/judge/inputlog"
	"memeval/internal/stats"
	"memeval/internal/suite"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"
)

// pflag supports the documented interspersed flag syntax.
func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	return dotenv.ApplyFlags(f, args)
}
func emit(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func newValidateCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "validate", Short: "validate a suite"}
	f := cmd.Flags()
	skipLeak := f.Bool("no-leak-scan", false, "skip public projection leak scanning (disclosed in output)")
	expected := f.String("expected-sha256", "", "trusted suite root from registry or an earlier report")

	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if f.NArg() != 1 {
			return fmt.Errorf("validate requires one suite path")
		}
		s, err := suite.Load(ctx, f.Arg(0))
		if err != nil {
			return err
		}
		var leak suite.LeakReport
		counts, err := suite.Validate(ctx, s, suite.ValidateOptions{SkipLeakScan: *skipLeak, LeakReport: &leak, ExpectedSHA256: *expected})
		if err != nil {
			return err
		}
		h, err := suite.SHA256(s)
		if err != nil {
			return err
		}
		var scan any = "skipped"
		valid := true
		if !*skipLeak {
			scan = leak
			valid = len(leak.Problems) == 0
		}
		if err := emit(out, map[string]any{"valid": valid, "counts": counts, "suite_sha256": h, "schema_version": s.SchemaVersion, "by_kind": counts.ByKind, "unresolved_evidence_refs": counts.UnresolvedEvidenceRefs, "leak_scan": scan}); err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("suite leak scan failed")
		}
		return nil
	}
	return cmd
}
func newRunCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "run", Short: "run an evaluation"}
	f := cmd.Flags()
	o := runner.DefaultOptions()
	f.StringVar(&o.RegistryDir, "registry", "data/registry", "dataset registry directory")
	evaluator := llmFlags(f)
	path := f.String("suite", "", "normalized suite path (or pmb)")
	sut := f.String("sut", "bm25", "null|recent|bm25|fullcontext")
	dest := f.String("out", "reports/run", "output directory")
	resumeDir := f.String("resume", "", "resume an existing run directory, preserving other cases and history")
	autoRounds := f.Int("auto-resume-rate-limited", 0, "automatically resume gateway exhaustion/timeouts (0..10 rounds; never budget stops)")
	resumeStatuses := f.String("resume-statuses", "error,invalid", "comma-separated statuses to retry")
	resumeCases := f.String("resume-cases", "", "with --resume: case IDs or sample IDs (comma-separated or @file, one ID per line); intersects --resume-statuses")
	resumeSUTChange := f.Bool("resume-sut-change", false, "with --resume: allow a new SUT identity for retried rows; disclose previous/current identities in history")
	f.StringVar(&o.Mode, "mode", o.Mode, "retrieval|fixed_reader")
	f.StringVar(&o.ResetPolicy, "reset-policy", o.ResetPolicy, "fresh_per_case")
	f.StringVar(&o.RunSeed, "run-seed", "", "64 hex characters")
	f.IntVar(&o.Repeats, "repeats", 1, "independent repeats")
	f.IntVar(&o.BatchSize, "batch-size", 64, "per_event batch size")
	f.IntVar(&o.Parallel, "parallel", 1, "episode workers (1..16)")
	f.IntVar(&o.Budget.TopK, "top-k", 10, "returned item budget")
	f.IntVar(&o.Budget.MaxContextChars, "max-context-chars", 100000, "Unicode code point budget")
	limit := f.Int("limit", 0, "source case limit")
	noLogs := f.Bool("no-logs", false, "disable persistent episode diagnostics")
	logCap := f.Int64("log-file-cap", episodelog.DefaultFileCap, "maximum bytes per diagnostic file including omission marker (minimum 256)")
	keep := f.Bool("keep", false, "retain episode directories")
	allow := f.Bool("allow-unenforced", false, "explicitly allow unverified isolation")
	network := f.String("network", "", "manifest network override none/gateway/open")
	cache := f.String("cache-dir", "", "installed SUT environment cache")
	f.BoolVar(&o.TraceText, "trace-text", false, "include public query and response")

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		if o.Parallel < 1 || o.Parallel > 16 {
			return fmt.Errorf("parallel must be between 1 and 16")
		}
		if warning := runner.ParallelWarning(o.Parallel, runtime.NumCPU()); warning != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), warning)
		}
		var resumed *report.ResumeOutput
		if *resumeDir == "" {
			for _, name := range []string{"resume-statuses", "resume-cases", "resume-sut-change"} {
				if f.Changed(name) {
					return fmt.Errorf("--%s requires --resume", name)
				}
			}
		}
		if *resumeDir != "" {
			if f.Changed("out") && !sameDir(*dest, *resumeDir) {
				return fmt.Errorf("--resume writes to its original directory; --out must match")
			}
			var err error
			resumed, err = report.LoadResume(ctx, *resumeDir)
			if err != nil {
				return err
			}
			defer resumed.Close()
			resumed.Auto, _ = ctx.Value(autoResumeRoundKey{}).(*result.AutoResume)
			if err = restoreRunFlags(f, resumed, &o, *resumeStatuses); err != nil {
				return err
			}
			if f.Changed("resume-cases") {
				if err = o.Resume.SelectCases(*resumeCases); err != nil {
					return err
				}
				resumed.Cases = new(len(o.Resume.Cases))
			}
			o.Resume.AllowSUTChange, resumed.AllowSUTChange = *resumeSUTChange, *resumeSUTChange
			if resumed.Auto != nil {
				o.Resume.RateLimitedOnly()
			}
			*dest = *resumeDir
		}
		if *path == "" || f.NArg() != 0 {
			return fmt.Errorf("run requires --suite")
		}
		f.Visit(func(v *flag.Flag) {
			if v.Name == "limit" {
				o.Limit = limit
			}
		})
		resolved, aliasErr := suiteAlias(*path, &o)
		if aliasErr != nil {
			return aliasErr
		}
		*path = resolved
		var s *suite.Suite
		var err error
		if *path == "pmb" {
			s, _, err = pmb.Import(ctx, "docs/data/pmb_projection.json")
		} else {
			s, err = suite.Load(ctx, *path)
			if err != nil {
				if projection, _, projectionErr := pmb.Import(ctx, *path); projectionErr == nil {
					s, err = projection, nil
				}
			}
		}
		if err != nil {
			return err
		}
		if !f.Changed("reset-policy") && s.ProtocolRef != "" {
			p, v, _, e := benchmark.Resolve(s.ProtocolRef)
			if e != nil {
				return e
			}
			if policy, ok := p.Variants[v].Params["reset_policy_default"].(string); ok {
				o.ResetPolicy = policy
			}
		}
		// Resume validation needs the parsed input mode before any Engine,
		// ledger, cassette or staging directory is opened. prepare attaches
		// the clients later, after this read-only preflight succeeds.
		if o.Mode == runner.FixedReader {
			o.FixedReader = &evaluator.reader
		}
		if err := runner.VerifyResume(ctx, s, o); err != nil {
			return err
		}
		finalDest := *dest
		if resumed != nil {
			if err := resumed.Begin(); err != nil {
				return err
			}
			*dest = resumed.StageDir
		}
		engine, err := evaluator.prepare(ctx, &o, *dest, s)
		if err != nil {
			return err
		}
		if engine != nil {
			defer engine.Close()
			recorder, e := inputlog.New(ctx, *dest)
			if e != nil {
				return e
			}
			defer recorder.Close()
			ctx = inputlog.WithRecorder(ctx, recorder)
		}
		if *sut != "null" && *sut != "recent" && *sut != "bm25" && *sut != "fullcontext" {
			if resumed == nil {
				return runExternal(ctx, s, *sut, *dest, *network, *cache, o, *keep, *allow, episodelog.Options{Disabled: *noLogs, FileCap: *logCap}, out, cmd.ErrOrStderr())
			}
			runErr := runExternal(ctx, s, *sut, *dest, *network, *cache, o, *keep, *allow, episodelog.Options{Disabled: *noLogs, FileCap: *logCap}, io.Discard, cmd.ErrOrStderr())
			publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			r, loadErr := report.Load(publication, filepath.Join(*dest, "report.json"))
			if loadErr != nil {
				if runErr != nil {
					var paused *governorExit
					if errors.As(runErr, &paused) {
						paused.dest = finalDest
					}
					return runErr
				}
				return loadErr
			}
			if err := resumed.Commit(publication, r, o.Resume.Selected, o.Resume.Statuses); err != nil {
				return err
			}
			if err := emit(out, map[string]any{"report": filepath.Join(finalDest, "report.json"), "summary": r.Summary, "redone_cases": r.ResumeHistory[len(r.ResumeHistory)-1].RedoneCases, "resume_count": r.ResumeCount}); err != nil {
				return err
			}
			var paused *governorExit
			if errors.As(runErr, &paused) {
				paused.dest = finalDest
			}
			return runErr
		}
		a, err := adapter.NewBaseline(*sut)
		if err != nil {
			return err
		}
		o.NewAdapter = func(context.Context) (adapter.Adapter, error) { return adapter.NewBaseline(*sut) }
		r, err := runner.Run(ctx, s, a, o)
		if err != nil {
			return err
		}
		publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if resumed != nil {
			err = resumed.Commit(publication, r, o.Resume.Selected, o.Resume.Statuses)
		} else {
			err = report.Save(publication, r, *dest)
		}
		if err != nil {
			return err
		}
		if err = emit(out, map[string]any{"report": filepath.Join(finalDest, "report.json"), "summary": r.Summary, "resume_count": r.ResumeCount}); err != nil {
			return err
		}
		if r.Interruption != nil {
			return &governorExit{reason: r.Interruption.Reason, dest: finalDest}
		}
		if r.Summary.ExecutionErrors > 0 {
			return fmt.Errorf("run completed with %d execution errors", r.Summary.ExecutionErrors)
		}
		return nil
	}
	wrapAutoResume(ctx, cmd, out, autoRounds)
	return cmd
}
func newCompareCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "compare", Short: "compare paired reports"}
	f := cmd.Flags()
	o := stats.DefaultOptions()
	f.StringVar(&o.PairBy, "pair-by", "", "sample (explicitly compare repeated experiments, allowing disclosed top_k differences)")
	f.StringVar(&o.ExcludeStatus, "exclude-status", "", "comma-separated invalid,unscored; explicitly exclude allowed non-scored samples (default strict; do not drop failed cases)")
	f.StringVar(&o.Metric, "metric", "pass", "metric name")
	f.Uint64Var(&o.Seed, "seed", 42, "PCG seed")
	f.IntVar(&o.Draws, "draws", 2000, "bootstrap draws")
	dest := f.String("out", "", "comparison JSON")
	indexPath := f.String("resample-indices", "", "JSONL cluster indices")

	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if f.NArg() != 2 {
			return fmt.Errorf("compare requires left and right report paths")
		}
		left, err := report.Load(ctx, f.Arg(0))
		if err != nil {
			return err
		}
		right, err := report.Load(ctx, f.Arg(1))
		if err != nil {
			return err
		}
		if *indexPath != "" {
			file, err := os.Open(*indexPath)
			if err != nil {
				return err
			}
			o.Indices, err = stats.ReadIndices(ctx, file)
			file.Close()
			if err != nil {
				return err
			}
		}
		c, err := stats.Compare(ctx, left, right, o)
		if err != nil {
			return err
		}
		path := *dest
		if path == "" {
			path = "reports/compare/delta.json"
		}
		if err = report.SaveComparison(ctx, c, path); err != nil {
			return err
		}
		return emit(out, c)
	}
	return cmd
}
func newReportCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "report", Short: "render HTML reports"}
	f := cmd.Flags()
	dest := f.String("out", "", "comparison HTML")
	comparisonPath := f.String("compare", "", "paired comparison JSON")

	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if *dest == "" || f.NArg() == 0 {
			return fmt.Errorf("report requires input paths and --out")
		}
		rs := []*result.Report{}
		for _, p := range f.Args() {
			r, err := report.Load(ctx, p)
			if err != nil {
				return err
			}
			rs = append(rs, r)
		}
		var comparison *result.Comparison
		if *comparisonPath != "" {
			var err error
			comparison, err = report.LoadComparison(ctx, *comparisonPath)
			if err != nil {
				return err
			}
		}
		if err := report.HTMLWithComparison(ctx, rs, comparison, *dest); err != nil {
			return err
		}
		return emit(out, map[string]string{"report": *dest})
	}
	return cmd
}
func newPmbCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "pmb", Short: "import PMB projection"}
	f := cmd.Flags()
	as := f.String("as", "typed_context", "typed_context|retrieval")
	input := f.String("input", "docs/data/pmb_projection.json", "PMB projection")
	dest := f.String("out", "", "output suite")
	format := f.String("format", "bundle", "bundle|single")

	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if *dest == "" || f.NArg() != 0 {
			return fmt.Errorf("import pmb requires --out")
		}
		s, importStats, err := pmb.ImportAs(ctx, *input, *as)
		if err != nil {
			return err
		}
		if err = saveImport(ctx, *dest, s, importStats, *format); err != nil {
			return err
		}
		counts, _ := suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true})
		root, err := importedRoot(ctx, *dest)
		if err != nil {
			return err
		}
		return emit(out, map[string]any{"suite_sha256": root, "suite": *dest, "counts": counts, "statistics": importStats})
	}
	return cmd
}
func main() {
	if handled, code := internalCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}
