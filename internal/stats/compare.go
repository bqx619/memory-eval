package stats

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"memeval/internal/mathutil"
	"memeval/internal/result"
	"reflect"
	"slices"
	"sort"
	"strings"
)

type Options struct {
	PairBy        string
	ExcludeStatus string
	Metric        string
	Seed          uint64
	Draws         int
	Indices       [][]int
}

func DefaultOptions() Options { return Options{Metric: "pass", Seed: 42, Draws: 2000} }
func value(c result.Case, metric string) *float64 {
	if metric == "judge.correct" {
		if c.QA == nil || c.QA.JudgeLabel == nil {
			return nil
		}
		v := 0.
		if *c.QA.JudgeLabel {
			v = 1
		}
		return &v
	}
	if metric != "pass" {
		return c.Metrics[metric]
	}
	if c.Passed == nil {
		return nil
	}
	v := 0.
	if *c.Passed {
		v = 1
	}
	return &v
}
func caseKey(c result.Case) string { return fmt.Sprintf("%s#%d", c.CaseID, c.RepeatIndex) }

func Compare(ctx context.Context, left, right *result.Report, o Options) (*result.Comparison, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.PairBy != "" && o.PairBy != "sample" {
		return nil, fmt.Errorf("pair-by must be sample")
	}
	statusPolicy, err := newStatusExclusion(o.ExcludeStatus)
	if err != nil {
		return nil, err
	}
	differences := []string{}
	if o.Metric == "" {
		return nil, fmt.Errorf("metric required")
	}
	if o.Draws < 1 && o.Indices == nil {
		return nil, fmt.Errorf("draws must be positive")
	}
	if left.SuiteSHA256 != right.SuiteSHA256 || left.Mode != right.Mode {
		return nil, fmt.Errorf("paired comparison requires identical suite hash and mode")
	}
	aRef, bRef := left.Protocol.Benchmark, right.Protocol.Benchmark
	if aRef != nil && bRef != nil {
		if aRef.ProtocolID != bRef.ProtocolID || aRef.Version != bRef.Version || aRef.Variant != bRef.Variant || aRef.SHA256 != bRef.SHA256 {
			return nil, fmt.Errorf("protocol differs: benchmark")
		}
	} else if aRef != nil || bRef != nil {
		ref := aRef
		if ref == nil {
			ref = bRef
		}
		differences = append(differences, fmt.Sprintf("benchmark declaration present on one side only (%s#%s)", ref.ProtocolID, ref.Variant))
	}
	for _, check := range []struct {
		name string
		a, b any
	}{{"ingest_render", left.Protocol.IngestRender, right.Protocol.IngestRender}, {"judge", left.Protocol.Judge, right.Protocol.Judge}, {"reader_input", left.Protocol.ReaderInput, right.Protocol.ReaderInput}, {"query_time_policy", left.Protocol.QueryTimePolicy, right.Protocol.QueryTimePolicy}, {"retrieval_metrics", left.Protocol.RetrievalMetrics, right.Protocol.RetrievalMetrics}, {"cluster_basis", left.Source["cluster_basis"], right.Source["cluster_basis"]}, {"budget", left.Protocol.Budget, right.Protocol.Budget}, {"batch_size", left.Protocol.BatchSize, right.Protocol.BatchSize}, {"scoring", left.Protocol.Scoring, right.Protocol.Scoring}, {"case_limit", left.Protocol.CaseLimit, right.Protocol.CaseLimit}, {"reader", left.Reader, right.Reader}, {"ingestion", left.Protocol.Ingestion, right.Protocol.Ingestion}, {"reset_policy", left.Protocol.ResetPolicy, right.Protocol.ResetPolicy}, {"k_unit", left.Protocol.KUnit, right.Protocol.KUnit}, {"repeats", left.Repeats, right.Repeats}} {
		if check.name == "budget" && o.PairBy == "sample" {
			a, b := left.Protocol.Budget, right.Protocol.Budget
			a.TopK = 0
			b.TopK = 0
			if reflect.DeepEqual(a, b) {
				if left.Protocol.Budget.TopK != right.Protocol.Budget.TopK {
					differences = append(differences, fmt.Sprintf("budget.top_k: %d -> %d", left.Protocol.Budget.TopK, right.Protocol.Budget.TopK))
				}
				continue
			}
		}
		if check.name == "reader" {
			readerDifferences, err := compareReader(check.a, check.b)
			if err != nil {
				return nil, err
			}
			differences = append(differences, readerDifferences...)
			continue
		}
		if !reflect.DeepEqual(check.a, check.b) {
			if check.name == "judge" {
				normalize := func(v any) any {
					j, _ := v.(*result.JudgeInfo)
					if j == nil {
						return v
					}
					copy := *j
					if copy.Kind == "" {
						copy.Kind = "llm"
					}
					return copy
				}
				if reflect.DeepEqual(normalize(check.a), normalize(check.b)) {
					continue
				}
			}
			return nil, fmt.Errorf("protocol differs: %s", check.name)
		}
	}
	a, b := map[string]result.Case{}, map[string]result.Case{}
	for _, pair := range []struct {
		rows []result.Case
		dest map[string]result.Case
	}{{left.Cases, a}, {right.Cases, b}} {
		for _, c := range pair.rows {
			k := caseKey(c)
			if _, ok := pair.dest[k]; ok {
				return nil, fmt.Errorf("duplicate sample %s", k)
			}
			pair.dest[k] = c
		}
	}
	if len(a) != len(b) {
		return nil, fmt.Errorf("case coverage differs")
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return nil, fmt.Errorf("case coverage differs")
		}
	}
	if o.PairBy == "sample" {
		for _, r := range []*result.Report{left, right} {
			if r.Repeats < 1 {
				return nil, fmt.Errorf("invalid repeats")
			}
			counts := map[string]map[int]bool{}
			for _, c := range r.Cases {
				if c.RepeatIndex < 0 || c.RepeatIndex >= r.Repeats {
					return nil, fmt.Errorf("repeat index outside declared range")
				}
				if counts[c.CaseID] == nil {
					counts[c.CaseID] = map[int]bool{}
				}
				counts[c.CaseID][c.RepeatIndex] = true
			}
			for _, indices := range counts {
				if len(indices) != r.Repeats {
					return nil, fmt.Errorf("incomplete repeat coverage")
				}
			}
		}
	}
	groups := map[string][]float64{}
	perCase := map[string][]float64{}
	clusters := map[string]string{}
	excluded := []string{}
	excludedSeen := map[string]bool{}
	pairedSamples := 0
	for _, x := range left.Cases {
		y := b[caseKey(x)]
		excludeStatus := statusPolicy.excludes(x.Status, y.Status)
		if x.Status != y.Status && !excludeStatus {
			return nil, fmt.Errorf("execution/scoring coverage differs; do not drop failed cases")
		}
		if x.ClusterID != y.ClusterID {
			return nil, fmt.Errorf("cluster differs")
		}
		if o.PairBy == "sample" && x.Budget.TopK != y.Budget.TopK && !(x.Budget.TopK == left.Protocol.Budget.TopK && y.Budget.TopK == right.Protocol.Budget.TopK) {
			change := fmt.Sprintf("case %s budget.top_k: %d -> %d", x.CaseID, x.Budget.TopK, y.Budget.TopK)
			if !slices.Contains(differences, change) {
				differences = append(differences, change)
			}
		}
		xb, yb := x.Budget, y.Budget
		if o.PairBy == "sample" {
			xb.TopK = 0
			yb.TopK = 0
		}
		if !reflect.DeepEqual(xb, yb) || x.Cutoff != y.Cutoff || x.Kind != y.Kind {
			return nil, fmt.Errorf("case protocol differs")
		}
		if excludeStatus {
			statusPolicy.count(x.Status, y.Status)
			if !excludedSeen[x.CaseID] {
				excluded = append(excluded, x.CaseID)
				excludedSeen[x.CaseID] = true
			}
			continue
		}
		ax, by := value(x, o.Metric), value(y, o.Metric)
		if (ax == nil) != (by == nil) {
			return nil, fmt.Errorf("metric coverage differs")
		}
		if x.Status != result.Scored || ax == nil {
			if !excludedSeen[x.CaseID] {
				excluded = append(excluded, x.CaseID)
				excludedSeen[x.CaseID] = true
			}
			continue
		}
		if previous, ok := clusters[x.CaseID]; ok && previous != x.ClusterID {
			return nil, fmt.Errorf("repeat cluster differs")
		}
		clusters[x.CaseID] = x.ClusterID
		perCase[x.CaseID] = append(perCase[x.CaseID], *by-*ax)
		pairedSamples++
	}
	// Average paired repeats within each original case before resampling clusters.
	caseIDs := make([]string, 0, len(perCase))
	for k := range perCase {
		caseIDs = append(caseIDs, k)
	}
	sort.Strings(caseIDs)
	sum := 0.
	for _, id := range caseIDs {
		values := perCase[id]
		v := 0.
		for _, d := range values {
			v += d
		}
		v /= float64(len(values))
		groups[clusters[id]] = append(groups[clusters[id]], v)
		sum += v
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("no paired measurements")
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := &result.Comparison{Left: left.SUT.Name, Right: right.SUT.Name, Metric: o.Metric, Delta: sum / float64(len(perCase)), PairedCases: len(perCase), PairedSamples: pairedSamples, Clusters: len(groups), ExcludedCases: excluded, ClusterOrder: keys, Seed: o.Seed}
	out.ExcludedByStatus = statusPolicy.counts
	if len(differences) > 0 {
		out.ProtocolDifferences = differences
	}
	if o.PairBy == "sample" {
		out.PairBy = "sample"
		out.ProtocolDifferences = differences
		out.LeftRepeats = Repeats(left.Cases, left.Repeats)
		out.RightRepeats = Repeats(right.Cases, right.Repeats)
	}
	defer func() {
		out.Note += statusPolicy.note()
		if slices.ContainsFunc(differences, func(s string) bool { return strings.Contains(s, "top_k") }) {
			out.Note += "; explicitly paired experiment with different top_k budgets; no claim of identical retrieval conditions"
		}
		if left.Source["cluster_basis"] == "episode" {
			out.Note += "; cluster_basis=episode: independently assembled question haystacks; filler sessions may share a source pool, so cross-episode dependence is not ruled out"
		}
	}()
	if len(groups) < 2 {
		if o.Indices != nil {
			return nil, fmt.Errorf("resampling requires at least two independent clusters")
		}
		out.Note = "One shared source corpus: no population-level confidence interval is reported"
		out.ResampleIndices = [][]int{}
		return out, nil
	}
	indices := o.Indices
	if indices == nil {
		rng := rand.New(rand.NewPCG(o.Seed, o.Seed))
		indices = make([][]int, o.Draws)
		for i := range indices {
			indices[i] = make([]int, len(keys))
			for j := range indices[i] {
				indices[i][j] = rng.IntN(len(keys))
			}
		}
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("empty resample indices")
	}
	boot := make([]float64, len(indices))
	for i, picks := range indices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(picks) != len(keys) {
			return nil, fmt.Errorf("resample row %d must contain %d cluster indices", i+1, len(keys))
		}
		total, n := 0., 0
		for _, j := range picks {
			if j < 0 || j >= len(keys) {
				return nil, fmt.Errorf("resample index out of range")
			}
			for _, v := range groups[keys[j]] {
				total += v
				n++
			}
		}
		boot[i] = total / float64(n)
	}
	out.CI95 = []float64{*mathutil.Percentile(boot, .025), *mathutil.Percentile(boot, .975)}
	out.Bootstrap = "paired cluster percentile"
	out.Draws = len(indices)
	out.ResampleIndices = indices
	out.Note = "Paired repeats averaged within case; clusters ordered lexicographically; PCG(seed,seed) for generated indices"
	return out, nil
}
func ReadIndices(ctx context.Context, r io.Reader) ([][]int, error) {
	out := [][]int{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var row []int
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if row == nil {
			return nil, fmt.Errorf("resample row must be an array")
		}
		out = append(out, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty resample file")
	}
	return out, nil
}
