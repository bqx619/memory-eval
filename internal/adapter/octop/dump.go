package octop

import (
	"context"
	"memeval/internal/adapter"
	"memeval/internal/protocol"
	"slices"
)

// The 1.0.1 dashboard limits its internal window to 10,000 before pagination.
// Raw sessions are partitioned and counted; atoms at that boundary fail closed.
func (a *Adapter) Dump(ctx context.Context, ns string) (protocol.DumpResponse, error) {
	out := protocol.DumpResponse{Items: []protocol.DumpItem{}}
	if e := a.check(ns); e != nil {
		return out, e
	}
	fetch := func(method, session string, expected int) error {
		offset := 0
		for {
			params := map[string]any{"limit": 500, "offset": offset, "include_deprecated": true, "order": "asc"}
			if session != "" {
				params["session_id"] = session
			}
			var page struct {
				Items []struct {
					ID        string
					Content   *string
					Assertion *string
					Timestamp string
					CreatedAt string `json:"created_at"`
				}
				Total int
				More  bool `json:"has_more"`
			}
			if e := a.Call(ctx, method, params, &page); e != nil {
				return e
			}
			if page.Items == nil || page.Total >= 10000 || expected >= 0 && page.Total != expected {
				return &adapter.ContractError{Reason: "octop dump completeness cannot be established"}
			}
			if len(page.Items) == 0 && page.More {
				return &adapter.ContractError{Reason: "octop dump pagination made no progress"}
			}
			for _, item := range page.Items {
				text, ts, layer := item.Content, item.Timestamp, "raw"
				if method == "list_atoms" {
					text, ts, layer = item.Assertion, item.CreatedAt, "atom"
				}
				if text == nil {
					return &adapter.ContractError{Reason: "octop dump text missing"}
				}
				out.Items = append(out.Items, protocol.DumpItem{ID: layer + ":" + item.ID, Text: *text, Timestamp: ts, Layer: layer})
			}
			offset += len(page.Items)
			if !page.More {
				if offset != page.Total {
					return &adapter.ContractError{Reason: "octop dump count mismatch"}
				}
				return nil
			}
		}
	}
	if e := fetch("list_atoms", "", -1); e != nil {
		return out, e
	}
	keys := []string{}
	for k := range a.dumpSessions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if e := fetch("list_raw_events", k, a.dumpSessions[k]); e != nil {
			return out, e
		}
	}
	slices.SortFunc(out.Items, func(x, y protocol.DumpItem) int {
		if x.ID < y.ID {
			return -1
		}
		if x.ID > y.ID {
			return 1
		}
		return 0
	})
	return out, adapter.ValidateDump(out)
}
