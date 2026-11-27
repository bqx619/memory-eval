package adapter

import (
	"context"
	"memeval/internal/protocol"
)

func ValidateDump(r protocol.DumpResponse) error {
	if r.Items == nil {
		return &ContractError{Reason: "dump.items must be a list"}
	}
	seen := map[string]bool{}
	for _, i := range r.Items {
		if i.ID == "" || seen[i.ID] {
			return &ContractError{Reason: "dump IDs must be nonempty and unique"}
		}
		seen[i.ID] = true
	}
	return nil
}
func (b *Baseline) Dump(ctx context.Context, ns string) (protocol.DumpResponse, error) {
	r := protocol.DumpResponse{Items: []protocol.DumpItem{}}
	if e := ctx.Err(); e != nil {
		return r, e
	}
	events, ok := b.store[ns]
	if !ok {
		return r, &ContractError{Reason: "namespace is not active"}
	}
	if b.strategy != "null" {
		for _, e := range events {
			r.Items = append(r.Items, protocol.DumpItem{ID: e.ID, Text: e.Content, Timestamp: e.Timestamp, Layer: "raw"})
		}
	}
	return r, ValidateDump(r)
}
