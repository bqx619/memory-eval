package mab

import (
	"memeval/internal/pytext"
	"memeval/internal/scoring/locomo"
	"regexp"
	"strings"
)

var rougeWords = regexp.MustCompile(`[a-z0-9]+`)

func rougeTokens(s string) []string {
	w := rougeWords.FindAllString(pytext.Lower(s), -1)
	for i, v := range w {
		if len(v) > 3 {
			w[i] = locomo.Stem(v)
		}
	}
	return w
}
func prf(h, p, g int) (float64, float64) {
	if p == 0 || g == 0 || h == 0 {
		return 0, 0
	}
	precision, recall := float64(h)/float64(p), float64(h)/float64(g)
	return 2 * precision * recall / (precision + recall), recall
}
func lcs(a, b []string) int {
	r := make([]int, len(b)+1)
	for _, x := range a {
		prev := 0
		for j, y := range b {
			old := r[j+1]
			if x == y {
				r[j+1] = prev + 1
			} else {
				r[j+1] = max(r[j], r[j+1])
			}
			prev = old
		}
	}
	return r[len(b)]
}

// lcsIndices reproduces rouge_score's backtracking (reference index decreases when tied).
func lcsIndices(ref, cand []string) []int {
	t := make([][]int, len(ref)+1)
	for i := range t {
		t[i] = make([]int, len(cand)+1)
	}
	for i, x := range ref {
		for j, y := range cand {
			if x == y {
				t[i+1][j+1] = t[i][j] + 1
			} else {
				t[i+1][j+1] = max(t[i][j+1], t[i+1][j])
			}
		}
	}
	var out []int
	i, j := len(ref), len(cand)
	for i > 0 && j > 0 {
		if ref[i-1] == cand[j-1] {
			out = append(out, i-1)
			i--
			j--
		} else if t[i][j-1] > t[i-1][j] {
			j--
		} else {
			i--
		}
	}
	return out
}

// Rouge uses rouge_score's ASCII tokenizer, Porter stemming and newline-based
// summary sentences; nltk sentence tokenization is not silently substituted.
func Rouge(pred, ref, kind string) (float64, float64) {
	p, g := rougeTokens(pred), rougeTokens(ref)
	switch kind {
	case "rouge1", "rouge2":
		n := 1
		if kind == "rouge2" {
			n = 2
		}
		count := func(w []string) map[string]int {
			m := map[string]int{}
			for i := 0; i+n <= len(w); i++ {
				m[strings.Join(w[i:i+n], " ")]++
			}
			return m
		}
		a, b := count(p), count(g)
		hit := 0
		for k, v := range a {
			hit += min(v, b[k])
		}
		return prf(hit, max(0, len(p)-n+1), max(0, len(g)-n+1))
	case "rougeL":
		return prf(lcs(g, p), len(p), len(g))
	case "rougeLsum":
		pc, gc := map[string]int{}, map[string]int{}
		for _, w := range p {
			pc[w]++
		}
		for _, w := range g {
			gc[w]++
		}
		hit := 0
		for _, line := range strings.Split(ref, "\n") {
			words := rougeTokens(line)
			indices := map[int]bool{}
			for _, pl := range strings.Split(pred, "\n") {
				for _, i := range lcsIndices(words, rougeTokens(pl)) {
					indices[i] = true
				}
			}
			for i, w := range words {
				if indices[i] && pc[w] > 0 && gc[w] > 0 {
					hit++
					pc[w]--
					gc[w]--
				}
			}
		}
		return prf(hit, len(p), len(g))
	}
	return 0, 0
}
