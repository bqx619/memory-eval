package mab

import (
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/pytext"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

var parentheses = regexp.MustCompile(`\([^()]*\)`)

func CleanParentheses(s string) string { return parentheses.ReplaceAllString(s, "") }
func RemoveNumbering(s string) string {
	r := []rune(s)
	i := 0
	for i < len(r) && unicode.Is(unicode.Nd, r[i]) {
		i++
	}
	if i == 0 {
		return s
	}
	if i < len(r) && strings.ContainsRune(".)、", r[i]) {
		i++
	}
	for i < len(r) && pytext.Space(r[i]) {
		i++
	}
	if i < len(r) && strings.ContainsRune("-—–", r[i]) {
		i++
	}
	for i < len(r) && pytext.Space(r[i]) {
		i++
	}
	return string(r[i:])
}
func CleanText(s string) string { return whitespace(RemoveNumbering(CleanParentheses(s))) }
func MovieName(s string) string {
	parts := strings.Split(s, "/")
	s = parts[len(parts)-1]
	s = strings.NewReplacer("_", " ", "-", " ", ">", " ").Replace(s)
	return whitespace(CleanParentheses(s))
}
func EditDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	row := make([]int, len(y)+1)
	for j := range row {
		row[j] = j
	}
	for i, r := range x {
		prev := row[0]
		row[0] = i + 1
		for j, s := range y {
			old := row[j+1]
			cost := 0
			if r != s {
				cost = 1
			}
			row[j+1] = min(row[j]+1, row[j+1]+1, prev+cost)
			prev = old
		}
	}
	return row[len(y)]
}

type Match struct {
	MovieName string `json:"movie_name"`
	Distance  int    `json:"min_edit_distance"`
	Nearest   string `json:"nearest_movie"`
}

// Python uses set iteration for tied minima. Stable lexical tie breaking is a
// declared protocol deviation; unique nearest neighbors are unchanged.
func FindNearest(target string, candidates []string) (Match, error) {
	if len(candidates) == 0 {
		return Match{}, fmt.Errorf("empty movie candidates")
	}
	names := slices.Clone(candidates)
	slices.Sort(names)
	names = slices.Compact(names)
	out := Match{MovieName: target, Distance: int(^uint(0) >> 1)}
	for _, n := range names {
		d := EditDistance(pytext.Lower(target), pytext.Lower(n))
		if d < out.Distance {
			out.Distance = d
			out.Nearest = n
		}
	}
	return out, nil
}
func Recommendations(text string, candidates []string) ([]Match, string, error) {
	pref, body, ok := strings.Cut(text, "1.")
	if !ok {
		pref = ""
		body = strings.ReplaceAll(text, ",", "\n")
	}
	out := []Match{}
	for _, line := range strings.Split(body, "\n") {
		v := CleanText(pytext.Strip(line))
		if candidates == nil {
			out = append(out, Match{MovieName: v})
			continue
		}
		m, e := FindNearest(v, candidates)
		if e != nil {
			return nil, pref, e
		}
		out = append(out, m)
	}
	return out, pref, nil
}

type Candidates struct {
	ByID  map[int]string
	Names []string
}

func LoadCandidates(path string) (*Candidates, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return ReadCandidates(f)
}
func ReadCandidates(r io.Reader) (*Candidates, error) {
	d := json.NewDecoder(r)
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("entity2id must be an object")
	}
	c := &Candidates{ByID: map[int]string{}}
	for d.More() {
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		var id int
		if e = d.Decode(&id); e != nil {
			return nil, e
		}
		c.ByID[id] = MovieName(t.(string))
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	for _, v := range c.ByID {
		c.Names = append(c.Names, v)
	}
	slices.Sort(c.Names)
	c.Names = slices.Compact(c.Names)
	return c, nil
}
func (c *Candidates) Score(pred string, gt []string) (Processed, error) {
	out := Processed{Metrics: map[string]float64{}}
	if len(gt) == 0 {
		return out, fmt.Errorf("empty recommendation ground truths")
	}
	matches, _, e := Recommendations(pred, c.Names)
	if e != nil {
		return out, e
	}
	names := []string{}
	for _, m := range matches {
		names = append(names, m.Nearest)
	}
	for _, s := range gt {
		id, e := strconv.Atoi(pytext.Strip(s))
		if e != nil {
			return out, e
		}
		name, ok := c.ByID[id]
		if !ok {
			return out, fmt.Errorf("unknown movie id %d", id)
		}
		out.GTMovies = append(out.GTMovies, name)
	}
	out.Parsed = names
	for _, k := range []int{1, 5, 10} {
		n := 0
		for _, g := range out.GTMovies {
			if slices.Contains(names[:min(k, len(names))], g) {
				n++
			}
		}
		out.Metrics[fmt.Sprintf("recsys_recall@%d", k)] = float64(n) / float64(len(gt))
	}
	return out, nil
}
