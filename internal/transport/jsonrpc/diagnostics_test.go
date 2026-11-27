package jsonrpc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRPCDiagnosticsRedacted(t *testing.T) {
	p := fake()
	c := client(t, p, Options{Secrets: []string{"private-value"}})
	go func() {
		bufio.NewReader(p.inR).ReadBytes('\n')
		fmt.Fprintln(p.outW, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"scope private-value unavailable","data":{"type":"Unsupported","detail":"private-value","authorization":"opaque-credential"}}}`)
	}()
	e := c.Call(context.Background(), "retrieve", nil, nil)
	var rpc *RPCError
	if !errors.As(e, &rpc) || e.Error() != "rpc_error(-32601): scope [REDACTED] unavailable [Unsupported]" {
		t.Fatal(e)
	}
	if strings.Contains(string(rpc.Data), "private-value") || strings.Contains(string(rpc.Data), "opaque-credential") {
		t.Fatal(string(rpc.Data))
	}
}
