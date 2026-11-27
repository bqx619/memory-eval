// Package halumem converts locked HaluMem locally. ND data is never a fixture.
package halumem

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Revision = "cb04336aa1b732d4b24f5186c552456b4099806e"
const Converter = "memeval-import-halumem/1"
const CostNotice = "HaluMem: approximately 65–70 regular sessions/user; judge calls scale with memory points + new memory units + updates + questions (tens of thousands for 20 users). Acceptance uses fake upstream only; live evaluation requires separately approved budget."

type Options struct{ Input, Variant, Out string }
type Imported struct {
	Path        string          `json:"path"`
	SuiteID     string          `json:"suite_id"`
	SuiteSHA256 string          `json:"suite_sha256"`
	Statistics  importers.Stats `json:"statistics"`
}
type Turn struct {
	Role, Content, Timestamp string
	DialogueTurn             int `json:"dialogue_turn"`
}
type Point struct {
	Index      int
	Content    string   `json:"memory_content"`
	Type       string   `json:"memory_type"`
	IsUpdate   string   `json:"is_update"`
	Originals  []string `json:"original_memories"`
	Importance json.Number
	Source     string `json:"memory_source"`
	Reason     any
}
type Question struct {
	Question, Answer string
	Type             string `json:"question_type"`
	Evidence         []struct {
		Content string `json:"memory_content"`
	}
}
type Session struct {
	Start     string `json:"start_time"`
	End       string `json:"end_time"`
	Dialogue  []Turn
	Points    []Point `json:"memory_points"`
	Questions []Question
	Generated bool `json:"is_generated_qa_session"`
}
type User struct {
	UUID     string
	Sessions []Session
}

