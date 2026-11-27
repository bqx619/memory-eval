package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func minimalV3(kind string) *Suite {
	c := Case{ID: "private-case-identifier", Kind: kind, Query: "What happened?", Cutoff: 1}
	switch kind {
	case "qa":
		c.Gold.Answers = []string{"answer"}
	case "mcq":
		c.Options = map[string]string{"A": "first"}
		c.Gold.Choice = "A"
	case "retrieval":
		c.Gold.EvidenceIDs = []string{}
	case "typed_context":
		c.Gold.Expect = json.RawMessage(`{}`)
	case "rubric":
		c.Gold.Rubric = &Rubric{Items: []RubricItem{{Text: "a", Polarity: "must_contain"}}, Aggregate: "mean"}
	case "set_match":
		c.Gold.PhraseSet = &PhraseSet{Items: []string{"a"}}
	case "ordering":
		c.Gold.Sequence = []string{"a", "b"}
	case "summary":
		c.Gold.Reference = "reference"
	case "memory_audit":
		c.Gold.MemoryPoints = []MemoryPoint{{ID: "m", Content: "fact", Polarity: "should_exist"}}
		c.QueryPoint = "after_session_k"
		c.SessionCheckpoint = "private-session-identifier"
	case "recsys":
		c.Gold.ItemIDs = []string{"item"}
	case "agentic":
		c.Gold.Checker = &Checker{Type: "environment", Params: json.RawMessage(`42`)}
	}
	return &Suite{SchemaVersion: "3.0", SuiteID: "v3-fixture", Provenance: map[string]any{"source_url": "fixture://local"}, Episodes: []Episode{{ID: "private-episode-identifier", Events: []Event{{SourceID: "private-source-identifier", Owner: "user", Role: "user", Session: "private-session-identifier", Content: "private-case-identifier is allowed in original content"}}, Cases: []Case{c}}}}
}
func loadBytes(t *testing.T, b []byte) (*Suite, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return Load(t.Context(), path)
}
func TestV3KindMinimumMatrix(t *testing.T) {
	for _, kind := range []string{"qa", "mcq", "retrieval", "typed_context", "rubric", "set_match", "ordering", "summary", "memory_audit", "recsys", "agentic"} {
		t.Run(kind, func(t *testing.T) {
			s := minimalV3(kind)
			counts, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true})
			if err != nil || counts.ByKind[kind] != 1 {
				t.Fatal(counts, err)
			}
			s.Episodes[0].Cases[0].Gold = Gold{}
			if _, err = Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err == nil {
				t.Fatal("missing gold accepted")
			}
		})
	}
}
func TestV3SemanticMatrix(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Suite)
		wantError bool
	}{
		{"abstain", func(s *Suite) { s.Episodes[0].Cases[0].Gold = Gold{Abstain: true} }, false},
		{"event ref", func(s *Suite) {
			s.Episodes[0].Cases[0].Gold.EvidenceRefs = []EvidenceRef{{Kind: "event", Ref: "private-source-identifier"}}
		}, false},
		{"future event ref", func(s *Suite) {
			s.Episodes[0].Cases[0].Gold.EvidenceRefs = []EvidenceRef{{Kind: "event", Ref: "future"}}
		}, true},
		{"future session ref", func(s *Suite) {
			s.Episodes[0].Cases[0].Gold.EvidenceRefs = []EvidenceRef{{Kind: "session", Ref: "future"}}
		}, true},
		{"mixed refs", func(s *Suite) {
			s.Episodes[0].Cases[0].Gold.EvidenceRefs = []EvidenceRef{}
			s.Episodes[0].Cases[0].Gold.EvidenceIDs = []string{}
		}, true},
		{"default end cutoff", func(s *Suite) { s.Episodes[0].Cases[0].Cutoff = 0 }, true},
		{"at index", func(s *Suite) { s.Episodes[0].Cases[0].Cutoff = 0; s.Episodes[0].Cases[0].QueryPoint = "at_index" }, false},
		{"unknown query point", func(s *Suite) { s.Episodes[0].Cases[0].QueryPoint = "future" }, true},
		{"disjoint splits", func(s *Suite) { s.Splits = map[string][]string{"test": {"private-case-identifier"}, "train": {}} }, false},
		{"overlapping splits", func(s *Suite) {
			s.Splits = map[string][]string{"test": {"private-case-identifier"}, "train": {"private-case-identifier"}}
		}, true},
		{"unknown split case", func(s *Suite) { s.Splits = map[string][]string{"test": {"missing"}} }, true},
		{"tool role", func(s *Suite) { s.Episodes[0].Events[0].Role = "tool" }, false},
		{"invalid modality", func(s *Suite) { s.Episodes[0].Events[0].Modality = "future" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := minimalV3("qa")
			tt.mutate(s)
			_, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true})
			if (err != nil) != tt.wantError {
				t.Fatal(err)
			}
		})
	}
	s := minimalV3("retrieval")
	s.Episodes[0].Cases[0].Gold = Gold{EvidenceRefs: []EvidenceRef{{Kind: "text", Ref: "answer"}, {Kind: "trajectory", Ref: "traj"}}}
	counts, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true})
	if err != nil || counts.UnresolvedEvidenceRefs != 2 {
		t.Fatal(counts, err)
	}
	for _, ingestion := range []string{"per_turn_batch:20", "per_chunk:4096:tok:wrapper", "per_document", "per_trajectory", "per_file"} {
		s := minimalV3("qa")
		s.Protocol.Ingestion = ingestion
		if _, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ingestion := range []string{"per_turn_batch:0", "per_chunk:1::wrapper", "per_chunk:x:tok:wrapper"} {
		s := minimalV3("qa")
		s.Protocol.Ingestion = ingestion
		if _, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err == nil {
			t.Fatal(ingestion)
		}
	}
}
func TestAfterSessionCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name       string
		cutoff     int
		checkpoint string
		valid      bool
	}{{"correct", 1, "private-session-identifier", true}, {"before", 0, "private-session-identifier", false}, {"past", 2, "private-session-identifier", false}, {"missing", 1, "missing", false}} {
		t.Run(test.name, func(t *testing.T) {
			s := minimalV3("memory_audit")
			s.Episodes[0].Events = append(s.Episodes[0].Events, Event{SourceID: "second-source-identifier", Owner: "user", Content: "second", Session: "next-session"})
			s.Episodes[0].Cases[0].Cutoff = test.cutoff
			s.Episodes[0].Cases[0].SessionCheckpoint = test.checkpoint
			_, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true})
			if (err == nil) != test.valid {
				t.Fatal(err)
			}
		})
	}
	s := minimalV3("memory_audit")
	s.Episodes[0].Events = append(s.Episodes[0].Events, Event{SourceID: "second-source-identifier", Owner: "user", Session: "private-session-identifier"})
	if _, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err == nil {
		t.Fatal("checkpoint before final session event accepted")
	}
}
func TestV2NewFieldsRejectedEvenEmpty(t *testing.T) {
	s := minimalV3("qa")
	s.SchemaVersion = "2.0"
	b, _ := json.Marshal(s)
	var base map[string]any
	json.Unmarshal(b, &base)
	for _, test := range []struct {
		path  []string
		key   string
		value any
	}{{nil, "protocol_ref", ""}, {nil, "license", nil}, {nil, "splits", map[string]any{}}, {[]string{"protocol"}, "text_only", false}, {[]string{"event"}, "attachments", []any{}}, {[]string{"case"}, "query_point", ""}, {[]string{"gold"}, "sequence", []any{}}} {
		t.Run(test.key, func(t *testing.T) {
			var m map[string]any
			json.Unmarshal(b, &m)
			dest := m
			for _, part := range test.path {
				switch part {
				case "event":
					dest = m["episodes"].([]any)[0].(map[string]any)["events"].([]any)[0].(map[string]any)
				case "case", "gold":
					dest = m["episodes"].([]any)[0].(map[string]any)["cases"].([]any)[0].(map[string]any)
					if part == "gold" {
						dest = dest["gold"].(map[string]any)
					}
				default:
					dest = dest[part].(map[string]any)
				}
			}
			dest[test.key] = test.value
			raw, _ := json.Marshal(m)
			_, err := loadBytes(t, raw)
			if err == nil || !strings.Contains(err.Error(), "requires schema_version 3.0") {
				t.Fatal(err)
			}
		})
	}
	s.License = &License{}
	if _, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err == nil {
		t.Fatal("programmatic v3 field accepted in v2")
	}
}
func TestV3UnknownFieldsAndV2Compatibility(t *testing.T) {
	for _, path := range [][]string{{}, {"protocol"}, {"episode"}, {"event"}, {"case"}, {"gold"}, {"checker"}, {"license"}, {"attachment"}} {
		t.Run(strings.Join(path, "/"), func(t *testing.T) {
			s := minimalV3("agentic")
			s.License = &License{}
			s.Episodes[0].Events[0].Attachments = []Attachment{{Kind: "image", Ref: "image.png"}}
			b, _ := json.Marshal(s)
			var m map[string]any
			json.Unmarshal(b, &m)
			dest := m
			for _, part := range path {
				ep := m["episodes"].([]any)[0].(map[string]any)
				switch part {
				case "episode":
					dest = ep
				case "event", "attachment":
					dest = ep["events"].([]any)[0].(map[string]any)
					if part == "attachment" {
						dest = dest["attachments"].([]any)[0].(map[string]any)
					}
				case "case", "gold", "checker":
					dest = ep["cases"].([]any)[0].(map[string]any)
					if part != "case" {
						dest = dest["gold"].(map[string]any)
					}
					if part == "checker" {
						dest = dest["checker"].(map[string]any)
					}
				default:
					dest = m[part].(map[string]any)
				}
			}
			dest["typo"] = true
			b, _ = json.Marshal(m)
			_, err := loadBytes(t, b)
			if err == nil || !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "typo") {
				t.Fatal(err)
			}
		})
	}
	s := minimalV3("qa")
	s.SchemaVersion = "2.0"
	s.Episodes[0].Cases[0].Gold = Gold{}
	s.Episodes[0].Cases[0].Cutoff = 0
	b, _ := json.Marshal(s)
	b = bytes.Replace(b, []byte(`"schema_version"`), []byte(`"unknown_legacy":true,"schema_version"`), 1)
	if _, err := loadBytes(t, b); err != nil {
		t.Fatalf("v2 tightened: %v", err)
	}
	if _, err := Upgrade(b); err == nil || !strings.Contains(err.Error(), "unknown_legacy") {
		t.Fatal(err)
	}
}
func TestUpgradeOnlyVersion(t *testing.T) {
	s := minimalV3("qa")
	s.SchemaVersion = "2.0"
	Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true})
	b, _ := json.MarshalIndent(s, "", "  ")
	b = append(b, '\n')
	want := bytes.Replace(b, []byte(`"schema_version": "2.0"`), []byte(`"schema_version": "3.0"`), 1)
	got, err := UpgradeBytes(b)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatal("upgrade changed more than version", err)
	}
	upgraded, err := Upgrade(b)
	if err != nil {
		t.Fatal(err)
	}
	s.SchemaVersion = "3.0"
	if !reflect.DeepEqual(s, upgraded) {
		t.Fatal("upgrade changed fields")
	}
}

