package pmb

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/importers"
	"memeval/internal/suite"
)

// ImportAs keeps the M0 typed projection unchanged; retrieval is a separate,
// explicitly labelled smoke suite using explicit relevantBeliefs inclusion assertions.
func ImportAs(ctx context.Context, path, kind string) (*suite.Suite, importers.Stats, error) {
	if kind != "typed_context" && kind != "retrieval" {
		return nil, importers.Stats{}, fmt.Errorf("unsupported PMB projection %q", kind)
	}
	s, stats, err := Import(ctx, path)
	if err != nil || kind == "typed_context" {
		return s, stats, err
	}
	skipped := 0
	err = s.Iter(ctx, func(ep *suite.Episode) error {
		cases := []suite.Case{}
		for _, c := range ep.Cases {
			var exp struct {
				Relevant struct {
					Only *[]string `json:"shouldOnlyInclude"`
					Must *[]string `json:"mustInclude"`
				} `json:"relevantBeliefs"`
			}
			if err := json.Unmarshal(c.Gold.Expect, &exp); err != nil {
				return err
			}
			ids, exhaustive := []string{}, exp.Relevant.Only != nil
			if exp.Relevant.Only != nil {
				ids = *exp.Relevant.Only
			} else if exp.Relevant.Must != nil {
				ids = *exp.Relevant.Must
			} else {
				skipped++
				continue
			}
			c.Kind = "retrieval"
			c.Gold = suite.Gold{EvidenceIDs: ids, EvidenceUnit: "event", Exhaustive: exhaustive}
			cases = append(cases, c)
		}
		ep.Cases = cases
		return nil
	})
	if err != nil {
		return nil, stats, err
	}
	s.SuiteID = "pmb-retrieval"
	s.Track = "retrieval-smoke"
	s.Limitations = append(s.Limitations, "PMB retrieval projection is an offline smoke suite, not the official PMB protocol", fmt.Sprintf("Skipped %d cases without relevantBeliefs shouldOnlyInclude/mustInclude labels", skipped))
	stats.Skipped = append(stats.Skipped, importers.Skipped{Reason: "no relevantBeliefs shouldOnlyInclude/mustInclude labels", Count: skipped})
	stats, err = importers.Complete(s, stats, "memeval-import-pmb-retrieval/1")
	return s, stats, err
}
