package runner_test

import (
	"encoding/json"
	"memeval/internal/importers/locomo"
	"memeval/internal/result"
	"memeval/internal/testoracle"
	"strings"
	"testing"
)

func TestLoCoMoSuffixesRemainEvaluatorOnly(t *testing.T) {
	testoracle.ProtocolSources(t, "locomo")
	s, _, err := locomo.Import(t.Context(), "../importers/locomo/testdata/synthetic_locomo.json")
	if err != nil {
		t.Fatal(err)
	}
	a := newSpy()
	client := &fakeLLM{answer: "(a)"}
	r := run(t, s, a, fixedOpts(client))
	wire, _ := json.Marshal([]any{a.inputs, a.request})
	for _, secret := range []string{"PRIVATE SYNTHETIC SENTINEL", "adversarial_answer", "Select the correct answer", "Use DATE of CONVERSATION"} {
		if strings.Contains(string(wire), secret) {
			t.Fatal("reader-only input leaked", secret)
		}
	}
	for i, row := range r.Cases {
		if row.QA.QuestionType != "5" {
			continue
		}
		if !strings.Contains(client.requests[i].Messages[0].Content, "PRIVATE SYNTHETIC SENTINEL") || row.QA.LoCoMo == nil || row.QA.LoCoMo.MappedOption != "a" || *row.QA.Hypothesis != row.QA.LoCoMo.Options["a"] {
			t.Fatal("reader/mapping missing", row)
		}
		want := 0.
		if row.QA.LoCoMo.OptionOrder[0] == "not_mentioned" {
			want = 1
		}
		if *row.Metrics["locomo_official.abstain_acc"] != want {
			t.Fatal("mapped answer not scored", row)
		}
	}
	// Old suites remain usable, with an explicit partial-fidelity deviation.
	for ei := range s.Episodes {
		for ci := range s.Episodes[ei].Cases {
			s.Episodes[ei].Cases[ci].Attributes = nil
		}
	}
	legacy := run(t, s, newSpy(), fixedOpts(&fakeLLM{answer: "(a)"}))
	for _, row := range legacy.Cases {
		if row.QA.QuestionType == "5" && (row.QA.LoCoMo.SuffixApplied || len(row.QA.Deviations) == 0 || *row.QA.Hypothesis != "(a)") {
			t.Fatal(row)
		}
	}
	official := legacy.Scores.Official.(result.Official)
	if official.Fidelity != "partial" || !strings.Contains(strings.Join(official.Deviations, " "), "suffix missing") {
		t.Fatal(official)
	}
}
