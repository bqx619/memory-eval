package suite

import (
	"encoding/json"
	mab "memeval/internal/benchmark/mab"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"strings"
)

var AttributeNames = []string{"scope", "type", "canonical_name", "aliases", "epistemic_status", "pinned", "superseded_by", "resolved_at", "user_edited", "participants", "speaker"}

func PublicEvents(events []Event, id identity.Identity) []protocol.PublicEvent {
	return RenderPublicEvents(events, id, "")
}

// RenderPublicEvents applies only the explicit public ingestion template.
func RenderPublicEvents(events []Event, id identity.Identity, render string) []protocol.PublicEvent {
	known := map[string]string{}
	for _, e := range events {
		known[e.SourceID] = id.Anon("event", e.SourceID)
	}
	out := make([]protocol.PublicEvent, 0, len(events))
	for i, e := range events {
		attrs := map[string]any{}
		for _, k := range AttributeNames {
			if v, ok := e.Attributes[k]; ok {
				b, _ := json.Marshal(v)
				var clone any
				_ = json.Unmarshal(b, &clone)
				attrs[k] = clone
			}
		}
		if v, ok := attrs["participants"]; ok {
			participants := []string{}
			if xs, ok := v.([]any); ok {
				for _, x := range xs {
					if s, ok := x.(string); ok {
						if mapped, yes := known[s]; yes {
							participants = append(participants, mapped)
						}
					}
				}
			}
			attrs["participants"] = participants
		}
		if s, ok := attrs["superseded_by"].(string); ok && s != "" {
			if mapped, yes := known[s]; yes {
				attrs["superseded_by"] = mapped
			} else {
				attrs["superseded_by"] = nil
			}
		}
		content := e.Content
		if render == mab.MemorizeID {
			source, _ := e.Attributes["mab_source"].(string)
			content, _ = mab.Render(source, content) // source is checked by Validate
		} else if speaker, ok := e.Attributes["speaker"].(string); ok && render != "" {
			content = strings.NewReplacer("{speaker}", speaker, "{content}", content).Replace(render)
		}
		out = append(out, protocol.PublicEvent{ID: known[e.SourceID], Owner: id.Anon("owner", e.Owner), SessionID: id.Anon("session", e.Session), Sequence: i, Role: e.Role, Content: content, Timestamp: e.Timestamp, TimeRaw: e.TimeRaw, Attributes: attrs})
	}
	return out
}
func PublicRequest(c Case, id identity.Identity, b protocol.Budget) protocol.RetrieveRequest {
	scope := append([]string{}, c.Scope...)
	var options map[string]string
	if c.Options != nil {
		options = map[string]string{}
		for k, v := range c.Options {
			options[k] = v
		}
	}
	format := "retrieval"
	if c.Kind == "typed_context" {
		format = "typed_context"
	}
	return protocol.RetrieveRequest{Query: c.Query, User: id.Anon("owner", c.User), Scope: scope, AsOf: c.QueryTime, Budget: b, Options: options, QueryInvocationID: id.Anon("invocation", ""), OutputFormat: format}
}
