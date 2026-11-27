// Package memora imports pinned Apache-2.0 Memora data without exposing memory annotations.
package memora

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const Revision = "a6493188efc836d6511ed5e4163fe3ba87da30ff"
const Converter = "memeval-import-memora/1"

var Periods = []string{"weekly", "monthly", "quarterly"}
var Categories = []string{"remembering", "reasoning", "recommending"}

type Options struct{ Input, Period, Out string }
type Imported struct {
	Path        string          `json:"path"`
	SuiteID     string          `json:"suite_id"`
	SuiteSHA256 string          `json:"suite_sha256"`
	Statistics  importers.Stats `json:"statistics"`
}
type Turn struct {
	Turn    int    `json:"turn"`
	Speaker string `json:"speaker"`
	Message string `json:"message"`
}
type Session struct {
	ID           int     `json:"session_id"`
	Type         string  `json:"session_type"`
	Operation    *string `json:"operation"`
	Date         string  `json:"date"`
	Persona      string  `json:"persona"`
	Conversation []Turn  `json:"conversation"`
}
type Evaluation struct {
	Question string `json:"evaluation_question"`
	Expected string `json:"expected_answer"`
	Type     string `json:"evaluation_type"`
}
type Question struct {
	ID         string `json:"question_id"`
	Query      string `json:"question"`
	Date       string `json:"question_date"`
	Memory     any    `json:"memory_evidence"`
	Forgetting any    `json:"forgetting_evidence"`
	Evaluation struct {
		Questions []Evaluation `json:"evaluation_questions"`
	} `json:"evaluation"`
}
type Questions struct {
	Persona   string                `json:"persona"`
	DateRange any                   `json:"date_range"`
	Questions map[string][]Question `json:"questions"`
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}

