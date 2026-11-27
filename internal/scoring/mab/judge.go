package mab

import (
	"context"
	"encoding/json"
	templates "memeval/internal/benchmark/mab"
	"memeval/internal/llm/decision"
	"memeval/internal/pytext"
	routing "memeval/internal/scoring/judge"
	"strings"
)

// Fluency retains the official book rubric, excluding only output formatting.
func Fluency(ctx context.Context, r *routing.Router, sample, text string) (routing.Outcome, error) {
	rubric, e := templates.SummaryPrompt("fluency", map[string]string{"text": ""})
	if e != nil {
		return routing.Outcome{}, e
	}
	if i := strings.LastIndex(rubric, "\n\nText:"); i >= 0 {
		rubric = rubric[:i]
	}
	rubric = strings.ReplaceAll(rubric, `Then output your score in the following json format: {"fluency": 1}.`, "")
	state, _ := json.Marshal(map[string]string{"text": pytext.Strip(text)})
	q := decision.Question{Name: "correct", Type: "noul", Instructions: rubric}
	return r.EvaluateBinary(ctx, sample, "mab_infbench_fluency", routing.TemplateID("memoryagentbench/fluency-noul-v2", []decision.Question{q}), string(state), q)
}
