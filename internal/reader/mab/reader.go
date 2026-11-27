// Package mab renders the literal MAB reader templates over selected sources or item text.
package mab

import (
	"context"
	"fmt"
	templates "memeval/internal/benchmark/mab"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"slices"
	"strings"
	"unicode/utf8"
)

const PromptID = "memoryagentbench/reader-v1"

type Input struct {
	Messages        []llm.Message
	Truncated       bool
	DroppedMessages int
}

func Build(items []protocol.Item, known map[string]suite.Event, source, question, mode string, full bool, maxChars int) (Input, error) {
	if maxChars < 0 || mode != "sources" && mode != "items" {
		return Input{}, fmt.Errorf("invalid reader input/budget")
	}
	t, e := templates.For(source)
	if e != nil {
		return Input{}, e
	}
	events := []suite.Event{}
	seen := map[string]bool{}
	if mode == "items" {
		for _, item := range items {
			events = append(events, suite.Event{Content: item.Text})
		}
	} else if full {
		for _, event := range known {
			events = append(events, event)
		}
		slices.SortFunc(events, func(a, b suite.Event) int { return sequence(a) - sequence(b) })
	} else {
		for _, item := range items {
			for _, id := range item.SourceIDs {
				ev, ok := known[id]
				if !ok {
					return Input{}, fmt.Errorf("unknown reader source")
				}
				if !seen[id] {
					seen[id] = true
					events = append(events, ev)
				}
			}
		}
	}
	// Source expansion retains its historical earliest-block truncation rule.
	// Items keep returned rank/text, dropping whole low-ranked items at the tail.
	// Newline separators count toward the memory budget; question/template do not.
	chars := func() int {
		n := max(0, len(events)-1)
		for _, ev := range events {
			n += utf8.RuneCountInString(ev.Content)
		}
		return n
	}
	dropped := 0
	for len(events) > 0 && chars() > maxChars {
		remove := len(events) - 1
		if mode == "sources" {
			remove = 0
			for i := range events {
				if sequence(events[i]) < sequence(events[remove]) {
					remove = i
				}
			}
		}
		events = slices.Delete(events, remove, remove+1)
		dropped++
	}
	texts := []string{}
	for _, ev := range events {
		texts = append(texts, ev.Content)
	}
	kind := "rag_agent"
	if full {
		kind = "long_context_agent"
	}
	query := templates.Format(t.Query[kind], map[string]string{"question": question})
	return Input{[]llm.Message{{Role: "system", Content: t.System}, {Role: "user", Content: strings.Join(texts, "\n") + "\n\n" + query}}, dropped > 0, dropped}, nil
}
func sequence(e suite.Event) int { n, _ := e.Attributes["memeval_reader_sequence"].(int); return n }
func Answer(ctx context.Context, c llm.Client, in Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	return c.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID + "@" + templates.TemplateSHA256(), Messages: in.Messages, MaxTokens: &maxTokens, N: 1})
}
