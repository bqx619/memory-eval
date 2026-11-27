package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/protocol"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/scoring"
	"memeval/internal/stats"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type decisionInput struct {
	SchemaVersion string `json:"schema_version"`
	Cases         []struct {
		ID         string              `json:"id"`
		ClusterID  string              `json:"cluster_id,omitempty"`
		State      string              `json:"state"`
		Questions  []decision.Question `json:"questions"`
		TemplateID string              `json:"template_id,omitempty"`
	} `json:"cases"`
}

func newDecideCommand(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "decide"}
	f := cmd.Flags()
	cfg := llm.DefaultConfig()
	commonLLMFlags(f, &cfg)
	provider := f.String("provider", "typesafe", "typesafe")
	model := f.String("model", "", "TypeSafe decision model (required)")
	input := f.String("input", "", "local decision fixture JSON (required)")
	dest := f.String("out", "reports/decide", "output directory")
	repeats := f.Int("repeats", 1, "independent requests per original case")
	seed := f.String("run-seed", "", "64 hex characters")
	headersPath := f.String("headers", "", "JSON object of TypeSafe-Danger-Zone-* experimental headers")
	fixture := f.Bool("fixture", false, "explicitly label local fake-provider evidence")
	cmd.RunE = func(_ *cobra.Command, args []string) error {

		if f.NArg() != 0 || *model == "" || *input == "" || *repeats < 1 {
			return fmt.Errorf("decide requires --model, --input and positive repeats")
		}
		data, err := os.ReadFile(*input)
		if err != nil {
			return err
		}
		var source decisionInput
		if err = json.Unmarshal(data, &source); err != nil {
			return err
		}
		if source.SchemaVersion != "1" || len(source.Cases) == 0 {
			return fmt.Errorf("decision input requires schema_version=1 and nonempty cases")
		}
		headers := map[string]string{}
		if *headersPath != "" {
			b, err := os.ReadFile(*headersPath)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &headers); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for i := range source.Cases {
			c := &source.Cases[i]
			if c.ID == "" || seen[c.ID] {
				return fmt.Errorf("empty/duplicate decision case id")
			}
			seen[c.ID] = true
			if c.ClusterID == "" {
				c.ClusterID = c.ID
			}
			if c.TemplateID == "" {
				c.TemplateID = "memeval/closed-rubric-v1"
			}
			normalized, err := decision.Normalize(decision.DecisionRequest{Purpose: "decision", Provider: *provider, Model: *model, State: c.State, Questions: c.Questions, SampleID: "validation", TemplateID: c.TemplateID, Headers: headers})
			if err != nil {
				return err
			}
			c.Questions = normalized.Questions
			headers = normalized.Headers
		}
		digest, err := suite.Digest(source)
		if err != nil {
			return err
		}
		*seed, err = identity.Seed(*seed)
		if err != nil {
			return err
		}
		invocationSeed, err := identity.Seed("")
		if err != nil {
			return err
		}
		invocation, _ := identity.New(invocationSeed, "")
		runID := invocation.Anon("invocation", "decision")
		cfg.LedgerPath = filepath.Join(*dest, "llm_ledger.jsonl")
		configureEngine(ctx, &cfg)
		engine, err := llm.New(cfg)
		if err != nil {
			return err
		}
		defer engine.Close()
		if err = engine.Start(runID); err != nil {
			return err
		}
		client := &decision.Client{Engine: engine}
		start := time.Now()
		warnings := []string{"Evaluator-side decision client only; no memory-system or Jev memory-quality evaluation", "TypeSafe wire follows Claude's recorded live observations; this implementation is verified with offline fixtures only", "Native token limits are server-enforced; no guessed tokenizer or temperature/seed is sent", "decision.<name> is P(true) for noul, P(selected option) for choice, expected zero-based level for score; means are over repeated requests"}
		if *fixture {
			warnings = append(warnings, "FIXTURE: fake upstream responses, no real provider/model evaluation or fees")
		}
		r := &result.Report{Parallel: 1, FrameworkVersion: "0.6.0-m3", RunID: runID, RunSeed: *seed, StartedAtUTC: start.UTC().Format(time.RFC3339Nano), SuiteID: "evaluator-decision-fixture", SuiteSHA256: digest, Source: map[string]any{"source_url": "local-decision-input", "cluster_basis": "case", "fixture": *fixture}, SourceCounts: result.Counts{Episodes: len(source.Cases), Cases: len(source.Cases)}, SourceLimitations: warnings, Mode: "decision", Repeats: *repeats, Cases: []result.Case{}, ByCategory: map[string]result.Summary{}, Warnings: warnings, EvaluatorLLM: engine.Configuration(), Environment: result.Environment{GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}}
		r.SUT = result.SUT{Name: "evaluator-decision-client", Variant: *provider + "/" + *model, Describe: protocol.Describe{Name: "evaluator-decision-client", Version: "0.6.0-m3", Capabilities: []string{}, Limitations: warnings}, Declared: map[string]any{"provider": *provider, "model": *model, "mapping_verified": true, "fixture": *fixture}, Fingerprint: map[string]result.FingerprintValue{}}
		r.Protocol = result.Protocol{Version: "paeval-v2", Scoring: "decision-values-v2", ResetPolicy: "independent_requests", Ingestion: "none", GoldHidden: true, KUnit: "named closed-rubric questions", Changes: []string{"Evaluator calls only; no SUT executed", "No native seed or temperature: each repeat has a distinct anonymous sample_id", "Question names sorted; choice options retain their supplied order"}}
		r.Isolation = result.Isolation{Tier: "C", Backend: "evaluator-process", NetworkMode: "inherit", Host: result.Host{OS: runtime.GOOS, Arch: runtime.GOARCH}}
		r.Protocol.Changes = append(r.Protocol.Changes, "TypeSafe: noul=P(true); choice=P(selected option); score=expected zero-based level, not a probability")
		for _, c := range source.Cases {
			for repeat := 0; repeat < *repeats; repeat++ {
				sample := identity.SampleID(c.ID, repeat)
				id, _ := identity.New(digest, sample)
				req := decision.DecisionRequest{Purpose: "decision", Provider: *provider, Model: *model, State: c.State, Questions: c.Questions, SampleID: id.Anon("evaluator-sample", sample), TemplateID: c.TemplateID, Headers: headers}
				before := time.Now()
				response, callErr := client.Decide(ctx, req)
				row := result.Case{CaseID: c.ID, EpisodeID: c.ID, SampleID: sample, RepeatIndex: repeat, Category: "closed-rubric", ClusterID: c.ClusterID, Kind: "decision", Status: result.Scored, Metrics: map[string]*float64{}, Failures: []string{}, RetrievedSourceIDs: []string{}, Diagnostics: map[string]int{}, Timings: map[string]float64{"decision_ms": float64(time.Since(before).Nanoseconds()) / 1e6}}
				row.PublicInputSHA256, _ = suite.Digest(struct {
					State     string
					Questions []decision.Question
				}{c.State, c.Questions})
				if callErr != nil {
					row.Status = result.Error
					row.ErrorType = "DecisionError"
					if e, ok := callErr.(interface{ ErrorType() string }); ok {
						row.ErrorType = e.ErrorType()
					}
					row.Note = stringPtr("Evaluator decision failed; inspect redacted ledger error")
				} else {
					for name, a := range response.Answers {
						p := decision.MetricValue(a)
						row.Metrics["decision."+name] = &p
					}
					row.Decision = map[string]any{"model_returned": response.ModelReturned, "answers": response.Answers, "calibration": response.Calibration, "request_sha": response.RequestSHA, "raw": response.Raw}
				}
				r.Cases = append(r.Cases, row)
			}
		}
		r.Summary = scoring.Aggregate(r.Cases)
		r.ByCategory["closed-rubric"] = r.Summary
		r.RepeatAnalysis = stats.Repeats(r.Cases, r.Repeats)
		r.Billing.Evaluator = engine.Billing()
		r.Summary.Billing.Evaluator = r.Billing.Evaluator
		r.EvaluatorLLM, err = engine.ReportMetadata()
		if err != nil {
			return err
		}
		r.WallSeconds = time.Since(start).Seconds()
		if err = report.Save(context.WithoutCancel(ctx), r, *dest); err != nil {
			return err
		}
		if err = emit(out, map[string]any{"report": filepath.Join(*dest, "report.json"), "summary": r.Summary, "repeats": r.RepeatAnalysis, "billing": r.Billing}); err != nil {
			return err
		}
		if r.Summary.ExecutionErrors > 0 {
			return fmt.Errorf("decision run completed with %d execution errors", r.Summary.ExecutionErrors)
		}
		return nil

	}
	return cmd
}

func stringPtr(s string) *string { return &s }
