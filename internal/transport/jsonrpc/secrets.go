package jsonrpc

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// SecretValues is the diagnostic substring-redaction set, not a heuristic for
// classifying configuration. Short values would corrupt unrelated paths/text.
// Longest first also prevents a prefix credential from exposing a suffix.
func SecretValues(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if utf8.RuneCountInString(value) >= 8 && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return out
}
