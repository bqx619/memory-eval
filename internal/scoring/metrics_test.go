package scoring

import (
	"encoding/json"
	"math"
	"memeval/internal/result"
	"testing"
)

func closeTo(t *testing.T, actual *float64, want float64) {
	t.Helper()
	if actual == nil || math.Abs(*actual-want) > 1e-12 {
		t.Fatalf("got %v, want %.15g", actual, want)
	}
}
func TestRetrievalPythonFixtures(t *testing.T) {
	m, _ := Retrieval([]string{"b", "x"}, []string{"a", "b", "c"}, false, 10)
	closeTo(t, m.Values["recall"], 1./3)
	closeTo(t, m.Values["hit"], 1)
	closeTo(t, m.Values["all_evidence"], 0)
	if m.Values["precision"] != nil {
		t.Fatal("non-exhaustive precision")
	}
	m, _ = Retrieval([]string{"a", "a", "b"}, []string{"a", "b"}, true, 10)
	closeTo(t, m.Values["recall"], 1)
	closeTo(t, m.Values["precision"], 1)
	m, _ = Retrieval([]string{"z", "a"}, []string{"a"}, false, 10)
	closeTo(t, m.Values["mrr"], .5)
	m, _ = Retrieval(nil, nil, true, 10)
	if m.Values["recall"] != nil || m.Values["precision"] != nil || !*m.EmptyExpected {
		t.Fatal("empty gold semantics")
	}
	closeTo(t, m.Values["empty_result_correct"], 1)
	m, _ = Retrieval([]string{"z"}, nil, true, 10)
	closeTo(t, m.Values["empty_result_correct"], 0)
	closeTo(t, m.Values["precision"], 0)
	m, _ = Retrieval([]string{"a"}, []string{"a"}, true, 0)
	closeTo(t, m.Values["recall"], 0)
	closeTo(t, m.Values["ndcg"], 0)
	if _, err := Retrieval(nil, nil, false, -1); err == nil {
		t.Fatal("negative k accepted")
	}
}
func TestTypedPythonFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, expect string
		response     TypedResponse
		passed       *bool
		assertions   int
	}{
		{"vacuous", `{"pinnedFacts":{"mustInclude":[]}}`, TypedResponse{}, nil, 0},
		{"empty exact", `{"relevantBeliefs":{"shouldOnlyInclude":[]}}`, TypedResponse{}, Bool(true), 1},
		{"include union", `{"relevantBeliefs":{"mustInclude":["a"]}}`, TypedResponse{PinnedFacts: []string{"a"}}, Bool(true), 1},
		{"exact relevant only", `{"relevantBeliefs":{"shouldOnlyInclude":["a"]}}`, TypedResponse{PinnedFacts: []string{"a"}}, Bool(false), 1},
		{"exclude union", `{"relevantBeliefs":{"mustExclude":["a"]}}`, TypedResponse{PinnedFacts: []string{"a"}}, Bool(false), 1},
		{"case sensitive", `{"personaPrelude":{"contains":["direct"]}}`, TypedResponse{PersonaPrelude: "Direct"}, Bool(false), 1},
		{"all typed rules", `{"relevantBeliefs":{"shouldInclude":["b"],"maxCount":2,"minCount":2,"orderedBefore":[["a","b"]]},"pinnedFacts":{"mustInclude":["p"],"mustExclude":["x"]},"openQuestions":{"mustInclude":["q"],"mustExclude":["x"]},"personaPrelude":{"nonEmpty":true,"contains":["text"],"mustNotContain":["bad"]}}`, TypedResponse{PinnedFacts: []string{"p"}, RelevantBeliefs: []string{"a", "b"}, OpenQuestions: []string{"q"}, PersonaPrelude: "text"}, Bool(true), 11},
		{"null prelude", `{"personaPrelude":{"isNull":true}}`, TypedResponse{PersonaPrelude: "x"}, Bool(false), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := ScoreTyped(json.RawMessage(tc.expect), tc.response, 10)
			if err != nil {
				t.Fatal(err)
			}
			if (o.Passed == nil) != (tc.passed == nil) || o.Passed != nil && *o.Passed != *tc.passed || o.Assertions != tc.assertions {
				t.Fatalf("unexpected outcome %+v", o)
			}
			if tc.passed == nil && o.Status != result.Unscored {
				t.Fatal("vacuous scored")
			}
		})
	}
}
func TestLexicalAndMCQ(t *testing.T) {
	m := LexicalAnswer("The blue box.", []string{"blue box"})
	closeTo(t, m["exact_match_diagnostic"], 1)
	closeTo(t, m["token_f1_diagnostic"], 1)
	if LexicalAnswer("blue", nil)["token_f1_diagnostic"] != nil {
		t.Fatal("missing references scored")
	}
	closeTo(t, LexicalAnswer("blue", []string{"blue box"})["token_f1_diagnostic"], 2./3)
	closeTo(t, LexicalAnswer("", []string{""})["token_f1_diagnostic"], 1)
	if NormalizeAnswer("the中文") != "the中文" {
		t.Fatal("Unicode word boundary")
	}
	options := map[string]string{"A": "blue", "B": "green"}
	ok, _, err := MCQ("A", options, "A")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, _, err := MCQ("I think A", options, "A"); err == nil {
		t.Fatal("substring choice accepted")
	}
}
func TestPercentilePythonFixtures(t *testing.T) {
	if Percentile(nil, .5) != nil {
		t.Fatal("empty percentile")
	}
	for _, tc := range []struct {
		xs   []float64
		q, w float64
	}{{[]float64{1}, .95, 1}, {[]float64{0, 10}, .5, 5}, {[]float64{0, 10}, .95, 9.5}} {
		closeTo(t, Percentile(tc.xs, tc.q), tc.w)
	}
}
func TestAggregateCoverage(t *testing.T) {
	one := 1
	rows := []result.Case{{Status: result.Scored, Passed: Bool(true), Metrics: map[string]*float64{"recall": Float(1), "precision": nil}, ReturnedChars: &one}, {Status: result.Unscored, Metrics: map[string]*float64{"recall": Float(0)}}, {Status: result.ContractError}, {Status: result.IngestFailed}, {Status: result.Unsupported}}
	s := Aggregate(rows)
	if s.Planned != 5 || s.Passed != 1 || s.NWithPassJudgment != 1 || s.ExecutionErrors != 2 || s.Unsupported != 1 || s.Metrics["recall"].N != 1 || s.Metrics["precision"].N != 0 {
		t.Fatalf("%+v", s)
	}
	closeTo(t, s.ScoringCoverage, .2)
}
