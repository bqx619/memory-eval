// Package prefeval mirrors upstream Claude-style generation and MCQ messages.
package prefeval

import (
	"context"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/reader"
	score "memeval/internal/scoring/prefeval"
	"memeval/internal/suite"
	"slices"
	"strings"
	"unicode/utf8"
)

const PromptID = "prefeval/reader-v1"

type Input struct {
	Messages        []llm.Message
	Truncated       bool
	DroppedMessages int
	MCQ             bool
	Deviations      []string
}

func FormatOptions(options map[string]string) (string, error) {
	parts := []string{}
	if len(options) != 4 {
		return "", fmt.Errorf("PrefEval requires four options")
	}
	for _, key := range []string{"A", "B", "C", "D"} {
		s, ok := options[key]
		if !ok {
			return "", fmt.Errorf("PrefEval missing option %s", key)
		}
		parts = append(parts, key+". "+s)
	}
	return strings.Join(parts, "\n"), nil
}
func Build(items []protocol.Item, known map[string]suite.Event, c suite.Case, full, remind bool, maxChars int) (Input, error) {
	in := Input{MCQ: c.Kind == "mcq", Deviations: []string{}}
	if maxChars < 0 {
		return in, fmt.Errorf("negative context budget")
	}
	events := []suite.Event{}
	var e error
	if full {
		for _, v := range known {
			events = append(events, v)
		}
		slices.SortFunc(events, func(a, b suite.Event) int { return reader.Order(a) - reader.Order(b) })
	} else {
		events, e = reader.Sources(items, known)
		if e != nil {
			return in, e
		}
	}
	chars := 0
	for _, ev := range events {
		chars += utf8.RuneCountInString(ev.Content)
	}
	for chars > maxChars && len(events) > 0 {
		chars -= utf8.RuneCountInString(events[0].Content)
		events = events[1:]
		in.DroppedMessages++
	}
	in.Truncated = in.DroppedMessages > 0
	if in.Truncated {
		in.Deviations = append(in.Deviations, "PrefEval framework character budget drops oldest whole messages, including preference if necessary")
	}
	in.Messages = []llm.Message{{Role: "system", Content: score.Templates["reader_system"]}}
	for _, ev := range events {
		in.Messages = append(in.Messages, llm.Message{Role: ev.Role, Content: ev.Content})
	}
	query := c.Query
	if remind {
		query += score.Templates["reminder"]
	}
	if in.MCQ {
		opts, e := FormatOptions(c.Options)
		if e != nil {
			return in, e
		}
		query += strings.ReplaceAll(score.Templates["mcq_question"], "{formatted_options}", opts)
	} else {
		query += strings.ReplaceAll(score.Templates["generation_suffix"], "{max_tokens}", "300")
	}
	in.Messages = append(in.Messages, llm.Message{Role: "user", Content: query})
	if in.MCQ {
		in.Messages = append(in.Messages, llm.Message{Role: "assistant", Content: "<choice>"})
	}
	return in, nil
}
func Answer(ctx context.Context, c llm.Client, in Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	// Prefill is negotiated explicitly at the reader boundary; the generic OpenAI
	// chat-completions adapter has no continuation capability. Anthropic accepts
	// assistant prefills through its messages wire; unsupported models may reject.
	prefill := in.MCQ && provider == "anthropic"
	if in.MCQ && !prefill {
		in.Messages = in.Messages[:len(in.Messages)-1]
		in.Deviations = append(in.Deviations, "PrefEval assistant <choice> prefill omitted: provider adapter has no assistant-continuation capability")
	}
	temp := 0.
	r, e := c.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID + "@" + score.SHA256(), Messages: in.Messages, Temperature: &temp, MaxTokens: &maxTokens, N: 1})
	if prefill && e == nil && !slices.Contains(r.Deviations, llm.AssistantPrefillOmitted) {
		r.Text = "<choice>" + r.Text
	}
	r.Deviations = append(r.Deviations, in.Deviations...)
	return r, e
}
