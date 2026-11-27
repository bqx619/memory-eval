package main

import (
	"context"
	"fmt"
	flag "github.com/spf13/pflag"
	"memeval/internal/config/dotenv"
	"memeval/internal/config/redact"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"path/filepath"
	"strconv"
	"strings"
)

type evaluatorFlags struct {
	flags  *flag.FlagSet
	config llm.Config
	reader runner.FixedReaderOptions
}

func llmFlags(f *flag.FlagSet) *evaluatorFlags {
	e := &evaluatorFlags{config: llm.DefaultConfig(), flags: f}
	commonLLMFlags(f, &e.config)
	f.BoolVar(&e.reader.PrefEvalRemind, "prefeval-remind", false, "append PrefEval official preference reminder (default off)")
	f.StringVar(&e.reader.Model, "reader-model", "gpt-6-luna", "fixed reader model")
	f.StringVar(&e.reader.Provider, "reader-provider", "openai-compat", "openai-compat|anthropic")
	f.StringVar(&e.reader.Input, "reader-input", "sources", "sources|items")
	f.IntVar(&e.reader.MaxTokens, "reader-max-tokens", 512, "reader output token limit")
	e.reader.MinTokensReasoning = f.Int("reader-min-tokens-reasoning", 128, "minimum reader output budget for known reasoning models (0 preserves historical budgets for replay)")
	f.StringVar(&e.reader.JudgeDecisionModel, "judge-decision-model", "jev-1.14.0", "pinned TypeSafe judge model")
	return e
}
func (e *evaluatorFlags) prepare(ctx context.Context, o *runner.Options, dest string, s *suite.Suite) (*llm.Engine, error) {
	if o.Mode != runner.FixedReader {
		return nil, nil
	}
	readerID, err := runner.ValidateReaderBinding(s, o.RegistryDir)
	if err != nil {
		return nil, err
	}
	if readerID == "prefeval" && !e.flags.Changed("reader-max-tokens") {
		e.reader.MaxTokens = 5
		if runner.NeedsPrefEvalJudge(s) {
			e.reader.MaxTokens = 300
		}
	}
	e.config.LedgerPath = filepath.Join(dest, "llm_ledger.jsonl")
	configureEngine(ctx, &e.config)
	engine, err := llm.New(e.config)
	if err != nil {
		return nil, err
	}
	e.reader.Client = engine
	e.reader.DecisionClient = &decision.Client{Engine: engine}
	if readerID != "convomem" && readerID != "longmemeval" && readerID != "memoryagentbench" && readerID != "beam" && readerID != "memora" && readerID != "halumem" && (readerID != "prefeval" || !runner.NeedsPrefEvalJudge(s)) {
		o.FixedReader = &e.reader
		return engine, nil
	}
	err = e.reader.JudgeRouter().Validate()
	if err != nil {
		engine.Close()
		return nil, err
	}
	o.FixedReader = &e.reader
	return engine, nil
}
func suiteAlias(path string, o *runner.Options) (string, error) {
	name, number, has := strings.Cut(path, "@")
	if name != "longmemeval-s" {
		return path, nil
	}
	if has {
		n, err := strconv.Atoi(number)
		if err != nil || n < 1 || n > 500 {
			return "", fmt.Errorf("longmemeval-s subset must be 1..500")
		}
		if o.Limit != nil && *o.Limit != n {
			return "", fmt.Errorf("suite alias and --limit disagree")
		}
		o.Limit = &n
	}
	return "data/normalized/longmemeval-s.json", nil
}

func commonLLMFlags(f *flag.FlagSet, cfg *llm.Config) {
	f.StringVar(&cfg.AnthropicEffort, "anthropic-effort", "low", "Anthropic adaptive-thinking effort")
	f.StringVar(&cfg.PricesPath, "prices", cfg.PricesPath, "optional token price JSON")
	f.BoolVar(&cfg.TracePrompts, "trace-prompts", false, "write complete evaluator prompts and responses to out/prompts.jsonl (0600)")
	f.StringVar(&cfg.Mode, "llm", "replay", "live|record|replay|record-once")
	f.StringVar(&cfg.BaseURL, "llm-base-url", "", "evaluator-only base URL override")
	f.StringVar(&cfg.CassetteDir, "cassette-dir", "", "evaluator cassette directory")
	f.StringVar(&cfg.PricesPath, "llm-prices", cfg.PricesPath, "optional token price JSON")
	f.IntVar(&cfg.Parallel, "llm-parallel", 4, "maximum evaluator calls in flight")
	f.DurationVar(&cfg.Timeout, "llm-timeout", cfg.Timeout, "per-attempt HTTP timeout")
	f.DurationVar(&cfg.RetryMaxWait, "llm-retry-max-wait", cfg.RetryMaxWait, "maximum Retry-After/exponential backoff wait (default 60s)")
	f.IntVar(&cfg.MaxRetries, "llm-retries", 5, "maximum retries (0..10)")
}
func configureEngine(ctx context.Context, cfg *llm.Config) {
	cfg.Redactor = redact.From(ctx)
	if s := dotenv.From(ctx); s != nil {
		cfg.ModelStatusPath = filepath.Join(s.Root, "config/judge_models.json")
	}
}
