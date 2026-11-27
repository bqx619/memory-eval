package prefeval

import (
	"context"
	"encoding/json"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge"
	"strings"
)

type Judgment struct {
	Probe  string `json:"probe"`
	Answer string `json:"answer"`
	judge.Outcome
}

const HallucinateDeviation = "official hallucinate evaluates an LLM-extracted restatement; Jev evaluates the full response without an extraction step"

func Instructions(probe string) string {
	text := Templates[probe]
	end := "\n\nHere is the conversation:"
	if probe == "acknow" {
		end = "\n\nHere is the assistant's response:"
	}
	if probe == "hallucinate" {
		end = "\n\nHere is the information:"
	}
	text, _, _ = strings.Cut(text, end)
	text = strings.ReplaceAll(text, ` You should extract the "xxxx" in your answer.`, "")
	if probe == "hallucinate" {
		text = strings.ReplaceAll(text, "assistant's restatement of the user's stated preference", "response's restatement or attribution of a preference to the user")
		text = strings.ReplaceAll(text, "assistant's restatement", "response's restatement or attribution of a preference to the user")
		text = strings.ReplaceAll(text, "The restatement is just empty", "There is no restatement or attribution of a preference to the user in the response")
	}
	return Templates["judge_system"] + "\n\n" + text
}
func Evaluate(ctx context.Context, r *judge.Router, sample, query, answer, preference string) ([]Judgment, error) {
	out := []Judgment{}
	state, _ := json.Marshal(map[string]string{"preference": preference, "user_query": query, "response": answer})
	for _, probe := range Probes {
		q := decision.Question{Name: "correct", Type: "noul", Instructions: Instructions(probe)}
		v, e := r.EvaluateBinary(ctx, sample+":"+probe, "prefeval_"+probe, judge.TemplateID("prefeval/noul-"+probe+"-v2", []decision.Question{q}), string(state), q)
		j := Judgment{Probe: probe, Answer: "No", Outcome: v}
		if v.Label {
			j.Answer = "Yes"
		}
		out = append(out, j)
		if e != nil {
			return out, e
		}
	}
	return out, nil
}
