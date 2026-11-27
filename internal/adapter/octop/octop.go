// Package octop maps paeval-v2 onto the installed octop-memory bridge protocol.
package octop

import (
	"context"
	"errors"
	"memeval/internal/adapter"
	"memeval/internal/protocol"
	"memeval/internal/sut"
	"memeval/internal/transport/jsonrpc"
	"slices"
	"strings"
	"time"
)

type Adapter struct {
	*sut.Adapter
	ns              string
	dumpSessions    map[string]int
	known           map[string]bool
	accepted, batch int
	diagnostics     map[string]int
}

func New(process *sut.Adapter) *Adapter { return &Adapter{Adapter: process} }
func ProtocolChanges() []string {
	return []string{
		"non-user roles mapped to user_message",
		"octop capture batches are split at session boundaries; payload.role preserves the original role",
		"octop missing timestamps use 2000-01-01T00:00:00Z plus public sequence seconds; explicit timestamps are unchanged",
		"octop capture strips think blocks; any skipped event invalidates the episode",
		"octop search does not implement user/scope filtering; cross-user sources remain visible in diagnostics",
		"octop raw text includes upstream Markdown front matter; no hidden budget clipping",
	}
}
func (a *Adapter) NeedsSessionBatching() bool { return true }
func (a *Adapter) Diagnostics() map[string]int {
	d := map[string]int{}
	for k, v := range a.diagnostics {
		d[k] = v
	}
	return d
}
func (a *Adapter) Describe(ctx context.Context) (protocol.Describe, error) {
	d := protocol.Describe{}
	probe, err := sut.Probe(ctx, a.Manifest, a.Lock, a.Backend, []string{"{venv}/bin/octopmemory-bridge", "--probe"})
	if err != nil {
		return d, err
	}
	if probe["protocol_version"] != "1.0" {
		return d, &adapter.ContractError{Reason: "unsupported octop protocol"}
	}
	if err = a.Reset(ctx, "describe"); err != nil {
		return d, err
	}
	if err = a.Close(ctx); err != nil {
		return d, err
	}
	version, _ := probe["octop_memory_version"].(string)
	d = protocol.Describe{Name: "octop-memory", Version: version, Capabilities: []string{"retrieve", "read_only_queries", "dump"}, ReadOnlyQueries: true, Limitations: []string{"as_of unsupported", "score unavailable (rank only)", "answer unsupported", "typed_context unsupported", "user/scope filtering unsupported", "lexical only; extract is never called", "dump raw 层无抽取；list_atoms + 按会话 list_raw_events，拒绝上游窗口截断", "查询原样传入；上游逐词取回候选、首词优先；memory_search 面向 agent 关键词，LongMemEval 传入完整问题", "blank queries return an empty result without calling upstream memory_search (which rejects them)"}, Config: map[string]any{"config_json": a.Manifest.Config, "probe": probe, "bridge_protocol": "1.0", "timestamp_fallback": "2000-01-01 UTC plus sequence seconds"}}
	return d, nil
}
func (a *Adapter) Reset(ctx context.Context, ns string) error {
	a.ns = ""
	a.known = map[string]bool{}
	a.dumpSessions = map[string]int{}
	a.diagnostics = map[string]int{"untraceable_hits": 0}
	a.accepted, a.batch = 0, 0
	if err := a.Start(ctx, ns); err != nil {
		return err
	}
	var h struct {
		Protocol  string `json:"protocol_version"`
		Server    string `json:"server"`
		Namespace string `json:"namespace"`
	}
	if err := a.Call(ctx, "handshake", map[string]any{"client_version": "memeval/0.3.1-m1b"}, &h); err != nil {
		return err
	}
	if h.Protocol != "1.0" || h.Server != "octop-memory.bridge" || h.Namespace != ns {
		return &adapter.ContractError{Reason: "bridge handshake mismatch"}
	}
	a.ns = ns
	return nil
}
func (a *Adapter) check(ns string) error {
	if ns == "" || ns != a.ns || a.Client == nil {
		return &adapter.ContractError{Reason: "namespace is not active"}
	}
	return nil
}
func session(s string) string {
	if s == "" {
		return "s0"
	}
	return s
}
func role(s string) string {
	if s == "assistant" {
		return "assistant_message"
	}
	return "user_message"
}
func (a *Adapter) Ingest(ctx context.Context, ns string, events []protocol.PublicEvent) (protocol.IngestHandle, error) {
	h := protocol.IngestHandle{}
	if err := a.check(ns); err != nil {
		return h, err
	}
	ordered := slices.Clone(events)
	slices.SortStableFunc(ordered, func(x, y protocol.PublicEvent) int {
		if x.Sequence < y.Sequence {
			return -1
		}
		if x.Sequence > y.Sequence {
			return 1
		}
		return 0
	})
	sid := "s0"
	if len(ordered) > 0 {
		sid = session(ordered[0].SessionID)
	}
	wire := []map[string]any{}
	seen := map[string]bool{}
	for _, e := range ordered {
		if session(e.SessionID) != sid {
			return h, &adapter.ContractError{Reason: "capture batch spans sessions"}
		}
		if e.ID == "" || seen[e.ID] || a.known[e.ID] {
			return h, &adapter.ContractError{Reason: "duplicate or empty public event id"}
		}
		seen[e.ID] = true
		stamp := e.Timestamp
		if stamp == "" {
			stamp = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(e.Sequence) * time.Second).Format(time.RFC3339)
		} else if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			return h, &adapter.ContractError{Reason: "invalid event timestamp"}
		}
		wire = append(wire, map[string]any{"id": e.ID, "event_type": role(e.Role), "content": e.Content, "payload": map[string]any{"role": e.Role}, "timestamp_iso": stamp})
	}
	var counts map[string]int
	if err := a.Call(ctx, "capture", map[string]any{"session_id": sid, "thread_id": nil, "user": nil, "host": "memeval", "events": wire}, &counts); err != nil {
		return h, err
	}
	a.batch++
	h.BatchSeq = a.batch
	h.Accepted = counts["accepted"]
	for _, k := range []string{"skipped_recall_marker", "skipped_too_short", "skipped_role", "skipped_tool_result"} {
		n, exists := counts[k]
		if !exists || n < 0 {
			return h, &adapter.ContractError{Reason: "capture count mismatch"}
		}
		h.Filtered += n
		a.diagnostics[k] += n
	}
	h.Failed = len(events) - h.Accepted - h.Filtered
	if _, exists := counts["accepted"]; !exists || h.Accepted < 0 || h.Failed != 0 {
		return h, &adapter.ContractError{Reason: "capture count mismatch"}
	}
	a.accepted += h.Accepted
	a.dumpSessions[sid] += h.Accepted
	if h.Filtered > 0 {
		return h, &adapter.IngestError{}
	}
	for id := range seen {
		a.known[id] = true
	}
	return h, nil
}
func (a *Adapter) Ready(ctx context.Context, ns string, _ protocol.IngestHandle, deadline time.Time) (protocol.ReadyResult, error) {
	r := protocol.ReadyResult{Stages: []protocol.Stage{{Name: "capture", Status: "failed"}}, Warnings: []string{}}
	if err := a.check(ns); err != nil {
		return r, err
	}
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	var stats struct {
		Counts struct {
			RawEvents *int `json:"raw_events"`
		} `json:"counts"`
	}
	if err := a.Call(ctx, "stats", map[string]any{}, &stats); err != nil {
		return r, err
	}
	if stats.Counts.RawEvents == nil || *stats.Counts.RawEvents != a.accepted {
		a.diagnostics["visibility_count_mismatch"]++
		return r, &adapter.IngestError{}
	}
	r.Visible = true
	r.Stages[0].Status = "ok"
	return r, nil
}

