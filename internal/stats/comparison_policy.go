package stats

import (
	"encoding/json"
	"fmt"
	"memeval/internal/result"
	"reflect"
	"slices"
	"strings"
)

// Only this explicit set of reader fields constrains pairing. All other fields
// describe implementation/capability details and remain visible as differences.
var readerPairingKeys = []string{
	"model", "provider", "prompt_id", "template_sha256", "input_mode",
	"max_tokens", "official_max_tokens", "temperature", "fullcontext",
	"context_budget_chars", "upstream_search_limit",
}

func compareReader(left, right any) ([]string, error) {
	if reflect.DeepEqual(left, right) {
		return nil, nil
	}
	decode := func(v any) (map[string]any, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		err = json.Unmarshal(b, &m)
		return m, err
	}
	a, err := decode(left)
	if err != nil {
		return nil, fmt.Errorf("protocol differs: reader: %w", err)
	}
	b, err := decode(right)
	if err != nil {
		return nil, fmt.Errorf("protocol differs: reader: %w", err)
	}
	equal := func(key string) bool {
		x, hasX := a[key]
		y, hasY := b[key]
		return hasX == hasY && reflect.DeepEqual(x, y)
	}
	for _, key := range readerPairingKeys {
		if !equal(key) {
			return nil, fmt.Errorf("protocol differs: reader.%s", key)
		}
	}
	keys := make([]string, 0, len(a)+len(b))
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	show := func(m map[string]any, key string) string {
		v, ok := m[key]
		if !ok {
			return "<absent>"
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	var differences []string
	for _, key := range keys {
		if !equal(key) {
			differences = append(differences, fmt.Sprintf("reader.%s (disclosure): %s -> %s", key, show(a, key), show(b, key)))
		}
	}
	return differences, nil
}

type statusExclusion struct {
	allowed           map[result.Status]bool
	counts            *result.ExcludedByStatus
	left, right, both int // Disjoint sample counts (both status entries may overlap).
}

func newStatusExclusion(raw string) (*statusExclusion, error) {
	p := &statusExclusion{allowed: map[result.Status]bool{}}
	if raw == "" {
		return p, nil
	}
	for _, s := range strings.Split(raw, ",") {
		status := result.Status(strings.TrimSpace(s))
		if status != result.Invalid && status != result.Unscored {
			return nil, fmt.Errorf("exclude-status accepts only invalid,unscored (got %q); do not drop failed cases", s)
		}
		p.allowed[status] = true
	}
	p.counts = &result.ExcludedByStatus{Left: map[result.Status]int{}, Right: map[result.Status]int{}, Both: map[result.Status]int{}}
	return p, nil
}

func (p *statusExclusion) excludes(left, right result.Status) bool {
	return (p.allowed[left] && (right == result.Scored || p.allowed[right])) ||
		(p.allowed[right] && left == result.Scored)
}

func (p *statusExclusion) count(left, right result.Status) {
	switch {
	case p.allowed[left] && p.allowed[right]:
		p.both++
		p.counts.Both[left]++
		if right != left {
			p.counts.Both[right]++
		}
	case p.allowed[left]:
		p.left++
		p.counts.Left[left]++
	default:
		p.right++
		p.counts.Right[right]++
	}
}

func (p *statusExclusion) note() string {
	if p.counts == nil {
		return ""
	}
	statuses := make([]string, 0, len(p.allowed))
	for status := range p.allowed {
		statuses = append(statuses, string(status))
	}
	slices.Sort(statuses)
	return fmt.Sprintf("; explicit exclude-status=%s: exclude case×repeat when an allowed status faces scored, or both statuses are allowed; excluded %d samples (left=%d, right=%d, both=%d); both status counts overlap when statuses differ; delta and bootstrap use retained pairs only, not the full-run estimand", strings.Join(statuses, ","), p.left+p.right+p.both, p.left, p.right, p.both)
}
