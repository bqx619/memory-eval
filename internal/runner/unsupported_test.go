package runner_test

import (
	"memeval/internal/adapter"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/transport/jsonrpc"
	"testing"
)

func TestUnsupportedRPCNoteAndOtherError(t *testing.T) {
	for _, unsupported := range []bool{true, false} {
		a := newSpy()
		a.retrieve = func(*spy) (protocol.RetrieveResponse, error) {
			rpc := &jsonrpc.RPCError{Code: -32000, Message: "sanitized SDK failure"}
			if unsupported {
				rpc.Code = -32601
				return protocol.RetrieveResponse{}, &adapter.UnsupportedError{Note: "requested scope unavailable", Cause: rpc}
			}
			return protocol.RetrieveResponse{}, rpc
		}
		r := run(t, toy(), a, opts())
		c := r.Cases[0]
		if unsupported {
			if c.Status != result.Unsupported || c.Note == nil || *c.Note != "requested scope unavailable" {
				t.Fatal(c)
			}
		} else if c.Status != result.Error {
			t.Fatal(c)
		}
	}
}
