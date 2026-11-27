// Package judge routes closed-rubric judgments exclusively to TypeSafe.
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge/inputlog"
	official "memeval/internal/scoring/judge/longmemeval"
	"regexp"
	"strings"
)

type Router struct {
	Decision      decision.DecisionClient
	DecisionModel string
}

var pinnedJev = regexp.MustCompile(`^jev-[0-9]+\.[0-9]+\.[0-9]+$`)

type Input struct {
	SampleID, QuestionType, Question, Reference, Response string
	Abstention                                            bool
}
type Outcome struct {
	Label         bool     `json:"label"`
	P             *float64 `json:"p,omitempty"`
	Confidence    *float64 `json:"confidence,omitempty"`
	RequestSHA    string   `json:"request_sha"`
	ModelReturned string   `json:"model_returned"`
}

func (r *Router) Validate() error {
	if r.Decision == nil || !pinnedJev.MatchString(r.DecisionModel) {
		return fmt.Errorf("decision judge requires a pinned jev version and client")
	}
	return nil
}

// DecisionPrompt retains the two instruction blocks verbatim, removing only the
// LLM output-format sentence. The three data blocks become structured state.
func DecisionPrompt(in Input) (string, decision.Question, error) {
	template, err := official.Template(in.QuestionType, in.Abstention)
	if err != nil {
		return "", decision.Question{}, err
	}
	head, _, ok := strings.Cut(template, "\n\nQuestion: {}")
	_, tail, okTail := strings.Cut(template, "\n\nModel Response: {}\n\n")
	if !ok || !okTail {
		return "", decision.Question{}, fmt.Errorf("official template shape changed")
	}
	instructions := head + "\n\n" + strings.TrimSuffix(tail, " Answer yes or no only.")
	key := "correct_answer"
	if in.Abstention {
		key = "explanation"
	} else if in.QuestionType == "single-session-preference" {
		key = "rubric"
	}
	state, err := json.Marshal(map[string]string{"question": in.Question, key: in.Reference, "model_response": in.Response})
	return string(state), decision.Question{Name: "correct", Type: "noul", Instructions: instructions, Criteria: nil}, err
}

func (r *Router) Evaluate(ctx context.Context, in Input) (Outcome, error) {
	state, q, e := DecisionPrompt(in)
	if e != nil {
		return Outcome{}, e
	}
	name := "longmemeval/noul-" + in.QuestionType + "-v2"
	if in.Abstention {
		name = "longmemeval/noul-abstention-v2"
	}
	return r.EvaluateBinary(ctx, in.SampleID, in.QuestionType, TemplateID(name, []decision.Question{q}), state, q)
}
func (r *Router) EvaluateBinary(ctx context.Context, sample, category, template, state string, q decision.Question) (out Outcome, err error) {
	recipe := inputlog.Recipe{Kind: "binary", State: state, Questions: []decision.Question{q}, TemplateID: template}
	resp, e := r.decide(ctx, sample, recipe)
	out.RequestSHA = resp.RequestSHA
	out.ModelReturned = resp.ModelReturned
	if e == nil {
		a, ok := resp.Answers[q.Name]
		p, valid := decision.TrueProbability(a)
		if !ok || !valid || math.IsNaN(p) {
			e = fmt.Errorf("decision judge probability invalid")
		} else {
			out.P = &p
			out.Label = p >= .5
			out.Confidence = a.Confidence
		}
	}
	v := verdict(r, resp, e)
	if e == nil {
		v.Label = &out.Label
		v.P = out.P
		v.Confidence = out.Confidence
	}
	return out, record(ctx, sample, category, recipe, v, e)
}