var pinned = map[string]string{
	"longmemeval-s.json":         "bd706a720088c33b5f2b9e73549c1a79b6f90a8ed5201d741eddbc195cdc0978",
	"longmemeval-s-strat70.json": "012e5685644d98004cbf6078f68142476451bebd253fbbfab524748555fc8bf4",
	"pmb.json":                   "7a1f053823087312d7e6a43c6d66c627416e52c8e34f90aa8d158aa6aecd330b",
	"pmb-retrieval.json":         "64bdace8103f89898262d659e3a69fc500b5162920cc2469d3dea8c28f9af0e9",
}

func normalized(t *testing.T, name string) *Suite {
	t.Helper()
	s, err := Load(context.Background(), testoracle.Dataset(t, filepath.Join("normalized", name)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestNormalizedSuiteHashesPinned(t *testing.T) {
	for _, name := range sortedKeys(pinned) {
		t.Run(name, func(t *testing.T) {
			got, err := SHA256(normalized(t, name))
			if err != nil || got != pinned[name] {
				t.Fatal(got, err)
			}
		})
	}
}
func TestNormalizedSuitesLeakFree(t *testing.T) {
	for _, name := range sortedKeys(pinned) {
		t.Run(name, func(t *testing.T) {
			r, err := LeakScan(normalized(t, name))
			if err != nil || len(r.Problems) > 0 {
				t.Fatalf("leak scan %v, problems=%d, first=%v", err, len(r.Problems), r.Problems[:min(3, len(r.Problems))])
			}
			if r.Checked == 0 || r.AnswerTextScan != "not_applicable" {
				t.Fatal(r)
			}
		})
	}
}
func TestLeakScanNegativeAndPublicSequence(t *testing.T) {
	s := minimalV3("qa")
	if _, err := Validate(context.Background(), s, ValidateOptions{SkipLeakScan: true}); err != nil {
		t.Fatal(err)
	}
	r, err := LeakScan(s)
	if err != nil || len(r.Problems) != 0 {
		t.Fatal(r, err)
	}
	for _, key := range []string{"has_answer", "sequence", "nested"} {
		s := minimalV3("qa")
		value := any(true)
		if key == "nested" {
			key = "aliases"
			value = map[string]any{"answers": []string{"bad"}}
		}
		s.Episodes[0].Events[0].Attributes = map[string]any{key: value}
		r, err := LeakScan(s)
		if err != nil || len(r.Problems) == 0 {
			t.Fatal(r, err)
		}
	}
	s.Episodes[0].Cases[0].Query = "echo private-case-identifier"
	r, err = LeakScan(s)
	if err != nil || len(r.Problems) == 0 {
		t.Fatal(r, err)
	}
}
