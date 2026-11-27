package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	reader "memeval/internal/reader/convomem"
	"memeval/internal/result"
	score "memeval/internal/scoring/convomem"
	"memeval/internal/scoring/judge/inputlog"
	"memeval/internal/suite"
	"strings"
)

func convoMemSupported(c suite.Case) bool {
	return (c.Kind == "qa" || c.Kind == "rubric") && c.Gold.Checker != nil && c.Gold.Checker.Type == "convomem_judge"
}
func convoMemAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	if !convoMemSupported(c) {
		return fmt.Errorf("ConvoMem requires convomem_judge checker")
	}
	f := o.FixedReader
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.QuestionType, Deviations: []string{}, JudgeDetails: []any{}}
	r.QA = qa
	in, e := reader.Build(items, known, c, o.fullContext, r.Budget.MaxContextChars)
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
	var params struct {
		Template string `json:"template"`
	}
	if e = json.Unmarshal(c.Gold.Checker.Params, &params); e != nil {
		return e
	}
	correct := strings.Join(c.Gold.Answers, "\n")
	if c.Gold.Rubric != nil {
		parts := []string{}
		for _, item := range c.Gold.Rubric.Items {
			parts = append(parts, item.Text)
		}
		correct = strings.Join(parts, "\n")
	}
	sourceEvents := map[string]suite.Event{}
	for _, event := range known {
		sourceEvents[event.SourceID] = event
	}
	evidence := []map[string]string{}
	for _, ref := range c.Gold.EvidenceRefs {
		item := map[string]string{"kind": ref.Kind, "ref": ref.Ref}
		if event, ok := sourceEvents[ref.Ref]; ok {
			item["speaker"] = event.Role
			item["text"] = event.Content
		} else if ref.Kind == "text" {
			item["text"] = ref.Ref
		}
		evidence = append(evidence, item)
	}
	ctx = inputlog.WithFields(ctx, map[string]any{"evidence": evidence})
	j, e := score.Evaluate(ctx, f.JudgeRouter(), sample, params.Template, c.Query, correct, answer.Text)
	qa.JudgeDetails = append(qa.JudgeDetails, j)
	if e != nil {
		qa.Deviations = append(qa.Deviations, "ConvoMem invalid judgment: excluded from official denominators")
		return e
	}
	qa.JudgeLabel = j.Label
	r.Passed = j.Label
	r.Status = result.Scored
	v := 0.
	if *j.Label {
		v = 1
	}
	r.Metrics["convomem.correct"] = &v
	if n, ok := c.Attributes["n_evidence"]; ok {
		r.Metrics[fmt.Sprintf("convomem.correct.n_evidence.%v", n)] = &v
	}
	return nil
}
func convoMemMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	r.FrameworkVersion = "0.16.0-p4b"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = "convomem-v1+retrieval-diagnostics-v2"
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": reader.PromptID, "template_set_sha256": score.SHA256(), "input_mode": f.Input, "fullcontext": o.fullContext, "max_tokens": f.MaxTokens, "context_budget_chars": o.Budget.MaxContextChars}
	deviations := []string{"Official Gemini 2.5 Flash declared only; replacement judge fidelity partial", "Only evidence conversations, no filler: official 1..300 conversation context ladder is unavailable", "implicit_connection uses rubric per official generator; three judge templates ignore evidenceMessages", "Official text-parser retries are replaced by typed decisions; transport uses framework retry/backoff; invalid results excluded from official denominators", "Framework reader/judge output token caps are additional; upstream call site specifies neither temperature nor token cap", "Reader omits optional upstream debug conversation IDs; retained Conversation k is the original number; empty histories return no answer without calling reader", "Unmatched evidence text excluded from retrieval gold denominators; exact matches only", "Metrics ending .n_evidence.N report the same correctness grouped by original conversation count"}
	var info *result.JudgeInfo
	{
		info = decisionJudgeInfo(f)

	}
	sums, ns := map[string]float64{}, map[string]int{}
	byS, byN := map[string]map[string]float64{}, map[string]map[string]int{}
	keys := []string{"convomem.correct"}
	primary := keys[0]
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
			if !strings.HasPrefix(k, "convomem.") {
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
	r.Scores.Official = result.Official{Benchmark: "convomem/" + r.Protocol.Benchmark.Variant, Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: reader.PromptID, InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: evaluated == total && len(r.Cases) == total && errors == 0 && unsupported == 0 && o.Repeats == 1}, Score: metrics[primary], Metrics: metrics, ByTypeMetrics: by, Fidelity: "partial", Deviations: deviations, Errors: errors, Unsupported: unsupported, Judged: judged}
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
