package judge

import (
	"context"
	"fmt"
	"math"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge/inputlog"
	"strconv"
)

type ScoreOutcome struct {
	Deviations    []string           `json:"deviations,omitempty"`
	Score         float64            `json:"score"`
	Expectation   *float64           `json:"expectation,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	RequestSHA    string             `json:"request_sha"`
	ModelReturned string             `json:"model_returned"`
}

func (r *Router) EvaluateScore(ctx context.Context, sample, category, template, state string, q decision.Question) (out ScoreOutcome, err error) {
	recipe := inputlog.Recipe{Kind: "score", State: state, Questions: []decision.Question{q}, TemplateID: template}
	resp, e := r.decide(ctx, sample, recipe)
	out.RequestSHA = resp.RequestSHA
	out.ModelReturned = resp.ModelReturned
	if e == nil {
		a, ok := resp.Answers[q.Name]
		expect, number := a.Value.(float64)
		if !ok || !number || math.IsNaN(expect) || expect < 0 || expect > float64(q.Levels-1) || a.Confidence == nil || math.IsNaN(*a.Confidence) || *a.Confidence < 0 || *a.Confidence > 1 || len(a.Probabilities) != q.Levels {
			e = fmt.Errorf("invalid ordinal judgment")
		} else {
			best := -1.
			level := 0
			for i := 0; i < q.Levels; i++ {
				p, found := a.Probabilities[strconv.Itoa(i)]
				if !found || math.IsNaN(p) || p < 0 || p > 1 {
					e = fmt.Errorf("invalid ordinal probability")
					break
				}
				if p > best {
					level = i
					best = p
				}
			}
			if d, err := decision.ValidateDistribution(a.Probabilities); err != nil {
				e = err
			} else if d != "" {
				out.Deviations = []string{d}
			}
			if e == nil {
				out.Score = float64(level) / float64(q.Levels-1)
				expect /= float64(q.Levels - 1)
				out.Expectation = &expect
				out.Probabilities = a.Probabilities
				out.Confidence = a.Confidence
			}
		}
	}
	v := verdict(r, resp, e)
	if e == nil {
		v.Score = &out.Score
		v.Expectation = out.Expectation
		v.Probabilities = out.Probabilities
		v.Confidence = out.Confidence
		v.Deviations = out.Deviations
	}
	return out, record(ctx, sample, category, recipe, v, e)
}
