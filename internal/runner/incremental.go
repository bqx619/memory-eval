package runner

import (
	"context"
	"maps"
	"memeval/internal/adapter"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"strings"
	"time"
)

type episodeState struct {
	previousDump map[string]bool
	id           identity.Identity
	public       []protocol.PublicEvent
	known        map[string]suite.Event
	digest       string
	err          error
	timings      map[string]float64
	diagnostics  map[string]int
	timed        bool
}

func prepareEpisode(ctx context.Context, a adapter.Adapter, ep suite.Episode, repeat int, o Options) *episodeState {
	id, _ := identity.New(o.RunSeed, identity.SampleID("episode:"+ep.ID, repeat))
	p := suite.RenderPublicEvents(ep.Events, id, o.ingestRender)
	x := &episodeState{id: id, public: p, known: map[string]suite.Event{}, timings: map[string]float64{}, diagnostics: map[string]int{}}
	x.digest, _ = suite.Digest(p)
	for i, e := range ep.Events {
		e.Attributes = maps.Clone(e.Attributes)
		if e.Attributes == nil {
			e.Attributes = map[string]any{}
		}
		e.Attributes["memeval_reader_sequence"] = i
		x.known[p[i].ID] = e
	}
	phase := func(name string, f func() error) error {
		start := time.Now()
		err := f()
		x.timings[name] = float64(time.Since(start).Nanoseconds()) / 1e6
		return err
	}
	ns := id.Namespace()
	x.err = phase("reset_ms", func() error { return a.Reset(ctx, ns) })
	if x.err != nil {
		return x
	}
	h := protocol.IngestHandle{}
	x.err = phase("ingest_ms", func() error {
		for off := 0; off < len(p); {
			end := min(off+o.BatchSize, len(p))
			if strings.HasPrefix(o.ingestion, "per_chunk:") {
				end = off + 1
			}
			if o.ingestion == "per_session" {
				end = off + 1
				for end < len(p) && p[end].SessionID == p[off].SessionID {
					end++
				}
			}
			if grouped, ok := a.(interface{ NeedsSessionBatching() bool }); ok && grouped.NeedsSessionBatching() {
				for i := off + 1; i < end; i++ {
					if p[i].SessionID != p[off].SessionID {
						end = i
						break
					}
				}
			}
			var e error
			h, e = a.Ingest(ctx, ns, p[off:end])
			if e != nil {
				return e
			}
			if h.Accepted < 0 || h.Filtered < 0 || h.Failed < 0 || h.Accepted+h.Filtered+h.Failed != end-off {
				return &adapter.ContractError{Reason: "ingest counts do not match batch"}
			}
			if h.Failed > 0 {
				return &ingestFailure{}
			}
			off = end
		}
		return nil
	})
	if x.err != nil {
		return x
	}
	x.err = phase("ready_ms", func() error {
		deadline, _ := ctx.Deadline()
		v, e := a.Ready(ctx, ns, h, deadline)
		if e != nil {
			return e
		}
		if !v.Visible {
			return &ingestFailure{}
		}
		for _, s := range v.Stages {
			if s.Status != "ok" {
				return &ingestFailure{}
			}
		}
		return nil
	})
	return x
}
func mabSupported(c suite.Case) bool {
	if c.Gold.Checker == nil {
		return false
	}
	return c.Kind == "summary" && c.Gold.Checker.Type == "mab_infbench_sum" || c.Kind == "recsys" && c.Gold.Checker.Type == "mab_recsys_recall@5"
}
