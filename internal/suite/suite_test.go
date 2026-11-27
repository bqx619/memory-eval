package suite

import (
	"context"
	"encoding/json"
	"memeval/internal/identity"
	"strings"
	"testing"
)

const v1 = `{"schema_version":"1.0","suite_id":"unit","provenance":{"source_url":"unit-test://fixture"},"episodes":[{"id":"ep","events":[{"source_id":"past","owner":"u","content":"past","time":"2024-01-01T00:00:00Z"},{"source_id":"future","owner":"u","content":"future","time":"yesterday"}],"cases":[{"id":"q","kind":"qa","query":"what","cutoff":1,"gold":{"evidence_ids":["past"]}}]}]}`

func TestUpgradeAndValidation(t *testing.T) {
	s, err := UpgradeV1([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	ep := s.Episodes[0]
	if s.SchemaVersion != "2.0" || ep.Events[0].Timestamp != "2024-01-01T00:00:00Z" || ep.Events[1].TimeRaw != "yesterday" || ep.Events[0].Role != "observation" || ep.ClusterID != "ep" || ep.Cases[0].ClusterID != "ep" {
		t.Fatalf("bad upgrade: %+v", s)
	}
	a, _ := SHA256(s)
	b, _ := SHA256(s)
	if a != b {
		t.Fatal("unstable canonical hash")
	}
}
func TestRejectInvalidSuites(t *testing.T) {
	for name, mutate := range map[string]func(*Suite){
		"schema": func(s *Suite) { s.SchemaVersion = "1.0" }, "source": func(s *Suite) { delete(s.Provenance, "source_url") }, "no episodes": func(s *Suite) { s.Episodes = nil },
		"duplicate episode": func(s *Suite) { s.Episodes = append(s.Episodes, s.Episodes[0]) }, "duplicate event": func(s *Suite) { s.Episodes[0].Events[1].SourceID = "past" },
		"duplicate case": func(s *Suite) { s.Episodes[0].Cases = append(s.Episodes[0].Cases, s.Episodes[0].Cases[0]) }, "owner": func(s *Suite) { s.Episodes[0].Events[0].Owner = "" },
		"role": func(s *Suite) { s.Episodes[0].Events[0].Role = "speaker" }, "timestamp": func(s *Suite) { s.Episodes[0].Events[0].Timestamp = "today" },
		"query time": func(s *Suite) { s.Episodes[0].Cases[0].QueryTime = "today" }, "cutoff": func(s *Suite) { s.Episodes[0].Cases[0].Cutoff = 99 }, "kind": func(s *Suite) { s.Episodes[0].Cases[0].Kind = "agent" },
		"future evidence": func(s *Suite) { s.Episodes[0].Cases[0].Gold.EvidenceIDs = []string{"future"} }, "missing retrieval evidence": func(s *Suite) { c := &s.Episodes[0].Cases[0]; c.Kind = "retrieval"; c.Gold.EvidenceIDs = nil },
		"MCQ": func(s *Suite) { s.Episodes[0].Cases[0].Kind = "mcq" }, "negative budget": func(s *Suite) { n := -1; s.Episodes[0].Cases[0].Budget.TopK = &n },
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := UpgradeV1([]byte(v1))
			mutate(s)
			if _, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err == nil {
				t.Fatal("accepted invalid suite")
			}
		})
	}
}
func TestDecodeRequiredFieldsAndTypes(t *testing.T) {
	for _, input := range []string{strings.Replace(v1, `"cutoff":1,`, "", 1), strings.Replace(v1, `"cutoff":1`, `"cutoff":true`, 1), strings.Replace(v1, `"content":"past",`, "", 1), strings.Replace(v1, `"query":"what",`, "", 1)} {
		if _, err := UpgradeV1([]byte(input)); err == nil {
			t.Fatal("accepted missing/wrong required field")
		}
	}
}
func TestPublicProjectionHidesGoldAndFuture(t *testing.T) {
	s, _ := UpgradeV1([]byte(v1))
	e := &s.Episodes[0].Events[0]
	e.Attributes = map[string]any{"gold": "SECRET", "has_answer": true, "participants": []any{"past", "future"}, "superseded_by": "future", "aliases": []any{"allowed"}}
	id, _ := identity.New(strings.Repeat("00", 32), "q#0")
	public := PublicEvents(s.Episodes[0].Events[:1], id)
	wire, _ := json.Marshal(public)
	for _, secret := range []string{"SECRET", "has_answer", "future", `"gold"`} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("leaked %s: %s", secret, wire)
		}
	}
	if public[0].Attributes["superseded_by"] != nil {
		t.Fatal("future reference retained")
	}
	if len(public[0].Attributes["participants"].([]string)) != 1 {
		t.Fatal("bad participant mapping")
	}
	public[0].Attributes["aliases"].([]any)[0] = "mutated"
	if e.Attributes["aliases"].([]any)[0] != "allowed" {
		t.Fatal("projection aliases private storage")
	}
}
func TestCanonicalKeyOrder(t *testing.T) {
	b, err := CanonicalJSON(struct {
		Z int    `json:"z"`
		A string `json:"a"`
	}{2, "<中文>"})
	if err != nil || string(b) != `{"a":"<中文>","z":2}` {
		t.Fatal(string(b), err)
	}
}
