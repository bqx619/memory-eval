package runner

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/protocol"
	readerbase "memeval/internal/reader"
	reader "memeval/internal/reader/longmemeval"
	"memeval/internal/result"
	routing "memeval/internal/scoring/judge"
	"memeval/internal/suite"
	"slices"
	"strings"
)

type FixedReaderOptions struct {
	PrefEvalRemind         bool
	JudgeDecisionModel     string
	DecisionClient         decision.DecisionClient
	Client                 llm.Client
	Provider, Model, Input string
	MaxTokens              int
	MinTokensReasoning     *int
}

func (o *FixedReaderOptions) validate(needsJudge bool) error {
	if o == nil || o.Client == nil || o.Model == "" || o.Provider == "" || o.MaxTokens < 1 || (o.Input != "sources" && o.Input != "items") {
		return fmt.Errorf("fixed_reader requires client, reader model/provider, valid input and token budget")
	}
	if o.MinTokensReasoning != nil && *o.MinTokensReasoning < 0 {
		return fmt.Errorf("reader-min-tokens-reasoning must be nonnegative")
	}
	if needsJudge {
		return o.JudgeRouter().Validate()
	}
	return nil
}
func readerSupportsItems(benchmark string) bool {
	return benchmark == "longmemeval" || benchmark == "memoryagentbench" || benchmark == "beam"
}

