// Package memora mirrors both upstream reader tracks; model tables are pinned
// declarations, never claims about current provider capabilities.
package memora

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	score "memeval/internal/scoring/memora"
	"memeval/internal/suite"
	"memeval/internal/tokenizer"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const PromptID = "memora/reader-v1"
const NoMemories = "I don't have enough information in my memory to answer this question accurately."
const HardCap = 200000

var encodings sync.Map

func CountTokens(s, model string) int {
	name := "cl100k_base"
	m := strings.ToLower(model)
	if strings.Contains(m, "gpt-3") && !strings.Contains(m, "gpt-3.5") && !strings.Contains(m, "gpt-4") && !strings.Contains(m, "gpt-5") {
		name = "p50k_base"
	}
	// A cached closure initializes each encoding once, with no network loader.
	value, _ := encodings.LoadOrStore(name, sync.OnceValue(func() func(string) int {
		enc, e := tokenizer.Encoding(name)
		if e != nil {
			return func(s string) int { return utf8.RuneCountInString(s) / 4 }
		}
		return func(s string) (n int) {
			defer func() {
				if recover() != nil {
					n = utf8.RuneCountInString(s) / 4
				}
			}()
			return len(enc.Encode(s, nil, []string{"all"}))
		}
	}))
	return value.(func() func(string) int)()(s)
}

type limit struct {
	Name   string
	Tokens int
}

var limits = func() []limit {
	var rows [][]json.RawMessage
	if e := json.Unmarshal(score.ContextLimitsJSON, &rows); e != nil {
		panic(e)
	}
	out := []limit{}
	for _, r := range rows {
		var l limit
		json.Unmarshal(r[0], &l.Name)
		json.Unmarshal(r[1], &l.Tokens)
		out = append(out, l)
	}
	return out
}()

func RawLimit(model string) int {
	for _, l := range limits {
		if l.Name == model {
			return l.Tokens
		}
	}
	base := model[strings.LastIndex(model, "/")+1:]
	for _, l := range limits {
		if strings.Contains(l.Name, base) || strings.Contains(base, l.Name) {
			return l.Tokens
		}
	}
	return 0
}
func ContextLimit(model string) int {
	n := RawLimit(model)
	base := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	if n == 0 {
		switch {
		case strings.Contains(base, "gpt-5"):
			n = 400000
		case strings.Contains(base, "gpt-4"):
			n = 128000
		case strings.Contains(base, "gpt-3.5"):
			n = 16385
		case strings.Contains(base, "claude-sonnet-4"):
			n = 1000000
		case strings.Contains(base, "claude"):
			n = 200000
		case strings.Contains(base, "gemini-3"):
			n = 1048576
		case strings.Contains(base, "gemini"):
			n = 1000000
		case strings.Contains(base, "qwen"):
			n = 40960
		}
	}
	if n == 0 {
		return 0
	}
	return max(0, min(int(float64(n)*.7)-10000, HardCap))
}

type Session struct {
	ID    int           `json:"id"`
	Date  string        `json:"date"`
	Turns []llm.Message `json:"turns"`
}
type ContextMetadata struct {
	Total     int  `json:"total_conversations"`
	Included  int  `json:"included_conversations"`
	Omitted   int  `json:"truncated_conversations"`
	Tokens    int  `json:"total_tokens"`
	Truncated bool `json:"was_truncated"`
}

