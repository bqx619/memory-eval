package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/benchmark"
	"memeval/internal/identity"
	importer "memeval/internal/importers/halumem"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	reader "memeval/internal/reader/halumem"
	"memeval/internal/result"
	score "memeval/internal/scoring/halumem"
	"memeval/internal/scoring/judge"
	"memeval/internal/suite"
	"strconv"
	"strings"
)

func halumemSupported(c suite.Case) bool {
	return c.Gold.Checker != nil && (c.Kind == "memory_audit" && c.Gold.Checker.Type == "halumem_memory_audit" || c.Kind == "qa" && c.Gold.Checker.Type == "halumem_qa_judge")
}

type auditResult struct {
	Records   score.Records         `json:"records"`
	DumpCount int                   `json:"dump_count"`
	NewCount  int                   `json:"new_count"`
	Skipped   bool                  `json:"skipped"`
	Details   []judge.ChoiceOutcome `json:"judge_details"`
}

func pointRecord(p suite.MemoryPoint, c suite.Case) score.Integrity {
	importance, _ := strconv.ParseFloat(p.Importance, 64)
	source := "system"
	if p.Polarity == "should_not_exist" {
		source = "interference"
	} else if annotations, ok := c.Attributes["memory_annotations"].(map[string]any); ok {
		if values, ok := annotations[p.ID].(map[string]any); ok {
			if v, ok := values["memory_source"].(string); ok {
				source = v
			}
		}
	}
	return score.Integrity{Source: source, Type: p.Type, Importance: importance}
}
func saveAudit(r *result.Case, v auditResult) { r.Audit, _ = json.Marshal(v) }
func recordUnsupportedAudit(r *result.Case, c suite.Case) {
	v := auditResult{Records: score.NewRecords(), Details: []judge.ChoiceOutcome{}}
	if skip, _ := c.Attributes["is_generated_qa_session"].(bool); skip {
		v.Skipped = true
	} else {
		for _, p := range c.Gold.MemoryPoints {
			v.Records.Integrity = append(v.Records.Integrity, pointRecord(p, c))
		}
	}
	saveAudit(r, v)
}
func halumemAudit(ctx context.Context, a adapter.Adapter, r *result.Case, ep suite.Episode, c suite.Case, known map[string]suite.Event, o Options) error {
	d, ok := a.(adapter.Dumper)
	if !ok {
		recordUnsupportedAudit(r, c)
		return adapter.ErrUnsupported
	}
	if o.shared == nil {
		return fmt.Errorf("audit requires checkpoint state")
	}
	dump, e := d.Dump(ctx, o.shared.id.Namespace())
	if e != nil {
		recordUnsupportedAudit(r, c)
		return e
	}
	if e = adapter.ValidateDump(dump); e != nil {
		recordUnsupportedAudit(r, c)
		return e
	}
	v := auditResult{Records: score.NewRecords(), DumpCount: len(dump.Items), Details: []judge.ChoiceOutcome{}}
	defer func() { saveAudit(r, v) }()
	added := []protocol.DumpItem{}
	next := map[string]bool{}
	for _, i := range dump.Items {
		if !o.shared.previousDump[i.ID] {
			added = append(added, i)
		}
		next[i.ID] = true
	}
	o.shared.previousDump = next
	v.NewCount = len(added)
	if skip, _ := c.Attributes["is_generated_qa_session"].(bool); skip {
		v.Skipped = true
		note := "Generated QA session checkpoint: ingested/ready/dump snapshot advanced; official scoring skipped"
		r.Note = &note
		return nil
	}
	if o.Mode != FixedReader {
		note := "Memory dump checkpoint completed; official audit requires fixed_reader evaluator"
		r.Note = &note
		return nil
	}
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	f := o.FixedReader
	router := f.JudgeRouter()
	errs := []error{}
	memories := []string{}
	for _, i := range added {
		memories = append(memories, i.Text)
	}
	all := strings.Join(memories, "\n")
	evaluate := func(suffix, kind string, values map[string]string) score.Parsed {
		v0, e := score.Evaluate(ctx, router, sample+suffix, kind, values)
		v.Details = append(v.Details, v0.Outcome)
		if e != nil {
			errs = append(errs, e)
		}
		return v0.Parsed
	}
	for i, p := range c.Gold.MemoryPoints {
		rec := pointRecord(p, c)
		retrieved := []protocol.Item{}
		if p.IsUpdate && len(p.UpdateOf) > 0 {
			req := suite.PublicRequest(c, o.shared.id, r.Budget)
			req.Query = p.Content
			req.Budget.TopK = 10
			req.QueryInvocationID = o.shared.id.Anon("invocation", fmt.Sprintf("%s:update:%d", r.SampleID, i))
			if o.queryTimePolicy == "reference_only" {
				req.AsOf = ""
			}
			response, e := a.Retrieve(ctx, o.shared.id.Namespace(), req)
			if e == nil {
				retrieved, _, e = ValidateResponse(response, known, req.Budget, false)
			}
			if e != nil {
				errs = append(errs, e)
				v.Records.Update = append(v.Records.Update, score.Update{Type: p.Type, Importance: rec.Importance})
				continue
			}
		}
		if p.IsUpdate && len(retrieved) > 0 {
			parsed := evaluate(fmt.Sprintf(":point:%d", i), "update", map[string]string{"memories": strings.Join(reader.Lines(retrieved, known), "\n"), "updated_memory": p.Content, "original_memory": strings.Join(p.UpdateOf, "\n")})
			v.Records.Update = append(v.Records.Update, score.Update{Type: p.Type, Importance: rec.Importance, Label: parsed.Label})
		} else {
			if strings.TrimSpace(all) == "" {
				zero := 0
				rec.Score = &zero
			} else {
				rec.Score = evaluate(fmt.Sprintf(":point:%d", i), "integrity", map[string]string{"memories": all, "expected_memory_point": p.Content}).Score
			}
			v.Records.Integrity = append(v.Records.Integrity, rec)
		}
	}
	dialogue, gold := []string{}, []string{}
	for _, ev := range ep.Events[:c.Cutoff] {
		if ev.Session == c.SessionCheckpoint {
			dialogue = append(dialogue, "["+ev.TimeRaw+"]"+ev.Role+": "+ev.Content)
			if ev.Role == "assistant" {
				dialogue = append(dialogue, "")
			}
		}
	}
	for _, p := range c.Gold.MemoryPoints {
		if p.Polarity != "should_not_exist" {
			gold = append(gold, p.Content)
		}
	}
	for i, m := range added {
		parsed := evaluate(fmt.Sprintf(":new:%d", i), "accuracy", map[string]string{"dialogue": strings.Join(dialogue, "\n"), "golden_memories": strings.Join(gold, "\n"), "candidate_memory": m.Text})
		v.Records.Accuracy = append(v.Records.Accuracy, score.Accuracy{Score: parsed.Score, Included: parsed.Included})
	}
	r.Metrics = score.Aggregate(v.Records)
	r.Status = result.Scored
	return errors.Join(errs...)
}
func halumemAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	if !halumemSupported(c) || c.Kind != "qa" {
		return fmt.Errorf("HaluMem QA checker required")
	}
	v := auditResult{Records: score.NewRecords(), Details: []judge.ChoiceOutcome{}}
	v.Records.QA = append(v.Records.QA, score.QA{Type: c.Category})
	defer func() { saveAudit(r, v) }()
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	f := o.FixedReader
	qa := &result.QA{QuestionType: c.Category, Deviations: []string{}, JudgeDetails: []any{}}
	r.QA = qa
	user := o.shared.id.Anon("owner", c.User)
	answer, e := reader.Answer(ctx, f.Client, items, known, user, c.Query, f.Provider, f.Model, sample, f.MaxTokens)
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if e != nil {
		return e
	}
	qa.Hypothesis = &answer.Text
	evidence := []string{}
	for _, ref := range c.Gold.EvidenceRefs {
		if ref.Kind == "text" {
			evidence = append(evidence, ref.Ref)
		}
	}
	parsed, e := score.Evaluate(ctx, f.JudgeRouter(), sample+":question", "question", map[string]string{"question": c.Query, "reference_answer": strings.Join(c.Gold.Answers, "\n"), "key_memory_points": strings.Join(evidence, "\n"), "response": answer.Text})
	v.Details = append(v.Details, parsed.Outcome)
	v.Records.QA[0].Label = parsed.Parsed.Label
	qa.JudgeDetails = append(qa.JudgeDetails, parsed.Outcome)
	if e != nil {
		return e
	}
	correct := *parsed.Parsed.Label == "Correct"
	qa.JudgeLabel = &correct
	r.Passed = &correct
	r.Status = result.Scored
	for k, v := range score.Aggregate(v.Records) {
		if strings.Contains(k, "question_answering.") {
			r.Metrics[k] = v
		}
	}
	return nil
}
func halumemMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	r.FrameworkVersion = "0.14.0-p3d"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = "halumem-v1+checkpoint-dump-v2"
	r.Limitations = appendUnique(r.Limitations, importer.CostNotice)
	deviations := []string{"Official gpt-4o temperature=0 max_tokens=16384 is declared only; fidelity partial", "Dump ID difference replaces mem0 add() return; same-ID edits are not new units; raw-only baseline/octop lexical dumps do not extract memories", "Zero official denominators are null; None/invalid judge outputs excluded only from valid denominators; generated QA checkpoints skipped", "Private persona_info never used: reader user name is a stable public pseudonym", "Per-session ingest passes turn timestamps, no separate add(start_time) parameter; UTC assumption; query_time reference_only; PROMPT_MEMZERO unchanged", "Unsupported dump: gold points retained as None integrity records; extraction/update denominators cannot be observed; QA continues"}
	info := decisionJudgeInfo(f)

	records := score.NewRecords()
	by := map[string]score.Records{}
	evaluated, judged, errs, unsupported, skipped := 0, 0, 0, 0, 0
	for _, c := range r.Cases {
		switch c.Status {
		case result.Error, result.ContractError, result.IngestFailed:
			errs++
		case result.Unsupported:
			unsupported++
		}
		if c.Status == result.Invalid {
			if c.QA != nil {
				for _, d := range c.QA.Deviations {
					deviations = appendUnique(deviations, d)
				}
			}
			continue // F18: no failed-answer placeholder in any QA denominator.
		}
		var v auditResult
		if len(c.Audit) > 0 {
			if e := json.Unmarshal(c.Audit, &v); e != nil {
				return e
			}
			if v.Skipped {
				skipped++
				continue
			}
			records.Append(v.Records)
			for _, q := range v.Records.QA {
				x := by[q.Type]
				x.QA = append(x.QA, q)
				by[q.Type] = x
			}

		} else if c.Kind == "qa" {
			rec := score.QA{Type: c.Category}
			records.QA = append(records.QA, rec)
			x := by[c.Category]
			x.QA = append(x.QA, rec)
			by[c.Category] = x
		}
		if c.Status == result.Scored {
			judged++
		}
		if c.QA != nil {
			if c.QA.Hypothesis != nil && c.Status != result.Invalid {
				evaluated++
			}
			for _, d := range c.QA.Deviations {
				deviations = appendUnique(deviations, d)
			}
		}
	}
	metrics := score.Aggregate(records)
	byType := map[string]map[string]*float64{}
	for typ, rs := range by {
		byType[typ] = map[string]*float64{}
		for k, v := range score.Aggregate(rs) {
			if strings.Contains(k, "question_answering.") {
				byType[typ][k] = v
			}
		}
	}
	total := 0
	if e := s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if c.Kind == "qa" {
				total++
			}
		}
		return nil
	}); e != nil {
		return e
	}
	// A subset remains incomplete against the bound benchmark's declared question set.
	p, variant, _, e := benchmark.Resolve(r.Protocol.Benchmark.Ref)
	if e != nil {
		return e
	}
	if n, ok := p.Variants[variant].Params["questions"].(float64); ok {
		total = int(n)
	}
	r.Reader = map[string]any{"provider": f.Provider, "model": f.Model, "prompt_id": reader.PromptID, "template_sha256": score.SHA256(), "top_k": 20, "update_top_k": 10, "temperature": 0, "max_tokens": f.MaxTokens, "input_mode": f.Input}
	r.Protocol.Judge = info
	r.Scores.Official = result.Official{Benchmark: "halumem/" + r.Protocol.Benchmark.Variant, Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: reader.PromptID, InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: evaluated == total && errs == 0 && unsupported == 0 && o.Repeats == 1 && judged+skipped == len(r.Cases)}, Score: metrics["halumem.memory_extraction_f1"], Metrics: metrics, ByTypeMetrics: byType, Fidelity: "partial", Deviations: deviations, Errors: errs, Unsupported: unsupported, Judged: judged}
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

func halumemCostNotice() string { return importer.CostNotice }
