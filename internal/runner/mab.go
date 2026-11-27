package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"memeval/internal/benchmark"
	templates "memeval/internal/benchmark/mab"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/pytext"
	reader "memeval/internal/reader/mab"
	"memeval/internal/result"
	routing "memeval/internal/scoring/judge"
	score "memeval/internal/scoring/mab"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strings"
)

func needsMABJudge(s *suite.Suite) bool {
	needed := false
	s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if c.Gold.Checker != nil && (c.Gold.Checker.Type == "mab_infbench_sum" || c.Gold.Checker.Type == "mab_longmemeval_judge") {
				needed = true
			}
		}
		return nil
	})
	return needed
}
func prepareMAB(s *suite.Suite, o *Options) error {
	needed := false
	err := s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if c.Gold.Checker != nil && c.Gold.Checker.Type == "mab_recsys_recall@5" {
				needed = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if needed {
		path := filepath.Join(benchmark.Root(), "data/upstream/memoryagentbench/entity2id.json")
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		binding, e := bindBenchmark(s, o.RegistryDir)
		if e != nil {
			return e
		}
		p, variant, _, e := benchmark.Resolve(binding.Ref)
		if e != nil {
			return e
		}
		want, _ := p.Variants[variant].Params["candidates_sha256"].(string)
		if want == "" || fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			return fmt.Errorf("entity2id differs from pinned benchmark candidates")
		}
		c, e := score.LoadCandidates(path)
		o.mabCandidates = c
		return e
	}
	return nil
}
func mabAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	if c.Gold.Checker == nil {
		return fmt.Errorf("MAB requires checker")
	}
	source, _ := c.Attributes["mab_source"].(string)
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.QuestionType, Deviations: []string{}}
	r.QA = qa
	f := o.FixedReader
	if f.Input == "items" {
		qa.Deviations = append(qa.Deviations, "reader_input=items uses returned memory text instead of original-source history")
	}
	in, err := reader.Build(items, known, source, c.Query, f.Input, o.fullContext, r.Budget.MaxContextChars)
	if err != nil {
		return err
	}
	qa.Truncated = in.Truncated
	qa.DroppedMessages = in.DroppedMessages
	answer, err := reader.Answer(ctx, f.Client, in, f.Provider, f.Model, sample, f.MaxTokens)
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if err != nil {
		return err
	}
	qa.Hypothesis = &answer.Text
	if answer.ModelReturned != "" && answer.ModelReturned != f.Model {
		qa.Deviations = append(qa.Deviations, "reader returned a different model identifier: "+answer.ModelReturned)
	}
	answers := c.Gold.Answers
	if c.Gold.Checker.Type == "mab_recsys_recall@5" {
		answers = c.Gold.ItemIDs
	}
	if c.Gold.Checker.Type == "mab_infbench_sum" && len(answers) == 0 {
		answers = []string{c.Gold.Reference}
	}
	processed, err := score.PostProcess(answer.Text, answers, source, o.mabCandidates)
	if err != nil {
		return err
	}
	for k, v := range processed.Metrics {
		r.Metrics["mab_"+k] = &v
	}
	primary := c.Gold.Checker.Type
	switch primary {
	case "mab_infbench_sum", "mab_longmemeval_judge":
		var judgment routing.Outcome
		if primary == "mab_infbench_sum" {
			for _, typ := range []string{"rouge1", "rouge2", "rougeL"} {
				v, _ := score.Rouge(answer.Text, c.Gold.Reference, typ)
				r.Metrics["mab_infbench_sum."+typ+"_f1"] = &v
			}
			judgment, err = score.Fluency(ctx, f.JudgeRouter(), sample, answer.Text)
		} else {
			var params struct {
				QuestionType string `json:"question_type"`
				Abstention   bool   `json:"abstention"`
			}
			if err = json.Unmarshal(c.Gold.Checker.Params, &params); err != nil {
				return err
			}
			judgment, err = f.JudgeRouter().Evaluate(ctx, routing.Input{SampleID: sample, QuestionType: params.QuestionType, Question: c.Query, Reference: pytext.ReprStrings(c.Gold.Answers), Response: answer.Text, Abstention: params.Abstention})
		}
		qa.Judge = judgment
		qa.JudgeRequestSHA = judgment.RequestSHA
		if err != nil {
			return err
		}
		label := judgment.Label
		qa.JudgeLabel = &label
		r.Passed = &label
		v := 0.
		if label {
			v = 1
		}
		if primary == "mab_infbench_sum" {
			r.Metrics["mab_infbench_sum.fluency"] = &v
		} else {
			r.Metrics[primary] = &v
		}
	case "mab_exact_match", "mab_substring_exact_match", "mab_eventqa_recall", "mab_recsys_recall@5":
		v := r.Metrics[primary]
		if v == nil {
			return fmt.Errorf("MAB checker metric missing")
		}
		passed := *v == 1
		r.Passed = &passed
	default:
		return fmt.Errorf("unsupported MAB checker")
	}
	r.Status = result.Scored
	return nil
}
func mabMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	r.FrameworkVersion = "0.11.0-p3a"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = "memoryagentbench-official-v1+retrieval-diagnostics-v2"
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": readerTemplateID(reader.PromptID, f.Input), "template_sha256": templates.TemplateSHA256(), "input_mode": f.Input, "fullcontext": o.fullContext, "max_tokens": f.MaxTokens, "context_budget_chars": o.Budget.MaxContextChars}
	truncation := "Reader drops earliest blocks by Unicode characters, preserves rank order of remaining retrieval sources"
	if f.Input == "items" {
		truncation = "reader_input=items uses returned text/rank; Unicode characters including newline separators determine the memory budget; whole tail items are dropped"
	}
	deviations := []string{"Sentence boundaries use neurosnap/sentences English punkt instead of nltk; sentence-boundary comparison pending", "Memorize time_stamp fixed to empty string; no wall-clock text", truncation, "ROUGE implementation has not been oracle-verified when rouge_score is unavailable"}
	sums, counts := map[string]float64{}, map[string]int{}
	bySums, byCounts := map[string]map[string]float64{}, map[string]map[string]int{}
	evaluated, judged, errors, unsupported := 0, 0, 0, 0
	total := 0
	s.Iter(context.Background(), func(ep *suite.Episode) error { total += len(ep.Cases); return nil })
	p, variant, _, err := benchmark.Resolve(r.Protocol.Benchmark.Ref)
	if err != nil {
		return err
	}
	if n, ok := p.Variants[variant].Params["questions"].(float64); ok {
		total = int(n)
	}
	for _, c := range r.Cases {
		if c.Status == result.Unsupported {
			unsupported++
		}
		if c.Status == result.Error || c.Status == result.ContractError || c.Status == result.IngestFailed {
			errors++
		}
		if c.QA != nil {
			if c.QA.Hypothesis != nil && c.Status != result.Invalid {
				evaluated++
			}
			if c.QA.JudgeLabel != nil {
				judged++
			}
			if c.QA.Truncated {
				if f.Input == "items" {
					deviations = appendUnique(deviations, "reader item context truncated by dropping low-ranked tail items")
				} else {
					deviations = appendUnique(deviations, "reader source history truncated")
				}
			}
			for _, d := range c.QA.Deviations {
				deviations = appendUnique(deviations, d)
			}
		}
		if bySums[c.Category] == nil {
			bySums[c.Category] = map[string]float64{}
			byCounts[c.Category] = map[string]int{}
		}
		// Do not include a failed judge's partial metrics as a completed evaluation.
		if c.Status != result.Scored {
			continue
		}
		for k, v := range c.Metrics {
			if v != nil && strings.HasPrefix(k, "mab_") {
				sums[k] += *v
				counts[k]++
				bySums[c.Category][k] += *v
				byCounts[c.Category][k]++
			}
		}
	}
	metrics := map[string]*float64{}
	by := map[string]map[string]*float64{}
	for k, v := range sums {
		n := v / float64(counts[k])
		metrics[k] = &n
	}
	for cat, ks := range bySums {
		by[cat] = map[string]*float64{}
		for k, v := range ks {
			n := v / float64(byCounts[cat][k])
			by[cat][k] = &n
		}
	}
	var info *result.JudgeInfo
	if needsMABJudge(s) {
		info = decisionJudgeInfo(f)

		deviations = append(deviations, "Official gpt-4o is a declared upstream setting; evaluator uses TypeSafe decisions")
		if strings.Contains(s.SuiteID, "long-range-understanding") {
			deviations = append(deviations, "Summary implements P3a ROUGE-1/2/L and fluency only, not upstream three-judge precision/recall/F1 aggregate")
		}
	}
	if strings.Contains(s.SuiteID, "test-time-learning") {
		deviations = append(deviations, "Recsys equal edit-distance ties use lexical ordering instead of nondeterministic Python set order")
	}
	if strings.Contains(s.SuiteID, "overlap") {
		deviations = append(deviations, "300 distinct LongMemEval-S questions in five contexts (60 per context); excluded from independent MAB aggregation")
	}
	complete := evaluated == total && len(r.Cases) == total && errors == 0 && unsupported == 0 && o.Repeats == 1
	r.Protocol.Judge = info
	r.Scores.Official = result.Official{Benchmark: "memoryagentbench/" + variant, Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: readerTemplateID(reader.PromptID, f.Input), InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: complete}, Metrics: metrics, ByTypeMetrics: by, Fidelity: "partial", Deviations: deviations, Errors: errors, Unsupported: unsupported, Judged: judged}
	if engine, ok := f.Client.(*llm.Engine); ok {
		metadata, e := engine.ReportMetadata()
		if e != nil {
			return e
		}
		r.EvaluatorLLM = metadata
		r.Billing.Evaluator = engine.Billing()
		r.Summary.Billing.Evaluator = r.Billing.Evaluator
		r.Billing.EvaluatorByPurpose = map[string]any{}
		for _, purpose := range []string{"reader", "judge-decision"} {
			r.Billing.EvaluatorByPurpose[purpose] = engine.BillingPurpose(purpose)
		}
		r.Summary.Billing.EvaluatorByPurpose = r.Billing.EvaluatorByPurpose
	}
	return nil
}
