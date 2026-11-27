package personamem

import (
	"memeval/internal/pytext"
	"regexp"
	"sort"
	"strings"
)

var parens = regexp.MustCompile(`\(([a-d])\)`)

func Options(text string) []string {
	text = pytext.Lower(text)
	set := map[string]bool{}
	for _, m := range parens.FindAllStringSubmatch(text, -1) {
		set[m[1]] = true
	}
	if len(set) == 0 {
		r := []rune(text)
		for i, c := range r {
			if c >= 'a' && c <= 'd' && (i == 0 || !pytext.REWord(r[i-1])) && (i+1 == len(r) || !pytext.REWord(r[i+1])) {
				set[string(c)] = true
			}
		}
	}
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Extract mirrors inference.py:extract_answer, including its full-response fallback.
func Extract(response, correct string) (passed bool, rawFinal string, options []string) {
	correct = strings.Trim(pytext.Lower(correct), "() ")
	rawFinal = pytext.Strip(response)
	if i := strings.LastIndex(rawFinal, "<final_answer>"); i >= 0 {
		rawFinal = pytext.Strip(rawFinal[i+len("<final_answer>"):])
	}
	if strings.HasSuffix(rawFinal, "</final_answer>") {
		rawFinal = pytext.Strip(strings.TrimSuffix(rawFinal, "</final_answer>"))
	}
	options = Options(rawFinal)
	if len(options) == 1 && options[0] == correct {
		return true, rawFinal, options
	}
	fallback := Options(response)
	return len(fallback) == 1 && fallback[0] == correct, rawFinal, options
}
