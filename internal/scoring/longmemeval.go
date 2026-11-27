package scoring

import (
	"fmt"
	"math"
	"memeval/internal/result"
	"memeval/internal/suite"
	"slices"
	"strings"
)

const LongMemEvalPrefix = "longmemeval_official."

// officialAt mirrors eval_utils.py, including vacuous recall_all and whole-corpus IDCG.
func officialAt(ranking, corpus, gold []string, k int) (float64, float64, float64) {
	g := set(gold)
	seen := map[string]bool{}
	dcg := 0.
	weight := func(i int) float64 {
		if i == 0 {
			return 1
		}
		return 1 / math.Log2(float64(i+1))
	}
	for i, id := range ranking[:min(k, len(ranking))] {
		seen[id] = true
		if g[id] {
			dcg += weight(i)
		}
	}
	any, all := false, true
	for id := range g {
		any = any || seen[id]
		all = all && seen[id]
	}
	relevant := 0
	for _, id := range corpus {
		if g[id] {
			relevant++
		}
	}
	ideal := 0.
	for i := 0; i < min(k, relevant); i++ {
		ideal += weight(i)
	}
	ndcg := 0.
	if ideal > 0 {
		ndcg = dcg / ideal
	}
	return boolFloat(any), boolFloat(all), ndcg
}

// LongMemEval keeps repeated session IDs in the ranking and corpus for official nDCG.
// Input ranking is expanded item citations, deduplicated by turn, never by session.
func LongMemEval(events []suite.Event, ranking, turnGold, sessionGold []string) map[string]*float64 {
	ranking = OrderedUnique(ranking)
	corpus := make([]string, len(events))
	sessions := make([]string, len(events))
	byID := map[string]string{}
	for i, e := range events {
		corpus[i] = e.SourceID
		sessions[i] = e.Session
		byID[e.SourceID] = e.Session
	}
	out := map[string]*float64{}
	for _, k := range []int{5, 10, 50} {
		a, b, c := officialAt(ranking, corpus, turnGold, k)
		for name, v := range map[string]float64{"recall_any": a, "recall_all": b, "ndcg_any": c} {
			out[fmt.Sprintf("%sturn.%s@%d", LongMemEvalPrefix, name, k)] = Float(v)
		}
	}
	sessionRanking := make([]string, len(ranking))
	for i, id := range ranking {
		sessionRanking[i] = byID[id]
	}
	for _, k := range []int{5, 10} {
		effective := k
		unique := set(sessionRanking[:min(k, len(sessionRanking))])
		for effective <= len(corpus) && len(unique) < k {
			effective++
			if effective <= len(sessionRanking) {
				unique[sessionRanking[effective-1]] = true
			} else {
				effective = len(corpus) + 1
				break
			}
		}
		_, all, ndcg := officialAt(sessionRanking, sessions, sessionGold, effective)
		out[fmt.Sprintf("%ssession.recall_all@%d", LongMemEvalPrefix, k)] = Float(all)
		out[fmt.Sprintf("%ssession.ndcg_any@%d", LongMemEvalPrefix, k)] = Float(ndcg)
	}
	return out
}
func MemoryOnly(metrics map[string]result.Metric, enabled []string) map[string]any {
	out := map[string]any{}
	groups := map[string]map[string]result.Metric{"session": {}, "turn": {}}
	for key, v := range metrics {
		if rest, ok := strings.CutPrefix(key, LongMemEvalPrefix); ok {
			unit, name, _ := strings.Cut(rest, ".")
			groups[unit][name] = v
		} else {
			out[key] = v
		}
	}
	if slices.Contains(enabled, "longmemeval_official") {
		out["longmemeval_official"] = groups
	}
	return out
}
