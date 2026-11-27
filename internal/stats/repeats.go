package stats

import (
	"memeval/internal/result"
	"reflect"
	"sort"
)

// A flip is any different judgment, decision value or non-null metric across a
// complete set of successful repeats. Missing/failed repeats remain excluded.
func Repeats(rows []result.Case, count int) *result.RepeatSummary {
	out := &result.RepeatSummary{Count: count, ExcludedCases: []string{}, ByMetric: map[string]result.Metric{}}
	groups := map[string][]result.Case{}
	for _, c := range rows {
		groups[c.CaseID] = append(groups[c.CaseID], c)
	}
	ids := []string{}
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cases := groups[id]
		valid := count >= 2 && len(cases) == count
		seen := map[int]bool{}
		for _, c := range cases {
			if c.RepeatIndex < 0 || c.RepeatIndex >= count || seen[c.RepeatIndex] || (c.Status != result.Scored && c.Status != result.Unscored) {
				valid = false
			}
			seen[c.RepeatIndex] = true
		}
		if !valid {
			out.ExcludedCases = append(out.ExcludedCases, id)
			continue
		}
		names := map[string]bool{"pass": true, "judge.correct": true}
		for _, c := range cases {
			for name := range c.Metrics {
				names[name] = true
			}
		}
		measured, flipped := false, false
		for name := range names {
			first := value(cases[0], name)
			complete := first != nil
			diff := false
			for _, c := range cases[1:] {
				v := value(c, name)
				if v == nil {
					complete = false
				} else if first != nil && *v != *first {
					diff = true
				}
			}
			if !complete {
				continue
			}
			measured = true
			flipped = flipped || diff
			metric := out.ByMetric[name]
			sum := 0.
			if metric.Mean != nil {
				sum = *metric.Mean * float64(metric.N)
			}
			metric.N++
			if diff {
				sum++
			}
			rate := sum / float64(metric.N)
			metric.Mean = &rate
			out.ByMetric[name] = metric
		}
		// Values can flip even when the selected-label probability stays identical.
		if cases[0].Decision != nil {
			measured = true
			for _, c := range cases[1:] {
				if !reflect.DeepEqual(cases[0].Decision["answers"], c.Decision["answers"]) {
					flipped = true
				}
			}
		}
		if !measured {
			out.ExcludedCases = append(out.ExcludedCases, id)
			continue
		}
		out.EligibleCases++
		if flipped {
			out.FlippedCases++
		}
	}
	if out.EligibleCases > 0 {
		rate := float64(out.FlippedCases) / float64(out.EligibleCases)
		out.FlipRate = &rate
	}
	return out
}
