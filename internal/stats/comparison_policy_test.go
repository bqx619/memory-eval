package stats

import (
	"context"
	"encoding/json"
	"math"
	"memeval/internal/result"
	"reflect"
	"strings"
	"testing"
)

func statusPolicyPair() (*result.Report, *result.Report) {
	a := &result.Report{SuiteSHA256: "same", RunSeed: "same-seed", Mode: "fixed_reader", Repeats: 2, SUT: result.SUT{Name: "left"}}
	b := &result.Report{SuiteSHA256: "same", RunSeed: "same-seed", Mode: "fixed_reader", Repeats: 2, SUT: result.SUT{Name: "right"}}
	for _, row := range []struct {
		id, cluster string
		left, right [2]result.Status
		lv, rv      [2]float64
	}{
		{"keep-a", "a", [2]result.Status{result.Scored, result.Scored}, [2]result.Status{result.Scored, result.Scored}, [2]float64{0, 0}, [2]float64{1, 0}},
		{"keep-b", "b", [2]result.Status{result.Scored, result.Scored}, [2]result.Status{result.Scored, result.Scored}, [2]float64{1, 0}, [2]float64{0, 1}},
		{"part-a", "a", [2]result.Status{result.Scored, result.Invalid}, [2]result.Status{result.Scored, result.Scored}, [2]float64{}, [2]float64{1, 1}},
		{"drop-left", "c", [2]result.Status{result.Invalid, result.Invalid}, [2]result.Status{result.Scored, result.Scored}, [2]float64{}, [2]float64{}},
		{"drop-right", "d", [2]result.Status{result.Scored, result.Scored}, [2]result.Status{result.Invalid, result.Invalid}, [2]float64{}, [2]float64{}},
		{"drop-both", "e", [2]result.Status{result.Invalid, result.Invalid}, [2]result.Status{result.Invalid, result.Invalid}, [2]float64{}, [2]float64{}},
	} {
		for repeat := range 2 {
			for _, side := range []struct {
				r *result.Report
				s result.Status
				v float64
			}{{a, row.left[repeat], row.lv[repeat]}, {b, row.right[repeat], row.rv[repeat]}} {
				c := result.Case{CaseID: row.id, ClusterID: row.cluster, RepeatIndex: repeat, Status: side.s, Metrics: map[string]*float64{}}
				if side.s == result.Scored {
					v := side.v
					c.Metrics["mcq.correct"] = &v
				}
				side.r.Cases = append(side.r.Cases, c)
			}
		}
	}
	return a, b
}