type hit struct {
	Path     string `json:"path"`
	Snippet  string `json:"snippet"`
	Layer    string `json:"layer"`
	SourceID string `json:"source_id"`
}
type provenance struct {
	RawIDs  []string `json:"raw_event_ids"`
	QuoteID string   `json:"quote_event_id"`
}
type document struct {
	Excerpt  *string    `json:"excerpt"`
	Metadata provenance `json:"metadata"`
}

func missing(err error) bool {
	var e *jsonrpc.RPCError
	return errors.As(err, &e) && (e.Code == -32010 || e.Code == -32011)
}
func (a *Adapter) sources(ids []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !a.known[id] {
			return nil, &adapter.ContractError{Reason: "invented/future citation"}
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}
func (a *Adapter) Retrieve(ctx context.Context, ns string, r protocol.RetrieveRequest) (protocol.RetrieveResponse, error) {
	out := protocol.RetrieveResponse{Items: []protocol.Item{}}
	if err := a.check(ns); err != nil {
		return out, err
	}
	if r.AsOf != "" || r.OutputFormat == "typed_context" {
		return out, adapter.ErrUnsupported
	}
	if r.Budget.TopK == 0 || strings.TrimSpace(r.Query) == "" {
		return out, nil
	}
	var search struct {
		Hits []hit `json:"hits"`
	}
	if err := a.Call(ctx, "memory_search", map[string]any{"query": r.Query, "maxResults": r.Budget.TopK, "corpus": "memory"}, &search); err != nil {
		return out, err
	}
	if search.Hits == nil {
		return out, &adapter.ContractError{Reason: "memory_search hits must be a list"}
	}
	for _, h := range search.Hits {
		item := protocol.Item{ID: h.SourceID, Text: h.Snippet, SourceIDs: []string{}}
		var ids []string
		if h.Layer == "raw" {
			ids = []string{h.SourceID}
			// Validate before fallback; a foreign raw hit is always a contract error.
			if _, err := a.sources(ids); err != nil {
				return out, err
			}
			var doc document
			err := a.Call(ctx, "memory_get", map[string]any{"path": h.Path}, &doc)
			if err != nil && !missing(err) {
				return out, err
			}
			if err == nil && doc.Excerpt != nil {
				item.Text = *doc.Excerpt
			}
		} else {
			resolved := false
			if h.Layer == "atom" {
				var atom provenance
				err := a.Call(ctx, "get_atom", map[string]any{"atom_id": h.SourceID}, &atom)
				if err != nil && !missing(err) {
					return out, err
				}
				if err == nil {
					ids = atom.RawIDs
					resolved = len(ids) > 0
				}
			}
			if !resolved {
				var doc document
				err := a.Call(ctx, "memory_get", map[string]any{"path": h.Path}, &doc)
				if err != nil && !missing(err) {
					return out, err
				}
				if err == nil {
					ids = doc.Metadata.RawIDs
					if len(ids) == 0 && doc.Metadata.QuoteID != "" {
						ids = []string{doc.Metadata.QuoteID}
					}
				}
			}
		}
		var err error
		if item.SourceIDs, err = a.sources(ids); err != nil {
			return out, err
		}
		if len(item.SourceIDs) == 0 {
			item.Status = "unsupported_item"
			a.diagnostics["untraceable_hits"]++
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

var _ adapter.Adapter = (*Adapter)(nil)
