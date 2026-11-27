package adapter

import (
	"context"
	"math"
	"memeval/internal/protocol"
	"reflect"
	"testing"
	"time"
)

func TestTokensAndBM25(t *testing.T) {
	if got := Tokens("The B-42 中文测试 İX!"); !reflect.DeepEqual(got, []string{"b", "42", "中", "文", "测", "试", "x"}) {
		t.Fatal(got)
	}
	scores := BM25Scores("box", []string{"box", "", "box box"})
	if len(scores) != 3 || scores[1] != 0 || scores[0] <= 0 || scores[2] <= scores[0] {
		t.Fatal(scores)
	}
	if len(BM25Scores("", nil)) != 0 {
		t.Fatal("empty docs")
	}
	expected := math.Log(1 + (3.-2.+.5)/(2.+.5))
	if math.Abs(scores[0]-expected) > 1e-12 {
		t.Fatal(scores, expected)
	}
}
func TestBudgetRoutingAndReady(t *testing.T) {
	ctx := context.Background()
	b, _ := NewBaseline("recent")
	if _, err := b.Ingest(ctx, "missing", nil); err == nil {
		t.Fatal("ingest before reset")
	}
	_ = b.Reset(ctx, "ns")
	events := []protocol.PublicEvent{{ID: "profile", Owner: "u", Content: "中文🙂", Attributes: map[string]any{"scope": []string{"user:universal"}}}, {ID: "pinned", Owner: "u", Content: "pin", Attributes: map[string]any{"scope": []string{"code"}, "pinned": true}}, {ID: "other", Owner: "other", Content: "SECRET", Attributes: map[string]any{}}}
	h, err := b.Ingest(ctx, "ns", events)
	if err != nil || h.Accepted != 3 {
		t.Fatal(h, err)
	}
	events[0].Attributes["scope"] = []string{"mutated"}
	ready, err := b.Ready(ctx, "ns", h, time.Time{})
	if err != nil || !ready.Visible {
		t.Fatal(ready, err)
	}
	r, err := b.Retrieve(ctx, "ns", protocol.RetrieveRequest{User: "u", Query: "anything", Scope: []string{"code"}, OutputFormat: "typed_context", Budget: protocol.Budget{TopK: 10, MaxContextChars: 2}})
	if err != nil || *r.PersonaPrelude != "中文" || len(r.Tiers.PinnedFacts) != 0 {
		t.Fatal(r, err)
	}
	r, err = b.Retrieve(ctx, "ns", protocol.RetrieveRequest{User: "u", Query: "anything", Scope: []string{"code"}, OutputFormat: "typed_context", Budget: protocol.Budget{TopK: 10, MaxContextChars: 100, HasMaxBeliefs: true}})
	if err != nil || len(r.Tiers.PinnedFacts) != 0 {
		t.Fatal("explicit zero maxBeliefs ignored")
	}
	if _, err = b.Answer(ctx, "ns", protocol.RetrieveRequest{}); err != ErrUnsupported {
		t.Fatal(err)
	}
}
