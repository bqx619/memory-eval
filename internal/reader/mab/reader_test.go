package mab

import (
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"strings"
	"testing"
)

func TestRankRawAndFullContext(t *testing.T) {
	known := map[string]suite.Event{"a": {Content: "EARLY", Attributes: map[string]any{"memeval_reader_sequence": 0}}, "b": {Content: "LATE", Attributes: map[string]any{"memeval_reader_sequence": 1}}}
	items := []protocol.Item{{Text: "WRAPPER MUST NOT LEAK", SourceIDs: []string{"b"}}, {Text: "WRAPPER", SourceIDs: []string{"a"}}}
	in, e := Build(items, known, "ruler_qa1_197K", "original {question}", "sources", false, 100)
	if e != nil || !strings.HasPrefix(in.Messages[1].Content, "LATE\nEARLY\n\n") || strings.Contains(in.Messages[1].Content, "WRAPPER") || !strings.Contains(in.Messages[1].Content, "original {question}") {
		t.Fatal(in, e)
	}
	in, e = Build(items, known, "ruler_qa1_197K", "Q", "sources", true, 100)
	if e != nil || !strings.HasPrefix(in.Messages[1].Content, "EARLY\nLATE\n\n") || strings.Contains(in.Messages[1].Content, "Now Answer") {
		t.Fatal(in, e)
	}
	in, e = Build(items, known, "ruler_qa1_197K", "Q", "sources", false, 4)
	if e != nil || in.DroppedMessages != 1 || !in.Truncated || !strings.HasPrefix(in.Messages[1].Content, "LATE\n\n") {
		t.Fatal(in, e)
	}
}
