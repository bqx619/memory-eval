package convomem

import (
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"strings"
	"testing"
)

func TestConversationRendering(t *testing.T) {
	known := map[string]suite.Event{"a": {SourceID: "raw-a", Session: "c0", Role: "user", Content: "first", Attributes: map[string]any{"memeval_reader_sequence": 0}}, "b": {SourceID: "raw-b", Session: "c2", Role: "assistant", Content: "second", Attributes: map[string]any{"memeval_reader_sequence": 1}}, "c": {SourceID: "raw-c", Session: "c2", Role: "user", Content: "third", Attributes: map[string]any{"memeval_reader_sequence": 2}}}
	items := []protocol.Item{{SourceIDs: []string{"c", "b"}}}
	c := suite.Case{Query: "$conversationContext"}
	in, e := Build(items, known, c, false, 9999)
	if e != nil {
		t.Fatal(e)
	}
	p := in.Messages[0].Content
	if !strings.Contains(p, "Conversation 3:\nAssistant: second\nUser: third") || strings.Contains(p, "Conversation 1:") || !strings.Contains(p, "Question: $conversationContext") {
		t.Fatal(p)
	}
	in, e = Build(nil, known, c, true, 9999)
	if e != nil || !strings.Contains(in.Messages[0].Content, "Conversation 1:\nUser: first\n\nConversation 3:") {
		t.Fatal(in, e)
	}
	in, e = Build(nil, known, c, true, 30)
	if e != nil || !in.Truncated || in.DroppedMessages != 2 || !strings.Contains(in.Messages[0].Content, "Conversation 3:\nUser: third") {
		t.Fatal(in, e)
	}
	if _, e = Build(nil, known, c, false, 9999); e == nil {
		t.Fatal("empty history must return no answer")
	}
}
