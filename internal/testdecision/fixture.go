// Package testdecision provides deterministic TypeSafe wire fixtures for offline tests.
package testdecision

import (
	"context"
	"encoding/json"
	"memeval/internal/llm/decision"
	"net/http"
	"sort"
	"strconv"
)

type Client struct {
	P        float64
	Err      error
	Calls    int
	Requests []decision.DecisionRequest
}

func (c *Client) Decide(_ context.Context, r decision.DecisionRequest) (decision.DecisionResponse, error) {
	c.Calls++
	c.Requests = append(c.Requests, r)
	if c.Err != nil {
		return decision.DecisionResponse{}, c.Err
	}
	out := decision.DecisionResponse{ModelReturned: r.Model, RequestSHA: "fixture-request", Answers: map[string]decision.Answer{}}
	for _, q := range r.Questions {
		a := decision.Answer{Type: q.Type}
		switch q.Type {
		case "noul":
			a.Noul = new(c.P)
			a.Value = c.P
		case "score":
			a.Value = float64(q.Levels - 1)
			a.Confidence = new(1.)
			a.Probabilities = map[string]float64{}
			a.Legend = map[string]string{}
			for i := range q.Levels {
				k := strconv.Itoa(i)
				a.Probabilities[k] = 0
				a.Legend[k] = k
			}
			a.Probabilities[strconv.Itoa(q.Levels-1)] = 1
		case "choice":
			key := q.Options[0]
			a.Value = key
			a.Confidence = new(1.)
			a.Probabilities = map[string]float64{}
			for _, k := range q.Options {
				a.Probabilities[k] = 0
			}
			a.Probabilities[key] = 1
		}
		out.Answers[q.Name] = a
	}
	return out, nil
}

// Wire turns logical fixture selections into the observed TypeSafe response.
// selectValue returns P(true), an integer score level, or a choice key.
func Wire(w http.ResponseWriter, model string, questions map[string]json.RawMessage, selectValue func(string, string) any) {
	answers := map[string]any{}
	for name, raw := range questions {
		var q struct {
			Type     string          `json:"type"`
			Criteria json.RawMessage `json:"criteria"`
		}
		json.Unmarshal(raw, &q)
		v := selectValue(name, q.Type)
		a := map[string]any{"type": q.Type}
		switch q.Type {
		case "noul":
			a["noul"] = v
		case "score":
			var labels []string
			json.Unmarshal(q.Criteria, &labels)
			level := v.(int)
			probs := map[string]float64{}
			legend := map[string]string{}
			for i, s := range labels {
				k := strconv.Itoa(i)
				probs[k] = 0
				legend[k] = s
			}
			probs[strconv.Itoa(level)] = 1
			a["score"] = level
			a["legend"] = legend
			a["probabilities"] = probs
			a["confidence"] = 1
		case "choice":
			var labels map[string]string
			json.Unmarshal(q.Criteria, &labels)
			key, _ := v.(string)
			if key == "" {
				keys := []string{}
				for k := range labels {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				key = keys[0]
			}
			probs := map[string]float64{}
			for k := range labels {
				probs[k] = 0
			}
			probs[key] = 1
			a["choice"] = key
			a["probabilities"] = probs
			a["confidence"] = 1
		}
		answers[name] = a
	}
	json.NewEncoder(w).Encode(map[string]any{"model": model, "answers": answers, "usage": map[string]int{"input_tokens": 20, "output_tokens": 0}})
}
