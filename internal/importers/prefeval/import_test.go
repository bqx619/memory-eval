package prefeval

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var update = flag.Bool("update", false, "update reviewed synthetic PrefEval conformance goldens")

func TestConformance(t *testing.T) {
	for _, form := range Forms {
		for _, task := range Tasks {
			t.Run(form+"-"+task, func(t *testing.T) {
				conformance.Run(t, conformance.Spec{Name: form + "-" + task, Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
					r, e := ImportData(ctx, Options{Input: "testdata/input", Form: form, Task: task, InterTurns: 1, Out: filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
					if e != nil {
						return nil, importers.Stats{}, e
					}
					s, e := suite.Open(ctx, r.Path)
					return s, r.Statistics, e
				}, GoldenSuite: "testdata/" + form + "-" + task + ".suite.golden.json", GoldenStats: "testdata/" + form + "-" + task + ".stats.golden.json", Update: *update})
			})
		}
	}
}
func TestMessagesAndMCQ(t *testing.T) {
	row := Row{Conversation: json.RawMessage(`"{'10': {'user': 'late', 'assistant': 'L'}, '2': {'user': 'early', 'assistant': 'E'}}"`)}
	ms, lit, e := PreferenceMessages("implicit-persona", row)
	if e != nil || !lit || len(ms) != 4 || ms[0].Content != "early" || ms[2].Content != "late" {
		t.Fatal(ms, lit, e)
	}
	r, e := ImportData(t.Context(), Options{Input: "testdata/input", Form: "explicit", Task: "mcq", InterTurns: 0, Out: filepath.Join(t.TempDir(), "bundle")}, "synthetic", true)
	if e != nil {
		t.Fatal(e)
	}
	s, e := suite.Open(t.Context(), r.Path)
	if e != nil {
		t.Fatal(e)
	}
	e = s.Iter(t.Context(), func(ep *suite.Episode) error {
		c := ep.Cases[0]
		if len(ep.Events) != 1 || c.Options[c.Gold.Choice] != "preferred" || *c.Gold.MCQ.Seed != 41 || len(c.Gold.MCQ.Order) != 4 {
			t.Fatal(c)
		}
		for _, ev := range ep.Events {
			if len(ev.Attributes) != 0 || ev.Timestamp != "" || ev.Session != "" {
				t.Fatal(ev)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	for _, n := range []int{-1, 3} {
		if _, e = ImportData(t.Context(), Options{Input: "testdata/input", Form: "explicit", Task: "mcq", InterTurns: n, Out: filepath.Join(t.TempDir(), "bad")}, "synthetic", true); e == nil {
			t.Fatal("invalid filler count accepted")
		}
	}
}
func TestNativeAndLiteralParity(t *testing.T) {
	native := json.RawMessage(`{"query":"q","assistant_options":"a","user_selection":"u","assistant_acknowledgment":"ok"}`)
	literal := json.RawMessage(`"{'query':'q','assistant_options':'a','user_selection':'u','assistant_acknowledgment':'ok'}"`)
	a, _, e := PreferenceMessages("implicit-choice", Row{Conversation: native})
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := PreferenceMessages("implicit-choice", Row{Conversation: literal})
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal(a, b, e)
	}
}
func TestRealImportStatistics(t *testing.T) {
	input := os.Getenv("MEMEVAL_PREFEVAL_INPUT")
	if input == "" {
		t.Skip("MEMEVAL_PREFEVAL_INPUT gates CC BY-NC real input")
	}
	absolute, err := filepath.Abs(input)
	if err != nil {
		t.Fatal(err)
	}
	input = testoracle.Dataset(t, absolute)
	h, e := Verify(t.Context(), input)
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range []struct {
		form, task string
		n, events  int
	}{{"explicit", "generation", 10, 21000}, {"explicit", "generation", 300, 601000}, {"explicit", "mcq", 10, 21000}, {"implicit-choice", "generation", 10, 24000}, {"implicit-persona", "generation", 10, 30620}} {
		r, e := ImportData(t.Context(), Options{Input: input, Form: x.form, Task: x.task, InterTurns: x.n, Out: filepath.Join(t.TempDir(), "bundle")}, h, false)
		if e != nil {
			t.Fatal(e)
		}
		d := r.Statistics.Diagnostics
		if r.Statistics.Questions != 1000 || r.Statistics.Events != x.events || d["topics"] != 20 || d["filler_messages_available"] != 632 || d["filler_conversations"] != 24 || d["literal_parse_failures"] != 0 {
			t.Fatal(r.Statistics)
		}
		if x.task == "mcq" && d["mcq_preference_differences_from_explicit"] != 30 {
			t.Fatal(d)
		}
		s, e := suite.Open(t.Context(), r.Path)
		if e != nil {
			t.Fatal(e)
		}
		leak, e := suite.LeakScan(s)
		if e != nil || len(leak.Problems) != 0 {
			t.Fatal(leak, e)
		}
		t.Logf("%s/%s/t%d: tasks=%d topics=20 events=%d filler=%d/632, validate/leak PASS", x.form, x.task, x.n, r.Statistics.Questions, r.Statistics.Events, x.n*2)
	}
}
