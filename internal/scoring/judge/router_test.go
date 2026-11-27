package judge

import (
	"context"
	"memeval/internal/llm/decision"
	"memeval/internal/testdecision"
	"testing"
)

type decisionFunc func(context.Context, decision.DecisionRequest) (decision.DecisionResponse, error)

func (f decisionFunc) Decide(c context.Context, r decision.DecisionRequest) (decision.DecisionResponse, error) {
	return f(c, r)
}
func TestNoulCutoffAndPinnedModel(t *testing.T) {
	for _, p := range []float64{0, .4999, .5, 1} {
		d := &testdecision.Client{P: p}
		r := Router{Decision: d, DecisionModel: "jev-1.14.0"}
		v, e := r.Evaluate(t.Context(), Input{SampleID: "s", QuestionType: "multi-session", Question: "Q", Reference: "A", Response: "R"})
		if e != nil || v.Label != (p >= .5) || *v.P != p || v.RequestSHA == "" {
			t.Fatal(v, e)
		}
		if d.Requests[0].Purpose != "judge-decision" {
			t.Fatal(d.Requests)
		}
	}
	for _, model := range []string{"", "jev-latest", "gpt-6-luna"} {
		r := Router{Decision: &testdecision.Client{}, DecisionModel: model}
		if r.Validate() == nil {
			t.Fatal(model)
		}
	}
}
func TestScoreArgmaxAndChoiceField(t *testing.T) {
	for _, tc := range []struct {
		p             map[string]float64
		expect, score float64
	}{{map[string]float64{"0": .45, "1": .1, "2": .45}, 1, 0}, {map[string]float64{"0": .4, "1": .15, "2": .45}, 1.05, 1}, {map[string]float64{"0": .1, "1": .7, "2": .2}, 1.1, .5}} {
		r := Router{DecisionModel: "jev-1.14.0", Decision: decisionFunc(func(context.Context, decision.DecisionRequest) (decision.DecisionResponse, error) {
			return decision.DecisionResponse{RequestSHA: "sha", Answers: map[string]decision.Answer{"score": {Type: "score", Value: tc.expect, Probabilities: tc.p, Confidence: new(.9)}}}, nil
		})}
		out, e := r.EvaluateScore(t.Context(), "s", "beam_rubric", "t", "{}", decision.Question{Name: "score", Type: "score", Levels: 3})
		if e != nil || out.Score != tc.score || *out.Expectation != tc.expect/2 {
			t.Fatal(out, e)
		}
	}
	r := Router{DecisionModel: "jev-1.14.0", Decision: decisionFunc(func(context.Context, decision.DecisionRequest) (decision.DecisionResponse, error) {
		return decision.DecisionResponse{RequestSHA: "sha", Answers: map[string]decision.Answer{"label": {Type: "choice", Value: "b", Probabilities: map[string]float64{"a": .8, "b": .2}, Confidence: new(.6)}}}, nil
	})}
	out, e := r.EvaluateChoices(t.Context(), "s", "test", "t", "{}", []decision.Question{{Name: "label", Type: "choice", Options: []string{"a", "b"}}})
	if e != nil || out.Labels["label"] != "b" || out.Probabilities["label"]["a"] != .8 {
		t.Fatal(out, e)
	}
}
