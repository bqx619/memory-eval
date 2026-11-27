// Package beam binds verbatim upstream BEAM prompts to the evaluator Router.
package beam

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge"
	"strings"
)

//go:embed templates.json
var raw []byte
var Templates = func() map[string]string {
	var v map[string]string
	if e := json.Unmarshal(raw, &v); e != nil {
		panic(e)
	}
	return v
}()

func SHA256() string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func Rubric(ctx context.Context, r *judge.Router, sample, item, response string) (judge.ScoreOutcome, error) {
	instructions, _, _ := strings.Cut(Templates["rubric"], "## OUTPUT FORMAT:")
	instructions = strings.ReplaceAll(instructions, "## EVALUATION INPUTS\n- RUBRIC CRITERION (what to check): <rubric_item>\n- RESPONSE TO EVALUATE: <llm_response>\n\n", "")
	// Retain every semantic rule; remove only the data block and output instruction.
	instructions = strings.ReplaceAll(instructions, "- RUBRIC CRITERION (what to check): <rubric_item>\n- RESPONSE TO EVALUATE: <llm_response>", "")
	lines := strings.Split(instructions, "\n")
	kept := []string{}
	for _, line := range lines {
		if strings.Contains(line, "**Provide Reasoning**") || strings.Contains(line, "## EVALUATION INPUTS") {
			continue
		}
		kept = append(kept, line)
	}
	instructions = strings.Join(kept, "\n")
	scale, _, _ := strings.Cut(strings.Split(instructions, "## SCORING SCALE:\n")[1], "## EVALUATION INSTRUCTIONS:")
	parts := strings.Split(scale, "- **")
	criteria := make([]string, 3)
	for i, n := range []int{3, 2, 1} {
		criteria[i] = "- **" + strings.TrimRight(parts[n], "\n")
	}
	state, _ := json.Marshal(map[string]string{"rubric_item": item, "llm_response": response})
	q := decision.Question{Name: "score", Type: "score", Instructions: instructions, Criteria: criteria, Levels: 3}
	return r.EvaluateScore(ctx, sample, "beam_rubric", judge.TemplateID("beam/rubric-score-v2", []decision.Question{q}), string(state), q)
}
func Equivalent(ctx context.Context, r *judge.Router, sample, a, b string) (judge.Outcome, error) {
	instructions := strings.ReplaceAll(Templates["equivalence_system"], " No extra words.", "")
	instructions = strings.ReplaceAll(instructions, "            DO NOT provide any exaplanation.\n", "")
	state, _ := json.Marshal(map[string]string{"first_snippet": a, "second_snippet": b})
	q := decision.Question{Name: "correct", Type: "noul", Instructions: instructions}
	return r.EvaluateBinary(ctx, sample, "beam_equivalence", judge.TemplateID("beam/equivalence-noul-v2", []decision.Question{q}), string(state), q)
}
func Align(ctx context.Context, r *judge.Router, sample string, reference, system []string) ([]string, []judge.Outcome, error) {
	out := []string{}
	trace := []judge.Outcome{}
	used := map[int]bool{}
	for i, s := range system {
		match := s
		for j, ref := range reference {
			if used[j] {
				continue
			}
			o, e := Equivalent(ctx, r, fmt.Sprintf("%s:align:%d:%d", sample, i, j), ref, s)
			trace = append(trace, o)
			if e != nil {
				return nil, trace, e
			}
			if o.Label {
				match = ref
				used[j] = true
				break
			}
		}
		out = append(out, match)
	}
	return out, trace, nil
}
