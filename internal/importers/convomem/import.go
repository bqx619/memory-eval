// Package convomem imports locked evidence-only ConvoMem histories.
package convomem

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"memeval/internal/importers"
	"memeval/internal/pytext"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const Revision = "e3e9b39115b02346824c70d349350de738f8be41"
const Converter = "memeval-import-convomem/1"

var Types = []string{"abstention", "assistant_facts", "changing", "implicit_connection", "preference"}

type Options struct{ Input, Type, Out string }
type Imported struct {
	Path        string          `json:"path"`
	SuiteID     string          `json:"suite_id"`
	SuiteSHA256 string          `json:"suite_sha256"`
	Statistics  importers.Stats `json:"statistics"`
}
type Message struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}
type Conversation struct {
	ID               string    `json:"id"`
	ContainsEvidence bool      `json:"containsEvidence"`
	ModelName        any       `json:"model_name"`
	Messages         []Message `json:"messages"`
}
type Item struct {
	Question      string          `json:"question"`
	Answer        string          `json:"answer"`
	Evidence      json.RawMessage `json:"message_evidences"`
	Conversations []Conversation  `json:"conversations"`
	Category      string          `json:"category"`
	Scenario      string          `json:"scenario_description"`
	PersonID      string          `json:"personId"`
	UseCaseModel  any             `json:"use_case_model_name"`
	CoreModel     any             `json:"core_model_name"`
}
type File struct {
	Items      []Item `json:"evidence_items"`
	Checkpoint string `json:"checkpoint"`
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func readJSON(p string, v any) error {
	b, e := os.ReadFile(p)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func SuiteID(typ string) string { return "convomem-" + typ + "@" + Revision[:12] }
func Template(typ string) string {
	switch typ {
	case "abstention":
		return "abstention"
	case "preference", "implicit_connection":
		return "rubric"
	default:
		return "default"
	}
}
func Evidence(raw json.RawMessage) ([]Message, bool, error) {
	var v any
	if e := json.Unmarshal(raw, &v); e != nil {
		return nil, false, e
	}
	literal := false
	if s, ok := v.(string); ok {
		literal = true
		var e error
		v, e = pytext.ParseLiteral(s)
		if e != nil {
			return nil, true, e
		}
	}
	xs, ok := v.([]any)
	if !ok {
		return nil, literal, fmt.Errorf("message_evidences must be a list")
	}
	out := []Message{}
	for _, x := range xs {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, literal, fmt.Errorf("evidence must be an object")
		}
		sp, a := m["speaker"].(string)
		txt, b := m["text"].(string)
		if !a || !b {
			return nil, literal, fmt.Errorf("evidence speaker/text must be strings")
		}
		out = append(out, Message{sp, txt})
	}
	return out, literal, nil
}

func Import(ctx context.Context, o Options) ([]Imported, error) {
	if !slices.Contains(Types, o.Type) && o.Type != "all" {
		return nil, fmt.Errorf("invalid ConvoMem type %q", o.Type)
	}
	h, e := Verify(ctx, o.Input)
	if e != nil {
		return nil, e
	}
	ts := []string{o.Type}
	if o.Type == "all" {
		ts = Types
	}
	out := []Imported{}
	for _, typ := range ts {
		one := o
		one.Type = typ
		if o.Type == "all" {
			one.Out = filepath.Join(o.Out, SuiteID(typ))
		}
		r, e := ImportData(ctx, one, h, false)
		if e != nil {
			return out, e
		}
		out = append(out, r)
	}
	return out, nil
}

// ImportData streams one episode at a time; only one upstream file is decoded at once.
func ImportData(ctx context.Context, o Options, inputHash string, fixture bool) (Imported, error) {
	if !slices.Contains(Types, o.Type) {
		return Imported{}, fmt.Errorf("invalid ConvoMem type %q", o.Type)
	}
	files, e := filepath.Glob(filepath.Join(o.Input, o.Type+"_evidence", "*_evidence", "*.json"))
	if e != nil {
		return Imported{}, e
	}
	if len(files) == 0 {
		return Imported{}, fmt.Errorf("no ConvoMem inputs for %s", o.Type)
	}
	w, e := suite.NewWriter(o.Out)
	if e != nil {
		return Imported{}, e
	}
	defer w.Abort()
	yes, no := true, false
	h := &suite.Suite{SchemaVersion: "3.0", SuiteID: SuiteID(o.Type), Track: "memory-qa", ProtocolRef: "protocols/convomem.json#" + o.Type,
		License:     &suite.License{ID: "CC-BY-NC-4.0", Noncommercial: true, RedistributeNormalized: true, Source: "https://huggingface.co/datasets/Salesforce/ConvoMem"},
		Provenance:  map[string]any{"source_url": "https://huggingface.co/datasets/Salesforce/ConvoMem", "revision": Revision, "converter": Converter, "local_input_sha256": inputHash, "fixture_synthetic": fixture, "attribution": "ConvoMem authors; Salesforce AI Research; CC BY-NC 4.0"},
		Protocol:    suite.SuiteProtocol{Ingestion: "per_session", ResetPolicyAllowed: []string{"fresh_per_case"}, TimeExposure: "none", InjectCurrentDate: &no, TextOnly: &yes, Haystack: &suite.Haystack{FillerSource: "none at revision e3e9b391", ContextLadder: []int{}}},
		Limitations: []string{"Only evidence conversations are available; no filler; official 1..300 conversation context ladder cannot be reproduced", "File n_evidence names may differ from actual conversation count; preserve all conversations, record both counts; by-n correctness uses actual count", "case.category/question_type use benchmark type; original topical category and conversation IDs remain private case attributes", "Evidence matches require exact speaker+text; multiple matches use first; unmatched text references are excluded from retrieval gold denominators", "Official Gemini 2.5 Flash judge declared only; replacement fidelity partial", "implicit_connection uses official generator rubric template while retaining QA answers; three supported judge templates ignore evidenceMessages"}}
	if fixture {
		h.SuiteID += "-fixture"
	}
	counts, msgCounts, byN, speakers, categories := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	people := map[string]bool{}
	ns := map[int]bool{}
	questions, events, sessions, exact, multiple, unmatched, native, literals, contains, abstentions, countMismatches := 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
	for _, file := range files {
		if e = ctx.Err(); e != nil {
			return Imported{}, e
		}
		n, e := strconv.Atoi(strings.TrimSuffix(filepath.Base(filepath.Dir(file)), "_evidence"))
		if e != nil || n < 1 {
			return Imported{}, fmt.Errorf("invalid evidence count directory: %s", file)
		}
		var f File
		if e = readJSON(file, &f); e != nil {
			return Imported{}, e
		}
		rel, _ := filepath.Rel(o.Input, file)
		for i, row := range f.Items {
			if row.PersonID == "" || row.Question == "" || len(row.Conversations) == 0 {
				return Imported{}, fmt.Errorf("%s item %d: person/question/conversation count invalid", rel, i)
			}
			if len(row.Conversations) != n {
				countMismatches++
			}
			epID := fmt.Sprintf("%s:%d:%s:%d", o.Type, n, row.PersonID, i)
			ep := &suite.Episode{ID: epID, ClusterID: row.PersonID, Events: []suite.Event{}, Cases: []suite.Case{}}
			matches := map[Message][]string{}
			ids := map[string]string{}
			for j, conv := range row.Conversations {
				session := fmt.Sprintf("c%d", j)
				ids[session] = conv.ID
				if conv.ContainsEvidence {
					contains++
				} else {
					return Imported{}, fmt.Errorf("%s item %d conversation %d: unexpected non-evidence conversation", rel, i, j)
				}
				for k, m := range conv.Messages {
					role := map[string]string{"User": "user", "Assistant": "assistant"}[m.Speaker]
					if role == "" {
						return Imported{}, fmt.Errorf("%s item %d conversation %d message %d: unsupported speaker %q (speaker_errors=1)", rel, i, j, k, m.Speaker)
					}
					source := fmt.Sprintf("%s:%s:m%d", epID, session, k)
					ep.Events = append(ep.Events, suite.Event{SourceID: source, Owner: "user", Content: m.Text, Role: role, Session: session, Attributes: map[string]any{}})
					matches[m] = append(matches[m], source)
					speakers[m.Speaker]++
				}
			}
			refs, lit, e := Evidence(row.Evidence)
			if e != nil {
				return Imported{}, fmt.Errorf("%s item %d evidence: %w", rel, i, e)
			}
			if lit {
				literals++
			} else {
				native++
			}
			gold := suite.Gold{EvidenceRefs: []suite.EvidenceRef{}, Answers: []string{row.Answer}, Checker: &suite.Checker{Type: "convomem_judge", Params: json.RawMessage(fmt.Sprintf(`{"template":%q}`, Template(o.Type)))}}
			for _, m := range refs {
				hits := matches[m]
				if len(hits) == 0 {
					unmatched++
					gold.EvidenceRefs = append(gold.EvidenceRefs, suite.EvidenceRef{Kind: "text", Ref: m.Speaker + ": " + m.Text})
				} else {
					exact++
					if len(hits) > 1 {
						multiple++
					}
					gold.EvidenceRefs = append(gold.EvidenceRefs, suite.EvidenceRef{Kind: "event", Ref: hits[0]})
				}
			}
			c := suite.Case{ID: epID + ":q", ClusterID: row.PersonID, Kind: "qa", Category: o.Type, QuestionType: o.Type, Query: row.Question, User: "user", Scope: []string{}, Cutoff: len(ep.Events), QueryPoint: "end_of_history", Gold: gold,
				Attributes: map[string]any{"n_evidence": len(row.Conversations), "file_n_evidence": n, "source_file": filepath.ToSlash(rel), "category": row.Category, "scenario_description": row.Scenario, "conversation_ids": ids, "checkpoint": f.Checkpoint, "use_case_model_name": row.UseCaseModel, "core_model_name": row.CoreModel}}
			if o.Type == "abstention" {
				c.Gold.Answers = []string{}
				c.Gold.Abstain = true
				abstentions++
			}
			if o.Type == "preference" {
				c.Kind = "rubric"
				c.Gold.Answers = nil
				c.Gold.Rubric = &suite.Rubric{Aggregate: "convomem_rubric", Items: []suite.RubricItem{{Text: row.Answer, Polarity: "as_written", Weight: 1}}}
			}
			ep.Cases = append(ep.Cases, c)
			if e = w.WriteEpisode(ep); e != nil {
				return Imported{}, e
			}
			questions++
			events += len(ep.Events)
			sessions += len(row.Conversations)
			byN[strconv.Itoa(n)]++
			counts[strconv.Itoa(len(row.Conversations))]++
			msgCounts[strconv.Itoa(len(ep.Events))]++
			categories[row.Category]++
			people[row.PersonID] = true
			ns[len(row.Conversations)] = true
		}
	}
	for n := range ns {
		h.Protocol.Haystack.ContextLadder = append(h.Protocol.Haystack.ContextLadder, n)
	}
	slices.Sort(h.Protocol.Haystack.ContextLadder)
	if e = w.Close(h); e != nil {
		return Imported{}, e
	}
	s, e := suite.Open(ctx, o.Out)
	if e != nil {
		return Imported{}, e
	}
	stats, e := importers.Complete(s, importers.Stats{Diagnostics: map[string]any{"type": o.Type, "files": len(files), "items": questions, "persons": len(people), "items_by_n_evidence": byN, "conversation_counts": counts, "message_counts": msgCounts, "speakers": speakers, "speaker_errors": 0, "original_categories": categories, "contains_evidence_true": contains, "contains_evidence_false": 0, "evidence_total": exact + unmatched, "evidence_exact": exact, "evidence_multiple": multiple, "evidence_unmatched": unmatched, "native_json_fields": native, "python_literal_fields": literals, "literal_parse_failures": 0, "conversation_count_directory_mismatches": countMismatches}}, Converter)
	if e != nil {
		return Imported{}, e
	}
	stats.Questions = questions
	stats.Events = events
	stats.Sessions = sessions
	stats.Abstentions = abstentions
	stats.UpstreamScale.Declared = map[string]any{"files_all_types": 906, "items_all_types": 56273, "items_by_type": map[string]int{"abstention": 14910, "assistant_facts": 12745, "changing": 18323, "implicit_connection": 7546, "preference": 2749}}
	stats.UpstreamScale.Explanation = "Pinned revision contains evidence-only histories. Earlier 880-file counts are superseded by the final 906-file lock."
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
