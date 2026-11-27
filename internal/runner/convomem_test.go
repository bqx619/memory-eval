package runner

import (
	"context"
	"encoding/json"
	"errors"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/suite"
	"strings"
	"testing"
)

type convoMemInvalid struct{ calls int }

func (f *convoMemInvalid) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	f.calls++
	if r.Purpose == "reader" {
		return llm.Response{Text: "answer"}, nil
	}
	return llm.Response{Text: "neither"}, nil
}
func TestConvoMemInvalidExcluded(t *testing.T) {
	c := suite.Case{ID: "one", Kind: "qa", Query: "Q", Gold: suite.Gold{Answers: []string{"A"}, Checker: &suite.Checker{Type: "convomem_judge", Params: json.RawMessage(`{"template":"default"}`)}}}
	s := &suite.Suite{SchemaVersion: "3.0", Episodes: []suite.Episode{{ID: "ep", Cases: []suite.Case{c, c}}}}
	f := &convoMemInvalid{}
	o := Options{suiteSHA: strings.Repeat("a", 64), Repeats: 1, FixedReader: &FixedReaderOptions{Client: f, Provider: "openai-compat", Model: "reader", Input: "sources", JudgeDecisionModel: "jev-1.14.0", DecisionClient: f, MaxTokens: 512}}
	failed := result.Case{SampleID: "sample", Category: "assistant_facts", Budget: protocol.Budget{MaxContextChars: 1000}, Metrics: map[string]*float64{}}
	known := map[string]suite.Event{"id": {SourceID: "raw", Role: "user", Session: "c0", Content: "fact", Attributes: map[string]any{"memeval_reader_sequence": 0}}}
	if e := convoMemAnswer(t.Context(), &failed, c, []protocol.Item{{SourceIDs: []string{"id"}}}, known, o); e == nil || f.calls != 2 || len(failed.Metrics) != 0 {
		t.Fatal(failed, e, f.calls)
	}
	failed.Status = result.Error
	v := 1.
	yes := true
	r := &result.Report{Protocol: result.Protocol{Benchmark: &result.BenchmarkRef{Variant: "assistant_facts"}}, Cases: []result.Case{failed, {Category: "assistant_facts", Status: result.Scored, Metrics: map[string]*float64{"convomem.correct": &v, "convomem.correct.n_evidence.2": &v}, QA: &result.QA{Hypothesis: new("ok"), JudgeLabel: &yes}}}}
	if e := convoMemMetadata(r, s, o); e != nil {
		t.Fatal(e)
	}
	off := r.Scores.Official.(result.Official)
	if off.Score == nil || *off.Score != 1 || off.Errors != 1 || off.Judged != 1 || off.QuestionSet.Complete || *off.ByTypeMetrics["assistant_facts"]["convomem.correct.n_evidence.2"] != 1 {
		t.Fatal(off)
	}
}

func (f *convoMemInvalid) Decide(context.Context, decision.DecisionRequest) (decision.DecisionResponse, error) {
	f.calls++
	return decision.DecisionResponse{}, errors.New("offline decision failure")
}
