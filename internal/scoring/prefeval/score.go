// Package prefeval mirrors PrefEval's four-probe aggregation and choice extraction.
package prefeval

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"strings"
)

//go:embed templates.json
var templateBytes []byte
var Templates = func() map[string]string {
	var m map[string]string
	if e := json.Unmarshal(templateBytes, &m); e != nil {
		panic(e)
	}
	return m
}()

func SHA256() string { return fmt.Sprintf("%x", sha256.Sum256(templateBytes)) }

var Probes = []string{"acknow", "violate", "helpful", "hallucinate"}

func firstTag(raw, tag string) *html.Node {
	root, e := html.Parse(strings.NewReader(raw))
	if e != nil {
		return nil
	}
	var walk func(*html.Node) *html.Node
	walk = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == tag {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v := walk(c); v != nil {
				return v
			}
		}
		return nil
	}
	return walk(root)
}

// BeautifulSoup Tag.string recurses through a sole child, unlike Tag.text.
func tagString(n *html.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	if n.Type == html.TextNode || n.Type == html.CommentNode {
		return n.Data, true
	}
	if n.FirstChild != nil && n.FirstChild.NextSibling == nil {
		return tagString(n.FirstChild)
	}
	return "", false
}
func ExtractChoice(raw string) string {
	s, ok := tagString(firstTag(raw, "choice"))
	if !ok {
		return ""
	}
	for _, r := range s {
		if r >= 'A' && r <= 'D' {
			return string(r)
		}
	}
	return ""
}

type Scores struct {
	Accuracy               bool `json:"preference_following_accuracy"`
	Unhelpful              bool `json:"unhelpful"`
	Inconsistent           bool `json:"inconsistent"`
	HallucinationViolation bool `json:"hallucination_of_preference_violation"`
	UnawareViolation       bool `json:"preference_unaware_violation"`
}

func Combine(answers map[string]string) Scores {
	has := func(k, s string) bool { return strings.Contains(strings.ToLower(answers[k]), s) }
	ack, viol, unhelpful := has("acknow", "yes"), has("violate", "yes"), has("helpful", "no")
	hall := ack && has("hallucinate", "yes")
	v := Scores{Unhelpful: unhelpful, Inconsistent: ack && !hall && viol && !unhelpful, HallucinationViolation: ack && hall && viol && !unhelpful, UnawareViolation: !ack && viol && !unhelpful}
	v.Accuracy = !(v.Unhelpful || v.Inconsistent || v.HallucinationViolation || v.UnawareViolation)
	return v
}
func (s Scores) Metrics() map[string]float64 {
	m := map[string]float64{}
	for k, v := range map[string]bool{"prefeval.preference_following_accuracy": s.Accuracy, "prefeval.error.unhelpful": s.Unhelpful, "prefeval.error.inconsistent": s.Inconsistent, "prefeval.error.hallucination_of_preference_violation": s.HallucinationViolation, "prefeval.error.preference_unaware_violation": s.UnawareViolation} {
		if v {
			m[k] = 1
		} else {
			m[k] = 0
		}
	}
	return m
}
