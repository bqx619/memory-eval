package decision

import (
	"encoding/json"
	"math"
	"testing"
)

func TestTypeSafeCriteriaShapes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		index    int
		criteria any
		valid    bool
	}{
		{"noul absent", 0, nil, true},
		{"noul unexpected", 0, "opaque", false},
		{"choice dictionary", 1, map[string]string{"keep": "retain", "drop": "discard"}, true},
		{"choice JSON dictionary", 1, map[string]any{"keep": "retain", "drop": "discard"}, true},
		{"choice list", 1, []string{"retain", "discard"}, false},
		{"choice missing key", 1, map[string]string{"keep": "retain"}, false},
		{"choice wrong key", 1, map[string]string{"keep": "retain", "other": "discard"}, false},
		{"choice null description", 1, map[string]any{"keep": "retain", "drop": nil}, false},
		{"score list", 2, []string{"low", "medium", "high"}, true},
		{"score JSON list", 2, []any{"low", "medium", "high"}, true},
		{"score integer", 2, 3, false},
		{"score dictionary", 2, map[string]string{"0": "low", "1": "medium", "2": "high"}, false},
		{"score length", 2, []string{"low", "high"}, false},
		{"score null description", 2, []any{"low", nil, "high"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := DecisionRequest{Purpose: "decision", Provider: "typesafe", Model: "jev-latest", SampleID: "fixture", TemplateID: "fixture", Questions: questions()}
			r.Questions[tc.index].Criteria = tc.criteria
			_, err := Normalize(r)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestTypeSafeObservedAnswersAndCalibration(t *testing.T) {
	q := Question{Type: "noul"}
	for _, p := range []float64{0, .01, .5, .8, .99, 1} {
		b, _ := json.Marshal(map[string]any{"type": "noul", "noul": p})
		a, err := decodeTypeSafeAnswer(q, b)
		if err != nil || a.Noul == nil || *a.Noul != p || a.Confidence != nil || a.Probabilities != nil || MetricValue(a) != p {
			t.Fatal(a, err)
		}
		conf, ok := CalibratedConfidence(q, a)
		if !ok || math.Abs(conf-math.Abs(2*p-1)) > 1e-12 {
			t.Fatal(conf)
		}
	}
	for _, raw := range []string{`{"type":"noul","noul":true}`, `{"type":"noul","noul":null}`, `{"type":"noul"}`, `{"type":"noul","noul":1.1}`, `{"type":"noul","noul":-0.1}`} {
		if _, err := decodeTypeSafeAnswer(q, []byte(raw)); err == nil {
			t.Fatal("bad noul accepted", raw)
		}
	}
	choice := questions()[1]
	a, err := decodeTypeSafeAnswer(choice, []byte(`{"type":"choice","choice":"keep","probabilities":{"keep":0.7,"drop":0.3},"confidence":0.4}`))
	conf, ok := CalibratedConfidence(choice, a)
	if err != nil || !ok || math.Abs(conf-.4) > 1e-12 || a.Confidence == nil || *a.Confidence != .4 || MetricValue(a) != .7 {
		t.Fatal(a, err)
	}
	score := Question{Type: "score", Levels: 5}
	raw := []byte(`{"type":"score","score":3.9,"legend":{"0":"none","1":"poor","2":"partial","3":"good","4":"full"},"probabilities":{"0":0,"1":0,"2":0.02,"3":0.06,"4":0.92},"confidence":0.93}`)
	a, err = decodeTypeSafeAnswer(score, raw)
	if err != nil || a.Value != 3.9 || MetricValue(a) != 3.9 || len(a.Legend) != 5 || *a.Confidence != .93 {
		t.Fatal(a, err)
	}
	if _, ok := CalibratedConfidence(score, a); ok {
		t.Fatal("invented score confidence formula")
	}
	for _, mutation := range []func(map[string]any){
		func(m map[string]any) { m["score"] = -1 },
		func(m map[string]any) { m["score"] = 5 },
		func(m map[string]any) { m["score"] = nil },
		func(m map[string]any) { delete(m, "confidence") },
		func(m map[string]any) { delete(m, "legend") },
		func(m map[string]any) { p := m["probabilities"].(map[string]any); p["5"] = p["0"]; delete(p, "0") },
		func(m map[string]any) { m["probabilities"].(map[string]any)["0"] = .5 },
	} {
		var m map[string]any
		json.Unmarshal(raw, &m)
		mutation(m)
		invalid, _ := json.Marshal(m)
		if _, err := decodeTypeSafeAnswer(score, invalid); err == nil {
			t.Fatal("invalid score accepted", string(invalid))
		}
	}
}
