package runner_test

import (
	"encoding/json"
	"fmt"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/transport/jsonrpc"
	"testing"
)

func TestGovernorRPCErrorClassification(t *testing.T) {
	for _, reason := range []string{"exhausted", "budget", "unknown"} {
		a := newSpy()
		a.retrieve = func(*spy) (protocol.RetrieveResponse, error) {
			data, _ := json.Marshal(map[string]string{"type": "SDKError", "governor": reason})
			return protocol.RetrieveResponse{}, fmt.Errorf("wrapper: %w", &jsonrpc.RPCError{Code: -32000, Message: "SDK 503 stop detail", Data: data})
		}
		r := run(t, toy(), a, opts())
		c := r.Cases[0]
		want := "gateway_" + reason
		if reason == "unknown" {
			want = "*jsonrpc.RPCError"
		}
		if c.Status != result.Error || c.ErrorType != want || c.ErrorDetail != "rpc_error(-32000): SDK 503 stop detail [SDKError]" {
			t.Fatal(c)
		}
	}
}
func TestAutomaticResumeOnlyRateLimitedSamples(t *testing.T) {
	r := &result.Report{RunID: "r", RunSeed: "s", Interruption: &result.Interruption{Reason: "exhausted", Unscheduled: 1}}
	types := []string{"gateway_exhausted", "rpc_timeout", "gateway_budget", "*jsonrpc.RPCError", "ReaderEmptyResponse"}
	for i, typ := range types {
		id := fmt.Sprint(i)
		r.Cases = append(r.Cases, result.Case{CaseID: id, SampleID: identity.SampleID(id, 0), Status: result.Error, ErrorType: typ})
	}
	p, e := runner.NewResume(r, "error,invalid")
	if e != nil {
		t.Fatal(e)
	}
	p.RateLimitedOnly()
	for i, c := range r.Cases {
		if p.Selected(c.SampleID) != (i < 2) {
			t.Fatal(i)
		}
	}
	if p.Count() != 2 || !p.Selected(identity.SampleID("unscheduled", 0)) {
		t.Fatal("unscheduled suffix lost")
	}
}
