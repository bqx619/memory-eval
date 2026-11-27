package locomo

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/identity"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update reviewed goldens")

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Spec{Name: "locomo synthetic", Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
		return Import(ctx, "testdata/synthetic_locomo.json")
	}, GoldenSuite: "testdata/golden.json", GoldenStats: "testdata/golden.import.json", Update: *update})
}
func TestDateAndAnnotations(t *testing.T) {
	for _, x := range []struct{ raw, want string }{{"12:01 am on 1 January, 2024", "2024-01-01T00:01:00Z"}, {"12:02 pm on 2 January, 2024", "2024-01-02T12:02:00Z"}, {"1:56 pm on 8 May, 2023", "2023-05-08T13:56:00Z"}} {
		v, e := ParseDate(x.raw)
		if e != nil || v != x.want {
			t.Fatal(v, e)
		}
	}
	if _, e := ParseDate("invalid"); e == nil {
		t.Fatal("bad date accepted")
	}
	s, st, e := Import(t.Context(), "testdata/synthetic_locomo.json")
	if e != nil {
		t.Fatal(e)
	}
	c := s.Episodes[0].Cases
	if len(c[0].Gold.EvidenceIDs) != 2 || c[1].Gold.Answers[0] != "3" || !c[4].Gold.Abstain || len(c[4].Gold.Answers) != 0 {
		t.Fatal(c)
	}
	if c[4].Attributes["adversarial_answer"] != "PRIVATE SYNTHETIC SENTINEL" {
		t.Fatal("private reader distractor missing")
	}
	id, _ := identity.New(strings.Repeat("42", 32), "fixture")
	b, _ := json.Marshal([]any{suite.PublicEvents(s.Episodes[0].Events, id), suite.PublicRequest(c[4], id, protocol.Budget{})})
	if strings.Contains(string(b), "PRIVATE SYNTHETIC SENTINEL") || strings.Contains(string(b), "adversarial_answer") {
		t.Fatal("private reader distractor leaked to public DTO")
	}
	if scan, e := suite.LeakScan(s); e != nil || len(scan.Problems) != 0 {
		t.Fatal(scan, e)
	}
	if st.SessionTimeFailures != 0 {
		t.Fatal(st)
	}
}
func TestRealStatistics(t *testing.T) {
	p := os.Getenv("MEMEVAL_LOCOMO_INPUT")
	if p == "" {
		t.Skip("MEMEVAL_LOCOMO_INPUT not set")
	}
	absolute, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	p = testoracle.Dataset(t, absolute)
	suiteData, s, e := Import(t.Context(), p)
	if e != nil {
		t.Fatal(e)
	}
	if s.Questions != 1986 || s.Events != 5882 || s.Sessions != 272 || s.Counts.Episodes != 10 {
		t.Fatal(s)
	}
	counts := s.Diagnostics["counts"].(map[string]int)
	for k, want := range map[string]int{"integer_answers": 6, "missing_answers": 444, "evidence_references_raw": 2815, "semicolon_evidence_values": 1, "session_dates": 272, "twelve_hour_boundaries": 14, "cat5_private_answers": 446, "cat5_adversarial_answer_field": 446, "cat5_answer_field_conflicts": 2, "cat5_answer_field_fallback": 0, "cat5_missing_private_answers": 0} {
		if counts[k] != want {
			t.Errorf("%s=%d != %d", k, counts[k], want)
		}
	}
	if s.Diagnostics["removed_multimodal_fields"].(map[string]int)["img_url"] != 910 {
		t.Fatal(s.Diagnostics)
	}
	for k, want := range map[string]int{"1": 282, "2": 321, "3": 96, "4": 841, "5": 446} {
		if s.ByQuestionType[k] != want {
			t.Fatal(s.ByQuestionType)
		}
	}
	if len(s.Skipped) != 1 || s.Skipped[0].Count != 3 {
		t.Fatal("missing dropped-reference audit", s.Skipped)
	}
	notes := 0
	for _, ep := range suiteData.Episodes {
		for _, c := range ep.Cases {
			if c.QuestionType == "5" {
				answer, ok := c.Attributes["adversarial_answer"].(string)
				if !ok || answer == "" || len(c.Gold.Answers) != 0 || !c.Gold.Abstain {
					t.Fatal("cat5 private field/abstention", c.ID)
				}
			}
			if c.Gold.Disputed != nil {
				notes++
			}
		}
	}
	if notes != 9 {
		t.Fatal("evidence correction case notes", notes)
	}
}

func TestPrivateCat5AnswerSources(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic_locomo.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, replacement, want, count string }{
		{"preferred", `"adversarial_answer": "PRIVATE SYNTHETIC SENTINEL", "answer": "different"`, "PRIVATE SYNTHETIC SENTINEL", "cat5_answer_field_conflicts"},
		{"fallback", `"answer": "answer-only distractor"`, "answer-only distractor", "cat5_answer_field_fallback"},
		{"missing", `"unused": "ignored"`, "", "cat5_missing_private_answers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := t.TempDir() + "/fixture.json"
			b := strings.ReplaceAll(string(data), `"adversarial_answer": "PRIVATE SYNTHETIC SENTINEL"`, tc.replacement)
			if err := os.WriteFile(p, []byte(b), 0600); err != nil {
				t.Fatal(err)
			}
			s, stats, err := Import(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			for _, ep := range s.Episodes {
				c := ep.Cases[4]
				got, ok := c.Attributes["adversarial_answer"].(string)
				if got != tc.want || (tc.want == "" && ok) || len(c.Gold.Answers) != 0 || !c.Gold.Abstain {
					t.Fatal(c)
				}
			}
			if stats.Diagnostics["counts"].(map[string]int)[tc.count] != 2 {
				t.Fatal(stats.Diagnostics)
			}
		})
	}
}
func TestEvidenceRepairsAndDrops(t *testing.T) {
	for in, want := range map[string]string{"D:11:26": "D11:26", "D30:05": "D30:5", "D": "D", "D1:1": "D1:1"} {
		if got := NormalizeEvidence(in); got != want {
			t.Fatal(in, got, want)
		}
	}
	data, e := os.ReadFile("testdata/synthetic_locomo.json")
	if e != nil {
		t.Fatal(e)
	}
	data = []byte(strings.Replace(string(data), "D1:1; D2:2", "D:1:01 D2:2 D99:99", 1))
	p := t.TempDir() + "/fixture.json"
	os.WriteFile(p, data, 0600)
	s, st, e := Import(t.Context(), p)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Episodes[0].Cases[0].Gold.EvidenceIDs) != 2 || st.Diagnostics["counts"].(map[string]int)["dropped_evidence_references"] != 1 {
		t.Fatal(st.Diagnostics)
	}
	note := s.Episodes[0].Cases[0].Gold.Disputed
	if note == nil || !strings.Contains(note.Note, `evidence "D:1:01 D2:2 D99:99"`) || !strings.Contains(note.Note, `normalized "D:1:01" to "D1:1"`) || !strings.Contains(note.Note, `dropped "D99:99"`) {
		t.Fatal("original reference or processing note missing", note)
	}
	if len(st.Skipped) != 1 || st.Skipped[0].Count != 1 || !strings.Contains(st.Skipped[0].Reason, "question retained") {
		t.Fatal(st.Skipped)
	}
	if scan, err := suite.LeakScan(s); err != nil || len(scan.Problems) != 0 {
		t.Fatal("private correction note leaked", scan, err)
	}
}
