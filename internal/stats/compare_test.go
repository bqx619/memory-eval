package stats

import (
	"context"
	"math"
	"memeval/internal/result"
	"reflect"
	"strings"
	"testing"
)

func pair() (*result.Report, *result.Report) {
	a := &result.Report{SuiteSHA256: "same", Mode: "retrieval", Repeats: 1, SUT: result.SUT{Name: "left"}}
	b := &result.Report{SuiteSHA256: "same", Mode: "retrieval", Repeats: 1, SUT: result.SUT{Name: "right"}}
	for i, id := range []string{"a1", "a2", "b1"} {
		cluster := "a"
		if i == 2 {
			cluster = "b"
		}
		zero, one := 0., 1.
		if i == 2 {
			one = 0
		}
		x := result.Case{CaseID: id, SampleID: id + "#0", ClusterID: cluster, Status: result.Scored, Metrics: map[string]*float64{"recall": &zero}}
		y := x
		y.Metrics = map[string]*float64{"recall": &one}
		a.Cases = append(a.Cases, x)
		b.Cases = append(b.Cases, y)
	}
	return a, b
}
func TestFixedIndicesCI(t *testing.T) {
	a, b := pair()
	o := DefaultOptions()
	o.Metric = "recall"
	o.Indices = [][]int{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	c, err := Compare(context.Background(), a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(c.Delta-2./3) > 1e-12 || math.Abs(c.CI95[0]-.05) > 1e-12 || math.Abs(c.CI95[1]-.975) > 1e-12 || c.Clusters != 2 {
		t.Fatal(c)
	}
}
func TestSingleClusterAndDefaultDeterminism(t *testing.T) {
	a, b := pair()
	o := DefaultOptions()
	o.Metric = "recall"
	x, err := Compare(context.Background(), a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	y, _ := Compare(context.Background(), a, b, o)
	if !reflect.DeepEqual(x.ResampleIndices, y.ResampleIndices) || x.Draws != 2000 {
		t.Fatal("default bootstrap nondeterministic")
	}
	for i := range a.Cases {
		a.Cases[i].ClusterID = "shared"
		b.Cases[i].ClusterID = "shared"
	}
	c, err := Compare(context.Background(), a, b, o)
	if err != nil || c.CI95 != nil || c.Note == "" {
		t.Fatal(c, err)
	}
}
func TestRepeatsDoNotIncreaseClusters(t *testing.T) {
	a, b := pair()
	for _, r := range []*result.Report{a, b} {
		r.Repeats = 2
		for _, row := range append([]result.Case{}, r.Cases...) {
			row.RepeatIndex = 1
			row.SampleID = row.CaseID + "#1"
			r.Cases = append(r.Cases, row)
		}
	}
	o := DefaultOptions()
	o.Metric = "recall"
	c, err := Compare(context.Background(), a, b, o)
	if err != nil || c.Clusters != 2 || c.PairedCases != 3 || c.PairedSamples != 6 {
		t.Fatal(c, err)
	}
}
func TestMismatchRefusal(t *testing.T) {
	for name, mutate := range map[string]func(*result.Report){"suite": func(r *result.Report) { r.SuiteSHA256 = "other" }, "mode": func(r *result.Report) { r.Mode = "other" }, "budget": func(r *result.Report) { r.Protocol.Budget.TopK = 99 }, "batch": func(r *result.Report) { r.Protocol.BatchSize = 99 }, "scoring": func(r *result.Report) { r.Protocol.Scoring = "other" }, "reader": func(r *result.Report) { r.Reader = "other" }, "judge": func(r *result.Report) { r.Protocol.Judge = &result.JudgeInfo{Model: "different"} }, "reader input": func(r *result.Report) { r.Protocol.ReaderInput = "items" }, "cases": func(r *result.Report) { r.Cases = r.Cases[:1] }, "status": func(r *result.Report) { r.Cases[0].Status = result.Error }, "cluster": func(r *result.Report) { r.Cases[0].ClusterID = "other" }, "metric coverage": func(r *result.Report) { r.Cases[0].Metrics["recall"] = nil }} {
		t.Run(name, func(t *testing.T) {
			a, b := pair()
			mutate(b)
			o := DefaultOptions()
			o.Metric = "recall"
			if _, err := Compare(context.Background(), a, b, o); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}
func TestInvalidIndices(t *testing.T) {
	for _, s := range []string{"", "null\n", "[true,0]\n", "[0.5,1]\n"} {
		if _, err := ReadIndices(context.Background(), strings.NewReader(s)); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	a, b := pair()
	for _, indices := range [][][]int{{{0}}, {{0, 2}}, {{-1, 0}}, {}} {
		o := DefaultOptions()
		o.Metric = "recall"
		o.Indices = indices
		if _, err := Compare(context.Background(), a, b, o); err == nil {
			t.Fatal("invalid indices accepted")
		}
	}
}

func TestEpisodeBasisAndMetricPolicy(t *testing.T) {
	a, b := pair()
	a.Source = map[string]any{"cluster_basis": "episode"}
	b.Source = map[string]any{"cluster_basis": "episode"}
	a.Protocol.RetrievalMetrics = []string{"longmemeval_official"}
	b.Protocol.RetrievalMetrics = []string{"longmemeval_official"}
	a.Protocol.QueryTimePolicy = "reference_only"
	b.Protocol.QueryTimePolicy = "reference_only"
	o := DefaultOptions()
	o.Metric = "recall"
	c, err := Compare(context.Background(), a, b, o)
	if err != nil || !strings.Contains(c.Note, "cluster_basis=episode") || !strings.Contains(c.Note, "share a source pool") || len(c.CI95) != 2 {
		t.Fatal(c, err)
	}
	b.Protocol.QueryTimePolicy = ""
	if _, err = Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("query time policy mismatch accepted")
	}
}

func TestLegacyLLMKindAndDecisionProtocolSeparation(t *testing.T) {
	a, b := pair()
	a.Protocol.Judge = &result.JudgeInfo{Provider: "openai-compat", Model: "gpt-4o-2024-08-06"}
	copy := *a.Protocol.Judge
	copy.Kind = "llm"
	b.Protocol.Judge = &copy
	o := DefaultOptions()
	o.Metric = "recall"
	if _, err := Compare(context.Background(), a, b, o); err != nil {
		t.Fatal("legacy llm report refused", err)
	}
	copy.Kind = "gated"
	if _, err := Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("different judge routing accepted")
	}
	copy.Kind = "llm"
	copy.Model = "another-model"
	if _, err := Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("different judge model accepted")
	}
}