func (o *FixedReaderOptions) JudgeRouter() *routing.Router {
	return &routing.Router{Decision: o.DecisionClient, DecisionModel: o.JudgeDecisionModel}
}
func answerCase(ctx context.Context, r *result.Case, c suite.Case, items []protocol.Item, known map[string]suite.Event, o Options) (err error) {
	// Per-case wrapper sees the raw provider response before any benchmark's
	// trim, prefill or letter mapping, and leaves judge requests untouched.
	f := *o.FixedReader
	f.Client = &readerClient{Client: f.Client, row: r, input: f.Input, official: officialReaderTokens(o.benchmarkID, c.Kind, o.fullContext, f.Provider), min: f.reasoningMinimum()}
	o.FixedReader = &f
	defer func() {
		var empty *readerbase.EmptyResponseError
		if errors.As(err, &empty) {
			r.Status, r.Passed, r.EmptyExpected = result.Invalid, nil, nil
			r.Metrics = map[string]*float64{}
			r.ErrorType = "ReaderEmptyResponse"
			note := "Empty reader response; invalid and excluded from official denominators"
			r.Note = &note
			r.QA.Hypothesis = &empty.Response.Text
			err = nil
		}
	}()
	if o.benchmarkID == "convomem" {
		return convoMemAnswer(ctx, r, c, items, known, o)
	}
	if o.benchmarkID == "prefeval" {
		return prefEvalAnswer(ctx, r, c, items, known, o)
	}
	if o.benchmarkID == "halumem" {
		return halumemAnswer(ctx, r, c, items, known, o)
	}
	if o.benchmarkID == "memora" {
		return memoraAnswer(ctx, r, c, items, known, o)
	}
	if o.benchmarkID == "beam" {
		return beamAnswer(ctx, r, c, items, known, o)
	}
	if o.benchmarkID == "memoryagentbench" {
		return mabAnswer(ctx, r, c, items, known, o)
	}
	if o.benchmarkID != "longmemeval" {
		return deterministicAnswer(ctx, r, c, items, known, o)
	}
	// Separate stable pseudonym namespace: independent of random SUT run_seed.
	// The suite digest scopes replay identity; raw question IDs never reach HTTP.
	id, _ := identity.New(o.suiteSHA, r.SampleID)
	sample := id.Anon("evaluator-sample", r.SampleID)
	qa := &result.QA{QuestionType: c.QuestionType, Deviations: []string{}}
	r.QA = qa
	rawDate, _ := c.Attributes["question_date_raw"].(string)
	in, err := reader.Build(items, known, c.Query, c.QueryTime, rawDate, o.FixedReader.Input, r.Budget.MaxContextChars)
	if err != nil {
		return err
	}
	qa.Truncated = in.Truncated
	qa.DroppedSessions = in.DroppedSessions
	answer, err := reader.Answer(ctx, o.FixedReader.Client, in, o.FixedReader.Provider, o.FixedReader.Model, sample, o.FixedReader.MaxTokens)
	qa.ReaderRequestSHA = answer.RequestSHA
	qa.Deviations = append(qa.Deviations, answer.Deviations...)
	if err != nil {
		return err
	}
	if answer.ModelReturned != "" && answer.ModelReturned != o.FixedReader.Model {
		qa.Deviations = append(qa.Deviations, "reader returned a different model identifier: "+answer.ModelReturned)
	}
	qa.Hypothesis = &answer.Text
	qa.ReaderAbstain = reader.ExplicitAbstain(answer.Text)
	reference := ""
	if len(c.Gold.Answers) > 0 {
		reference = c.Gold.Answers[0]
	}
	judgment, err := o.FixedReader.JudgeRouter().Evaluate(ctx, routing.Input{SampleID: sample, QuestionType: c.QuestionType, Question: c.Query, Reference: reference, Response: answer.Text, Abstention: strings.Contains(c.ID, "_abs")})
	label := judgment.Label
	qa.Judge = judgment
	qa.JudgeRequestSHA = judgment.RequestSHA
	if m := judgment.ModelReturned; m != "" && m != o.FixedReader.JudgeDecisionModel {
		qa.Deviations = append(qa.Deviations, "decision judge returned a different model identifier: "+m)
	}
	if err != nil {
		return err
	}
	qa.JudgeLabel = &label
	r.Passed = &label
	r.Status = result.Scored
	accuracy := 0.
	if label {
		accuracy = 1
	}
	r.Metrics["qa_accuracy"] = &accuracy
	return nil
}
func fixedMetadata(r *result.Report, s *suite.Suite, o Options) error {
	if o.benchmarkID == "convomem" {
		return convoMemMetadata(r, s, o)
	}
	if o.benchmarkID == "prefeval" {
		return prefEvalMetadata(r, s, o)
	}
	if o.benchmarkID == "halumem" {
		return halumemMetadata(r, s, o)
	}
	if o.benchmarkID == "memora" {
		return memoraMetadata(r, s, o)
	}
	if o.benchmarkID == "beam" {
		return beamMetadata(r, s, o)
	}
	if o.benchmarkID == "memoryagentbench" {
		return mabMetadata(r, s, o)
	}
	if o.benchmarkID != "longmemeval" {
		return deterministicMetadata(r, s, o)
	}
	f := o.FixedReader
	r.FrameworkVersion = "0.17.0-p5"
	r.Protocol.ReaderInput = f.Input
	info := decisionJudgeInfo(f)
	r.Protocol.Judge = info
	r.Protocol.Scoring = "longmemeval-official-judge-v1+retrieval-diagnostics-v2"
	r.Protocol.Changes = append(r.Protocol.Changes, "Reader Current Date uses query_time even when SUT as_of is omitted", "Reader truncation drops earliest complete sessions by Unicode character budget; upstream truncates a token prefix", "Evaluator sample pseudonyms scoped to suite digest for replay independently of SUT run_seed", "Free-text reader abstain is explicit only for literal ABSTAIN or [ABSTAIN]; natural-language abstention judged using the official template")
	r.Reader = map[string]any{"model": f.Model, "provider": f.Provider, "prompt_id": readerTemplateID(reader.PromptID, f.Input), "input_mode": f.Input, "temperature": 0, "max_tokens": f.MaxTokens, "context_budget_chars": o.Budget.MaxContextChars}
	if engine, ok := f.Client.(*llm.Engine); ok {
		metadata, err := engine.ReportMetadata()
		if err != nil {
			return err
		}
		r.EvaluatorLLM = metadata
		r.Billing.Evaluator = engine.Billing()
		r.Summary.Billing.Evaluator = r.Billing.Evaluator
		r.Billing.EvaluatorByPurpose = map[string]any{"judge-decision": engine.BillingPurpose("judge-decision"), "reader": engine.BillingPurpose("reader")}
		r.Summary.Billing.EvaluatorByPurpose = r.Billing.EvaluatorByPurpose
	}
	deviations := []string{}
	if f.Input != "sources" {
		deviations = append(deviations, "reader_input=items differs from original-source history")
	}
	types := map[string]result.Accuracy{}
	for _, kind := range []string{"single-session-user", "single-session-assistant", "single-session-preference", "multi-session", "knowledge-update", "temporal-reasoning"} {
		types[kind] = result.Accuracy{}
	}
	evaluated, correct, judged, errors, unsupported := 0, 0, 0, 0, 0
	unique := map[string]bool{}
	for _, c := range r.Cases {
		if c.Status == result.Unsupported {
			unsupported++
		}
		if c.Status == result.Error || c.Status == result.IngestFailed || c.Status == result.ContractError {
			errors++
		}
		if c.QA == nil {
			continue
		}
		q := c.QA
		if q.Truncated {
			deviations = appendUnique(deviations, "reader history truncated by characters, dropping earliest sessions")
		}
		for _, d := range q.Deviations {
			deviations = appendUnique(deviations, d)
		}
		if q.Hypothesis == nil || c.Status == result.Invalid {
			continue
		}
		evaluated++
		unique[c.CaseID] = true
		a := types[q.QuestionType]
		a.N++
		if q.JudgeLabel != nil {
			judged++
			if *q.JudgeLabel {
				correct++
				a.Correct++
			}
		}
		value := float64(a.Correct) / float64(a.N)
		a.Accuracy = &value
		types[q.QuestionType] = a
	}
	complete := strings.HasPrefix(s.SuiteID, "longmemeval-s-cleaned@") && len(unique) == 500 && len(r.Cases) == 500 && judged == 500 && errors == 0 && unsupported == 0
	if !complete {
		deviations = append(deviations, "question set incomplete or not the 500-question longmemeval-s-cleaned suite")
	}
	if o.Repeats != 1 {
		deviations = append(deviations, "repeated answers are not additional independent benchmark questions")
	}
	if errors > 0 || unsupported > 0 {
		deviations = append(deviations, "execution errors/unsupported cases reported separately; judge errors count as incorrect among reader answers")
	}
	var score *float64
	if evaluated > 0 {
		v := float64(correct) / float64(evaluated)
		score = &v
	}
	fidelity := "partial"
	r.Scores.Official = result.Official{Benchmark: "longmemeval-s-cleaned", Judge: info, Reader: result.ReaderInfo{Model: f.Model, PromptID: readerTemplateID(reader.PromptID, f.Input), InputMode: f.Input}, QuestionSet: result.QuestionSet{Total: 500, Evaluated: evaluated, Complete: complete}, Score: score, ByType: types, Deviations: deviations, Fidelity: fidelity, Errors: errors, Unsupported: unsupported, Judged: judged}
	return nil
}
func appendUnique(s []string, x string) []string {
	if !slices.Contains(s, x) {
		s = append(s, x)
	}
	return s
}
