// Package halumem renders eval_memzero.search_memory and PROMPT_MEMZERO.
package halumem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	score "memeval/internal/scoring/halumem"
	"memeval/internal/suite"
	"strings"
	"unicode/utf16"
)

const PromptID = "halumem/memzero-reader-v1"

func Timestamp(item protocol.Item, known map[string]suite.Event) string {
	if item.Timestamp != "" {
		return item.Timestamp
	}
	// Traceable event timestamps replace unavailable provider created_at, never wall time.
	ts := ""
	for _, id := range item.SourceIDs {
		if e, ok := known[id]; ok && e.Timestamp > ts {
			ts = e.Timestamp
		}
	}
	return ts
}
func Lines(items []protocol.Item, known map[string]suite.Event) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, Timestamp(item, known)+": "+item.Text)
	}
	return out
}
func pythonJSON(v []string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "    ")
	_ = e.Encode(v)
	var out strings.Builder
	for _, r := range strings.TrimSuffix(b.String(), "\n") {
		if r > 127 {
			if r > 0xffff {
				a, b := utf16.EncodeRune(r)
				fmt.Fprintf(&out, "\\u%04x\\u%04x", a, b)
			} else {
				fmt.Fprintf(&out, "\\u%04x", r)
			}
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
func Build(items []protocol.Item, known map[string]suite.Event, user, question string) []llm.Message {
	context := "Memories for user " + user + ":\n\n    " + pythonJSON(Lines(items, known)) + "\n"
	return []llm.Message{{Role: "user", Content: score.Format(score.Templates["PROMPT_MEMZERO"], map[string]string{"context": context, "question": question})}}
}
func Answer(ctx context.Context, c llm.Client, items []protocol.Item, known map[string]suite.Event, user, question, provider, model, sample string, maxTokens int) (llm.Response, error) {
	temp := 0.
	resp, e := c.Complete(ctx, llm.Request{Purpose: "reader", Provider: provider, Model: model, SampleID: sample, TemplateID: PromptID + "@" + score.SHA256(), Messages: Build(items, known, user, question), Temperature: &temp, MaxTokens: &maxTokens, N: 1})
	for _, i := range items {
		if i.Timestamp == "" {
			resp.Deviations = append(resp.Deviations, "HaluMem created_at unavailable: latest cited event timestamp used (empty if unavailable)")
			break
		}
	}
	return resp, e
}
