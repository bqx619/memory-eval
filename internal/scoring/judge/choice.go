package judge

import (
	"context"
	"fmt"
	"math"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge/inputlog"
	"slices"
)

type ChoiceOutcome struct {
	Deviations    []string                      `json:"deviations,omitempty"`
	Labels        map[string]string             `json:"labels"`
	Probabilities map[string]map[string]float64 `json:"probabilities,omitempty"`
	Confidence    map[string]float64            `json:"confidence,omitempty"`
	RequestSHA    string                        `json:"request_sha"`
	ModelReturned string                        `json:"model_returned"`
}

func (r *Router) EvaluateChoices(ctx context.Context, sample, category, template, state string, qs []decision.Question) (out ChoiceOutcome, err error) {
	recipe := inputlog.Recipe{Kind: "choices", State: state, Questions: qs, TemplateID: template}
	resp, e := r.decide(ctx, sample, recipe)
	out = ChoiceOutcome{Labels: map[string]string{}, Probabilities: map[string]map[string]float64{}, Confidence: map[string]float64{}, RequestSHA: resp.RequestSHA, ModelReturned: resp.ModelReturned}
	if e == nil {
		for _, q := range qs {
			a, ok := resp.Answers[q.Name]
			label, valid := a.Value.(string)
			if !ok || !valid || !slices.Contains(q.Options, label) || a.Confidence == nil || math.IsNaN(*a.Confidence) || *a.Confidence < 0 || *a.Confidence > 1 {
				e = fmt.Errorf("invalid categorical judgment")
				break
			}
			out.Labels[q.Name] = label
			out.Probabilities[q.Name] = a.Probabilities
			out.Confidence[q.Name] = *a.Confidence
			for _, d := range a.Deviations {
				out.Deviations = append(out.Deviations, q.Name+": "+d)
			}
		}
	}
	v := verdict(r, resp, e)
	if e == nil {
		v.Labels = out.Labels
		v.ChoiceProbabilities = out.Probabilities
		v.ChoiceConfidence = out.Confidence
		v.Deviations = out.Deviations
	}
	return out, record(ctx, sample, category, recipe, v, e)
}
