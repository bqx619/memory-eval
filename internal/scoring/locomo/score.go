// Package locomo mirrors the official evaluation.py deterministic scorer.
package locomo

import (
	"memeval/internal/pytext"
	"strings"
)

func Normalize(s string) string {
	s = strings.Map(func(c rune) rune {
		if strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) {
			return -1
		}
		return c
	}, pytext.Lower(s))
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		if pytext.RegexWord(r[i]) {
			j := i + 1
			for j < len(r) && pytext.RegexWord(r[j]) {
				j++
			}
			w := string(r[i:j])
			if w == "a" || w == "an" || w == "the" || w == "and" {
				b.WriteByte(' ')
			} else {
				b.WriteString(w)
			}
			i = j
		} else {
			b.WriteRune(r[i])
			i++
		}
	}
	return strings.Join(strings.FieldsFunc(b.String(), pytext.Space), " ")
}
func F1(prediction, gold string) float64 {
	p, g := strings.Fields(Normalize(prediction)), strings.Fields(Normalize(gold))
	counts := map[string]int{}
	for _, w := range g {
		counts[Stem(w)]++
	}
	same := 0
	for _, w := range p {
		s := Stem(w)
		if counts[s] > 0 {
			same++
			counts[s]--
		}
	}
	if same == 0 {
		return 0
	}
	precision, recall := float64(same)/float64(len(p)), float64(same)/float64(len(g))
	return 2 * precision * recall / (precision + recall)
}
func Score(prediction, gold string, category int) float64 {
	switch category {
	case 1:
		sum := 0.
		gs := strings.Split(gold, ",")
		for _, g := range gs {
			best := 0.
			for _, p := range strings.Split(prediction, ",") {
				best = max(best, F1(pytext.Strip(p), pytext.Strip(g)))
			}
			sum += best
		}
		return sum / float64(len(gs))
	case 3:
		return F1(prediction, pytext.Strip(strings.Split(gold, ";")[0]))
	case 5:
		p := pytext.Lower(prediction)
		if strings.Contains(p, "no information available") || strings.Contains(p, "not mentioned") {
			return 1
		}
		return 0
	default:
		return F1(prediction, gold)
	}
}
