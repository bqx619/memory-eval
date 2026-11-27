package adapter

import (
	"encoding/json"
	"memeval/internal/protocol"
	"reflect"
	"slices"
	"testing"
)

func TestBaselineDumpContract(t *testing.T) {
	for _, name := range []string{"bm25", "recent", "fullcontext", "null"} {
		t.Run(name, func(t *testing.T) {
			b, _ := NewBaseline(name)
			defer b.Close(t.Context())
			d, _ := b.Describe(t.Context())
			if !slices.Contains(d.Capabilities, "dump") {
				t.Fatal(d)
			}
			if _, e := b.Dump(t.Context(), "missing"); e == nil {
				t.Fatal("namespace")
			}
			b.Reset(t.Context(), "ns")
			events := []protocol.PublicEvent{{ID: "a", Content: "first", Timestamp: "2025-01-01T00:00:00Z"}, {ID: "b", Content: "second"}}
			b.Ingest(t.Context(), "ns", events)
			x, e := b.Dump(t.Context(), "ns")
			if e != nil {
				t.Fatal(e)
			}
			y, _ := b.Dump(t.Context(), "ns")
			if !reflect.DeepEqual(x, y) {
				t.Fatal("unstable")
			}
			n := 2
			if name == "null" {
				n = 0
			}
			if len(x.Items) != n {
				t.Fatal(x)
			}
			if n > 0 && (x.Items[0].ID != "a" || x.Items[0].Text != "first" || x.Items[0].Timestamp != events[0].Timestamp) {
				t.Fatal(x)
			}
			b.Reset(t.Context(), "ns")
			x, _ = b.Dump(t.Context(), "ns")
			if len(x.Items) != 0 {
				t.Fatal("reset")
			}
		})
	}
}
func TestDumpRejectsIncompleteContract(t *testing.T) {
	for _, raw := range []string{`{}`, `{"items":null}`, `{"items":[{"id":"a"}]}`, `{"items":[{"id":"","text":"x"}]}`, `{"items":[{"id":"a","text":"x"},{"id":"a","text":"y"}]}`} {
		var r protocol.DumpResponse
		e := json.Unmarshal([]byte(raw), &r)
		if e == nil {
			e = ValidateDump(r)
		}
		if e == nil {
			t.Fatal(raw)
		}
	}
}
