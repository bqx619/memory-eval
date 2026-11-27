package result

import (
	"encoding/json"
	"fmt"
)

type RepeatSummary struct {
	Count         int               `json:"count"`
	FlipRate      *float64          `json:"flip_rate"`
	EligibleCases int               `json:"eligible_cases"`
	FlippedCases  int               `json:"flipped_cases"`
	ExcludedCases []string          `json:"excluded_cases"`
	ByMetric      map[string]Metric `json:"by_metric"`
}

// New repeated runs expose repeats.flip_rate; legacy numeric reports still load.
func (r Report) MarshalJSON() ([]byte, error) {
	type plain Report
	var repeats any = r.Repeats
	if r.RepeatAnalysis != nil {
		copy := *r.RepeatAnalysis
		copy.Count = r.Repeats
		repeats = copy
	}
	return json.Marshal(struct {
		plain
		Repeats any `json:"repeats"`
	}{plain(r), repeats})
}
func (r *Report) UnmarshalJSON(data []byte) error {
	type plain Report
	var wire struct {
		*plain
		Repeats json.RawMessage `json:"repeats"`
	}
	wire.plain = (*plain)(r)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.RepeatAnalysis = nil
	if len(wire.Repeats) == 0 {
		return nil
	}
	if wire.Repeats[0] != '{' {
		return json.Unmarshal(wire.Repeats, &r.Repeats)
	}
	var s RepeatSummary
	if err := json.Unmarshal(wire.Repeats, &s); err != nil {
		return err
	}
	if s.Count < 1 {
		return fmt.Errorf("invalid repeats count")
	}
	r.Repeats = s.Count
	r.RepeatAnalysis = &s
	return nil
}