func stamp(s string) (string, error) {
	t, e := time.Parse("Jan 02, 2006, 15:04:05", s)
	if e != nil {
		return "", e
	}
	return t.UTC().Format(time.RFC3339), nil
}
func Episode(u User) (*suite.Episode, error) {
	if u.UUID == "" {
		return nil, fmt.Errorf("missing user uuid")
	}
	ep := &suite.Episode{ID: u.UUID, ClusterID: u.UUID, Events: []suite.Event{}, Cases: []suite.Case{}}
	yes := true
	for k, s := range u.Sessions {
		if _, e := stamp(s.Start); e != nil {
			return nil, e
		}
		sid := fmt.Sprintf("s%d", k)
		for i, t := range s.Dialogue {
			ts, e := stamp(t.Timestamp)
			if e != nil {
				return nil, e
			}
			ep.Events = append(ep.Events, suite.Event{SourceID: fmt.Sprintf("%s:%s:t%d", u.UUID, sid, i), Owner: "user", Role: t.Role, Content: t.Content, Session: sid, TurnGroup: strconv.Itoa(t.DialogueTurn), Timestamp: ts, TimeRaw: t.Timestamp, Attributes: map[string]any{}})
		}
		ts, e := stamp(s.End)
		if e != nil {
			return nil, e
		}
		if len(s.Dialogue) == 0 {
			return nil, fmt.Errorf("empty HaluMem session")
		}
		g := suite.Gold{MemoryPoints: []suite.MemoryPoint{}, Checker: &suite.Checker{Type: "halumem_memory_audit"}}
		private := map[string]any{}
		seenPoints := map[int]bool{}
		for _, p := range s.Points {
			if seenPoints[p.Index] || p.Index < 0 {
				return nil, fmt.Errorf("duplicate/invalid memory point index")
			}
			seenPoints[p.Index] = true
			if p.IsUpdate != "True" && p.IsUpdate != "False" {
				return nil, fmt.Errorf("invalid is_update")
			}
			importance, e := strconv.ParseFloat(p.Importance.String(), 64)
			if e != nil || importance < 0 || importance > 1 {
				return nil, fmt.Errorf("invalid importance")
			}
			pol := "should_exist"
			if p.Source == "interference" {
				pol = "should_not_exist"
			}
			id := fmt.Sprintf("%s:%s:m%d", u.UUID, sid, p.Index)
			g.MemoryPoints = append(g.MemoryPoints, suite.MemoryPoint{ID: id, Content: p.Content, Type: p.Type, Polarity: pol, Importance: p.Importance.String(), UpdateOf: p.Originals, IsUpdate: p.IsUpdate == "True"})
			private[id] = map[string]any{"memory_source": p.Source, "reason": p.Reason}
		}
		base := suite.Case{ClusterID: u.UUID, User: "user", Scope: []string{}, Cutoff: len(ep.Events), QueryPoint: "after_session_k", SessionCheckpoint: sid, QueryTime: ts, InjectCurrentDate: &yes}
		c := base
		c.ID = u.UUID + ":" + sid + ":audit"
		c.Kind = "memory_audit"
		c.Category = "memory_audit"
		c.Gold = g
		c.Attributes = map[string]any{"memory_annotations": private, "is_generated_qa_session": s.Generated}
		ep.Cases = append(ep.Cases, c)
		for j, q := range s.Questions {
			c := base
			c.ID = fmt.Sprintf("%s:%s:q%d", u.UUID, sid, j)
			c.Kind = "qa"
			c.Query = q.Question
			c.Category = q.Type
			c.QuestionType = q.Type
			c.Attributes = map[string]any{}
			c.Gold = suite.Gold{Answers: []string{q.Answer}, Checker: &suite.Checker{Type: "halumem_qa_judge"}, EvidenceRefs: []suite.EvidenceRef{}}
			for _, v := range q.Evidence {
				c.Gold.EvidenceRefs = append(c.Gold.EvidenceRefs, suite.EvidenceRef{Kind: "text", Ref: v.Content})
			}
			c.Gold.Abstain = q.Type == "Memory Boundary" && len(q.Evidence) == 0
			ep.Cases = append(ep.Cases, c)
		}
	}
	return ep, nil
}
func Verify(input, variant string) (string, error) {
	expected := map[string]string{"medium": "486fbc130a5c8781a2af27ffa508a1d7855245137aa449c193ac4d29c45634e7", "long": "dfdbed570b402b7b8c17e0d7808fc6f3ae7a53b6144f18feb16bbdd3f55cb0c9"}[variant]
	if expected == "" {
		return "", fmt.Errorf("variant must be medium|long")
	}
	b, e := os.ReadFile(filepath.Join(filepath.Dir(input), "source.lock.json"))
	if e != nil {
		return "", e
	}
	var lock struct {
		Revision string
		Files    []struct {
			Path, SHA256 string
			Bytes        int64
		}
	}
	if e = json.Unmarshal(b, &lock); e != nil {
		return "", e
	}
	if lock.Revision != Revision {
		return "", fmt.Errorf("HaluMem revision mismatch")
	}
	f, e := os.Open(input)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return "", e
	}
	got := fmt.Sprintf("%x", h.Sum(nil))
	if got != expected {
		return "", fmt.Errorf("HaluMem input SHA256 mismatch")
	}
	for _, row := range lock.Files {
		if row.Path == filepath.Base(input) && row.SHA256 == got && row.Bytes == n {
			return got, nil
		}
	}
	return "", fmt.Errorf("HaluMem lock entry mismatch")
}
func Import(ctx context.Context, o Options) (Imported, error) {
	h, e := Verify(o.Input, o.Variant)
	if e != nil {
		return Imported{}, e
	}
	return ImportFile(ctx, o, h, false)
}

