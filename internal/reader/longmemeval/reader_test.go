package longmemeval

import (
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"strings"
	"testing"
)

func TestVendoredTemplateAndNLHistory(t *testing.T) {
	b, err := os.ReadFile(testoracle.Upstream(t, "longmemeval/src/generation/run_generation.py"))
	if err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(Template, "\n", `\n`)
	for _, literal := range []string{"answer_prompt_template = '" + escaped + "'", `sess_string += "\n\n{}: {}".format(turn_entry['role'], turn_entry['content'].strip())`, `'\n### Session {}:\nSession Date: {}\nSession Content:\n{}\n'.format(i+1, chunk_date, sess_string)`} {
		if !strings.Contains(string(b), literal) {
			t.Fatal("vendored literal drift:", literal)
		}
	}
	known := map[string]suite.Event{
		"a": {SourceID: "a", Session: "old", Timestamp: "2023-01-02T03:04:00Z", Role: "user", Content: " \nhello\x1c", Attributes: map[string]any{"memeval_reader_sequence": 0}},
		"b": {SourceID: "b", Session: "old", Timestamp: "2023-01-02T03:04:00Z", Role: "assistant", Content: " world ", Attributes: map[string]any{"memeval_reader_sequence": 1}},
		"c": {SourceID: "c", Session: "new", Timestamp: "2024-01-02T03:04:00Z", Role: "user", Content: "next", Attributes: map[string]any{"memeval_reader_sequence": 2}},
	}
	items := []protocol.Item{{Text: "SUT TEXT MUST NOT ENTER SOURCES", SourceIDs: []string{"c", "b", "a", "b"}}}
	in, err := Build(items, known, "Question?", "2025-01-02T03:04:00Z", "", "sources", 10000)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n### Session 1:\nSession Date: 2023/01/02 (Mon) 03:04\nSession Content:\n\n\nuser: hello\n\nassistant: world\n\n### Session 2:\nSession Date: 2024/01/02 (Tue) 03:04\nSession Content:\n\n\nuser: next\n"
	if in.History != want || !strings.Contains(in.Prompt, "Current Date: 2025/01/02 (Thu) 03:04\nQuestion: Question?\nAnswer:") {
		t.Fatalf("%+v", in)
	}
	truncated, err := Build(items, known, "q", "", "", "sources", 110)
	if err != nil || !truncated.Truncated || truncated.DroppedSessions != 1 || strings.Contains(truncated.History, "hello") || !strings.Contains(truncated.History, "next") {
		t.Fatal(truncated, err)
	}
	zero, err := Build(items, known, "q", "", "", "sources", 0)
	if err != nil || zero.History != "" || zero.DroppedSessions != 2 {
		t.Fatal(zero, err)
	}
	itemIn, err := Build(items, known, "q", "", "", "items", 10000)
	if err != nil || !strings.Contains(itemIn.History, items[0].Text) || !strings.Contains(itemIn.History, "2023/01/02") {
		t.Fatal(itemIn, err)
	}
	unknown, err := Build([]protocol.Item{{Text: "raw", SourceIDs: []string{}}}, known, "q", "", "", "items", 1000)
	if err != nil || !strings.Contains(unknown.History, "Session Date: unknown") {
		t.Fatal(unknown, err)
	}
	if _, err := Build([]protocol.Item{{SourceIDs: []string{"missing"}}}, known, "q", "", "", "sources", 1000); err == nil {
		t.Fatal("unknown citation accepted")
	}
}
