package runner

import (
	"context"
	"fmt"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	reader "memeval/internal/reader/prefeval"
	"memeval/internal/result"
	score "memeval/internal/scoring/prefeval"
	"memeval/internal/suite"
	"strings"
)

func prefEvalSupported(c suite.Case) bool {
	return c.Kind == "rubric" && c.Gold.Rubric != nil && c.Gold.Rubric.Aggregate == "prefeval" || c.Kind == "mcq" && c.Gold.MCQ != nil && c.Gold.MCQ.Parse == "prefeval_choice_tag"
}
func NeedsPrefEvalJudge(s *suite.Suite) bool {
	needs := false
	_ = s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if c.Kind == "rubric" && prefEvalSupported(c) {
				needs = true
			}
		}
		return nil
	})
	return needs
}
func prefEvalAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	if !prefEvalSupported(c) {
		return fmt.Errorf("PrefEval requires prefeval rubric or choice-tag MCQ")
	}
	f := o.FixedReader
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.Category, Deviations: []string{}, JudgeDetails: []any{}}
	r.QA = qa
	in, e := reader.Build(items, known, c, o.fullContext, f.PrefEvalRemind, r.Budget.MaxContextChars)
	if e != nil {
		return e
	}
	qa.Truncated = in.Truncated
	qa.DroppedMessages = in.DroppedMessages
	answer, e := reader.Answer(ctx, f.Client, in, f.Provider, f.Model, sample, f.MaxTokens)
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if e != nil {
		return e
	}
	qa.Hypothesis = &answer.Text
	passed := false
	if c.Kind == "mcq" {
		choice := score.ExtractChoice(answer.Text)
		passed = choice == c.Gold.Choice && choice != ""
		found := []string{}
		if choice != "" {
			found = append(found, choice)
		}
		r.AnswerParse = &result.AnswerParse{RawFinal: choice, OptionsFound: found}
		v := 0.
		if passed {
			v = 1
		}
		r.Metrics["mcq.correct"] = &v
	} else {
		judgments, e := score.Evaluate(ctx, f.JudgeRouter(), sample, c.Query, answer.Text, c.Gold.Reference)
		answers := map[string]string{}
		for _, j := range judgments {
			qa.JudgeDetails = append(qa.JudgeDetails, j)
			answers[j.Probe] = j.Answer
		}
		if e != nil {
			qa.Deviations = append(qa.Deviations, "PrefEval invalid judgment: entire question excluded from official denominators")
			return e
		}
		v := score.Combine(answers)
		for k, x := range v.Metrics() {
			r.Metrics[k] = &x
		}
		passed = v.Accuracy
		qa.JudgeLabel = &passed
	}
	r.Status = result.Scored
	r.Passed = &passed
	return nil
}
func prefEvalMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	generation := NeedsPrefEvalJudge(s)
	r.FrameworkVersion = "0.15.0-p4a"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = "prefeval-v1+retrieval-diagnostics-v2"
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": reader.PromptID, "template_set_sha256": score.SHA256(), "input_mode": f.Input, "fullcontext": o.fullContext, "max_tokens": f.MaxTokens, "temperature": 0, "remind": f.PrefEvalRemind, "context_budget_chars": o.Budget.MaxContextChars}
	deviations := []string{"Official Claude 3 Sonnet (Bedrock anthropic.claude-3-sonnet-20240229-v1:0), judge temperature=0/max_tokens=100 declared only; fidelity partial", "Official realtime assistant pref_generation is omitted", "PrefEval judge errors mark cases error (invalid) and exclude entire questions from official denominators", "Reader max_tokens official generation=300/MCQ=5; actual limit reported; framework character budget drops oldest messages"}
	var info *result.JudgeInfo
	if generation {
		info = decisionJudgeInfo(f)

	} else {
		deviations = append(deviations, "MCQ shuffle uses Go NewSource(41).Perm and stored order, not Python random.sample; no judge calls")
	}
	sums, ns := map[string]float64{}, map[string]int{}
	byS, byN := map[string]map[string]float64{}, map[string]map[string]int{}
	keys := []string{"mcq.correct"}
	primary := "mcq.correct"
	if generation {
		keys = []string{"prefeval.preference_following_accuracy", "prefeval.error.unhelpful", "prefeval.error.inconsistent", "prefeval.error.hallucination_of_preference_violation", "prefeval.error.preference_unaware_violation"}
		primary = keys[0]
	}
	for _, k := range keys {
		ns[k] = 0
	}
	total, evaluated, judged, errors, unsupported := 0, 0, 0, 0, 0
	if e := s.Iter(context.Background(), func(ep *suite.Episode) error { total += len(ep.Cases); return nil }); e != nil {
		return e
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
			for _, d := range c.QA.Deviations {
				deviations = appendUnique(deviations, d)
			}
		}
		if byS[c.Category] == nil {
			byS[c.Category] = map[string]float64{}
			byN[c.Category] = map[string]int{}
			for _, k := range keys {
				byN[c.Category][k] = 0
			}
		}
		if c.Status != result.Scored {
			continue
		}
		for k, v := range c.Metrics {
			if k != "mcq.correct" && !strings.HasPrefix(k, "prefeval.") {
				continue
			}
			if v != nil {
				sums[k] += *v
				ns[k]++
				byS[c.Category][k] += *v
				byN[c.Category][k]++
			}
		}
	}
	means := func(sum map[string]float64, ns map[string]int) map[string]*float64 {
		out := map[string]*float64{}
		for k, n := range ns {
			out[k] = nil
			if n > 0 {
				v := sum[k] / float64(n)
				out[k] = &v
			}
		}
		return out
	}
	metrics := means(sums, ns)
	by := map[string]map[string]*float64{}
	for k, v := range byN {
		by[k] = means(byS[k], v)
	}
	r.Protocol.Judge = info
	r.Scores.Official = result.Official{Benchmark: "prefeval/" + r.Protocol.Benchmark.Variant, Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: reader.PromptID, InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: evaluated == total && len(r.Cases) == total && errors == 0 && unsupported == 0 && o.Repeats == 1}, Score: metrics[primary], Metrics: metrics, ByTypeMetrics: by, Fidelity: "partial", Deviations: deviations, Errors: errors, Unsupported: unsupported, Judged: judged}
	for _, d := range deviations {
		r.Limitations = appendUnique(r.Limitations, d)
	}
	if engine, ok := f.Client.(*llm.Engine); ok {
		m, e := engine.ReportMetadata()
		if e != nil {
			return e
		}
		r.EvaluatorLLM = m
		r.Billing.Evaluator = engine.Billing()
		r.Summary.Billing.Evaluator = r.Billing.Evaluator
		r.Billing.EvaluatorByPurpose = map[string]any{}
		for _, p := range []string{"reader", "judge-decision"} {
			r.Billing.EvaluatorByPurpose[p] = engine.BillingPurpose(p)
		}
		r.Summary.Billing.EvaluatorByPurpose = r.Billing.EvaluatorByPurpose
	}
	return nil
}
