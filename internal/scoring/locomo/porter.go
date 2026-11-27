package locomo

// Rule-for-rule port of design/upstream/nltk/porter.py, NLTK_EXTENSIONS.
// NLTK copyright/license and provenance: design/upstream/nltk/{LICENSE.txt,SOURCES.md}.
import (
	"memeval/internal/pytext"
	"strings"
	"unicode/utf8"
)

var irregular = map[string]string{"sky": "sky", "skies": "sky", "dying": "die", "lying": "lie", "tying": "tie", "news": "news", "innings": "inning", "inning": "inning", "outings": "outing", "outing": "outing", "cannings": "canning", "canning": "canning", "howe": "howe", "proceed": "proceed", "exceed": "exceed", "succeed": "succeed"}

func flags(s string) []bool {
	r := []rune(s)
	a := make([]bool, len(r))
	for i, c := range r {
		a[i] = !strings.ContainsRune("aeiou", c)
		if c == 'y' && i > 0 {
			a[i] = !a[i-1]
		}
	}
	return a
}
func measure(s string) int {
	a := flags(s)
	n := 0
	for i := 1; i < len(a); i++ {
		if !a[i-1] && a[i] {
			n++
		}
	}
	return n
}
func vowel(s string) bool {
	for _, c := range flags(s) {
		if !c {
			return true
		}
	}
	return false
}
func double(s string) bool {
	r := []rune(s)
	a := flags(s)
	return len(r) >= 2 && r[len(r)-1] == r[len(r)-2] && a[len(a)-1]
}
func cvc(s string) bool {
	r := []rune(s)
	a := flags(s)
	n := len(a)
	return n >= 3 && a[n-3] && !a[n-2] && a[n-1] && !strings.ContainsRune("wxy", r[n-1]) || n == 2 && !a[0] && a[1]
}

type rule struct {
	suffix, replacement string
	condition           func(string) bool
}

func apply(s string, rs []rule) string {
	for _, r := range rs {
		if strings.HasSuffix(s, r.suffix) {
			stem := strings.TrimSuffix(s, r.suffix)
			if r.condition == nil || r.condition(stem) {
				return stem + r.replacement
			}
			return s
		}
	}
	return s
}
func positive(s string) bool { return measure(s) > 0 }
func step1b(s string) string {
	if strings.HasSuffix(s, "ied") {
		if utf8.RuneCountInString(s) == 4 {
			return strings.TrimSuffix(s, "ied") + "ie"
		}
		return strings.TrimSuffix(s, "ied") + "i"
	}
	if strings.HasSuffix(s, "eed") {
		return apply(s, []rule{{"eed", "ee", positive}})
	}
	for _, suffix := range []string{"ed", "ing"} {
		if strings.HasSuffix(s, suffix) {
			stem := strings.TrimSuffix(s, suffix)
			if !vowel(stem) {
				continue
			}
			for _, ending := range []string{"at", "bl", "iz"} {
				if strings.HasSuffix(stem, ending) {
					return stem + "e"
				}
			}
			if double(stem) {
				r := []rune(stem)
				if !strings.ContainsRune("lsz", r[len(r)-1]) {
					return string(r[:len(r)-1])
				}
				return stem
			}
			if measure(stem) == 1 && cvc(stem) {
				return stem + "e"
			}
			return stem
		}
	}
	return s
}
func step2(s string) string {
	for strings.HasSuffix(s, "alli") && positive(strings.TrimSuffix(s, "alli")) {
		s = strings.TrimSuffix(s, "alli") + "al"
	}
	return apply(s, []rule{{"ational", "ate", positive}, {"tional", "tion", positive}, {"enci", "ence", positive}, {"anci", "ance", positive}, {"izer", "ize", positive}, {"bli", "ble", positive}, {"alli", "al", positive}, {"entli", "ent", positive}, {"eli", "e", positive}, {"ousli", "ous", positive}, {"ization", "ize", positive}, {"ation", "ate", positive}, {"ator", "ate", positive}, {"alism", "al", positive}, {"iveness", "ive", positive}, {"fulness", "ful", positive}, {"ousness", "ous", positive}, {"aliti", "al", positive}, {"iviti", "ive", positive}, {"biliti", "ble", positive}, {"fulli", "ful", positive}, {"logi", "log", func(_ string) bool { return positive(s[:len(s)-3]) }}})
}
func Stem(word string) string {
	s := pytext.Lower(word)
	if v, ok := irregular[s]; ok {
		return v
	}
	if utf8.RuneCountInString(word) <= 2 {
		return s
	}
	if strings.HasSuffix(s, "ies") && utf8.RuneCountInString(s) == 4 {
		s = strings.TrimSuffix(s, "ies") + "ie"
	} else {
		s = apply(s, []rule{{"sses", "ss", nil}, {"ies", "i", nil}, {"ss", "ss", nil}, {"s", "", nil}})
	}
	s = step1b(s)
	s = apply(s, []rule{{"y", "i", func(stem string) bool { a := flags(stem); return len(a) > 1 && a[len(a)-1] }}})
	s = step2(s)
	s = apply(s, []rule{{"icate", "ic", positive}, {"ative", "", positive}, {"alize", "al", positive}, {"iciti", "ic", positive}, {"ical", "ic", positive}, {"ful", "", positive}, {"ness", "", positive}})
	rs := []rule{}
	for _, suffix := range []string{"al", "ance", "ence", "er", "ic", "able", "ible", "ant", "ement", "ment", "ent", "ion", "ou", "ism", "ate", "iti", "ous", "ive", "ize"} {
		check := func(stem string) bool { return measure(stem) > 1 }
		if suffix == "ion" {
			check = func(stem string) bool {
				return measure(stem) > 1 && (strings.HasSuffix(stem, "s") || strings.HasSuffix(stem, "t"))
			}
		}
		rs = append(rs, rule{suffix, "", check})
	}
	s = apply(s, rs)
	if strings.HasSuffix(s, "e") {
		stem := strings.TrimSuffix(s, "e")
		m := measure(stem)
		if m > 1 || m == 1 && !cvc(stem) {
			s = stem
		}
	}
	if strings.HasSuffix(s, "ll") && measure(s[:len(s)-1]) > 1 {
		s = s[:len(s)-1]
	}
	return s
}
