package runner

import (
	lr "memeval/internal/reader/locomo"
	"memeval/internal/result"
	"memeval/internal/suite"
	"strconv"
	"testing"
)

func TestCompleteLoCoMoWithoutSuffixIsPartial(t *testing.T) {
	o := DefaultOptions()
	o.Repeats = 1
	o.benchmarkID = "locomo"
	o.FixedReader = &FixedReaderOptions{}
	r := &result.Report{}
	hypothesis := "Not mentioned in the conversation"
	for i := 0; i < 1986; i++ {
		r.Cases = append(r.Cases, result.Case{CaseID: strconv.Itoa(i), Status: result.Scored, QA: &result.QA{QuestionType: "5", Hypothesis: &hypothesis}})
	}
	if err := deterministicMetadata(r, &suite.Suite{}, o); err != nil {
		t.Fatal(err)
	}
	if off := r.Scores.Official.(result.Official); !off.QuestionSet.Complete || off.Fidelity != "partial" {
		t.Fatal(off)
	}
	for i := range r.Cases {
		r.Cases[i].QA.LoCoMo = &lr.QuestionAudit{Protocol: lr.QuestionProtocol, SuffixApplied: true}
	}
	if err := deterministicMetadata(r, &suite.Suite{}, o); err != nil {
		t.Fatal(err)
	}
	if r.Scores.Official.(result.Official).Fidelity != "full" {
		t.Fatal("suffix gate never opens")
	}
}
