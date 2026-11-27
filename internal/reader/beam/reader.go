package beam

import (
	"context"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/reader"
	templates "memeval/internal/scoring/judge/beam"
	"memeval/internal/suite"
	"slices"
	"strings"
	"unicode/utf8"
)

const PromptID = "beam/reader-v1"

type Input struct {
	Messages        []llm.Message
	Truncated       bool
	DroppedMessages int
}

func Build(items []protocol.Item, known map[string]suite.Event, query, mode string, full bool, maxChars int) (Input, error) {
	if maxChars < 0 {
		return Input{}, fmt.Errorf("negative budget")
	}
	if mode != "sources" && mode != "items" || mode == "items" && full {
		return Input{}, fmt.Errorf("invalid BEAM reader input/fullcontext combination")
	}
	note := llm.Message{Role: "user", Content: strings.ReplaceAll(templates.Templates["reader_note"], "{query}", query)}
	if mode == "items" {
		// Memory items have rank and text, not original turn roles/chronology.
		// Join verbatim text with newlines; budget includes these separators.
		texts := make([]string, len(items))
		chars := max(0, len(items)-1)
		for i, item := range items {
			texts[i] = item.Text
			chars += utf8.RuneCountInString(item.Text)
		}
		for len(texts) > 0 && chars > maxChars {
			chars -= utf8.RuneCountInString(texts[len(texts)-1])
			texts = texts[:len(texts)-1]
			if len(texts) > 0 {
				chars--
			}
		}
		messages := []llm.Message{}
		if len(texts) > 0 {
			messages = append(messages, llm.Message{Role: "user", Content: strings.Join(texts, "\n")})
		}
		messages = append(messages, note)
		dropped := len(items) - len(texts)
		return Input{messages, dropped > 0, dropped}, nil
	}
	events := []suite.Event{}
	var err error
	if full {
		for _, e := range known {
			events = append(events, e)
		}
		slices.SortFunc(events, func(a, b suite.Event) int {
			x, _ := a.Attributes["memeval_reader_sequence"].(int)
			y, _ := b.Attributes["memeval_reader_sequence"].(int)
			return x - y
		})
	} else {
		events, err = reader.Sources(items, known)
		if err != nil {
			return Input{}, err
		}
	}
	n := 0
	for _, e := range events {
		n += utf8.RuneCountInString(e.Content)
	}
	drop := 0
	for n > maxChars && len(events) > 0 {
		n -= utf8.RuneCountInString(events[0].Content)
		events = events[1:]
		drop++
	}
	messages := []llm.Message{}
	for _, e := range events {
		messages = append(messages, llm.Message{Role: e.Role, Content: e.Content})
	}
	messages = append(messages, note)
	return Input{messages, drop > 0, drop}, nil
}
func Answer(ctx context.Context, c llm.Client, in Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	temp := 0.
	return c.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID + "@" + templates.SHA256(), Messages: in.Messages, Temperature: &temp, MaxTokens: &maxTokens, N: 1})
}
