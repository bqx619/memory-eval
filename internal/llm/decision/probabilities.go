package decision

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ValidateDistribution retains the wire probabilities, accepting sums within
// the rounding error of a common decimal precision (at least two places).
// Higher precision tightens the bound; a coarse value cannot loosen it.
// The returned disclosure is empty for sums within the historical 1e-6 bound.
func ValidateDistribution(probabilities map[string]float64) (string, error) {
	keys := make([]string, 0, len(probabilities))
	for k := range probabilities {
		keys = append(keys, k)
	}
	slices.Sort(keys) // Stable sums and disclosure across runs and replay.
	sum, places := 0., 2
	for _, k := range keys {
		p := probabilities[k]
		if !probability(p) {
			return "", fmt.Errorf("invalid probability")
		}
		sum += p
		if _, fraction, ok := strings.Cut(strconv.FormatFloat(p, 'f', -1, 64), "."); ok {
			places = max(places, len(fraction))
		}
	}
	if len(keys) == 0 || sum == 0 {
		return "", fmt.Errorf("empty probability mass")
	}
	delta := math.Abs(sum - 1)
	if delta <= 1e-6 {
		return "", nil
	}
	bound := float64(len(keys)) * .5 * math.Pow10(-places)
	if delta > bound+1e-6 {
		return "", fmt.Errorf("probabilities must sum to one within decimal rounding error")
	}
	return fmt.Sprintf("probability sum %.12g accepted within decimal rounding bound %.12g; raw probabilities retained, not renormalized", sum, bound), nil
}