// ConversationContext preserves even the upstream double header-token reserve
// and first-turn partial-session selection; maxTokens=0 selects model defaults.
func ConversationContext(sessions []Session, model string, maxTokens int) (string, ContextMetadata) {
	return conversationContext(sessions, model, maxTokens, func(s string) int { return CountTokens(s, model) })
}
func conversationContext(sessions []Session, model string, maxTokens int, count func(string) int) (string, ContextMetadata) {
	meta := ContextMetadata{Total: len(sessions)}
	if maxTokens == 0 {
		maxTokens = ContextLimit(model)
	}
	header := score.Templates["full_header"]
	current := count(header)
	available := 0
	if maxTokens != 0 {
		available = maxTokens - current
	}
	type included struct {
		id   int
		text string
	}
	kept := []included{}
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		head := fmt.Sprintf("\n## Session %d - %s\n", s.ID, s.Date)
		parts := []string{head}
		for _, t := range s.Turns {
			if t.Role == "user" {
				parts = append(parts, "User: "+t.Content)
			} else if t.Role == "assistant" {
				parts = append(parts, "Assistant: "+t.Content)
			}
		}
		parts = append(parts, "")
		text := strings.Join(parts, "\n")
		n := count(text)
		if maxTokens == 0 || current+n <= available {
			kept = append(kept, included{s.ID, text})
			current += n
			meta.Included++
		} else {
			if available != 0 && current < available {
				remaining := available - current
				if n > remaining && remaining > 100 {
					partial := head
					pn := count(partial)
					for _, t := range s.Turns {
						role := "User"
						if t.Role == "assistant" {
							role = "Assistant"
						}
						turn := role + ": " + t.Content + "\n"
						tn := count(turn)
						if pn+tn <= remaining {
							partial += turn
							pn += tn
						} else {
							break
						}
					}
					if pn > count(strings.Split(partial, "\n")[0]) {
						partial += "\n[Conversation truncated due to token limit...]"
						kept = append(kept, included{s.ID, partial})
						current += pn
						meta.Included++
						meta.Truncated = true
					}
				}
			}
			meta.Omitted++
			meta.Truncated = true
		}
	}
	slices.SortFunc(kept, func(a, b included) int { return a.id - b.id })
	parts := []string{header}
	if meta.Truncated {
		note := fmt.Sprintf("[Note: Showing %d of %d conversations due to token limits. Most recent conversations are prioritized.]\n\n", meta.Included, meta.Total)
		parts = append(parts, note)
		current += count(note)
	}
	for _, v := range kept {
		parts = append(parts, v.text)
	}
	meta.Tokens = current
	return strings.Join(parts, "\n"), meta
}
func QuestionPrompt(context, query, date, model string) ([]llm.Message, bool) {
	return questionPrompt(context, query, date, model, func(s string) int { return CountTokens(s, model) })
}
func questionPrompt(context, query, date, model string, count func(string) int) ([]llm.Message, bool) {
	dc := ""
	if date != "" {
		dc = strings.ReplaceAll(score.Templates["full_date_context"], "{current_date}", date)
	}
	system := strings.ReplaceAll(score.Templates["full_system_prompt"], "{date_context}", dc)
	template := "\n---\n\n# Current Question\nUser: " + query + "\n\nBased on the conversation history above, please provide your answer:"
	overhead := count(system) + count(template)
	ct := count(context)
	limit := RawLimit(model)
	hard, over := ct > HardCap, limit > 0 && overhead+ct > limit
	truncated := false
	if hard || over {
		available := limit - overhead - int(float64(limit)*.3) - 5000
		if hard {
			available = HardCap - overhead - 20000
		}
		available = max(available, 1000)
		marker := "\n## Session "
		pos := strings.Index(context, marker)
		if available < ct && pos > 0 {
			header := context[:pos]
			convs := context[pos:]
			room := max(available-count(header), 500)
			if count(convs) > room {
				runes := []rune(convs)
				keep := room * 4
				at := len(runes) - keep
				start, end := max(0, at-5000), max(0, min(len(runes), at+1000))
				found := -1
				if start <= end {
					if v := strings.LastIndex(string(runes[start:end]), marker); v >= 0 {
						found = start + utf8.RuneCountInString(string(runes[start:end])[:v])
					}
				}
				if found > 0 {
					convs = string(runes[found:])
				} else {
					convs = string(runes[max(0, len(runes)-keep):])
				}
				context = header + "\n[Note: Additional truncation applied to fit within model context limits. Showing most recent conversations only.]\n" + convs
				truncated = true
			}
		}
	}
	user := strings.NewReplacer("{context}", context, "{question}", query).Replace(score.Templates["full_user_prompt"])
	return []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, truncated
}

type Input struct {
	FullContext     bool
	Messages        []llm.Message
	FixedAnswer     string
	Truncated       bool
	DroppedSessions int
	Deviations      []string
}

