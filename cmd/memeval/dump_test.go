package main

import (
	"context"
	"memeval/internal/adapter"
	"memeval/internal/protocol"
	"memeval/internal/sandbox/none"
	"memeval/internal/transport/jsonrpc"
	"reflect"
	"testing"
)

func TestEchoDumpContract(t *testing.T) {
	c, _ := echoClient(t, none.Backend{}, "none", jsonrpc.Options{})
	ctx := context.Background()
	var d protocol.Describe
	if e := c.Call(ctx, "describe", map[string]any{}, &d); e != nil {
		t.Fatal(e)
	}
	if e := c.Call(ctx, "reset", map[string]any{"ns": "n"}, nil); e != nil {
		t.Fatal(e)
	}
	events := []protocol.PublicEvent{{ID: "a", Content: "one"}, {ID: "b", Content: "two"}}
	var h protocol.IngestHandle
	if e := c.Call(ctx, "ingest", map[string]any{"ns": "n", "events": events}, &h); e != nil {
		t.Fatal(e)
	}
	var a, b protocol.DumpResponse
	if e := c.Call(ctx, "dump", map[string]any{"ns": "n"}, &a); e != nil {
		t.Fatal(e)
	}
	if e := adapter.ValidateDump(a); e != nil {
		t.Fatal(e)
	}
	if e := c.Call(ctx, "dump", map[string]any{"ns": "n"}, &b); e != nil || !reflect.DeepEqual(a, b) || len(a.Items) != 2 || a.Items[1].Text != "two" {
		t.Fatal(a, b, e)
	}
	if e := c.Call(ctx, "dump", map[string]any{"ns": "other"}, &b); e == nil {
		t.Fatal("namespace")
	}
}
