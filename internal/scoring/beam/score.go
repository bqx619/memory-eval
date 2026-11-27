// Package beam mirrors the official deterministic ordering metric.
package beam

import (
	"fmt"
	"math"
)

// KendallTauB counts discordant and concordant pairs, excluding ties on either
// axis from the numerator; pairs tied on both axes enter neither denominator.
func KendallTauB(x, y []int) float64 {
	if len(x) != len(y) {
		return math.NaN()
	}
	c, d, tx, ty := 0, 0, 0, 0
	for i := range x {
		for j := i + 1; j < len(x); j++ {
			a, b := sign(x[i]-x[j]), sign(y[i]-y[j])
			if a == 0 && b == 0 {
				continue
			}
			if a == 0 {
				tx++
			} else if b == 0 {
				ty++
			} else if a == b {
				c++
			} else {
				d++
			}
		}
	}
	den := math.Sqrt(float64(c+d+tx) * float64(c+d+ty))
	if den == 0 {
		return math.NaN()
	}
	return float64(c-d) / den
}
func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}

type Ordering struct {
	Precision float64  `json:"precision"`
	Recall    float64  `json:"recall"`
	F1        float64  `json:"f1"`
	Tau       *float64 `json:"tau_b"`
	TauNorm   *float64 `json:"tau_norm"`
	Final     *float64 `json:"final"`
}

func OrderingScore(reference, system []string) Ordering {
	r, s := map[string]bool{}, map[string]bool{}
	union := []string{}
	seen := map[string]bool{}
	for _, seq := range [][]string{reference, system} {
		for _, x := range seq {
			if !seen[x] {
				seen[x] = true
				union = append(union, x)
			}
		}
	}
	for _, x := range reference {
		r[x] = true
	}
	for _, x := range system {
		s[x] = true
	}
	tp, fp, fn := 0, 0, 0
	for x := range r {
		if s[x] {
			tp++
		}
	}
	for _, x := range system {
		if !r[x] {
			fp++
		}
	}
	for _, x := range reference {
		if !s[x] {
			fn++
		}
	}
	o := Ordering{}
	if tp+fp > 0 {
		o.Precision = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		o.Recall = float64(tp) / float64(tp+fn)
	}
	if o.Precision+o.Recall > 0 {
		o.F1 = 2 * o.Precision * o.Recall / (o.Precision + o.Recall)
	}
	rank := func(seq []string) []int {
		m := map[string]int{}
		for i, x := range seq {
			m[x] = i + 1
		}
		out := []int{}
		for _, x := range union {
			n, ok := m[x]
			if !ok {
				n = len(union) + 1
			}
			out = append(out, n)
		}
		return out
	}
	tau := KendallTauB(rank(reference), rank(system))
	if !math.IsNaN(tau) {
		norm := (tau + 1) / 2
		final := norm * o.F1
		o.Tau = &tau
		o.TauNorm = &norm
		o.Final = &final
	}
	return o
}

// RubricMean is the upstream unweighted mean of per-criterion scores.
func RubricMean(scores []float64) (float64, error) {
	if len(scores) == 0 {
		return 0, fmt.Errorf("empty rubric")
	}
	sum := 0.
	for _, v := range scores {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("nonfinite rubric score")
		}
		sum += v
	}
	return sum / float64(len(scores)), nil
}
