package halumem

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge"
	"regexp"
	"strconv"
	"strings"
)

//go:embed templates.json
var TemplatesJSON []byte
var Templates = func() map[string]string {
	m := map[string]string{}
	if e := json.Unmarshal(TemplatesJSON, &m); e != nil {
		panic(e)
	}
	return m
}()

func SHA256() string { return fmt.Sprintf("%x", sha256.Sum256(TemplatesJSON)) }

var fields = regexp.MustCompile(`\{\{|\}\}|\{([a-z_]+)\}`)

// Format implements the literal braces and named placeholders used by Python str.format.
func Format(template string, values map[string]string) string {
	return fields.ReplaceAllStringFunc(template, func(s string) string {
		if s == "{{" {
			return "{"
		}
		if s == "}}" {
			return "}"
		}
		if v, ok := values[s[1:len(s)-1]]; ok {
			return v
		}
		return s
	})
}

type Parsed struct {
	Score    *int
	Label    *string
	Included any
}
type Judgment struct {
	Parsed  Parsed              `json:"-"`
	Outcome judge.ChoiceOutcome `json:"outcome"`
}

func section(text, start, end string) string {
	_, tail, _ := strings.Cut(text, start)
	if end != "" {
		tail, _, _ = strings.Cut(tail, end)
	}
	return strings.TrimSpace(tail)
}
func Questions(kind string) ([]decision.Question, error) {
	names := map[string]string{"integrity": "MEMORY_INTEGRITY", "accuracy": "MEMORY_ACCURACY", "update": "UPDATE_MEMORY", "question": "QUESTION"}
	name, ok := names[kind]
	if !ok {
		return nil, fmt.Errorf("unknown HaluMem judge kind")
	}
	text := Templates["EVALUATION_PROMPT_FOR_"+name]
	instructions := text
	if kind == "question" {
		instructions, _, _ = strings.Cut(text, "# Information for Evaluation")
		instructions = strings.ReplaceAll(instructions, " Finally, output your judgment **strictly** in the specified JSON format.", "")
	} else {
		start, end := "# Evaluation Context & Data:", "# Evaluation Instructions:"
		if kind == "accuracy" {
			start, end = "# Input Content", "# Evaluation Principles and Definitions"
		}
		if kind == "update" {
			start, end = "# Background Information", "# Evaluation Criteria"
		}
		head, _, _ := strings.Cut(text, start)
		_, tail, _ := strings.Cut(text, end)
		instructions = head + end + tail
		instructions, _, _ = strings.Cut(instructions, "# Output")
	}
	instructions = strings.ReplaceAll(instructions, `5. Provide a **concise Chinese explanation** in `+"`\"reason\"`"+`, citing key evidence (short excerpts allowed), and clearly state any unsupported or contradictory parts if applicable.`, "")
	options := []string{"0", "1", "2"}
	criteria := map[string]string{}
	switch kind {
	case "integrity":
		for _, v := range options {
			end := "\n\n* **"
			if v == "0" {
				end = "\n\n# Scoring Notes:"
			}
			criteria[v] = section(text, "* **"+v+":**", end)
		}
	case "accuracy":
		for _, v := range options {
			unit := " points"
			if v == "1" {
				unit = " point"
			}
			criteria[v] = section(text, "* **"+v+unit+":**", "\n")
		}
	case "update":
		options = []string{"Correct", "Hallucination", "Omission", "Other"}
		heads := []string{"Correct Update", "Hallucinated Update", "Omitted Update", "Other"}
		for i, v := range options {
			end := "\n# Output"
			if i+1 < len(heads) {
				end = "### " + heads[i+1]
			}
			criteria[v] = section(text, "### "+heads[i], end)
		}
	case "question":
		options = []string{"Correct", "Hallucination", "Omission"}
		for i, v := range options {
			end := "## Priority Rules"
			if i+1 < len(options) {
				end = fmt.Sprintf("### %d. %s", i+2, options[i+1])
			}
			criteria[v] = section(text, fmt.Sprintf("### %d. %s", i+1, v), end)
		}
	}
	qs := []decision.Question{{Name: "label", Type: "choice", Options: options, Criteria: criteria, Instructions: instructions}}
	if kind == "accuracy" {
		qs = append(qs, decision.Question{Name: "included", Type: "choice", Options: []string{"true", "false"}, Criteria: map[string]string{"true": section(text, "* **true:**", "* **false:**"), "false": section(text, "* **false:**", "**Important Notes:**")}, Instructions: instructions})
	}
	return qs, nil
}
func Evaluate(ctx context.Context, r *judge.Router, sample, kind string, values map[string]string) (Judgment, error) {
	qs, e := Questions(kind)
	if e != nil {
		return Judgment{}, e
	}
	state, _ := json.Marshal(values)
	out, e := r.EvaluateChoices(ctx, sample, "halumem_"+kind, judge.TemplateID("halumem/"+kind+"-v2", qs), string(state), qs)
	parsed := Parsed{Included: "false"}
	if e == nil {
		if kind == "integrity" || kind == "accuracy" {
			n, _ := strconv.Atoi(out.Labels["label"])
			parsed.Score = &n
		} else {
			label := out.Labels["label"]
			parsed.Label = &label
		}
		if kind == "accuracy" {
			parsed.Included = out.Labels["included"]
		}
	}
	return Judgment{parsed, out}, e
}
