package suite

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func TestIdentifierMatcherLiteralSubstringParity(t *testing.T) {
	r := rand.New(rand.NewPCG(42, 17))
	alphabet := []rune("ab01/-:中文")
	word := func(n int) string {
		v := make([]rune, n)
		for i := range v {
			v[i] = alphabet[r.IntN(len(alphabet))]
		}
		return string(v)
	}
	for trial := 0; trial < 100; trial++ {
		ids := map[string]bool{"": true}
		for i := 0; i < 50; i++ {
			ids[word(1+r.IntN(20))] = true
		}
		m := newIdentifierMatcher(ids)
		for i := 0; i < 100; i++ {
			value := word(r.IntN(100))
			want := false
			for id := range ids {
				want = want || (id != "" && strings.Contains(value, id))
			}
			if m.contains(value) != want {
				t.Fatalf("substring mismatch: %q", value)
			}
		}
	}
}
