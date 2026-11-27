package scoring

import (
	"encoding/json"
	"fmt"
	"memeval/internal/result"
	"slices"
	"strings"
)

type Rule struct {
	MustInclude       []string   `json:"mustInclude"`
	ShouldInclude     []string   `json:"shouldInclude"`
	MustExclude       []string   `json:"mustExclude"`
	ShouldOnlyInclude *[]string  `json:"shouldOnlyInclude"`
	MaxCount          *int       `json:"maxCount"`
	MinCount          *int       `json:"minCount"`
	OrderedBefore     [][]string `json:"orderedBefore"`
}
type Expect struct {
	RelevantBeliefs Rule `json:"relevantBeliefs"`
	PinnedFacts     Rule `json:"pinnedFacts"`
	OpenQuestions   Rule `json:"openQuestions"`
	PersonaPrelude  struct {
		NonEmpty       bool     `json:"nonEmpty"`
		IsNull         bool     `json:"isNull"`
		Contains       []string `json:"contains"`
		MustNotContain []string `json:"mustNotContain"`
	} `json:"personaPrelude"`
}
type TypedResponse struct {
	PinnedFacts, RelevantBeliefs, OpenQuestions []string
	PersonaPrelude                              string
}
type Outcome struct {
	Status                       result.Status
	Passed                       *bool
	Assertions, AssertionsPassed int
	Failures                     []string
	Metrics                      Metrics
	Note                         *string
}

func pythonRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = "\""
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, quote, "\\"+quote)
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return quote + s + quote
}
func ScoreTyped(raw json.RawMessage, response TypedResponse, k int) (Outcome, error) {
	var e Expect
	if len(raw) == 0 {
		return Outcome{}, fmt.Errorf("missing typed expectations")
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Status: result.Unscored, Failures: []string{}}
	check := func(ok bool, msg string) {
		out.Assertions++
		if ok {
			out.AssertionsPassed++
		} else {
			out.Failures = append(out.Failures, msg)
		}
	}
	pinned, relevant, questions := set(response.PinnedFacts), set(response.RelevantBeliefs), set(response.OpenQuestions)
	union := set(append(slices.Clone(response.PinnedFacts), response.RelevantBeliefs...))
	rb := e.RelevantBeliefs
	for _, rule := range []struct {
		name string
		ids  []string
	}{{"mustInclude", rb.MustInclude}, {"shouldInclude", rb.ShouldInclude}} {
		for _, id := range rule.ids {
			check(union[id], rule.name+": missing "+id)
		}
	}
	for _, id := range rb.MustExclude {
		check(!union[id], "mustExclude: leaked "+id)
	}
	if rb.ShouldOnlyInclude != nil {
		g := set(*rb.ShouldOnlyInclude)
		same := len(g) == len(relevant)
		for id := range g {
			same = same && relevant[id]
		}
		check(same, "relevantBeliefs exact-set mismatch")
	}
	if rb.MaxCount != nil {
		check(len(relevant) <= *rb.MaxCount, "relevantBeliefs too many")
	}
	if rb.MinCount != nil {
		check(len(relevant) >= *rb.MinCount, "relevantBeliefs too few")
	}
	for _, pair := range rb.OrderedBefore {
		if len(pair) != 2 {
			return Outcome{}, fmt.Errorf("orderedBefore requires pairs")
		}
		a, b := slices.Index(response.RelevantBeliefs, pair[0]), slices.Index(response.RelevantBeliefs, pair[1])
		check(a >= 0 && b >= 0 && a < b, "ranking: "+pair[0]+" before "+pair[1])
	}
	for _, r := range []struct {
		name string
		rule Rule
		pred map[string]bool
	}{{"pinnedFacts", e.PinnedFacts, pinned}, {"openQuestions", e.OpenQuestions, questions}} {
		for _, id := range r.rule.MustInclude {
			check(r.pred[id], r.name+": missing "+id)
		}
		for _, id := range r.rule.MustExclude {
			check(!r.pred[id], r.name+": forbidden "+id)
		}
	}
	pp := e.PersonaPrelude
	if pp.NonEmpty {
		check(response.PersonaPrelude != "", "personaPrelude empty")
	}
	if pp.IsNull {
		check(response.PersonaPrelude == "", "personaPrelude should be empty")
	}
	for _, s := range pp.Contains {
		check(strings.Contains(response.PersonaPrelude, s), "personaPrelude missing substring "+pythonRepr(s))
	}
	for _, s := range pp.MustNotContain {
		check(!strings.Contains(response.PersonaPrelude, s), "personaPrelude forbidden substring "+pythonRepr(s))
	}
	if rb.ShouldOnlyInclude != nil {
		m, err := Retrieval(response.RelevantBeliefs, *rb.ShouldOnlyInclude, true, k)
		if err != nil {
			return Outcome{}, err
		}
		out.Metrics = m
	} else {
		out.Metrics.Values = map[string]*float64{"precision": nil, "recall": nil, "hit": nil, "mrr": nil, "all_evidence": nil, "ndcg": nil}
	}
	wanted := set(e.PinnedFacts.MustInclude)
	out.Metrics.Values["pinned_coverage"] = nil
	if len(wanted) > 0 {
		hits := 0
		for id := range wanted {
			if pinned[id] {
				hits++
			}
		}
		out.Metrics.Values["pinned_coverage"] = Float(float64(hits) / float64(len(wanted)))
	}
	if out.Assertions > 0 {
		out.Status = result.Scored
		out.Passed = Bool(out.Assertions == out.AssertionsPassed)
	} else {
		note := "No effective source assertions; not a free pass"
		out.Note = &note
	}
	return out, nil
}
