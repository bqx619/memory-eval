package personamem

import (
	"bytes"
	"context"
	"encoding/csv"
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

var update = flag.Bool("update", false, "update reviewed goldens")

func TestConformance(t *testing.T) {
	for _, mode := range []string{"include", "exclude"} {
		t.Run(mode, func(t *testing.T) {
			conformance.Run(t, conformance.Spec{Name: "personamem " + mode, Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
				return Import(ctx, "testdata/questions.csv", "testdata/contexts.jsonl", "32k", mode)
			}, GoldenSuite: "testdata/golden-" + mode + ".json", GoldenStats: "testdata/golden-" + mode + ".import.json", Update: *update})
		})
	}
}
func TestExcludeCutoff(t *testing.T) {
	a, _, e := Import(t.Context(), "testdata/questions.csv", "testdata/contexts.jsonl", "32k", "include")
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := Import(t.Context(), "testdata/questions.csv", "testdata/contexts.jsonl", "32k", "exclude")
	if e != nil {
		t.Fatal(e)
	}
	for i, c := range a.Episodes[0].Cases {
		n := 0
		for _, ev := range a.Episodes[0].Events[:c.Cutoff] {
			if ev.Role != "system" {
				n++
			}
		}
		if b.Episodes[0].Cases[i].Cutoff != n {
			t.Fatal("exclusive cutoff mismatch")
		}
	}
	for _, ev := range b.Episodes[0].Events {
		if ev.Role == "system" {
			t.Fatal("system retained")
		}
	}
}
func TestLiteral(t *testing.T) {
	for _, s := range []string{`['a', "b", 'it\'s', 'line\nnext']`, `["a","b","c","d"]`, `['a', 'b', 'c', 'd',]`} {
		if v, e := ParseOptions(s); e != nil || len(v) != 4 {
			t.Fatal(v, e)
		}
	}
	for _, s := range []string{`[1]`, `['x', None]`, `[f'x']`, `['x'] junk`, `['unterminated]`, `null`, `[null]`} {
		if _, e := ParseOptions(s); e == nil {
			t.Fatal("accepted", s)
		}
	}
}
func TestOptionsPythonOracle(t *testing.T) {
	testoracle.Python(t)
	f, e := os.Open(testoracle.Dataset(t, "upstream/personamem-v1/questions_32k.csv"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	rows, e := csv.NewReader(f).ReadAll()
	if e != nil {
		t.Fatal(e)
	}
	col := 0
	for i, k := range rows[0] {
		if k == "all_options" {
			col = i
		}
	}
	input := []string{}
	for _, r := range rows[1:] {
		input = append(input, r[col])
	}
	var want [][]string
	testoracle.Run(t, `import ast,json,sys; print(json.dumps([ast.literal_eval(s) for s in json.load(sys.stdin)]))`, input, &want)
	for i, s := range input {
		g, e := ParseOptions(s)
		if e != nil || !reflect.DeepEqual(g, want[i]) {
			t.Fatalf("row %d: %v", i, e)
		}
	}
	t.Logf("ast.literal_eval oracle: %d rows exact", len(input))
}
func TestRealStatistics(t *testing.T) {
	q := testoracle.Dataset(t, "upstream/personamem-v1/questions_32k.csv")
	h := testoracle.Dataset(t, "upstream/personamem-v1/shared_contexts_32k.jsonl")
	for _, mode := range []string{"include", "exclude"} {
		_, st, e := Import(t.Context(), q, h, "32k", mode)
		if e != nil {
			t.Fatal(e)
		}
		counts := st.Diagnostics["counts"].(map[string]int)
		for k, want := range map[string]int{"contexts": 37, "personas": 20, "messages": 6462, "system_messages": 195, "context_messages_min": 116, "context_messages_max": 238, "contexts_starting_system": 37, "end_index_min": 96, "end_index_max": 237, "question_equals_at_end": 0, "question_equals_at_end_without_user_prefix": 139, "question_equals_before_end": 64} {
			if counts[k] != want {
				t.Errorf("%s %s=%d != %d", mode, k, counts[k], want)
			}
		}
		for k, want := range map[string]int{"recall_user_shared_facts": 129, "track_full_preference_evolution": 139, "recalling_the_reasons_behind_previous_updates": 99, "suggest_new_ideas": 93, "generalizing_to_new_scenarios": 57, "provide_preference_aligned_recommendations": 55, "recalling_facts_mentioned_by_the_user": 17} {
			if st.ByQuestionType[k] != want {
				t.Fatal(st.ByQuestionType)
			}
		}
		if !reflect.DeepEqual(st.Diagnostics["correct_answers"], map[string]int{"a": 143, "b": 146, "c": 158, "d": 142}) || !reflect.DeepEqual(st.Diagnostics["contexts_per_persona_distribution"], map[string]int{"1": 3, "2": 17}) {
			t.Fatal(st.Diagnostics)
		}
		expected := 6462
		if mode == "exclude" {
			expected -= 195
		}
		if st.Events != expected || st.Questions != 589 {
			t.Fatal(st)
		}
	}
}
func TestRejectSharedPersonaAndDuplicateQuestion(t *testing.T) {
	data, e := os.ReadFile("testdata/questions.csv")
	if e != nil {
		t.Fatal(e)
	}
	rows, e := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"persona_id", "question_id"} {
		clone := make([][]string, len(rows))
		for i := range rows {
			clone[i] = append([]string{}, rows[i]...)
		}
		col := 0
		for i, k := range rows[0] {
			if k == field {
				col = i
			}
		}
		if field == "persona_id" {
			clone[2][col] = "other-persona"
		} else {
			clone[2][col] = clone[1][col]
		}
		var b bytes.Buffer
		w := csv.NewWriter(&b)
		w.WriteAll(clone)
		p := filepath.Join(t.TempDir(), "questions.csv")
		os.WriteFile(p, b.Bytes(), 0600)
		if _, _, e := Import(t.Context(), p, "testdata/contexts.jsonl", "32k", "include"); e == nil {
			t.Fatal("accepted", field)
		}
	}
}