func TestCompareStatusExclusionPairedSubset(t *testing.T) {
	a, b := statusPolicyPair()
	o := DefaultOptions()
	o.PairBy, o.Metric = "sample", "mcq.correct"
	if _, err := Compare(context.Background(), a, b, o); err == nil || !strings.Contains(err.Error(), "do not drop failed cases") {
		t.Fatal("default must remain strict", err)
	}
	o.ExcludeStatus = "invalid"
	o.Indices = [][]int{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	c, err := Compare(context.Background(), a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	// Retained case deltas: keep-a=(1+0)/2, keep-b=(-1+1)/2,
	// part-a=1. Cluster a=[.5,1], b=[0]; bootstrap [.75,.5,.5,0].
	if c.Delta != .5 || c.PairedCases != 3 || c.PairedSamples != 5 || c.Clusters != 2 || !reflect.DeepEqual(c.ClusterOrder, []string{"a", "b"}) {
		t.Fatalf("wrong retained subset: %+v", c)
	}
	if math.Abs(c.CI95[0]-.0375) > 1e-12 || math.Abs(c.CI95[1]-.73125) > 1e-12 {
		t.Fatal(c.CI95)
	}
	want := &result.ExcludedByStatus{Left: map[result.Status]int{result.Invalid: 3}, Right: map[result.Status]int{result.Invalid: 2}, Both: map[result.Status]int{result.Invalid: 2}}
	if !reflect.DeepEqual(c.ExcludedByStatus, want) || !reflect.DeepEqual(c.ExcludedCases, []string{"part-a", "drop-left", "drop-right", "drop-both"}) || !strings.Contains(c.Note, "excluded 7 samples (left=3, right=2, both=2)") {
		t.Fatalf("wrong exclusion disclosure: %+v", c)
	}
	// Neither a removed cluster nor the pre-filter cluster count can enter CI.
	o.Indices = [][]int{{0, 2}}
	if _, err = Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("accepted index of excluded cluster")
	}
}

func TestCompareStatusExclusionBoundaries(t *testing.T) {
	statuses := []result.Status{result.Scored, result.Invalid, result.Unscored, result.Error, result.ContractError, result.IngestFailed, result.Unsupported}
	for _, left := range statuses {
		for _, right := range statuses {
			t.Run(string(left)+"/"+string(right), func(t *testing.T) {
				a, b := pair()
				a.Cases[0].Status, b.Cases[0].Status = left, right
				o := DefaultOptions()
				o.Metric, o.ExcludeStatus = "recall", "invalid,unscored"
				_, err := Compare(context.Background(), a, b, o)
				allowed := func(s result.Status) bool { return s == result.Invalid || s == result.Unscored }
				wantOK := left == right || allowed(left) && (right == result.Scored || allowed(right)) || allowed(right) && left == result.Scored
				if (err == nil) != wantOK || err != nil && !strings.Contains(err.Error(), "do not drop failed cases") {
					t.Fatalf("accept=%v, error=%v", wantOK, err)
				}
			})
		}
	}
	for _, bad := range []string{"error", "contract_error", "ingest_failed", "unsupported", "scored", "invalid,error", "invalid,", " ", "INVALID"} {
		t.Run("option/"+bad, func(t *testing.T) {
			a, b := pair()
			o := DefaultOptions()
			o.ExcludeStatus = bad
			if _, err := Compare(context.Background(), a, b, o); err == nil || !strings.Contains(err.Error(), "do not drop failed cases") {
				t.Fatal("invalid whitelist accepted", err)
			}
		})
	}
}

func TestCompareMixedAllowedStatusesAndCaseGuards(t *testing.T) {
	a, b := pair()
	a.Cases[0].Status, b.Cases[0].Status = result.Invalid, result.Unscored
	o := DefaultOptions()
	o.Metric, o.ExcludeStatus = "recall", "invalid"
	if _, err := Compare(context.Background(), a, b, o); err == nil {
		t.Fatal("unlisted unscored accepted")
	}
	o.ExcludeStatus = "invalid,unscored,invalid"
	c, err := Compare(context.Background(), a, b, o)
	if err != nil || c.ExcludedByStatus.Both[result.Invalid] != 1 || c.ExcludedByStatus.Both[result.Unscored] != 1 || !strings.Contains(c.Note, "excluded 1 samples") {
		t.Fatal(c, err)
	}
	for _, mutate := range []func(*result.Case){
		func(c *result.Case) { c.ClusterID = "changed" },
		func(c *result.Case) { c.Cutoff++ },
		func(c *result.Case) { c.Budget.TopK++ },
		func(c *result.Case) { c.Kind = "changed" },
	} {
		original := b.Cases[0]
		mutate(&b.Cases[0])
		if _, err := Compare(context.Background(), a, b, o); err == nil {
			t.Fatal("status exclusion bypassed case protocol check")
		}
		b.Cases[0] = original
	}
}

func TestCompareStatusDefaultJSONUnchanged(t *testing.T) {
	a, b := pair()
	o := DefaultOptions()
	o.Metric = "recall"
	c, err := Compare(context.Background(), a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(c)
	if err != nil || strings.Contains(string(blob), "excluded_by_status") || strings.Contains(c.Note, "exclude-status") {
		t.Fatal("default output changed", string(blob), err)
	}
}

func TestCompareReaderDisclosures(t *testing.T) {
	a, b := pair()
	a.Reader = map[string]any{"model": "same", "max_tokens": 5, "official_max_tokens": 5}
	b.Reader = map[string]any{"model": "same", "max_tokens": 5., "official_max_tokens": 5,
		"effective_max_tokens": 128, "requested_max_tokens": 5, "min_tokens_reasoning": 128,
		"reasoning": true, "token_budget_note": "new disclosure", "token_budgets": []any{5, 128}}
	o := DefaultOptions()
	o.Metric = "recall"
	c, err := Compare(context.Background(), a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ProtocolDifferences) != 6 || !strings.Contains(c.ProtocolDifferences[0], "reader.effective_max_tokens (disclosure): <absent> -> 128") {
		t.Fatal(c.ProtocolDifferences)
	}
	b.Reader.(map[string]any)["max_tokens"] = 128
	if _, err := Compare(context.Background(), a, b, o); err == nil || !strings.Contains(err.Error(), "reader.max_tokens") {
		t.Fatal("request budget difference accepted", err)
	}
}

func TestCompareReaderStrictKeysIncludePresence(t *testing.T) {
	for _, key := range []string{"model", "provider", "prompt_id", "template_sha256", "input_mode", "max_tokens", "official_max_tokens", "temperature", "fullcontext", "context_budget_chars", "upstream_search_limit"} {
		for _, tc := range []struct {
			name string
			a, b map[string]any
		}{
			{"value", map[string]any{key: 1}, map[string]any{key: 2}},
			{"absent-left", map[string]any{}, map[string]any{key: nil}},
			{"absent-right", map[string]any{key: nil}, map[string]any{}},
		} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				a, b := pair()
				a.Reader, b.Reader = tc.a, tc.b
				o := DefaultOptions()
				o.Metric = "recall"
				if _, err := Compare(context.Background(), a, b, o); err == nil || !strings.Contains(err.Error(), "reader."+key) {
					t.Fatal("strict reader key accepted", err)
				}
			})
		}
	}
}
