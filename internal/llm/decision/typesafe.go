package decision

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// Shapes below come from the real-wire observations recorded in
// design/m3-upstream-facts.md.
func validateTypeSafeCriteria(q Question) error {
	if q.Type == "noul" {
		if q.Criteria != nil {
			return fmt.Errorf("TypeSafe noul uses instructions, not criteria")
		}
		return nil
	}
	b, err := json.Marshal(q.Criteria)
	if err != nil {
		return fmt.Errorf("invalid TypeSafe criteria")
	}
	var value any
	if json.Unmarshal(b, &value) != nil {
		return fmt.Errorf("invalid TypeSafe criteria")
	}
	switch q.Type {
	case "choice":
		criteria, ok := value.(map[string]any)
		if !ok || len(criteria) != len(q.Options) {
			return fmt.Errorf("TypeSafe choice criteria must map every option to a description")
		}
		for _, key := range q.Options {
			if _, ok := criteria[key].(string); !ok {
				return fmt.Errorf("TypeSafe choice description missing or not a string")
			}
		}
	case "score":
		criteria, ok := value.([]any)
		if !ok || len(criteria) != q.Levels {
			return fmt.Errorf("TypeSafe score criteria must list each level description in order")
		}
		for _, description := range criteria {
			if _, ok := description.(string); !ok {
				return fmt.Errorf("TypeSafe score description must be a string")
			}
		}
	}
	return nil
}

func decodeTypeSafeAnswer(q Question, data []byte) (Answer, error) {
	var wire struct {
		Type          string             `json:"type"`
		Noul          *float64           `json:"noul"`
		Choice        *string            `json:"choice"`
		Score         *float64           `json:"score"`
		Legend        map[string]string  `json:"legend"`
		Probabilities map[string]float64 `json:"probabilities"`
		Confidence    *float64           `json:"confidence"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.Type != q.Type {
		return Answer{}, fmt.Errorf("invalid TypeSafe answer")
	}
	a := Answer{Type: q.Type, Confidence: wire.Confidence, Probabilities: wire.Probabilities}
	switch q.Type {
	case "noul":
		if wire.Noul == nil || !probability(*wire.Noul) {
			return Answer{}, fmt.Errorf("TypeSafe noul must be scalar P(true)")
		}
		// Do not manufacture server confidence or a probability table.
		a = Answer{Type: q.Type, Value: *wire.Noul, Noul: wire.Noul}
	case "choice":
		if wire.Choice == nil {
			return Answer{}, fmt.Errorf("TypeSafe choice missing")
		}
		a.Value = *wire.Choice
		if err := validateAnswer(q, a); err != nil {
			return Answer{}, err
		}
	case "score":
		if wire.Score == nil || math.IsNaN(*wire.Score) || math.IsInf(*wire.Score, 0) || *wire.Score < 0 || *wire.Score > float64(q.Levels-1) || wire.Confidence == nil || !probability(*wire.Confidence) {
			return Answer{}, fmt.Errorf("TypeSafe score expectation/confidence invalid")
		}
		if len(wire.Probabilities) != q.Levels || len(wire.Legend) != q.Levels {
			return Answer{}, fmt.Errorf("TypeSafe score distribution/legend incomplete")
		}
		for i := range q.Levels {
			key := strconv.Itoa(i)
			p, ok := wire.Probabilities[key]
			_, hasLegend := wire.Legend[key]
			if !ok || !probability(p) || !hasLegend {
				return Answer{}, fmt.Errorf("TypeSafe score requires zero-based levels")
			}
		}
		a.Value, a.Legend = *wire.Score, wire.Legend
	}
	if q.Type != "noul" {
		d, err := ValidateDistribution(a.Probabilities)
		if err != nil {
			return Answer{}, err
		}
		if d != "" {
			a.Deviations = []string{d}
		}
	}
	return a, nil
}
