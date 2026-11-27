package decision

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProbabilityRoundingValidation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		p              map[string]float64
		valid, rounded bool
	}{
		{"exact", map[string]float64{"0": .1, "1": .5, "2": .4}, true, false},
		{"one-hot", map[string]float64{"0": 0, "1": 1, "2": 0}, true, false},
		{"three-decimals", map[string]float64{"0": .333, "1": .333, "2": .333}, true, true},
		{"precision-tightens-bound", map[string]float64{"0": .111, "1": .56, "2": .32}, false, false},
		{"all-zero", map[string]float64{"0": 0, "1": 0, "2": 0}, false, false},
		{"missing-level", map[string]float64{"0": .5, "1": .5}, false, false},
		{"wrong-level", map[string]float64{"0": .5, "1": .5, "3": 0}, false, false},
		{"excess-level", map[string]float64{"0": .2, "1": .4, "2": .4, "3": 0}, false, false},
		{"out-of-range", map[string]float64{"0": -.01, "1": 0, "2": 1.01}, false, false},
		{"underfull", map[string]float64{"0": .02, "1": .04, "2": .9}, false, false},
		{"overfull", map[string]float64{"0": .3, "1": .3, "2": .5}, false, false},
		{"coarse-decimals", map[string]float64{"0": .3, "1": .3, "2": .3}, false, false},
	} {
		for _, kind := range []string{"score", "choice"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "rounding-fixture")
				q := questions()[2]
				if kind == "choice" {
					q = Question{Name: "quality", Type: "choice", Instructions: "Choose.", Options: []string{"0", "1", "2"}, Criteria: map[string]string{"0": "low", "1": "medium", "2": "high"}}
				}
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					a := map[string]any{"type": kind, "score": 1, "choice": "1", "probabilities": tc.p, "confidence": .8, "legend": map[string]string{"0": "low", "1": "medium", "2": "high"}}
					json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}, "answers": map[string]any{"quality": a}})
				}))
				defer s.Close()
				c := client(t, config(t, s.URL))
				out, e := c.Decide(t.Context(), DecisionRequest{Provider: "typesafe", Purpose: "judge-decision", Model: "jev-1.14.0", SampleID: "s", TemplateID: "v2", State: "{}", Questions: []Question{q}})
				if tc.valid {
					if e != nil {
						t.Fatal(e)
					}
					a := out.Answers["quality"]
					if !reflect.DeepEqual(tc.p, a.Probabilities) || (len(a.Deviations) > 0) != tc.rounded {
						t.Fatal(a)
					}
				} else if e == nil || e.Error() != "DecisionResponseInvalid" {
					t.Fatal("invalid distribution accepted", out, e)
				}
			})
		}
	}
}
