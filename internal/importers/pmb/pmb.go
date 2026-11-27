package pmb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
)

func importTyped(ctx context.Context, path string) (*suite.Suite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Beliefs []map[string]any `json:"beliefs"`
		Cases   []struct {
			ID       string          `json:"caseId"`
			Query    string          `json:"query"`
			User     *string         `json:"userId"`
			Scope    []string        `json:"scope"`
			Category string          `json:"category"`
			Budget   suite.Budget    `json:"budget"`
			Expect   json.RawMessage `json:"expect"`
		} `json:"cases"`
		Provenance map[string]any `json:"provenance"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Beliefs == nil || raw.Cases == nil || raw.Provenance == nil {
		return nil, fmt.Errorf("expected PMB field projection")
	}
	if raw.Provenance["source_url"] == nil {
		raw.Provenance["source_url"] = raw.Provenance["repository"]
		if raw.Provenance["source_url"] == nil {
			raw.Provenance["source_url"] = "https://github.com/tenurehq/precisionMemBench"
		}
	}
	h := sha256.Sum256(data)
	raw.Provenance["projection_sha256"] = hex.EncodeToString(h[:])
	raw.Provenance["local_input_sha256"] = hex.EncodeToString(h[:])
	e := suite.Episode{ID: "pmb-seed-corpus", ClusterID: "pmb-seed-corpus", Events: []suite.Event{}, Cases: []suite.Case{}}
	for _, b := range raw.Beliefs {
		id, okID := b["_id"].(string)
		owner, okOwner := b["user_id"].(string)
		content, okContent := b["content"].(string)
		if !okID || !okOwner || !okContent {
			return nil, fmt.Errorf("invalid PMB seed")
		}
		attrs := map[string]any{}
		for _, k := range suite.AttributeNames {
			if k == "speaker" {
				continue
			}
			if v, ok := b[k]; ok {
				attrs[k] = v
			}
		}
		e.Events = append(e.Events, suite.Event{SourceID: id, Owner: owner, Content: content, Session: "seed", Role: "fact", Attributes: attrs})
	}
	for _, c := range raw.Cases {
		user := "test-user"
		if c.User != nil {
			user = *c.User
		}
		e.Cases = append(e.Cases, suite.Case{ID: c.ID, Query: c.Query, User: user, Scope: c.Scope, Kind: "typed_context", Category: c.Category, Budget: c.Budget, Cutoff: len(e.Events), Gold: suite.Gold{Expect: c.Expect}})
	}
	s := &suite.Suite{SchemaVersion: "2.0", SuiteID: "precisionmembench-projection-v1", Track: "seeded-state-diagnostic", Provenance: raw.Provenance, Episodes: []suite.Episode{e}, Limitations: []string{"Source-specific output tiers and expectations are not a universal memory benchmark", "35 structured seeds are not longitudinal raw conversations", "Vacuous assertions are unscored, unlike upstream all([]) success", "Context prelude is produced by the tested adapter, not the upstream fixed persona service"}}
	_, err = suite.Validate(context.Background(),

		s, suite.ValidateOptions{
			SkipLeakScan: true},
	)
	return s, err
}

func Import(ctx context.Context, path string) (*suite.Suite, importers.Stats, error) {
	s, err := importTyped(ctx, path)
	if err != nil {
		return nil, importers.Stats{}, err
	}
	stats, err := importers.Complete(s, importers.Stats{}, "memeval-import-pmb/1")
	return s, stats, err
}
