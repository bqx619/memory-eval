// Package mab mirrors the pinned MemoryAgentBench eval_other_utils.py.
package mab

import (
	"fmt"
	"memeval/internal/pytext"
	"regexp"
	"strings"
)

func Normalize(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			return -1
		}
		return r
	}, pytext.Lower(s))
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		if !pytext.REWord(r[i]) {
			b.WriteRune(r[i])
			i++
			continue
		}
		j := i + 1
		for j < len(r) && pytext.REWord(r[j]) {
			j++
		}
		w := string(r[i:j])
		if w == "a" || w == "an" || w == "the" {
			b.WriteByte(' ')
		} else {
			b.WriteString(w)
		}
		i = j
	}
	return whitespace(b.String())
}
func whitespace(s string) string { return strings.Join(strings.FieldsFunc(s, pytext.Space), " ") }
func F1(p, g string) (f, precision, recall float64) {
	p, g = Normalize(p), Normalize(g)
	special := func(s string) bool { return s == "yes" || s == "no" || s == "noanswer" }
	if (special(p) || special(g)) && p != g {
		return
	}
	ps, gs := strings.Fields(p), strings.Fields(g)
	counts := map[string]int{}
	for _, w := range gs {
		counts[w]++
	}
	same := 0
	for _, w := range ps {
		if counts[w] > 0 {
			same++
			counts[w]--
		}
	}
	if same == 0 {
		return
	}
	precision = float64(same) / float64(len(ps))
	recall = float64(same) / float64(len(gs))
	f = 2 * precision * recall / (precision + recall)
	return
}
func Exact(p, g string) float64     { return binary(Normalize(p) == Normalize(g)) }
func Substring(p, g string) float64 { return binary(strings.Contains(Normalize(p), Normalize(g))) }
func binary(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// GroundTruths implements the upstream string/list/one-level nested list forms.
func GroundTruths(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []string:
		return x, nil
	case [][]string:
		var out []string
		for _, s := range x {
			out = append(out, s...)
		}
		return out, nil
	case []any:
		var out []string
		for _, s := range x {
			switch z := s.(type) {
			case string:
				out = append(out, z)
			case []any:
				for _, a := range z {
					t, ok := a.(string)
					if !ok {
						return nil, fmt.Errorf("ground truth must be string")
					}
					out = append(out, t)
				}
			default:
				return nil, fmt.Errorf("invalid ground truths")
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("invalid ground truths")
}
func MaxOverGT(metric func(string, string) float64, p string, gt any) (float64, error) {
	g, e := GroundTruths(gt)
	if e != nil {
		return 0, e
	}
	if len(g) == 0 {
		return 0, fmt.Errorf("max() arg is an empty sequence")
	}
	v := metric(p, g[0])
	for _, s := range g[1:] {
		v = max(v, metric(p, s))
	}
	return v, nil
}
func ParseOutput(s, prefix string) (string, error) {
	// The first prefix is deliberately a regular expression, as in upstream.
	re, err := regexp.Compile("(?i)(?:" + prefix + ")(.*)(?:\\n|$)")
	if err != nil {
		return "", err
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		m = regexp.MustCompile(`^(.*)(?:\n|$)`).FindStringSubmatch(s)
	}
	if m == nil {
		return "", nil
	}
	v := pytext.Strip(m[1])
	return pytext.Strip(regexp.MustCompile("(?i)^"+regexp.QuoteMeta(prefix)).ReplaceAllString(v, "")), nil
}
func Calculate(pred string, gt any) (map[string]float64, error) {
	gs, e := GroundTruths(gt)
	if e != nil {
		return nil, e
	}
	if len(gs) == 0 {
		return nil, fmt.Errorf("empty answers")
	}
	out := map[string]float64{}
	for _, g := range gs {
		f, _, _ := F1(pred, g)
		for k, v := range map[string]float64{"exact_match": Exact(pred, g), "f1": f, "substring_exact_match": Substring(pred, g)} {
			out[k] = max(out[k], v)
		}
		for _, typ := range []string{"rougeL", "rougeLsum"} {
			f, r := Rouge(pred, g, typ)
			out[typ+"_f1"] = max(out[typ+"_f1"], f)
			out[typ+"_recall"] = max(out[typ+"_recall"], r)
		}
	}
	return out, nil
}

type Processed struct {
	Metrics  map[string]float64
	Parsed   any
	GTMovies []string
}

func PostProcess(pred string, gt []string, source string, candidates *Candidates) (Processed, error) {
	if len(gt) == 0 {
		return Processed{}, fmt.Errorf("empty ground truths")
	}
	parsed, e := ParseOutput(pred, "Answer:")
	if e != nil {
		return Processed{}, e
	}
	out := Processed{Parsed: parsed}
	lower := pytext.Lower(pred)
	recall := func() float64 {
		n := 0
		for _, g := range gt {
			if strings.Contains(lower, pytext.Lower(g)) {
				n++
			}
		}
		return float64(n) / float64(len(gt))
	}
	switch {
	case strings.Contains(source, "icl"), strings.Contains(source, "eventqa"):
		out.Metrics, e = Calculate(parsed, gt)
		if strings.Contains(source, "eventqa") {
			out.Metrics["eventqa_recall"] = binary(recall() == 1)
		}
	case (strings.Contains(source, "ruler") || strings.Contains(source, "mermory_merging")) && strings.Contains(source, "ruler_niah"):
		out.Parsed = pred
		out.Metrics = map[string]float64{"ruler_recall": recall()}
	case strings.Contains(source, "recsys"):
		if candidates == nil {
			return out, fmt.Errorf("recsys requires entity2id candidates")
		}
		return candidates.Score(pred, gt)
	default:
		out.Metrics, e = Calculate(pred, gt)
		if e != nil {
			return out, e
		}
		other, err := Calculate(parsed, gt)
		if err != nil {
			return out, err
		}
		for k, v := range other {
			out.Metrics[k] = max(out.Metrics[k], v)
		}
		if strings.Contains(source, "infbench") && strings.Contains(source, "choice_eng") {
			if len(gt) < 2 {
				return out, fmt.Errorf("choice_eng requires answer option")
			}
			match := binary(strings.Contains(lower, pytext.Lower(gt[1])))
			out.Metrics["substring_exact_match"] = match
			if match == 1 {
				out.Metrics["exact_match"] = 1
			}
		}
	}
	return out, e
}
