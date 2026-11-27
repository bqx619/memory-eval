package longmemeval

import (
	"context"
	"encoding/json"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture() map[string]any {
	return map[string]any{"question_id": "PRIVATE-Q1", "question_type": "knowledge-update", "question": "Now?", "answer": "new", "question_date": "2025-01-01", "haystack_session_ids": []string{"private-session"}, "haystack_dates": []string{"2024-01-01"}, "haystack_sessions": [][]map[string]any{{{"role": "user", "content": "new", "has_answer": true}}}, "answer_session_ids": []string{"private-session"}}
}
func importRows(t *testing.T, rows any) (*suite.Suite, Stats, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "data.json")
	b, _ := json.Marshal(rows)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return Import(context.Background(), p, "s", strings.Repeat("a", 40))
}
func TestPythonSchemaFixtureAndPrivateGold(t *testing.T) {
	row := fixture()
	s, stats, err := importRows(t, []any{row})
	if err != nil {
		t.Fatal(err)
	}
	ep := s.Episodes[0]
	c := ep.Cases[0]
	if stats.Questions != 1 || stats.SessionTimeFailures != 1 || stats.QuestionTimeFailures != 1 || ep.Events[0].TimeRaw != "2024-01-01" || c.Attributes["question_date_raw"] != "2025-01-01" {
		t.Fatal(stats, ep)
	}
	if c.Gold.EvidenceUnit != "session" || !reflect.DeepEqual(c.Gold.EvidenceIDs, []string{"private-session:0"}) || !reflect.DeepEqual(c.Gold.EvidenceSessionIDs, []string{"private-session"}) {
		t.Fatal(c.Gold)
	}
	id, _ := identity.New(strings.Repeat("42", 32), c.ID)
	wire, _ := json.Marshal([]any{suite.PublicEvents(ep.Events, id), suite.PublicRequest(c, id, protocol.Budget{})})
	for _, bad := range []string{"has_answer", "answer_session_ids", "PRIVATE-Q1", "private-session", "question_id"} {
		if strings.Contains(string(wire), bad) {
			t.Fatalf("leaked %s", bad)
		}
	}
	row["question_id"] = "PRIVATE-Q1_abs"
	s, stats, err = importRows(t, []any{row})
	if err != nil || !s.Episodes[0].Cases[0].Gold.Abstain || stats.Abstentions != 1 {
		t.Fatal(stats, err)
	}
	// M2a retains explicit turn labels even for abstentions; runner excludes their retrieval metrics.
	if len(s.Episodes[0].Cases[0].Gold.EvidenceIDs) != 1 {
		t.Fatal("lost labeled turn")
	}
}
func TestTimeOrderNumbersAndGoldValidation(t *testing.T) {
	row := fixture()
	row["answer"] = 42
	row["question_date"] = "2025/01/01 (Wed) 12:30"
	row["haystack_session_ids"] = []string{"later", "earlier", "same", "bad"}
	row["haystack_dates"] = []string{"2024/01/02 (Tue) 00:00", "2024/01/01 (Mon) 00:00", "2024/01/01 (Mon) 00:00", "invalid"}
	row["haystack_sessions"] = [][]map[string]any{{{"role": "assistant", "content": "later"}}, {{"role": "user", "content": "earlier", "has_answer": true}}, {{"role": "user", "content": "same"}}, {{"role": "user", "content": "bad"}}}
	row["answer_session_ids"] = []string{"earlier"}
	s, stats, err := importRows(t, []any{row})
	if err != nil {
		t.Fatal(err)
	}
	ep := s.Episodes[0]
	order := []string{}
	for _, e := range ep.Events {
		order = append(order, e.Session)
	}
	if !reflect.DeepEqual(order, []string{"earlier", "same", "later", "bad"}) || ep.Cases[0].QueryTime != "2025-01-01T12:30:00Z" || ep.Cases[0].Gold.Answers[0] != "42" || stats.SessionTimeFailures != 1 {
		t.Fatal(ep, stats)
	}
	ep.Cases[0].Gold.EvidenceSessionIDs = []string{"unknown"}
	s.Episodes[0] = ep
	if _, err = suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true}); err == nil {
		t.Fatal("unknown session accepted")
	}
	row["answer_session_ids"] = []string{"unknown"}
	if _, _, err = importRows(t, []any{row}); err == nil {
		t.Fatal("unknown session imported")
	}
	row["haystack_dates"] = []string{}
	if _, _, err = importRows(t, []any{row}); err == nil {
		t.Fatal("mismatched arrays accepted")
	}
}

func TestRepeatedSessionOccurrencesRetainAllTurns(t *testing.T) {
	row := fixture()
	row["haystack_session_ids"] = []string{"private-session", "private-session"}
	row["haystack_dates"] = []string{"2024/01/02 (Tue) 00:00", "2024/01/01 (Mon) 00:00"}
	row["haystack_sessions"] = [][]map[string]any{{{"role": "user", "content": "same"}}, {{"role": "user", "content": "same", "has_answer": true}}}
	s, stats, err := importRows(t, []any{row})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Events != 2 || stats.Sessions != 2 || stats.RepeatedSessionOccurrences != 1 || s.Episodes[0].Events[0].SourceID != "private-session:1" || s.Episodes[0].Events[1].SourceID != "private-session:0" {
		t.Fatal(s, stats)
	}
}