func Build(items []protocol.Item, known map[string]suite.Event, query, date, model string, full bool, maxChars int) (Input, error) {
	if maxChars < 0 {
		return Input{}, fmt.Errorf("negative reader budget")
	}
	in := Input{FullContext: full, Deviations: []string{}}
	if !full {
		if len(items) == 0 {
			in.FixedAnswer = NoMemories
			return in, nil
		}
		lines := []string{}
		for i, item := range items {
			score := 0.
			if item.Score != nil {
				score = *item.Score
			} else {
				in.Deviations = append(in.Deviations, "Memora memory relevance null rendered as 0.00")
			}
			lines = append(lines, fmt.Sprintf("%d. %s (relevance: %.2f)", i+1, item.Text, score))
		}
		user := strings.NewReplacer("{question}", query, "{memory_text}", strings.Join(lines, "\n")).Replace(score.Templates["sources_user_prompt"])
		in.Messages = []llm.Message{{Role: "system", Content: score.Templates["sources_system_prompt"]}, {Role: "user", Content: user}}
		return in, nil
	}
	events := []suite.Event{}
	for _, e := range known {
		events = append(events, e)
	}
	slices.SortFunc(events, func(a, b suite.Event) int {
		x, _ := a.Attributes["memeval_reader_sequence"].(int)
		y, _ := b.Attributes["memeval_reader_sequence"].(int)
		return x - y
	})
	sessions := []Session{}
	for _, e := range events {
		id, err := strconv.Atoi(strings.TrimPrefix(e.Session, "s"))
		if err != nil {
			return in, fmt.Errorf("Memora session identity malformed")
		}
		if len(sessions) == 0 || sessions[len(sessions)-1].ID != id {
			sessions = append(sessions, Session{ID: id, Date: e.TimeRaw, Turns: []llm.Message{}})
		}
		s := &sessions[len(sessions)-1]
		s.Turns = append(s.Turns, llm.Message{Role: e.Role, Content: e.Content})
	}
	// Framework character budget is additional to upstream token limits. Drop
	// oldest complete sessions first, then oldest turns of the final session.
	chars := 0
	for _, s := range sessions {
		for _, t := range s.Turns {
			chars += utf8.RuneCountInString(t.Content)
		}
	}
	for chars > maxChars && len(sessions) > 0 {
		s := &sessions[0]
		if len(sessions) > 1 {
			for _, t := range s.Turns {
				chars -= utf8.RuneCountInString(t.Content)
			}
			sessions = sessions[1:]
			in.DroppedSessions++
		} else if len(s.Turns) > 0 {
			chars -= utf8.RuneCountInString(s.Turns[0].Content)
			s.Turns = s.Turns[1:]
		} else {
			sessions = nil
		}
		in.Truncated = true
	}
	if in.Truncated {
		in.Deviations = append(in.Deviations, "Memora framework character budget additionally drops oldest sessions/turns before upstream token truncation")
	}
	context, meta := ConversationContext(sessions, model, 0)
	messages, trunc := QuestionPrompt(context, query, date, model)
	in.Messages = messages
	in.Truncated = in.Truncated || meta.Truncated || trunc
	in.DroppedSessions += meta.Omitted
	return in, nil
}
func Answer(ctx context.Context, c llm.Client, in Input, provider, model, sample string, outputLimit int) (llm.Response, error) {
	if in.FixedAnswer != "" {
		return llm.Response{Text: in.FixedAnswer, Deviations: in.Deviations}, nil
	}
	temp, tokens := .7, 500
	var limit *int = &tokens
	if in.FullContext {
		limit = nil
		if provider == "anthropic" {
			limit = &outputLimit
			in.Deviations = append(in.Deviations, "Memora Track 1 leaves output limit unset; Anthropic requires configured max_tokens")
		}
	}
	r, e := c.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID + "@" + score.SHA256(), Messages: in.Messages, Temperature: &temp, MaxTokens: limit, N: 1})
	if e == nil && !in.FullContext {
		r.Text = strings.TrimSpace(r.Text)
	}
	if e == nil && in.FullContext && strings.TrimSpace(r.Text) == "" {
		e = fmt.Errorf("Memora Track 1 empty response")
	}
	r.Deviations = append(r.Deviations, in.Deviations...)
	return r, e
}
