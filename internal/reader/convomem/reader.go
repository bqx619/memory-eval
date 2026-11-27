// Package convomem mirrors LongContextMemoryAnswerer and MemoryPromptUtils.
package convomem

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/reader"
	score "memeval/internal/scoring/convomem"
	"memeval/internal/suite"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const PromptID = "convomem/reader-v1"

type Input struct {
	Messages        []llm.Message
	Truncated       bool
	DroppedMessages int
	Deviations      []string
}

func Build(items []protocol.Item, known map[string]suite.Event, c suite.Case, full bool, maxChars int) (Input, error) {
	in := Input{Deviations: []string{}}
	if maxChars < 0 {
		return in, fmt.Errorf("negative context budget")
	}
	events := []suite.Event{}
	var e error
	if full {
		for _, ev := range known {
			events = append(events, ev)
		}
		slices.SortFunc(events, func(a, b suite.Event) int { return reader.Order(a) - reader.Order(b) })
	} else {
		events, e = reader.Sources(items, known)
		if e != nil {
			return in, e
		}
	}
	render := func(events []suite.Event) (string, error) {
		blocks := []string{}
		previous := ""
		for _, ev := range events {
			n, e := strconv.Atoi(strings.TrimPrefix(ev.Session, "c"))
			if e != nil || n < 0 || ev.Session != "c"+strconv.Itoa(n) {
				return "", fmt.Errorf("invalid ConvoMem session")
			}
			speaker := map[string]string{"user": "User", "assistant": "Assistant"}[ev.Role]
			if speaker == "" {
				return "", fmt.Errorf("invalid ConvoMem role")
			}
			if ev.Session != previous {
				blocks = append(blocks, fmt.Sprintf("Conversation %d:", n+1))
				previous = ev.Session
			}
			blocks[len(blocks)-1] += "\n" + speaker + ": " + ev.Content
		}
		return strings.Join(blocks, "\n\n"), nil
	}
	history, e := render(events)
	if e != nil {
		return in, e
	}
	for utf8.RuneCountInString(history) > maxChars && len(events) > 0 {
		events = events[1:]
		in.DroppedMessages++
		history, e = render(events)
		if e != nil {
			return in, e
		}
	}
	in.Truncated = in.DroppedMessages > 0
	if in.Truncated {
		in.Deviations = append(in.Deviations, "ConvoMem additional framework character budget drops oldest whole messages; retained conversation numbers are original")
	}
	if len(events) == 0 {
		return in, fmt.Errorf("ConvoMem LongContextMemoryAnswerer has no conversations: no answer, no reader call")
	}
	in.Messages = []llm.Message{{Role: "user", Content: score.Render("reader", map[string]string{"question": c.Query, "conversationContext": history})}}
	return in, nil
}

// Upstream Utils.retry with maxRetries=20 permits 21 reader attempts.
const maxAttempts = 21

func Answer(ctx context.Context, c llm.Client, in Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	var r llm.Response
	var e error
	for i := 0; i < maxAttempts; i++ {
		if e = ctx.Err(); e != nil {
			return r, e
		}
		r, e = c.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: fmt.Sprintf("%s:reader:%d", sample, i), TemplateID: PromptID + "@" + score.SHA256(), Messages: in.Messages, MaxTokens: &maxTokens, N: 1})
		var empty *reader.EmptyResponseError
		if e == nil || errors.As(e, &empty) {
			break
		}
	}
	r.Deviations = append(r.Deviations, in.Deviations...)
	return r, e
}
