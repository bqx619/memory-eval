package octop

import (
	"fmt"
	"memeval/internal/protocol"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBridgeDumpContract(t *testing.T) {
	a := installed(t)
	ingest(t, a)
	r, e := a.Dump(t.Context(), "contract")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Items) != 3 {
		t.Fatal("raw count", len(r.Items))
	}
	for _, item := range r.Items {
		if item.Layer != "raw" || !strings.HasPrefix(item.ID, "raw:evt-") || item.Timestamp == "" {
			t.Fatal(item)
		}
	}
	again, e := a.Dump(t.Context(), "contract")
	if e != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("stable dump", e)
	}
	seed(t, a, "atom")
	r, e = a.Dump(t.Context(), "contract")
	if e != nil || len(r.Items) != 4 {
		t.Fatal("atom+raw", len(r.Items), e)
	}
	if _, e = a.Dump(t.Context(), "other"); e == nil {
		t.Fatal("namespace")
	}
	if e = a.Reset(t.Context(), "next"); e != nil {
		t.Fatal(e)
	}
	r, e = a.Dump(t.Context(), "next")
	if e != nil || len(r.Items) != 0 {
		t.Fatal("reset", r, e)
	}
}

func TestBridgeDumpPagination(t *testing.T) {
	a := installed(t)
	ev := []protocol.PublicEvent{}
	for i := 0; i < 503; i++ {
		ev = append(ev, protocol.PublicEvent{ID: fmt.Sprintf("page-%04d", i), Sequence: i, Role: "user", SessionID: "pagination", Content: fmt.Sprintf("Synthetic dump pagination record number %d", i), Timestamp: "2025-01-01T00:00:00Z"})
	}
	h, e := a.Ingest(t.Context(), "contract", ev)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Ready(t.Context(), "contract", h, time.Time{}); e != nil {
		t.Fatal(e)
	}
	d, e := a.Dump(t.Context(), "contract")
	if e != nil || len(d.Items) != 503 {
		t.Fatal(len(d.Items), e)
	}
	for i, item := range d.Items {
		if item.ID != "raw:"+ev[i].ID || item.Text != ev[i].Content {
			t.Fatal("pagination or stable ordering", i)
		}
	}
}
