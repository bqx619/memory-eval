// Package convomem mirrors the three Scala AnsweringEvaluation prompts and parser.
package convomem

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge"
	"regexp"
	"strings"
)

//go:embed templates.json
var templateBytes []byte
var Templates = func() map[string]string {
	var m map[string]string
	if e := json.Unmarshal(templateBytes, &m); e != nil {
		panic(e)
	}
	return m
}()

func SHA256() string { return fmt.Sprintf("%x", sha256.Sum256(templateBytes)) }

var placeholder = regexp.MustCompile(`\$(question|correctAnswer|modelAnswer|conversationContext)\b`)

// Render interpolates once, like Scala; dollar expressions in user text stay literal.
func Render(key string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(Templates[key], func(s string) string { return values[s[1:]] })
}

type Judgment struct {
	Template      string   `json:"template"`
	Label         *bool    `json:"label"`
	P             *float64 `json:"p,omitempty"`
	Confidence    *float64 `json:"confidence,omitempty"`
	RequestSHA    string   `json:"request_sha"`
	ModelReturned string   `json:"model_returned"`
}

func Evaluate(ctx context.Context, r *judge.Router, sample, template, question, correct, answer string) (Judgment, error) {
	if template != "default" && template != "rubric" && template != "abstention" {
		return Judgment{}, fmt.Errorf("unknown ConvoMem template %q", template)
	}
	instructions, _, _ := strings.Cut(Templates[template], "**Answer only")
	state := map[string]string{"question": question, "reference_answer": correct, "response": answer}
	if template == "rubric" {
		head, _, _ := strings.Cut(Templates[template], "\n\nQuestion asked:")
		_, tail, _ := strings.Cut(Templates[template], "Guidelines:")
		tail = strings.TrimSuffix(tail, "\n\nRespond with only RIGHT or WRONG.")
		instructions = head + "\n\nGuidelines:" + tail
		state["rubric"] = correct
	}
	if template == "abstention" {
		state["abstention_rule"] = "The model should indicate it cannot answer due to insufficient information"
	}
	b, _ := json.Marshal(state)
	q := decision.Question{Name: "correct", Type: "noul", Instructions: instructions}
	v, e := r.EvaluateBinary(ctx, sample, "convomem_"+template, judge.TemplateID("convomem/noul-"+template+"-v2", []decision.Question{q}), string(b), q)
	j := Judgment{Template: template, P: v.P, Confidence: v.Confidence, RequestSHA: v.RequestSHA, ModelReturned: v.ModelReturned}
	if e == nil {
		j.Label = &v.Label
	}
	return j, e
}
