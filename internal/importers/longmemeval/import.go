// Package longmemeval converts private upstream labels into suite v2 gold.
package longmemeval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/fetch"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Stats = importers.Stats

type turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer"`
}
type question struct {
	ID               string          `json:"question_id"`
	Type             string          `json:"question_type"`
	Query            string          `json:"question"`
	Answer           json.RawMessage `json:"answer"`
	Date             string          `json:"question_date"`
	SessionIDs       []string        `json:"haystack_session_ids"`
	Dates            []string        `json:"haystack_dates"`
	Sessions         [][]turn        `json:"haystack_sessions"`
	EvidenceSessions []string        `json:"answer_session_ids"`
}

func parseDate(raw string) (string, bool) {
	t, err := time.Parse("2006/01/02 (Mon) 15:04", raw)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}
func answer(raw json.RawMessage) (string, error) {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var n json.Number
	if len(raw) > 0 && raw[0] != '"' && json.Unmarshal(raw, &n) == nil && n != "" {
		return n.String(), nil
	}
	return "", fmt.Errorf("answer must be a string or number")
}
func Import(ctx context.Context, path, variant, revision string) (*suite.Suite, Stats, error) {
	stats := Stats{LongMemEvalStats: importers.LongMemEvalStats{QuestionTypes: map[string]int{}}}
	if variant != "s" && variant != "oracle" {
		return nil, stats, fmt.Errorf("variant must be s or oracle")
	}
	if lock, err := fetch.ReadLock(filepath.Dir(path)); err == nil {
		if revision != "" && revision != lock.Revision {
			return nil, stats, fmt.Errorf("revision disagrees with source lock")
		}
		revision = lock.Revision
	} else if !os.IsNotExist(err) {
		return nil, stats, err
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(revision) {
		return nil, stats, fmt.Errorf("immutable revision required via source.lock.json or --revision")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, stats, err
	}
	defer f.Close()
	hash := sha256.New()
	d := json.NewDecoder(io.TeeReader(f, hash))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, stats, fmt.Errorf("LongMemEval input must be an array")
	}
	s := &suite.Suite{SchemaVersion: "2.0", SuiteID: "longmemeval-" + variant + "-cleaned@" + revision[:12], Track: "history-qa", Provenance: map[string]any{"source_url": fetch.SourceURL, "source_revision": revision, "converter": "memeval-import-longmemeval/1", "cluster_basis": "episode"}, Episodes: []suite.Episode{}}
	s.Protocol.Ingestion = "per_session"
	s.Protocol.QueryTimePolicy = "reference_only"
	s.Protocol.RetrievalMetrics = []string{"longmemeval_official"}
	s.Limitations = []string{"Each question haystack is independently assembled; filler sessions may share a source pool, so episode clusters do not guarantee independent underlying conversations.", "Memory-only retrieval evaluation; abstention/answer judging deferred. Query time is reference metadata only; no as_of filtering is requested."}
	for d.More() {
		if err = ctx.Err(); err != nil {
			return nil, stats, err
		}
		var q question
		if err = d.Decode(&q); err != nil {
			return nil, stats, err
		}
		if len(q.SessionIDs) != len(q.Dates) || len(q.Dates) != len(q.Sessions) {
			return nil, stats, fmt.Errorf("question %s: haystack array lengths differ", q.ID)
		}
		ans, e := answer(q.Answer)
		if e != nil {
			return nil, stats, e
		}
		ep := suite.Episode{ID: q.ID, ClusterID: q.ID, Events: []suite.Event{}}
		c := suite.Case{ID: q.ID, Kind: "qa", Query: q.Query, User: "user", Scope: []string{}, Category: q.Type, QuestionType: q.Type, ClusterID: q.ID, Gold: suite.Gold{Answers: []string{ans}, Abstain: strings.Contains(q.ID, "_abs"), EvidenceIDs: []string{}, EvidenceSessionIDs: append([]string{}, q.EvidenceSessions...), EvidenceUnit: "session"}}
		var ok bool
		c.QueryTime, ok = parseDate(q.Date)
		if !ok {
			stats.QuestionTimeFailures++
			c.Attributes = map[string]any{"question_date_raw": q.Date}
		}
		order := make([]int, len(q.Sessions))
		dates := make([]string, len(order))
		seen := map[string]bool{}
		offsets := make([]int, len(order))
		nextTurn := map[string]int{}
		for i, sid := range q.SessionIDs {
			if sid == "" {
				return nil, stats, fmt.Errorf("question %s: empty/duplicate session", q.ID)
			}
			if seen[sid] {
				stats.RepeatedSessionOccurrences++
			}
			seen[sid] = true
			offsets[i] = nextTurn[sid]
			nextTurn[sid] += len(q.Sessions[i])
			order[i] = i
			dates[i], ok = parseDate(q.Dates[i])
			if !ok {
				stats.SessionTimeFailures++
			}
		}
		sort.SliceStable(order, func(i, j int) bool {
			a, b := dates[order[i]], dates[order[j]]
			if a == "" {
				return false
			}
			if b == "" {
				return true
			}
			return a < b
		})
		for _, i := range order {
			for j, t := range q.Sessions[i] {
				if t.Role != "user" && t.Role != "assistant" {
					return nil, stats, fmt.Errorf("question %s: unsupported role", q.ID)
				}
				ev := suite.Event{SourceID: fmt.Sprintf("%s:%d", q.SessionIDs[i], offsets[i]+j), Owner: "user", Role: t.Role, Content: t.Content, Session: q.SessionIDs[i], Timestamp: dates[i], Attributes: map[string]any{}}
				if ev.Timestamp == "" {
					ev.TimeRaw = q.Dates[i]
				}
				ep.Events = append(ep.Events, ev)
				if t.HasAnswer {
					c.Gold.EvidenceIDs = append(c.Gold.EvidenceIDs, ev.SourceID)
				}
			}
		}
		c.Cutoff = len(ep.Events)
		ep.Cases = []suite.Case{c}
		s.Episodes = append(s.Episodes, ep)
		stats.Questions++
		stats.QuestionTypes[q.Type]++
		stats.Sessions += len(q.Sessions)
		stats.Events += len(ep.Events)
		if c.Gold.Abstain {
			stats.Abstentions++
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, stats, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, stats, fmt.Errorf("trailing input after array")
	}
	s.Provenance["local_input_sha256"] = hex.EncodeToString(hash.Sum(nil))
	if stats.RepeatedSessionOccurrences > 0 {
		s.Limitations = append(s.Limitations, fmt.Sprintf("%d repeated session occurrences retained with original session IDs; turn indices continue across occurrences in upstream order to prevent source_id collisions (no deduplication).", stats.RepeatedSessionOccurrences))
	}
	b, _ := json.Marshal(stats.LongMemEvalStats)
	s.Limitations = append(s.Limitations, "Import statistics: "+string(b))
	stats, err = importers.Complete(s, stats, "memeval-import-longmemeval/1")
	return s, stats, err
}
