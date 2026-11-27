package pmb

import (
	"context"
	"memeval/internal/suite"
	"reflect"
	"strings"
	"testing"
)

func TestBundledProjection(t *testing.T) {
	s, _, err := Import(context.Background(), "../../../docs/data/pmb_projection.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := suite.Validate(context.Background(),

		s, suite.ValidateOptions{
			SkipLeakScan: true},
	)
	if err != nil || c.Episodes != 1 || c.Events != 35 || c.Cases != 77 {
		t.Fatal(c, err)
	}
	if s.Episodes[0].ClusterID != "pmb-seed-corpus" || s.Provenance["projection_sha256"] == nil {
		t.Fatal("missing provenance")
	}
}

func TestRetrievalProjection(t *testing.T) {
	ctx := context.Background()
	path := "../../../docs/data/pmb_projection.json"
	typed, _, err := Import(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, _, err := ImportAs(ctx, path, "retrieval")
	if err != nil {
		t.Fatal(err)
	}
	if retrieval.SuiteID != "pmb-retrieval" || len(retrieval.Episodes[0].Cases) != 75 {
		t.Fatal("projection counts")
	}
	if !reflect.DeepEqual(typed.Episodes[0].Events, retrieval.Episodes[0].Events) {
		t.Fatal("retrieval changed events")
	}
	originals := map[string]suite.Case{}
	for _, c := range typed.Episodes[0].Cases {
		originals[c.ID] = c
	}
	empty := 0
	for _, c := range retrieval.Episodes[0].Cases {
		if len(c.Gold.EvidenceIDs) == 0 {
			empty++
			if !c.Gold.Exhaustive {
				t.Fatal("empty inclusion must be exhaustive")
			}
		}
		original := originals[c.ID]
		if c.Kind != "retrieval" || c.Gold.Expect != nil {
			t.Fatal(c)
		}
		if c.Query != original.Query || c.User != original.User || !reflect.DeepEqual(c.Scope, original.Scope) || !reflect.DeepEqual(c.Budget, original.Budget) {
			t.Fatal("public query changed")
		}
	}
	if empty != 32 {
		t.Fatalf("empty cases = %d", empty)
	}
	if !strings.Contains(strings.Join(retrieval.Limitations, " "), "Skipped 2 cases") {
		t.Fatal(retrieval.Limitations)
	}
	if _, _, err = ImportAs(ctx, path, "invented"); err == nil {
		t.Fatal("unsupported projection accepted")
	}
}
