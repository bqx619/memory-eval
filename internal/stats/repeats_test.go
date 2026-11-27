package stats

import (
	"context"
	"math"
	"memeval/internal/result"
	"strings"
	"testing"
)

func repeatedPair() (*result.Report, *result.Report) {
	a, b := pair()
	for _, r := range []*result.Report{a, b} {
		r.Repeats = 3
		original := append([]result.Case{}, r.Cases...)
		for n := 1; n < 3; n++ {
			for _, c := range original {
				c.RepeatIndex = n
				c.Metrics = map[string]*float64{"recall": c.Metrics["recall"]}
				r.Cases = append(r.Cases, c)
			}
		}
	}
	return a, b
}
func TestSamplePairDifferentTopKAndFlipCI(t *testing.T) {
	a, b := repeatedPair()
	a.Protocol.Budget.TopK = 10
	b.Protocol.Budget.TopK = 50
	for i := range a.Cases {
		a.Cases[i].Budget.TopK = 10
		b.Cases[i].Budget.TopK = 50
	}
	zero := 0.
	b.Cases[3].Metrics["recall"] = &zero
	o := DefaultOptions()
	o.Metric = "recall"
	o.Indices = [][]int{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	if _, err := Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("implicit budget mismatch accepted")
	}
	o.PairBy = "sample"
	r, err := Compare(context.Background(), a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.Delta-5./9) > 1e-12 || math.Abs(r.CI95[0]-1./24) > 1e-12 || math.Abs(r.CI95[1]-.8125) > 1e-12 || r.PairedSamples != 9 || r.PairedCases != 3 || r.Clusters != 2 {
		t.Fatal(r)
	}
	if *r.LeftRepeats.FlipRate != 0 || *r.RightRepeats.FlipRate != 1./3 || len(r.ProtocolDifferences) != 1 || !strings.Contains(r.Note, "different top_k") {
		t.Fatal(r)
	}
	b.Protocol.Budget.MaxContextChars = 100
	if _, err := Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("unrelated budget mismatch accepted")
	}
}
func TestSamplePairIncompleteRepeats(t *testing.T) {
	a, b := repeatedPair()
	a.Cases = a.Cases[:8]
	b.Cases = b.Cases[:8]
	o := DefaultOptions()
	o.PairBy = "sample"
	o.Metric = "recall"
	if _, err := Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("missing repeat accepted")
	}
	f := Repeats(a.Cases, 3)
	if f.EligibleCases != 2 || len(f.ExcludedCases) != 1 {
		t.Fatal(f)
	}
	a.Cases[0].Status = result.Error
	f = Repeats(a.Cases, 3)
	if f.EligibleCases != 1 || len(f.ExcludedCases) != 2 {
		t.Fatal(f)
	}
	if Repeats(a.Cases, 1).FlipRate != nil {
		t.Fatal("one repeat pretends deterministic")
	}
}
func TestJudgeAndDecisionMetricAliases(t *testing.T) {
	a, b := repeatedPair()
	yes, no := true, false
	for i := range a.Cases {
		a.Cases[i].QA = &result.QA{JudgeLabel: &no}
		b.Cases[i].QA = &result.QA{JudgeLabel: &yes}
		v, w := .2, .8
		a.Cases[i].Metrics["decision.correct"] = &v
		b.Cases[i].Metrics["decision.correct"] = &w
	}
	o := DefaultOptions()
	o.PairBy = "sample"
	o.Metric = "judge.correct"
	c, err := Compare(context.Background(), a, b, o)
	if err != nil || c.Delta != 1 || *c.RightRepeats.ByMetric["judge.correct"].Mean != 0 {
		t.Fatal(c, err)
	}
	o.Metric = "decision.correct"
	c, err = Compare(context.Background(), a, b, o)
	if err != nil || math.Abs(c.Delta-.6) > 1e-12 {
		t.Fatal(c, err)
	}
}
func TestDecisionValueFlipAtEqualProbability(t *testing.T) {
	a, _ := repeatedPair()
	for i := range a.Cases {
		a.Cases[i].Decision = map[string]any{"answers": map[string]any{"choice": "a"}}
	}
	a.Cases[3].Decision = map[string]any{"answers": map[string]any{"choice": "b"}}
	r := Repeats(a.Cases, 3)
	if r.FlipRate == nil || *r.FlipRate != 1./3 {
		t.Fatal(r)
	}
}

func TestCaseLevelBudgetDifferenceIsDisclosed(t *testing.T) {
	a, b := repeatedPair()
	for i := range b.Cases {
		if b.Cases[i].CaseID == "a1" {
			b.Cases[i].Budget.TopK = 20
		}
	}
	o := DefaultOptions()
	o.Metric = "recall"
	o.PairBy = "sample"
	r, err := Compare(context.Background(), a, b, o)
	if err != nil || len(r.ProtocolDifferences) != 1 || r.ProtocolDifferences[0] != "case a1 budget.top_k: 0 -> 20" {
		t.Fatal(r, err)
	}
}
