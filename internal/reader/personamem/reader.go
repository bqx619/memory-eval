package personamem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/reader"
	"memeval/internal/suite"
	"unicode/utf8"
)

const Instructions = "Find the most appropriate model response and give your final answer (a), (b), (c), or (d) after the special token <final_answer>."
const PromptID = "personamem-v1-reader-v1"

func TemplateSHA256() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(Instructions))) }

type Input struct {
	Messages        []llm.Message
	Truncated       bool
	DroppedMessages int
}

func Build(items []protocol.Item, known map[string]suite.Event, question, rawOptions string, maxChars int) (Input, error) {
	if maxChars < 0 || rawOptions == "" {
		return Input{}, fmt.Errorf("PersonaMem reader requires original options_raw and nonnegative budget")
	}
	events, err := reader.Sources(items, known)
	if err != nil {
		return Input{}, err
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
	messages = append(messages, llm.Message{Role: "user", Content: question + "\n\n" + Instructions + "\n\n" + rawOptions})
	return Input{messages, drop > 0, drop}, nil
}
func Answer(ctx context.Context, client llm.Client, in Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	return client.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID, Messages: in.Messages, MaxTokens: &maxTokens, N: 1})
}
