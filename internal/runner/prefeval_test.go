package runner

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/result"
	"memeval/internal/suite"
	"strings"
	"testing"
)

type prefEvalFailingJudge struct{ calls int }

func (f *prefEvalFailingJudge) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	f.calls++
	if r.Purpose == "reader" {
		return llm.Response{Text: "answer"}, nil
	}
	return llm.Response{}, fmt.Errorf("offline judge failure")
}
func TestPrefEvalInvalidExcluded(t *testing.T) {
	s := &suite.Suite{SchemaVersion: "3.0", Episodes: []suite.Episode{{ID: "ep", Cases: []suite.Case{{ID: "one", Kind: "rubric", Gold: suite.Gold{Reference: "P", Rubric: &suite.Rubric{Aggregate: "prefeval"}}}, {ID: "two", Kind: "rubric", Gold: suite.Gold{Rubric: &suite.Rubric{Aggregate: "prefeval"}}}}}}}
	f := &prefEvalFailingJudge{}
	o := Options{suiteSHA: strings.Repeat("a", 64), Repeats: 1, FixedReader: &FixedReaderOptions{Client: f, Provider: "openai-compat", Model: "reader", Input: "sources", JudgeDecisionModel: "jev-1.14.0", DecisionClient: f, MaxTokens: 300}}
	failed := result.Case{SampleID: "sample", Category: "topic", Metrics: map[string]*float64{}}
	if e := prefEvalAnswer(t.Context(), &failed, s.Episodes[0].Cases[0], nil, nil, o); e == nil || f.calls != 2 || len(failed.Metrics) != 0 {
		t.Fatal(failed, e, f.calls)
	}
	failed.Status = result.Error
	v := 1.
	passed := true
	report := &result.Report{Protocol: result.Protocol{Benchmark: &result.BenchmarkRef{Variant: "explicit-generation"}}, Cases: []result.Case{failed, {Category: "topic", Status: result.Scored, Metrics: map[string]*float64{"prefeval.preference_following_accuracy": &v}, QA: &result.QA{Hypothesis: new("ok"), JudgeLabel: &passed}}}}
	if e := prefEvalMetadata(report, s, o); e != nil {
		t.Fatal(e)
	}
	off := report.Scores.Official.(result.Official)
	if off.Score == nil || *off.Score != 1 || off.Errors != 1 || off.Judged != 1 || off.QuestionSet.Complete || *off.ByTypeMetrics["topic"]["prefeval.preference_following_accuracy"] != 1 {
		t.Fatal(off)
	}
	if !strings.Contains(strings.Join(off.Deviations, " "), "invalid") {
		t.Fatal("missing exclusion disclosure")
	}
}

func (f *prefEvalFailingJudge) Decide(context.Context, decision.DecisionRequest) (decision.DecisionResponse, error) {
	f.calls++
	return decision.DecisionResponse{}, errors.New("offline decision failure")
}
