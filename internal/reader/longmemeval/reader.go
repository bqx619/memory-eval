package longmemeval

import (
	"context"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Python str.strip additionally treats U+001C..U+001F as whitespace.
func strip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
}
func Date(timestamp, raw string) string {
	if t, e := time.Parse(time.RFC3339Nano, timestamp); e == nil {
		return t.UTC().Format("2006/01/02 (Mon) 15:04")
	}
	if raw != "" {
		return raw
	}
	return "unknown"
}

type session struct {
	key, date, stamp, text string
	events                 []suite.Event
}
type Input struct {
	History         string `json:"history"`
	Prompt          string `json:"prompt"`
	Truncated       bool   `json:"truncated"`
	DroppedSessions int    `json:"dropped_sessions"`
	Sessions        int    `json:"sessions"`
}

// Build uses only selected public citations and their original event payloads.
// Gold, private question IDs and evidence annotations never enter the reader.
func Build(items []protocol.Item, known map[string]suite.Event, question, queryTime, rawDate, mode string, maxChars int) (Input, error) {
	if mode != "sources" && mode != "items" || maxChars < 0 {
		return Input{}, fmt.Errorf("invalid reader input/budget")
	}
	sessions := []session{}
	if mode == "sources" {
		groups := map[string]int{}
		seen := map[string]bool{}
		for _, item := range items {
			for _, id := range item.SourceIDs {
				ev, ok := known[id]
				if !ok {
					return Input{}, fmt.Errorf("reader source does not resolve")
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				key := ev.Session
				if key == "" {
					key = ev.SourceID
				}
				index, ok := groups[key]
				if !ok {
					index = len(sessions)
					groups[key] = index
					sessions = append(sessions, session{key: key, date: Date(ev.Timestamp, ev.TimeRaw), stamp: ev.Timestamp})
				}
				s := &sessions[index]
				s.events = append(s.events, ev)
				if ev.Timestamp != "" && (s.stamp == "" || ev.Timestamp < s.stamp) {
					s.stamp = ev.Timestamp
					s.date = Date(ev.Timestamp, ev.TimeRaw)
				}
			}
		}
		// The runner supplies the original prefix sequence independently of
		// retrieval ranking and opaque public IDs.
		for i := range sessions {
			s := &sessions[i]
			sort.SliceStable(s.events, func(i, j int) bool { return eventOrder(s.events[i]) < eventOrder(s.events[j]) })
			for _, ev := range s.events {
				s.text += "\n\n" + ev.Role + ": " + strip(ev.Content)
			}
		}
	} else {
		for _, item := range items {
			s := session{date: "unknown", text: item.Text}
			for _, id := range item.SourceIDs {
				ev, ok := known[id]
				if !ok {
					return Input{}, fmt.Errorf("reader source does not resolve")
				}
				if ev.Timestamp != "" && (s.stamp == "" || ev.Timestamp < s.stamp) {
					s.stamp = ev.Timestamp
					s.date = Date(ev.Timestamp, ev.TimeRaw)
				}
			}
			sessions = append(sessions, s)
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].stamp == "" {
			return false
		}
		if sessions[j].stamp == "" {
			return true
		}
		if sessions[i].stamp == sessions[j].stamp {
			return sessions[i].key < sessions[j].key
		}
		return sessions[i].stamp < sessions[j].stamp
	})
	render := func(ss []session) string {
		var b strings.Builder
		for i, s := range ss {
			fmt.Fprintf(&b, "\n### Session %d:\nSession Date: %s\nSession Content:\n%s\n", i+1, s.date, s.text)
		}
		return b.String()
	}
	history := render(sessions)
	dropped := 0
	for utf8.RuneCountInString(history) > maxChars && len(sessions) > 0 {
		sessions = sessions[1:]
		dropped++
		history = render(sessions)
	}
	parts := strings.Split(Template, "{}")
	return Input{History: history, Prompt: parts[0] + history + parts[1] + Date(queryTime, rawDate) + parts[2] + question + parts[3], Truncated: dropped > 0, DroppedSessions: dropped, Sessions: len(sessions)}, nil
}
func eventOrder(e suite.Event) int {
	switch x := e.Attributes["memeval_reader_sequence"].(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}
func Answer(ctx context.Context, client llm.Client, input Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	temp := 0.
	response, err := client.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID, Messages: []llm.Message{{Role: "user", Content: input.Prompt}}, Temperature: &temp, MaxTokens: &maxTokens, N: 1})
	response.Text = strip(response.Text) // run_generation.py strips the hypothesis before writing it.
	return response, err
}

// Free-text upstream output has no structured abstain field: record only an
// explicit literal marker; natural-language abstention is evaluated by judge.
func ExplicitAbstain(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "abstain") || strings.EqualFold(strings.TrimSpace(text), "[abstain]")
}