// Verify checks the entire pinned extracted tree; no network or archive extraction occurs.
func Verify(ctx context.Context, input string) (string, error) {
	root := filepath.Dir(filepath.Clean(input))
	var lock struct {
		Revision string
		Manifest string `json:"files_manifest"`
		Total    int    `json:"files_total"`
	}
	if e := readJSON(filepath.Join(root, "source.lock.json"), &lock); e != nil {
		return "", e
	}
	if lock.Revision != Revision || lock.Manifest != "files.sha256.jsonl" {
		return "", fmt.Errorf("Memora lock differs from pinned revision/manifest")
	}
	b, e := os.ReadFile(filepath.Join(root, lock.Manifest))
	if e != nil {
		return "", e
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	seen := map[string]bool{}
	for sc.Scan() {
		if e = ctx.Err(); e != nil {
			return "", e
		}
		var row struct {
			Path, SHA256 string
			Bytes        int64
		}
		if e = json.Unmarshal(sc.Bytes(), &row); e != nil {
			return "", e
		}
		if !filepath.IsLocal(row.Path) || seen[row.Path] {
			return "", fmt.Errorf("invalid/duplicate manifest path %q", row.Path)
		}
		seen[row.Path] = true
		f, e := os.Open(filepath.Join(root, row.Path))
		if e != nil {
			return "", e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		f.Close()
		if e != nil {
			return "", e
		}
		if n != row.Bytes || fmt.Sprintf("%x", h.Sum(nil)) != row.SHA256 {
			return "", fmt.Errorf("Memora manifest mismatch: %s", row.Path)
		}
	}
	if e = sc.Err(); e != nil {
		return "", e
	}
	if len(seen) != lock.Total {
		return "", fmt.Errorf("manifest count differs")
	}
	// Refuse extra importable inputs not covered by the manifest.
	e = filepath.WalkDir(input, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if !seen[filepath.ToSlash(rel)] {
			return fmt.Errorf("unlocked input %s", rel)
		}
		return nil
	})
	return hash(b), e
}
func Import(ctx context.Context, o Options) ([]Imported, error) {
	if o.Period != "all" && !slices.Contains(Periods, o.Period) {
		return nil, fmt.Errorf("period must be weekly|monthly|quarterly|all")
	}
	h, e := Verify(ctx, o.Input)
	if e != nil {
		return nil, e
	}
	periods := []string{o.Period}
	if o.Period == "all" {
		periods = Periods
	}
	out := []Imported{}
	for _, p := range periods {
		dest := o.Out
		if o.Period == "all" {
			dest = filepath.Join(dest, p)
		}
		v, e := ImportPeriod(ctx, Options{o.Input, p, dest}, h, false)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func date(raw string) (string, error) {
	t, e := time.Parse("2006-01-02", raw)
	if e != nil {
		return "", e
	}
	return t.Format(time.RFC3339), nil
}

// Evidence retains the complete private source text and adds only defensible exact
// value matches within an explicitly referenced session. No semantic guesses.
func evidence(v any, events []suite.Event) ([]suite.EvidenceRef, error) {
	out := []suite.EvidenceRef{}
	if v == nil {
		return out, nil
	}
	b, e := suite.CanonicalJSON(v)
	if e != nil {
		return nil, e
	}
	out = append(out, suite.EvidenceRef{Kind: "text", Ref: string(b)})
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, y := range x {
				walk(y)
			}
		case map[string]any:
			value, _ := x["value"].(string)
			sid, ok := x["session_id"].(float64)
			if value != "" && ok && sid == float64(int(sid)) {
				for _, ev := range events {
					if ev.Session == "s"+strconv.Itoa(int(sid)) && strings.Contains(ev.Content, value) && !seen[ev.SourceID] {
						seen[ev.SourceID] = true
						out = append(out, suite.EvidenceRef{Kind: "event", Ref: ev.SourceID})
					}
				}
			}
			keys := []string{}
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				walk(x[k])
			}
		}
	}
	walk(v)
	return out, nil
}
func Episode(persona, period string, sessions []Session, q Questions) (*suite.Episode, error) {
	id := persona + "_" + period
	ep := &suite.Episode{ID: id, ClusterID: id, Events: []suite.Event{}, Cases: []suite.Case{}}
	sessions = slices.Clone(sessions)
	slices.SortFunc(sessions, func(a, b Session) int { return a.ID - b.ID })
	seen := map[int]bool{}
	for _, s := range sessions {
		if s.ID < 1 || seen[s.ID] || s.Persona != persona {
			return nil, fmt.Errorf("invalid session identity")
		}
		seen[s.ID] = true
		ts, e := date(s.Date)
		if e != nil {
			return nil, e
		}
		for _, t := range s.Conversation {
			role := ""
			switch t.Speaker {
			case "user_agent", "user":
				role = "user"
			case "ai_agent":
				role = "assistant"
			default:
				return nil, fmt.Errorf("unknown speaker %q", t.Speaker)
			}
			ep.Events = append(ep.Events, suite.Event{SourceID: fmt.Sprintf("%s:s%d:t%d", id, s.ID, t.Turn), Owner: "user", Role: role, Session: fmt.Sprintf("s%d", s.ID), Content: t.Message, Timestamp: ts, TimeRaw: s.Date, Attributes: map[string]any{}})
		}
	}
	if q.Persona != persona {
		return nil, fmt.Errorf("question persona mismatch")
	}
	for k := range q.Questions {
		if !slices.Contains(Categories, k) {
			return nil, fmt.Errorf("unknown task %q", k)
		}
	}
	for _, cat := range Categories {
		for _, qv := range q.Questions[cat] {
			ts, e := date(qv.Date)
			if e != nil {
				return nil, e
			}
			yes := true
			g := suite.Gold{Rubric: &suite.Rubric{Aggregate: "fama", Items: []suite.RubricItem{}}, Checker: &suite.Checker{Type: "memora_fama"}, EvidenceRefs: []suite.EvidenceRef{}}
			for _, item := range qv.Evaluation.Questions {
				if item.Type != "memory_presence" && item.Type != "forgetting_absence" {
					return nil, fmt.Errorf("unknown evaluation_type")
				}
				pol := "must_contain"
				if item.Expected == "no" {
					pol = "must_not_contain"
				} else if item.Expected != "yes" {
					return nil, fmt.Errorf("unknown expected_answer")
				}
				g.Rubric.Items = append(g.Rubric.Items, suite.RubricItem{Text: item.Question, Polarity: pol, Weight: 1, Group: item.Type})
			}
			for _, v := range []any{qv.Memory, qv.Forgetting} {
				refs, e := evidence(v, ep.Events)
				if e != nil {
					return nil, e
				}
				g.EvidenceRefs = append(g.EvidenceRefs, refs...)
			}
			ep.Cases = append(ep.Cases, suite.Case{ID: id + ":" + qv.ID, ClusterID: id, Kind: "rubric", Category: cat, QuestionType: cat, Query: qv.Query, QueryTime: ts, InjectCurrentDate: &yes, User: "user", Scope: []string{}, Cutoff: len(ep.Events), QueryPoint: "end_of_history", Attributes: map[string]any{"date_range": q.DateRange}, Gold: g})
		}
	}
	return ep, nil
}
func ImportPeriod(ctx context.Context, o Options, inputHash string, fixture bool) (Imported, error) {
	if !slices.Contains(Periods, o.Period) {
		return Imported{}, fmt.Errorf("invalid period")
	}
	dirs, e := os.ReadDir(filepath.Join(o.Input, o.Period))
	if e != nil {
		return Imported{}, e
	}
	w, e := suite.NewWriter(o.Out)
	if e != nil {
		return Imported{}, e
	}
	defer w.Abort()
	yes := true
	header := &suite.Suite{SchemaVersion: "3.0", SuiteID: "memora-" + o.Period + "@" + Revision[:12], Track: "memory-qa", ProtocolRef: "protocols/memora.json#" + o.Period, License: &suite.License{ID: "Apache-2.0", RedistributeNormalized: true, Source: "https://github.com/geniesinc/Memora"}, Provenance: map[string]any{"revision": Revision, "converter": Converter, "local_input_sha256": inputHash, "fixture_truncated": fixture, "source_url": "https://github.com/geniesinc/Memora", "attribution": "Genies Inc., Memora"}, Limitations: []string{"Official three-judge majority is not reproduced; configured evaluator Router has partial fidelity without a matching agreement study", "Evidence text is retained privately; exact value matches in the referenced session add event refs, without inferring missing evidence", "Track 1 upstream full-session renderer checks speaker=user while release uses user_agent; normalized user roles retain these turns"}, Protocol: suite.SuiteProtocol{Ingestion: "per_session", ResetPolicyAllowed: []string{"incremental_per_episode"}, TimeExposure: "session", QueryTimePolicy: "reference_only", InjectCurrentDate: &yes, TextOnly: &yes}}
	if fixture {
		header.SuiteID += "-fixture"
	}
	types, ops, groups, cats := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	personas, sessionsCount, eventRefs, textRefs := 0, 0, 0, 0
	per := map[string]any{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if e = ctx.Err(); e != nil {
			return Imported{}, e
		}
		p := filepath.Join(o.Input, o.Period, d.Name())
		files, e := filepath.Glob(filepath.Join(p, "conversations/session_*.json"))
		if e != nil {
			return Imported{}, e
		}
		ss := []Session{}
		for _, f := range files {
			var s Session
			if e = readJSON(f, &s); e != nil {
				return Imported{}, e
			}
			ss = append(ss, s)
			types[s.Type]++
			op := "null"
			if s.Operation != nil {
				op = *s.Operation
			}
			ops[op]++
		}
		var q Questions
		if e = readJSON(filepath.Join(p, "evaluation_questions_"+d.Name()+".json"), &q); e != nil {
			return Imported{}, e
		}
		ep, e := Episode(d.Name(), o.Period, ss, q)
		if e != nil {
			return Imported{}, fmt.Errorf("%s: %w", d.Name(), e)
		}
		personas++
		sessionsCount += len(ss)
		per[d.Name()] = map[string]int{"sessions": len(ss), "turns": len(ep.Events), "questions": len(ep.Cases)}
		for _, c := range ep.Cases {
			cats[c.Category]++
			for _, item := range c.Gold.Rubric.Items {
				groups[item.Group]++
			}
			for _, ref := range c.Gold.EvidenceRefs {
				if ref.Kind == "event" {
					eventRefs++
				} else {
					textRefs++
				}
			}
		}
		if e = w.WriteEpisode(ep); e != nil {
			return Imported{}, e
		}
	}
	if e = w.Close(header); e != nil {
		return Imported{}, e
	}
	s, e := suite.Open(ctx, o.Out)
	if e != nil {
		return Imported{}, e
	}
	stats, e := importers.Complete(s, importers.Stats{Diagnostics: map[string]any{"period": o.Period, "periods": 1, "personas": personas, "sessions": sessionsCount, "per_persona": per, "questions_by_category": cats, "evaluation_subquestions": groups["memory_presence"] + groups["forgetting_absence"], "evaluation_types": groups, "session_types": types, "operations": ops, "evidence_event_refs": eventRefs, "evidence_text_refs": textRefs}}, Converter)
	if e != nil {
		return Imported{}, e
	}
	stats.Questions = stats.Counts.Cases
	stats.Events = stats.Counts.Events
	stats.Sessions = sessionsCount
	stats.UpstreamScale.Declared = map[string]any{"personas": 10, "sessions_per_persona_approx": map[string]int{"weekly": 150, "monthly": 600, "quarterly": 2000}[o.Period], "questions_per_persona": map[string]int{"weekly": 15, "monthly": 15, "quarterly": 30}[o.Period]}
	stats.UpstreamScale.Explanation = "data/README.md session counts are approximate; diagnostics.per_persona records exact locked-file counts. Each period is a separate bundle."
	b, e := suite.CanonicalJSON(stats)
	if e != nil {
		return Imported{}, e
	}
	if e = os.WriteFile(filepath.Join(o.Out, "import.json"), append(b, '\n'), 0600); e != nil {
		return Imported{}, e
	}
	sha, e := suite.SHA256(s)
	return Imported{o.Out, s.SuiteID, sha, stats}, e
}
