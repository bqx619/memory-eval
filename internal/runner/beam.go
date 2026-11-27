package runner

import (
	"context"
	"fmt"
	"memeval/internal/benchmark"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	reader "memeval/internal/reader/beam"
	"memeval/internal/result"
	score "memeval/internal/scoring/beam"
	judge "memeval/internal/scoring/judge/beam"
	"memeval/internal/suite"
	"strings"
)

const beamItemsDeviation = "reader_input=items uses returned memory text in rank order, without original turn roles/chronology or source expansion; newline separators count toward the Unicode context budget"
const beamItemsTruncation = "reader item text truncated by Unicode character budget, dropping lowest-ranked whole items"

func beamSupported(c suite.Case) bool {
	return c.Kind == "rubric" && c.Gold.Checker != nil && (c.Gold.Checker.Type == "beam_rubric" || c.Gold.Checker.Type == "beam_event_ordering")
}
func beamAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	if !beamSupported(c) || c.Gold.Rubric == nil {
		return fmt.Errorf("BEAM requires rubric/checker")
	}
	f := o.FixedReader
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.Category, Deviations: []string{}, JudgeDetails: []any{}}
	r.QA = qa
	if f.Input == "items" {
		qa.Deviations = append(qa.Deviations, beamItemsDeviation)
	}
	in, e := reader.Build(items, known, c.Query, f.Input, o.fullContext, r.Budget.MaxContextChars)
	if e != nil {
		return e
	}
	qa.Truncated = in.Truncated
	qa.DroppedMessages = in.DroppedMessages
	if f.Input == "items" && in.Truncated {
		qa.Deviations = append(qa.Deviations, beamItemsTruncation)
	}
	answer, e := reader.Answer(ctx, f.Client, in, f.Provider, f.Model, sample, f.MaxTokens)
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if e != nil {
		return e
	}
	qa.Hypothesis = &answer.Text
	if c.Gold.Checker.Type == "beam_event_ordering" {
		aligned, trace, e := judge.Align(ctx, f.JudgeRouter(), sample, c.Gold.Sequence, strings.Split(answer.Text, "\n"))
		for i, t := range trace {
			qa.JudgeDetails = append(qa.JudgeDetails, map[string]any{"phase": "alignment", "index": i, "outcome": t})
		}
		if e != nil {
			return e
		}
		v := score.OrderingScore(c.Gold.Sequence, aligned)
		r.Metrics["beam.event_ordering.precision"] = &v.Precision
		r.Metrics["beam.event_ordering.recall"] = &v.Recall
		r.Metrics["beam.event_ordering.f1"] = &v.F1
		r.Metrics["beam.event_ordering.tau_b"] = v.Tau
		r.Metrics["beam.event_ordering.tau_norm"] = v.TauNorm
		r.Metrics["beam.event_ordering.final"] = v.Final
		if v.Tau == nil {
			qa.Deviations = append(qa.Deviations, "Kendall tau_b is undefined (upstream NaN); tau_b/tau_norm/final recorded as null")
		}
	}
	scores := []float64{}
	for i, item := range c.Gold.Rubric.Items {
		v, e := judge.Rubric(ctx, f.JudgeRouter(), fmt.Sprintf("%s:rubric:%d", sample, i), item.Text, answer.Text)
		qa.JudgeDetails = append(qa.JudgeDetails, map[string]any{"phase": "rubric", "index": i, "outcome": v})
		qa.Deviations = append(qa.Deviations, v.Deviations...)
		if e != nil {
			return e
		}
		scores = append(scores, v.Score)
	}
	mean, e := score.RubricMean(scores)
	if e != nil {
		return e
	}
	r.Metrics["beam.llm_judge_score"] = &mean
	r.Status = result.Scored
	passed := mean == 1
	r.Passed = &passed
	qa.JudgeLabel = &passed
	return nil
}
func beamMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	r.FrameworkVersion = "0.12.0-p3b"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = "beam-rubric-ordering-v1+retrieval-diagnostics-v2"
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": readerTemplateID(reader.PromptID, f.Input), "template_sha256": judge.SHA256(), "input_mode": f.Input, "fullcontext": o.fullContext, "max_tokens": f.MaxTokens, "context_budget_chars": o.Budget.MaxContextChars}
	deviations := []string{"Reader retains original chronological role/content turns and drops earliest whole turns by Unicode characters instead of upstream model-token pruning/pair adjustment", "Official gpt-4.1-mini temperature 0 is declared only; TypeSafe replacement fidelity is partial", "Jev rubric uses probabilities argmax over levels 0/0.5/1, ties choose the lower level; native expectation and confidence are audit metadata", "Typed decisions replace official free-text JSON score parsing; ordering metrics and NaN handling are unchanged", "Undefined Kendall tau_b and resulting final score are null (excluded from numeric means)"}
	if f.Input == "items" {
		deviations[0] = beamItemsDeviation
	}
	info := decisionJudgeInfo(f)

	sums, ns := map[string]float64{}, map[string]int{}
	byS, byN := map[string]map[string]float64{}, map[string]map[string]int{}
	evaluated, judged, errors, unsupported, total := 0, 0, 0, 0, 0
	if e := s.Iter(context.Background(), func(ep *suite.Episode) error { total += len(ep.Cases); return nil }); e != nil {
		return e
	}
	p, variant, _, e := benchmark.Resolve(r.Protocol.Benchmark.Ref)
	if e != nil {
		return e
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
			for _, d := range c.QA.Deviations {
				deviations = appendUnique(deviations, d)
			}
			if c.QA.Truncated {
				if f.Input == "items" {
					deviations = appendUnique(deviations, beamItemsTruncation)
				} else {
					deviations = appendUnique(deviations, "reader source history truncated")
				}
			}
		}
		if c.Status != result.Scored {
			continue
		}
		if byS[c.Category] == nil {
			byS[c.Category] = map[string]float64{}
			byN[c.Category] = map[string]int{}
		}
		for k, v := range c.Metrics {
			if !strings.HasPrefix(k, "beam.") {
				continue
			}
			if _, ok := ns[k]; !ok {
				ns[k] = 0
			}
			if _, ok := byN[c.Category][k]; !ok {
				byN[c.Category][k] = 0
			}
			if v != nil {
				sums[k] += *v
				ns[k]++
				byS[c.Category][k] += *v
				byN[c.Category][k]++
			}
		}
	}
	means := func(sums map[string]float64, ns map[string]int) map[string]*float64 {
		out := map[string]*float64{}
		for k, n := range ns {
			out[k] = nil
			if n > 0 {
				v := sums[k] / float64(n)
				out[k] = &v
			}
		}
		return out
	}
	metrics := means(sums, ns)
	by := map[string]map[string]*float64{}
	for cat, ns := range byN {
		by[cat] = means(byS[cat], ns)
	}
	r.Protocol.Judge = info
	r.Scores.Official = result.Official{Benchmark: "beam/" + variant, Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: readerTemplateID(reader.PromptID, f.Input), InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: evaluated == total && len(r.Cases) == total && errors == 0 && unsupported == 0 && o.Repeats == 1}, Score: metrics["beam.llm_judge_score"], Metrics: metrics, ByTypeMetrics: by, Fidelity: "partial", Deviations: deviations, Errors: errors, Unsupported: unsupported, Judged: judged}
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
