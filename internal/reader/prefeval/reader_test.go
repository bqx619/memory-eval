package prefeval

import (
	"context"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/reader"
	score "memeval/internal/scoring/prefeval"
	"memeval/internal/suite"
	"strings"
	"testing"
)

type capture struct{ r llm.Request }

func (c *capture) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	c.r = r
	return llm.Response{Text: "B</choice>"}, nil
}

type emptyCapture struct{}

func (emptyCapture) Complete(context.Context, llm.Request) (llm.Response, error) {
	r := llm.Response{FinishReason: "max_tokens"}
	return r, &reader.EmptyResponseError{Response: r}
}
func TestPrefillCannotMaskEmptyReaderResponse(t *testing.T) {
	in := Input{MCQ: true, Messages: []llm.Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "<choice>"}}}
	r, err := Answer(t.Context(), emptyCapture{}, in, "anthropic", "fixture", "sample", 5)
	if err == nil || r.Text != "" {
		t.Fatal("prefill masked empty response", r, err)
	}
}
func TestMessagesOrderReminderAndPrefill(t *testing.T) {
	known := map[string]suite.Event{"p": {Role: "user", Content: "preference", Attributes: map[string]any{"memeval_reader_sequence": 0}}, "f": {Role: "assistant", Content: "filler", Attributes: map[string]any{"memeval_reader_sequence": 1}}}
	c := suite.Case{Kind: "rubric", Query: "question"}
	in, e := Build([]protocol.Item{{SourceIDs: []string{"f"}}, {SourceIDs: []string{"p"}}}, known, c, false, true, 100)
	if e != nil {
		t.Fatal(e)
	}
	if len(in.Messages) != 4 || in.Messages[1].Content != "preference" || in.Messages[2].Content != "filler" || in.Messages[3].Content != "question"+score.Templates["reminder"]+" (Please respond within 300 words.)" {
		t.Fatal(in)
	}
	truncated, e := Build(nil, known, c, true, false, 6)
	if e != nil || truncated.DroppedMessages != 1 || truncated.Messages[1].Content != "filler" {
		t.Fatal(truncated, e)
	}
	c.Kind = "mcq"
	c.Options = map[string]string{"A": "a", "B": "b", "C": "c", "D": "d"}
	in, e = Build(nil, known, c, true, false, 100)
	if e != nil {
		t.Fatal(e)
	}
	if in.Messages[len(in.Messages)-1].Content != "<choice>" || !strings.Contains(in.Messages[len(in.Messages)-2].Content, "A. a\nB. b\nC. c\nD. d") {
		t.Fatal(in)
	}
	cl := &capture{}
	r, e := Answer(t.Context(), cl, in, "openai-compat", "fixture", "s", 5)
	if e != nil || len(r.Deviations) == 0 || cl.r.Messages[len(cl.r.Messages)-1].Role != "user" || *cl.r.MaxTokens != 5 {
		t.Fatal(cl.r, r, e)
	}
	r, e = Answer(t.Context(), cl, in, "anthropic", "fixture", "s", 5)
	if e != nil || r.Text != "<choice>B</choice>" || cl.r.Messages[len(cl.r.Messages)-1].Role != "assistant" {
		t.Fatal(cl.r, r, e)
	}
}
