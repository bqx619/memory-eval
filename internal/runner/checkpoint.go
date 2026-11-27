package runner

import (
	"context"
	"fmt"
	"maps"
	"memeval/internal/adapter"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"time"
)

func checkpointEpisode(ep *suite.Episode) bool {
	for _, c := range ep.Cases {
		if c.QueryPoint == "after_session_k" {
			return true
		}
	}
	return false
}
func validateIncremental(ep *suite.Episode, ingestion string) error {
	if !checkpointEpisode(ep) {
		for _, c := range ep.Cases {
			if c.Cutoff != len(ep.Events) || (c.QueryPoint != "" && c.QueryPoint != "end_of_history") {
				return fmt.Errorf("incremental_per_episode requires end_of_history with full cutoff or ordered after_session_k checkpoints")
			}
		}
		return nil
	}
	if ingestion != "per_session" {
		return fmt.Errorf("after_session_k requires per_session ingestion")
	}
	previous := -1
	seenQuery, seenAudit := false, false
	for _, c := range ep.Cases {
		if c.QueryPoint != "after_session_k" || c.Cutoff < previous {
			return fmt.Errorf("checkpoint cases must be monotone after_session_k")
		}
		if c.Cutoff != previous {
			seenQuery, seenAudit = false, false
		}
		if c.Kind == "memory_audit" {
			if seenQuery || seenAudit {
				return fmt.Errorf("checkpoint audit must precede QA and occur once")
			}
			seenAudit = true
		} else {
			seenQuery = true
		}
		previous = c.Cutoff
	}
	return nil
}
func prepareCheckpointEpisode(ctx context.Context, a adapter.Adapter, ep suite.Episode, repeat int, o Options) *episodeState {
	id, _ := identity.New(o.RunSeed, identity.SampleID("episode:"+ep.ID, repeat))
	x := &episodeState{id: id, public: []protocol.PublicEvent{}, known: map[string]suite.Event{}, timings: map[string]float64{}, diagnostics: map[string]int{}, previousDump: map[string]bool{}}
	start := time.Now()
	x.err = a.Reset(ctx, id.Namespace())
	x.timings["reset_ms"] = float64(time.Since(start).Nanoseconds()) / 1e6
	return x
}
func advanceCheckpoint(ctx context.Context, a adapter.Adapter, ep suite.Episode, cutoff int, x *episodeState, o Options) {
	if x.err != nil || len(x.public) == cutoff {
		return
	}
	x.timed = false
	// Render with original sequence numbers; publish only the prefix already ready.
	all := suite.RenderPublicEvents(ep.Events[:cutoff], x.id, o.ingestRender)
	for off := len(x.public); off < cutoff; {
		end := off + 1
		for end < cutoff && all[end].SessionID == all[off].SessionID {
			end++
		}
		start := time.Now()
		h, e := a.Ingest(ctx, x.id.Namespace(), all[off:end])
		x.timings["ingest_ms"] += float64(time.Since(start).Nanoseconds()) / 1e6
		if e == nil && (h.Accepted < 0 || h.Filtered < 0 || h.Failed < 0 || h.Accepted+h.Filtered+h.Failed != end-off) {
			e = &adapter.ContractError{Reason: "ingest counts do not match checkpoint"}
		}
		if e == nil && h.Failed > 0 {
			e = &ingestFailure{}
		}
		if e != nil {
			x.err = e
			return
		}
		start = time.Now()
		deadline, _ := ctx.Deadline()
		v, e := a.Ready(ctx, x.id.Namespace(), h, deadline)
		x.timings["ready_ms"] += float64(time.Since(start).Nanoseconds()) / 1e6
		if e == nil && !v.Visible {
			e = &ingestFailure{}
		}
		for _, stage := range v.Stages {
			if e == nil && stage.Status != "ok" {
				e = &ingestFailure{}
			}
		}
		if e != nil {
			x.err = e
			return
		}
		for i := off; i < end; i++ {
			event := ep.Events[i]
			event.Attributes = maps.Clone(event.Attributes)
			if event.Attributes == nil {
				event.Attributes = map[string]any{}
			}
			event.Attributes["memeval_reader_sequence"] = i
			x.known[all[i].ID] = event
		}
		x.public = all[:end]
		off = end
	}
	x.digest, _ = suite.Digest(x.public)
}
