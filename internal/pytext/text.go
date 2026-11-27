// Package pytext implements the Python text semantics needed by vendored scorers.
package pytext

import (
	"strings"
	"unicode"
)

func Space(r rune) bool     { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }
func Strip(s string) string { return strings.TrimFunc(s, Space) }

// Lower includes Python's full lowercase expansion and contextual final sigma.
func Lower(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if c == 'İ' {
			b.WriteString("i\u0307")
			continue
		}
		if c == 'Σ' {
			before, after := false, false
			for j := i - 1; j >= 0; j-- {
				if ignorable(r[j]) {
					continue
				}
				before = cased(r[j])
				break
			}
			for j := i + 1; j < len(r); j++ {
				if ignorable(r[j]) {
					continue
				}
				after = cased(r[j])
				break
			}
			if before && !after {
				b.WriteRune('ς')
				continue
			}
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}
func ignorable(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Lm, r) || unicode.Is(unicode.Sk, r) || r == '\'' || r == '’'
}

// REWord matches Python re's Unicode \w, used by PersonaMem.
func REWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }

// RegexWord matches regex's default Unicode \w: Alphabetic, marks, Nd, Pc, join controls.
func RegexWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r) || r == 0x200c || r == 0x200d || unicode.Is(unicode.Other_Alphabetic, r)
}

func cased(r rune) bool {
	return unicode.IsLower(r) || unicode.IsUpper(r) || unicode.IsTitle(r) || unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}
