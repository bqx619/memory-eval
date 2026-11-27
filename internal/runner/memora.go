package runner

import (
	"context"
	"fmt"
	"memeval/internal/benchmark"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	reader "memeval/internal/reader/memora"
	"memeval/internal/result"
	"memeval/internal/scoring/judge/inputlog"
	score "memeval/internal/scoring/memora"
	"memeval/internal/suite"
	"strings"
	"time"
)

func memoraSupported(c suite.Case) bool {
	return c.Kind == "rubric" && c.Gold.Rubric != nil && c.Gold.Rubric.Aggregate == "fama" && c.Gold.Checker != nil && c.Gold.Checker.Type == "memora_fama"
}
func memoraAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	if !memoraSupported(c) {
		return fmt.Errorf("Memora requires fama rubric/checker")
	}
	f := o.FixedReader
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.Category, Deviations: []string{}, JudgeDetails: []any{}}
	r.QA = qa
	date := ""
	if c.QueryTime != "" {
		d, e := time.Parse(time.RFC3339, c.QueryTime)
		if e != nil {
			return e
		}
		date = d.Format("2006-01-02")
	}
	in, e := reader.Build(items, known, c.Query, date, f.Model, o.fullContext, r.Budget.MaxContextChars)
	if e != nil {
		return e
	}
	qa.Truncated = in.Truncated
	qa.DroppedSessions = in.DroppedSessions
	answer, e := reader.Answer(ctx, f.Client, in, f.Provider, f.Model, sample, f.MaxTokens)
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if e != nil {
		return e
	}
	qa.Hypothesis = &answer.Text
	p, np, af, nf := 0, 0, 0, 0
	all := true
	for i, item := range c.Gold.Rubric.Items {
		if item.Polarity != "must_contain" && item.Polarity != "must_not_contain" {
			return fmt.Errorf("Memora invalid rubric polarity")
		}
		subctx := inputlog.WithFields(ctx, map[string]any{"expected_answer": map[bool]string{true: "yes", false: "no"}[item.Polarity == "must_contain"], "group": item.Group})
		v, e := score.Evaluate(subctx, f.JudgeRouter(), fmt.Sprintf("%s:eval:%d", sample, i), item.Text, answer.Text)
		correct := v.Outcome.Label == (item.Polarity == "must_contain") && e == nil
		qa.JudgeDetails = append(qa.JudgeDetails, map[string]any{"index": i, "group": item.Group, "is_correct": correct, "outcome": v.Outcome})
		if e != nil {
			return e
		}
		all = all && correct
		switch item.Group {
		case "memory_presence":
			np++
			if correct {
				p++
			}
		case "forgetting_absence":
			nf++
			if correct {
				af++
			}
		default:
			return fmt.Errorf("Memora invalid rubric group")
		}
	}
	v := score.FAMA(p, np, af, nf)
	r.Metrics["memora.mpa"] = &v.MPA
	r.Metrics["memora.faa"] = &v.FAA
	r.Metrics["memora.fama"] = &v.FAMA
	qc := 0.
	if all {
		qc = 1
	}
	r.Metrics["memora.question_correct"] = &qc
	r.Status = result.Scored
	r.Passed = &all
	qa.JudgeLabel = &all
	return nil
}
func memoraMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	r.FrameworkVersion = "0.13.0-p3c"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = "memora-fama-v1+retrieval-diagnostics-v2"
	var outputTokens any = 500
	if o.fullContext {
		outputTokens = nil
		if f.Provider == "anthropic" {
			outputTokens = f.MaxTokens
		}
	}
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": reader.PromptID, "template_sha256": score.SHA256(), "input_mode": f.Input, "fullcontext": o.fullContext, "max_tokens": outputTokens, "temperature": 0.7, "upstream_search_limit": 50, "context_budget_chars": o.Budget.MaxContextChars}
	deviations := []string{"Official three-model majority (gpt-4.1/claude-haiku-4.5/gemini-2.5-flash) is not reproduced; TypeSafe replacement fidelity partial", "Track 1 user_agent is normalized to user, retaining user turns omitted by upstream full-session speaker check", "Track 2 retrieved item text and relevance scores are used directly; null score rendered 0.00; search limit=50 is upstream declaration, actual top_k is reported", "Reader temperature=0.7; Track 2 max_tokens=500, Track 1 unset (Anthropic requires an explicit limit); model context limits are pinned upstream declarations; framework character budget additionally applies", "FAMA denominator defaults: MPA=0 without presence; FAA=1 without absence; reported on [0,1] scale; unresolved judge outputs are errors"}
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
				deviations = appendUnique(deviations, "reader source history truncated")
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
			if !strings.HasPrefix(k, "memora.") {
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
	r.Scores.Official = result.Official{Benchmark: "memora/" + variant, Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: reader.PromptID, InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: evaluated == total && len(r.Cases) == total && errors == 0 && unsupported == 0 && o.Repeats == 1}, Score: metrics["memora.fama"], Metrics: metrics, ByTypeMetrics: by, Fidelity: "partial", Deviations: deviations, Errors: errors, Unsupported: unsupported, Judged: judged}
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
