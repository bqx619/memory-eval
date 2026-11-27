package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/benchmark"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	lr "memeval/internal/reader/locomo"
	pr "memeval/internal/reader/personamem"
	"memeval/internal/result"
	ls "memeval/internal/scoring/locomo"
	ps "memeval/internal/scoring/personamem"
	"memeval/internal/suite"
	"strconv"
	"strings"
)

// ValidateReaderBinding is also used by the CLI before constructing a judge.
func ValidateReaderBinding(s *suite.Suite, dir string) (string, error) {
	b, err := bindBenchmark(s, dir)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", fmt.Errorf("fixed_reader requires a bound benchmark reader")
	}
	p, _, _, err := benchmark.Resolve(b.Ref)
	if err != nil {
		return "", err
	}
	if p.Scoring.Reader == nil {
		return "", fmt.Errorf("benchmark %s has no reader", p.ProtocolID)
	}
	switch p.ProtocolID {
	case "locomo":
		if b.Variant != "text_only" {
			return "", fmt.Errorf("LoCoMo variant %s has no implemented reader", b.Variant)
		}
	case "longmemeval", "personamem", "memoryagentbench", "beam", "memora", "halumem", "prefeval", "convomem":
	default:
		return "", fmt.Errorf("benchmark reader %s unsupported", p.ProtocolID)
	}
	err = s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if convoMemSupported(c) && p.ProtocolID != "convomem" {
				return fmt.Errorf("convomem checker requires convomem reader binding")
			}
			if prefEvalSupported(c) && p.ProtocolID != "prefeval" {
				return fmt.Errorf("prefeval checker requires prefeval reader binding")
			}
			if halumemSupported(c) && p.ProtocolID != "halumem" {
				return fmt.Errorf("halumem checker requires halumem reader binding")
			}
			if memoraSupported(c) && p.ProtocolID != "memora" {
				return fmt.Errorf("memora checker requires memora reader binding")
			}
			if beamSupported(c) && p.ProtocolID != "beam" {
				return fmt.Errorf("beam checker requires beam reader binding")
			}
			if c.Gold.Checker != nil && strings.HasPrefix(c.Gold.Checker.Type, "mab_") && p.ProtocolID != "memoryagentbench" {
				return fmt.Errorf("mab checker requires memoryagentbench reader binding")
			}
			if suite.NewKind(c.Kind) {
				continue
			}
			if c.Gold.Checker != nil && c.Gold.Checker.Type == "locomo_official" && p.ProtocolID != "locomo" {
				return fmt.Errorf("locomo_official checker requires locomo reader binding")
			}
			if c.Kind == "mcq" && c.Gold.MCQ != nil && c.Gold.MCQ.Parse == "personamem_final_answer" && p.ProtocolID != "personamem" {
				return fmt.Errorf("personamem_final_answer requires personamem reader binding")
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return p.ProtocolID, nil
}
func deterministicAnswer(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) error {
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.QuestionType, Deviations: []string{}}
	r.QA = qa
	f := o.FixedReader
	var answer llm.Response
	var err error
	category := 0
	switch o.benchmarkID {
	case "locomo":
		if c.Gold.Checker == nil || c.Gold.Checker.Type != "locomo_official" {
			return fmt.Errorf("LoCoMo requires locomo_official checker")
		}
		var params struct {
			Category int `json:"category"`
		}
		if err = json.Unmarshal(c.Gold.Checker.Params, &params); err != nil || params.Category < 1 || params.Category > 5 {
			return fmt.Errorf("invalid LoCoMo checker category")
		}
		category = params.Category
		questionOptions := lr.QuestionOptions{RunSeed: o.RunSeed, SampleID: r.SampleID}
		if text, ok := c.Attributes["adversarial_answer"].(string); ok {
			questionOptions.AdversarialAnswer = &text
		}
		in, e := lr.Build(items, known, c.Query, category, o.episodeSpeakers[r.EpisodeID], r.Budget.MaxContextChars, questionOptions)
		if e != nil {
			return e
		}
		qa.Truncated = in.Truncated
		qa.DroppedSessions = in.DroppedSessions
		qa.LoCoMo = &in.QuestionAudit
		qa.Deviations = append(qa.Deviations, in.Deviations...)
		answer, err = lr.Answer(ctx, f.Client, in, f.Provider, f.Model, sample, f.MaxTokens)
		if err == nil {
			raw := answer.Text
			qa.LoCoMo.RawResponse = &raw
			answer.Text, qa.LoCoMo.MappedOption = lr.ProcessAnswer(raw, in.QuestionAudit.Options)
		}
	case "personamem":
		if c.Kind != "mcq" || c.Gold.MCQ == nil || c.Gold.MCQ.Parse != "personamem_final_answer" {
			return fmt.Errorf("PersonaMem requires personamem_final_answer MCQ")
		}
		raw, _ := c.Attributes["options_raw"].(string)
		in, e := pr.Build(items, known, c.Query, raw, r.Budget.MaxContextChars)
		if e != nil {
			return e
		}
		qa.Truncated = in.Truncated
		qa.DroppedMessages = in.DroppedMessages
		answer, err = pr.Answer(ctx, f.Client, in, f.Provider, f.Model, sample, f.MaxTokens)
	}
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if err != nil {
		return err
	}
	if answer.ModelReturned != "" && answer.ModelReturned != f.Model {
		qa.Deviations = append(qa.Deviations, "reader returned a different model identifier: "+answer.ModelReturned)
	}
	qa.Hypothesis = &answer.Text
	r.Status = result.Scored
	if o.benchmarkID == "locomo" {
		gold := ""
		if len(c.Gold.Answers) > 0 {
			gold = c.Gold.Answers[0]
		}
		v := ls.Score(answer.Text, gold, category)
		r.Metrics["locomo_official.overall"] = &v
		r.PredictionInput = &result.PredictionInput{Question: c.Query, Answer: gold, Category: category}
		r.Metrics["locomo_official.f1.cat"+strconv.Itoa(category)] = &v
		if category == 5 {
			r.Metrics["locomo_official.abstain_acc"] = &v
		} else if len(c.Gold.Answers) > 0 {
			r.Metrics["locomo_official.f1"] = &v
		}
	} else {
		passed, raw, found := ps.Extract(answer.Text, c.Gold.Choice)
		r.Passed = &passed
		r.AnswerParse = &result.AnswerParse{RawFinal: raw, OptionsFound: found}
		v := 0.
		if passed {
			v = 1
		}
		r.Metrics["mcq.correct"] = &v
	}
	return nil
}
func deterministicMetadata(r *result.Report, s *suite.Suite, o Options) error {
	f := o.FixedReader
	id := o.benchmarkID
	prompt, template, total, metric := lr.PromptID, lr.TemplateSHA256(), 1986, "locomo_official.overall"
	if id == "personamem" {
		prompt, template, total, metric = pr.PromptID, pr.TemplateSHA256(), 589, "mcq.correct"
	}
	r.FrameworkVersion = "0.9.0-p2"
	r.Protocol.ReaderInput = f.Input
	r.Protocol.Scoring = id + "-official-deterministic-v1+retrieval-diagnostics-v2"
	r.Protocol.Judge = nil
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": prompt, "template_sha256": template, "input_mode": f.Input, "max_tokens": f.MaxTokens, "context_budget_chars": o.Budget.MaxContextChars}
	deviations := []string{}
	if id == "locomo" {
		r.Reader.(map[string]any)["question_protocol"] = lr.QuestionProtocol
		r.Reader.(map[string]any)["cat5_order_rng"] = lr.OptionRNG
		deviations = append(deviations, "Text-only history; speaker_a/speaker_b order is deterministic instead of upstream unordered set", "Official cat2 temporal suffix and cat5 choices are evaluator-only; SUT receives the original question. Cat5 uses run_seed/sample_id-derived ChaCha8 instead of Python global random; actual order recorded in case.qa.locomo", "Reader uses chronological sessions and drops earliest whole sessions by character budget; upstream uses token/turn assembly")
	} else {
		deviations = append(deviations, "Reader budget drops earliest whole messages by character count; original roles, content prefixes and CSV options string preserved")
	}
	sums := map[string]float64{}
	ns := map[string]int{}
	by := map[string]result.Accuracy{}
	byMetrics := map[string]map[string]*float64{}
	typeSums := map[string]float64{}
	typeN := map[string]int{}
	evaluated, errors, unsupported := 0, 0, 0
	unique := map[string]bool{}
	truncated := false
	missingSuffix := false
	for _, row := range r.Cases {
		if row.Status == result.Unsupported {
			unsupported++
		}
		if row.Status == result.Error || row.Status == result.IngestFailed || row.Status == result.ContractError {
			errors++
		}
		if row.QA != nil {
			if id == "locomo" && (row.QA.QuestionType == "2" || row.QA.QuestionType == "5") && (row.QA.LoCoMo == nil || !row.QA.LoCoMo.SuffixApplied) {
				missingSuffix = true
			}
			truncated = truncated || row.QA.Truncated
			for _, d := range row.QA.Deviations {
				deviations = appendUnique(deviations, d)
			}
		}
		if row.Status != result.Scored || row.QA == nil || row.QA.Hypothesis == nil {
			continue
		}
		evaluated++
		unique[row.CaseID] = true
		for key, v := range row.Metrics {
			if v != nil && (strings.HasPrefix(key, "locomo_official.") || key == "mcq.correct") {
				sums[key] += *v
				ns[key]++
			}
		}
		qt := row.QA.QuestionType
		if v := row.Metrics[metric]; v != nil {
			typeSums[qt] += *v
			typeN[qt]++
		}
		if id == "personamem" {
			a := by[qt]
			a.N++
			if row.Passed != nil && *row.Passed {
				a.Correct++
			}
			v := float64(a.Correct) / float64(a.N)
			a.Accuracy = &v
			by[qt] = a
		}
	}
	metrics := map[string]*float64{}
	for key, n := range ns {
		v := sums[key] / float64(n)
		metrics[key] = &v
	}
	for qt, n := range typeN {
		v := typeSums[qt] / float64(n)
		byMetrics[qt] = map[string]*float64{metric: &v}
	}
	complete := len(unique) == total && len(r.Cases) == total && errors == 0 && unsupported == 0 && o.Repeats == 1
	fidelity := "partial"
	if complete && !truncated && !missingSuffix && (id != "personamem" || r.Protocol.Benchmark.Variant == "official") {
		fidelity = "full"
	}
	if !complete {
		deviations = append(deviations, "question set incomplete or repeated")
	}
	if truncated {
		deviations = append(deviations, "reader history truncated")
	}
	if missingSuffix {
		deviations = appendUnique(deviations, "official LoCoMo question suffix missing; fidelity partial")
	}
	if id == "personamem" && r.Protocol.Benchmark.Variant != "official" {
		deviations = append(deviations, "system persona excluded; memory-only variant is not comparable to official setting")
	}
	official := result.Official{Benchmark: id, Judge: nil, Reader: result.ReaderInfo{Model: f.Model, PromptID: prompt, InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: total, Evaluated: evaluated, Complete: complete}, Score: metrics[metric], Metrics: metrics, ByType: by, ByTypeMetrics: byMetrics, Deviations: deviations, Fidelity: fidelity, Errors: errors, Unsupported: unsupported}
	r.Scores.Official = official
	if engine, ok := f.Client.(*llm.Engine); ok {
		metadata, e := engine.ReportMetadata()
		if e != nil {
			return e
		}
		r.EvaluatorLLM = metadata
		r.Billing.Evaluator = engine.Billing()
		r.Summary.Billing.Evaluator = r.Billing.Evaluator
		r.Billing.EvaluatorByPurpose = map[string]any{"reader": engine.BillingPurpose("reader")}
		r.Summary.Billing.EvaluatorByPurpose = r.Billing.EvaluatorByPurpose
	}
	return nil
}
