package report

import (
	"context"
	"encoding/json"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestComparisonExclusionsRoundTripToHTML(t *testing.T) {
	c := &result.Comparison{Left: "left", Right: "right", Metric: "mcq.correct", PairedCases: 2, PairedSamples: 2, Clusters: 2,
		ExcludedCases:       []string{"x", "y", "z"},
		ExcludedByStatus:    &result.ExcludedByStatus{Left: map[result.Status]int{result.Invalid: 1}, Right: map[result.Status]int{result.Unscored: 2}, Both: map[result.Status]int{result.Invalid: 3}},
		ProtocolDifferences: []string{"reader.effective_max_tokens (disclosure): <absent> -> 128"}}
	dir := t.TempDir()
	path := filepath.Join(dir, "delta.json")
	if err := SaveComparison(context.Background(), c, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadComparison(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.ExcludedByStatus, c.ExcludedByStatus) {
		t.Fatal("counts changed")
	}
	dest := filepath.Join(dir, "comparison.html")
	if err = HTMLWithComparison(context.Background(), []*result.Report{{SUT: result.SUT{Name: "left"}}, {SUT: result.SUT{Name: "right"}}}, loaded, dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), `<script type="application/json" id="memeval-data">`)
	if !ok {
		t.Fatal("missing JSON node")
	}
	blob, _, _ := strings.Cut(rest, "</script>")
	var decoded pageData
	if err = json.Unmarshal([]byte(blob), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Compare.ExcludedByStatus, c.ExcludedByStatus) || !reflect.DeepEqual(decoded.Compare.ProtocolDifferences, c.ProtocolDifferences) {
		t.Fatal("HTML lost exclusion counts or reader disclosure")
	}
}
