package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestJSONRPC2WireBytes(t *testing.T) {
	p := fake()
	c := client(t, p, Options{})
	params := []any{nil, map[string]any{"text": "汉字 <&>\n\u2028", "z": []any{true, 1, nil}, "a": map[string]any{}}, map[string]any{"namespace": "fixture", "query": "book", "top_k": 10}}
	go func() {
		defer p.finish(0)
		r := bufio.NewReader(p.inR)
		for i, param := range params {
			got, err := r.ReadBytes('\n')
			want, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "search", "params": param})
			want = append(want, '\n')
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("wire differs: %q != %q (%v)", got, want, err)
				return
			}
			fmt.Fprintf(p.outW, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":null}\n", i+1)
		}
	}()
	for _, param := range params {
		if err := c.Call(context.Background(), "search", param, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNDJSONBoundaryRemainsStrict(t *testing.T) {
	for _, input := range []string{"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":null}", "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":\"\xff\"}\n", "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":null}{}\n"} {
		p := fake()
		c := client(t, p, Options{})
		go func() { bufio.NewReader(p.inR).ReadBytes('\n'); fmt.Fprint(p.outW, input); p.finish(0) }()
		var protocol *ProtocolError
		if err := c.Call(context.Background(), "search", nil, nil); !errors.As(err, &protocol) {
			t.Fatalf("invalid frame accepted: %v", err)
		}
	}
}

func TestInvalidLocalParamsDoNotKillBridge(t *testing.T) {
	p := fake()
	c := client(t, p, Options{})
	var unsupported *json.UnsupportedTypeError
	if err := c.Call(context.Background(), "search", make(chan int), nil); !errors.As(err, &unsupported) || p.killed {
		t.Fatalf("local marshal error: %v", err)
	}
}
