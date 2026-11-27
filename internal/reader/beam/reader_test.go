package beam

import (
	"fmt"
	"memeval/internal/protocol"
	templates "memeval/internal/scoring/judge/beam"
	"memeval/internal/suite"
	"reflect"
	"strings"
	"testing"
)

func TestItemsWholeTailBudgetAndLiteral(t *testing.T) {
	items := []protocol.Item{{Text: "🙂甲"}, {Text: "乙"}, {Text: "丙"}}
	for _, tc := range []struct {
		budget  int
		text    string
		dropped int
	}{{0, "", 3}, {1, "", 3}, {2, "🙂甲", 2}, {3, "🙂甲", 2}, {4, "🙂甲\n乙", 1}, {5, "🙂甲\n乙", 1}, {6, "🙂甲\n乙\n丙", 0}} {
		t.Run(fmt.Sprint(tc.budget), func(t *testing.T) {
			in, err := Build(items, nil, "Q{query}", "items", false, tc.budget)
			if err != nil || in.Truncated != (tc.dropped > 0) || in.DroppedMessages != tc.dropped {
				t.Fatal(in, err)
			}
			memory := in.Messages[:len(in.Messages)-1]
			if tc.text == "" {
				if len(memory) != 0 {
					t.Fatal("invented memory after dropping all items", memory)
				}
			} else if len(memory) != 1 || memory[0].Role != "user" || memory[0].Content != tc.text {
				t.Fatal("rank/text/Unicode budget changed", memory)
			}
			if in.Messages[len(in.Messages)-1].Content != strings.ReplaceAll(templates.Templates["reader_note"], "{query}", "Q{query}") {
				t.Fatal("official question template changed")
			}
		})
	}
	for _, tc := range []struct {
		mode   string
		full   bool
		budget int
	}{{"unknown", false, 10}, {"items", true, 10}, {"items", false, -1}} {
		if _, err := Build(nil, nil, "Q", tc.mode, tc.full, tc.budget); err == nil {
			t.Fatal("invalid input accepted", tc)
		}
	}
}

func TestChronologyBudgetAndLiteral(t *testing.T) {
	known := map[string]suite.Event{"a": {Role: "user", Content: "old", Attributes: map[string]any{"memeval_reader_sequence": 0}}, "b": {Role: "assistant", Content: "new", Attributes: map[string]any{"memeval_reader_sequence": 1}}}
	items := []protocol.Item{{SourceIDs: []string{"b", "a", "b"}}}
	in, e := Build(items, known, "Q{query}", "sources", false, 6)
	if e != nil || len(in.Messages) != 3 || in.Messages[0].Content != "old" || in.Messages[1].Role != "assistant" {
		t.Fatal(in, e)
	}
	if in.Messages[2].Content != strings.ReplaceAll(templates.Templates["reader_note"], "{query}", "Q{query}") {
		t.Fatal("literal")
	}
	full, e := Build(nil, known, "Q{query}", "sources", true, 6)
	if e != nil || !reflect.DeepEqual(full, in) {
		t.Fatal(full, e)
	}
	small, e := Build(items, known, "Q", "sources", false, 3)
	if e != nil || !small.Truncated || small.DroppedMessages != 1 || small.Messages[0].Content != "new" {
		t.Fatal(small, e)
	}
}
