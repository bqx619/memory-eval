package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestItemRequiresText(t *testing.T) {
	for _, s := range []string{`{"source_ids":["a"]}`, `{"text":null}`, `{"text":12}`} {
		var i Item
		if err := json.Unmarshal([]byte(s), &i); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	var i Item
	if err := json.Unmarshal([]byte(`{"text":"","source_ids":["a"]}`), &i); err != nil {
		t.Fatal(err)
	}
}
func TestExplicitZeroBeliefs(t *testing.T) {
	var b Budget
	if err := json.Unmarshal([]byte(`{"top_k":10,"max_context_chars":100,"maxBeliefs":0}`), &b); err != nil {
		t.Fatal(err)
	}
	if !b.HasMaxBeliefs {
		t.Fatal("lost explicit zero")
	}
	wire, _ := json.Marshal(b)
	if !strings.Contains(string(wire), `"maxBeliefs":0`) {
		t.Fatal(string(wire))
	}
	b.HasMaxBeliefs = false
	wire, _ = json.Marshal(b)
	if strings.Contains(string(wire), "maxBeliefs") {
		t.Fatal("default budget gained maxBeliefs")
	}
}