// ImportFile also accepts synthetic inputs; callers must label them as fixtures.
func ImportFile(ctx context.Context, o Options, inputHash string, fixture bool) (Imported, error) {
	if o.Variant != "medium" && o.Variant != "long" {
		return Imported{}, fmt.Errorf("variant must be medium|long")
	}
	f, e := os.Open(o.Input)
	if e != nil {
		return Imported{}, e
	}
	defer f.Close()
	w, e := suite.NewWriter(o.Out)
	if e != nil {
		return Imported{}, e
	}
	defer w.Abort()
	yes := true
	h := &suite.Suite{SchemaVersion: "3.0", SuiteID: "halumem-" + o.Variant + "@" + Revision[:12], Track: "memory-audit", ProtocolRef: "protocols/halumem.json#" + o.Variant, License: &suite.License{ID: "CC-BY-NC-ND-4.0", Source: "https://huggingface.co/datasets/IAAR-Shanghai/HaluMem", RedistributeNormalized: false}, Provenance: map[string]any{"source_url": "https://huggingface.co/datasets/IAAR-Shanghai/HaluMem", "revision": Revision, "converter": Converter, "local_input_sha256": inputHash, "synthetic_fixture": fixture}, Limitations: []string{CostNotice, "CC BY-NC-ND: normalized copies local only; do not redistribute", "Naive upstream timestamps interpreted as UTC; session array order and zero-based s<k> retained", "Per-session ingest uses turn timestamps; no separate mem0 add timestamp parameter. Query time uses session end for reference only; reader prompt unchanged", "Generated QA sessions are ingested and checkpointed, but official scoring is skipped", "Zero official denominators yield null; replacement judge fidelity partial without a matching study"}, Protocol: suite.SuiteProtocol{Ingestion: "per_session", ResetPolicyAllowed: []string{"incremental_per_episode"}, TimeExposure: "turn", QueryTimePolicy: "reference_only", InjectCurrentDate: &yes, TextOnly: &yes}}
	if fixture {
		h.SuiteID += "-synthetic"
		h.License = &suite.License{ID: "CC0-1.0", RedistributeNormalized: true, Source: "synthetic schema fixture; no upstream examples"}
	}
	sessions, generated, points, updates, questions, users := 0, 0, 0, 0, 0, 0
	sources, types, cats := map[string]int{}, map[string]int{}, map[string]int{}
	dec := json.NewDecoder(f)
	for {
		var u User
		e = dec.Decode(&u)
		if e == io.EOF {
			break
		}
		if e != nil {
			return Imported{}, e
		}
		if e = ctx.Err(); e != nil {
			return Imported{}, e
		}
		ep, e := Episode(u)
		if e != nil {
			return Imported{}, e
		}
		users++
		sessions += len(u.Sessions)
		for _, s := range u.Sessions {
			if s.Generated {
				generated++
			}
			for _, p := range s.Points {
				points++
				sources[p.Source]++
				types[p.Type]++
				if p.IsUpdate == "True" {
					updates++
				}
			}
			for _, q := range s.Questions {
				questions++
				cats[q.Type]++
			}
		}
		if e = w.WriteEpisode(ep); e != nil {
			return Imported{}, e
		}
	}
	if e = w.Close(h); e != nil {
		return Imported{}, e
	}
	s, e := suite.Open(ctx, o.Out)
	if e != nil {
		return Imported{}, e
	}
	st, e := importers.Complete(s, importers.Stats{Diagnostics: map[string]any{"users": users, "sessions": sessions, "generated_qa_sessions": generated, "memory_points": points, "updates": updates, "memory_sources": sources, "memory_types": types, "questions_by_type": cats}}, Converter)
	if e != nil {
		return Imported{}, e
	}
	st.Questions = questions
	st.Events = st.Counts.Events
	st.Sessions = sessions
	dialogues := 30073
	if o.Variant == "long" {
		dialogues = 53516
	}
	st.UpstreamScale.Declared = map[string]any{"users": 20, "dialogues": dialogues, "memory_points": 14948, "questions": 3467}
	st.UpstreamScale.Observed = map[string]any{"users": users, "sessions": sessions, "turns": st.Events, "dialogue_pairs": st.Events / 2, "memory_points": points, "questions": questions}
	st.UpstreamScale.Explanation = "Data card #Dialogues counts paired user/assistant exchanges, not individual turns; session averages 70/120 are rounded. Generated QA sessions add no memory or question labels."
	b, e := suite.CanonicalJSON(st)
	if e != nil {
		return Imported{}, e
	}
	if e = os.WriteFile(filepath.Join(o.Out, "import.json"), append(b, '\n'), 0600); e != nil {
		return Imported{}, e
	}
	sha, e := suite.SHA256(s)
	return Imported{o.Out, s.SuiteID, sha, st}, e
}

// NormalizeVariant is shared by CLI callers without inferring unknown variants.
func NormalizeVariant(s string) string { return strings.ToLower(s) }
