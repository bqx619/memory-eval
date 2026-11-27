// Package mathutil contains numeric primitives shared by scoring and statistics.
package mathutil

import (
	"math"
	"slices"
)

// Percentile uses the Python reference's linear interpolation at (n-1)*q.
// Empty input has no percentile. Callers supply q in [0,1]; input is not mutated.
func Percentile(values []float64, q float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	a := slices.Clone(values)
	slices.Sort(a)
	i := float64(len(a)-1) * q
	lo, hi := int(i), int(math.Ceil(i))
	v := a[lo] + (a[hi]-a[lo])*(i-float64(lo))
	return &v
}
