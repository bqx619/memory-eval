package scoring

import (
	"fmt"
	"math"
	"memeval/internal/mathutil"
	"strings"
	"unicode"
)

func Float(v float64) *float64 { return &v }
func Bool(v bool) *bool        { return &v }
func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
func OrderedUnique(xs []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
func set(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

type Metrics struct {
	Values        map[string]*float64
	EmptyExpected *bool
}

func Retrieval(predicted, gold []string, exhaustive bool, k int) (Metrics, error) {
	if k < 0 {
		return Metrics{}, fmt.Errorf("k must be non-negative")
	}
	p := OrderedUnique(predicted)
	if len(p) > k {
		p = p[:k]
	}
	g := set(gold)
	m := map[string]*float64{"hit": nil, "recall": nil, "mrr": nil, "all_evidence": nil, "ndcg": nil, "precision": nil, "empty_result_correct": nil}
	out := Metrics{m, Bool(len(g) == 0)}
	if len(g) == 0 {
		if exhaustive {
			if len(p) > 0 {
				m["precision"] = Float(0)
			}
			m["empty_result_correct"] = Float(boolFloat(len(p) == 0))
		}
		return out, nil
	}
	hits := 0
	dcg, ideal, mrr := 0., 0., 0.
	for i, x := range p {
		if g[x] {
			hits++
			dcg += 1 / math.Log2(float64(i+2))
			if mrr == 0 {
				mrr = 1 / float64(i+1)
			}
		}
	}
	for i := 0; i < min(len(g), k); i++ {
		ideal += 1 / math.Log2(float64(i+2))
	}
	ndcg := 0.
	if ideal > 0 {
		ndcg = dcg / ideal
	}
	m["hit"] = Float(boolFloat(hits > 0))
	m["recall"] = Float(float64(hits) / float64(len(g)))
	m["mrr"] = Float(mrr)
	m["all_evidence"] = Float(boolFloat(hits == len(g)))
	m["ndcg"] = Float(ndcg)
	if exhaustive {
		precision := 0.
		if len(p) > 0 {
			precision = float64(hits) / float64(len(p))
		}
		m["precision"] = Float(precision)
	}
	return out, nil
}
func Percentile(values []float64, q float64) *float64 { return mathutil.Percentile(values, q) }

const punctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

func NormalizeAnswer(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "İ", "i\u0307"))
	runes := []rune{}
	for _, r := range text {
		if !strings.ContainsRune(punctuation, r) {
			runes = append(runes, r)
		}
	}
	// Python's Unicode \b treats letters, numbers and '_' as word characters.
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }
	var out strings.Builder
	for i := 0; i < len(runes); {
		if !word(runes[i]) {
			out.WriteRune(runes[i])
			i++
			continue
		}
		j := i + 1
		for j < len(runes) && word(runes[j]) {
			j++
		}
		w := string(runes[i:j])
		if w == "a" || w == "an" || w == "the" {
			out.WriteByte(' ')
		} else {
			out.WriteString(w)
		}
		i = j
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
func LexicalAnswer(prediction string, references []string) map[string]*float64 {
	m := map[string]*float64{"exact_match_diagnostic": nil, "token_f1_diagnostic": nil}
	if len(references) == 0 {
		return m
	}
	pp := NormalizeAnswer(prediction)
	em, best := 0., 0.
	for _, ref := range references {
		aa := NormalizeAnswer(ref)
		if pp == aa {
			em = 1
		}
		p, a := strings.Fields(pp), strings.Fields(aa)
		f1 := 0.
		if len(p) == 0 || len(a) == 0 {
			f1 = boolFloat(pp == aa)
		} else {
			pc, ac := map[string]int{}, map[string]int{}
			for _, w := range p {
				pc[w]++
			}
			for _, w := range a {
				ac[w]++
			}
			overlap := 0
			for w, n := range pc {
				overlap += min(n, ac[w])
			}
			if overlap > 0 {
				precision, recall := float64(overlap)/float64(len(p)), float64(overlap)/float64(len(a))
				f1 = 2 * precision * recall / (precision + recall)
			}
		}
		best = max(best, f1)
	}
	m["exact_match_diagnostic"] = Float(em)
	m["token_f1_diagnostic"] = Float(best)
	return m
}
func MCQ(choice string, options map[string]string, gold string) (bool, map[string]*float64, error) {
	if _, ok := options[choice]; !ok {
		return false, nil, fmt.Errorf("MCQ answer requires one exact choice key")
	}
	if _, ok := options[gold]; !ok {
		return false, nil, fmt.Errorf("invalid gold choice")
	}
	ok := choice == gold
	return ok, map[string]*float64{"choice_accuracy": Float(boolFloat(ok))}, nil
}
