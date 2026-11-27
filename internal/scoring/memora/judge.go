package memora

import (
	"context"
	"encoding/json"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge"
	"strings"
)

type Judgment struct {
	Outcome judge.Outcome `json:"outcome"`
}

func Evaluate(ctx context.Context, r *judge.Router, sample, question, answer string) (Judgment, error) {
	head, _, _ := strings.Cut(Templates["judge_system_prompt"], "\n\nYou must provide")
	_, tail, _ := strings.Cut(Templates["judge_system_prompt"], "\n\nBe objective")
	user, _, _ := strings.Cut(Templates["judge_user_prompt"], "\n\nAI RESPONSE")
	instructions := head + "\n\nBe objective" + tail + "\n\n" + user
	state, _ := json.Marshal(map[string]string{"generated_answer": answer, "evaluation_question": question})
	q := decision.Question{Name: "correct", Type: "noul", Instructions: instructions}
	out, e := r.EvaluateBinary(ctx, sample, "memora_eval", judge.TemplateID("memora/noul-v2", []decision.Question{q}), string(state), q)
	return Judgment{out}, e
}
