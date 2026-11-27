package locomo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/reader"
	"memeval/internal/suite"
	"sort"
	"strings"
	"unicode/utf8"
)

const StartTemplate = "Below is a conversation between two people: {} and {}. The conversation takes place over multiple days and the date of each conversation is wriiten at the beginning of the conversation.\n\n"
const QATemplate = "\nBased on the above context, write an answer in the form of a short phrase for the following question. Answer with exact words from the context whenever possible.\n\nQuestion: {} Short answer:\n"
const Cat5Template = "\nBased on the above context, answer the following question.\n\nQuestion: {} Short answer:\n"
const PromptID = "locomo-text-only-reader-v1"

func TemplateSHA256() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(StartTemplate+QATemplate+Cat5Template+TemporalSuffix+Cat5Suffix)))
}

type Input struct {
	Prompt          string
	Truncated       bool
	DroppedSessions int
	QuestionAudit   QuestionAudit
	Deviations      []string
}

func Build(items []protocol.Item, known map[string]suite.Event, question string, category int, speakers []string, maxChars int, options ...QuestionOptions) (Input, error) {
	if len(speakers) != 2 || maxChars < 0 {
		return Input{}, fmt.Errorf("LoCoMo reader requires original speaker pair and nonnegative budget")
	}
	if len(options) > 1 {
		return Input{}, fmt.Errorf("LoCoMo reader accepts one question options value")
	}
	opt := QuestionOptions{}
	if len(options) == 1 {
		opt = options[0]
	}
	question, audit, deviations, err := prepareQuestion(question, category, opt)
	if err != nil {
		return Input{}, err
	}
	events, err := reader.Sources(items, known)
	if err != nil {
		return Input{}, err
	}
	type session struct {
		events      []suite.Event
		date, stamp string
	}
	ss := []session{}
	indexes := map[string]int{}
	for _, e := range events {
		i, ok := indexes[e.Session]
		if !ok {
			i = len(ss)
			indexes[e.Session] = i
			ss = append(ss, session{date: e.TimeRaw, stamp: e.Timestamp})
		}
		ss[i].events = append(ss[i].events, e)
	}
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].stamp == "" || ss[j].stamp == "" {
			return reader.Order(ss[i].events[0]) < reader.Order(ss[j].events[0])
		}
		return ss[i].stamp < ss[j].stamp
	})
	render := func() string {
		var b strings.Builder
		for _, s := range ss {
			b.WriteString("DATE: " + s.date + "\nCONVERSATION:\n")
			for _, e := range s.events {
				speaker, _ := e.Attributes["speaker"].(string)
				b.WriteString(speaker + " said, \"" + e.Content + "\"\n\n")
			}
		}
		return b.String()
	}
	history := render()
	dropped := 0
	for utf8.RuneCountInString(history) > maxChars && len(ss) > 0 {
		ss = ss[1:]
		dropped++
		history = render()
	}
	start := strings.Split(StartTemplate, "{}")
	qa := QATemplate
	if category == 5 {
		qa = Cat5Template
	}
	parts := strings.Split(qa, "{}")
	return Input{Prompt: start[0] + speakers[0] + start[1] + speakers[1] + start[2] + history + "\n\n" + parts[0] + question + parts[1], Truncated: dropped > 0, DroppedSessions: dropped, QuestionAudit: audit, Deviations: deviations}, nil
}
func Answer(ctx context.Context, client llm.Client, in Input, provider, model, sample string, maxTokens int) (llm.Response, error) {
	temp := 0.
	r, e := client.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID, Messages: []llm.Message{{Role: "user", Content: in.Prompt}}, Temperature: &temp, MaxTokens: &maxTokens, N: 1})
	return r, e
}
