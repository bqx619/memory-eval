package result

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyAndStructuredRepeats(t *testing.T) {
	for _, wire := range []string{`{"repeats":3}`, `{"repeats":{"count":3,"flip_rate":0,"eligible_cases":2,"flipped_cases":0,"excluded_cases":[],"by_metric":{}}}`} {
		var r Report
		if err := json.Unmarshal([]byte(wire), &r); err != nil || r.Repeats != 3 {
			t.Fatal(r, err)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(wire, "flip_rate") && !strings.Contains(string(b), `"flip_rate":0`) {
			t.Fatal(string(b))
		}
	}
	var r Report
	if json.Unmarshal([]byte(`{"repeats":{"count":0}}`), &r) == nil {
		t.Fatal("invalid count")
	}
}
