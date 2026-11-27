// Package prefeval imports pinned PrefEval task histories. Annotations remain private.
package prefeval

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"memeval/internal/importers"
	"memeval/internal/pytext"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const Revision = "50795054b5ff5f418d2b768a331d71e480f93331"
const Converter = "memeval-import-prefeval/1"

var Forms = []string{"explicit", "implicit-choice", "implicit-persona"}
var Tasks = []string{"generation", "mcq"}

type Options struct {
	Input, Form, Task, Out string
	InterTurns             int
}
type Imported struct {
	Path        string          `json:"path"`
	SuiteID     string          `json:"suite_id"`
	SuiteSHA256 string          `json:"suite_sha256"`
	Statistics  importers.Stats `json:"statistics"`
}
type Row struct {
	Preference   string          `json:"preference"`
	Question     string          `json:"question"`
	Explanation  string          `json:"explanation"`
	Conversation json.RawMessage `json:"conversation"`
	Options      json.RawMessage `json:"classification_task_options"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Filler struct {
	Conversation []Message `json:"conversation"`
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func readJSON(p string, v any) error {
	b, e := os.ReadFile(p)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func value(b json.RawMessage) (any, bool, error) {
	var v any
	if e := json.Unmarshal(b, &v); e != nil {
		return nil, false, e
	}
	if s, ok := v.(string); ok {
		v, e := pytext.ParseLiteral(s)
		return v, true, e
	}
	return v, false, nil
}
func field(m map[string]any, k string) (string, error) {
	s, ok := m[k].(string)
	if !ok {
		return "", fmt.Errorf("missing/nonstring %s", k)
	}
	return s, nil
}
func PreferenceMessages(form string, row Row) ([]Message, bool, error) {
	if form == "explicit" {
		return []Message{{"user", row.Preference}}, false, nil
	}
	v, literal, e := value(row.Conversation)
	if e != nil {
		return nil, literal, e
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, literal, fmt.Errorf("conversation must be a dict")
	}
	out := []Message{}
	if form == "implicit-choice" {
		for i, k := range []string{"query", "assistant_options", "user_selection", "assistant_acknowledgment"} {
			s, e := field(m, k)
			if e != nil {
				return nil, literal, e
			}
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			out = append(out, Message{role, s})
		}
	} else if form == "implicit-persona" {
		keys := []int{}
		for k := range m {
			i, e := strconv.Atoi(k)
			if e != nil || i < 0 || strconv.Itoa(i) != k {
				return nil, literal, fmt.Errorf("invalid persona conversation key")
			}
			keys = append(keys, i)
		}
		slices.Sort(keys)
		for _, k := range keys {
			pair, ok := m[strconv.Itoa(k)].(map[string]any)
			if !ok {
				return nil, literal, fmt.Errorf("persona pair must be dict")
			}
			for _, role := range []string{"user", "assistant"} {
				s, e := field(pair, role)
				if e != nil {
					return nil, literal, e
				}
				out = append(out, Message{role, s})
			}
		}
	} else {
		return nil, literal, fmt.Errorf("unknown form")
	}
	if len(out) == 0 {
		return nil, literal, fmt.Errorf("empty preference conversation")
	}
	return out, literal, nil
}
func OptionList(raw json.RawMessage) ([]string, string, bool, error) {
	v, literal, e := value(raw)
	if e != nil {
		return nil, "", literal, e
	}
	list, ok := v.([]any)
	if !ok || len(list) != 4 {
		return nil, "", literal, fmt.Errorf("MCQ needs four options")
	}
	out := []string{}
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, "", literal, fmt.Errorf("option must be string")
		}
		out = append(out, s)
	}
	text := string(raw)
	if literal {
		json.Unmarshal(raw, &text)
	}
	return out, text, literal, nil
}
func Import(ctx context.Context, o Options) (Imported, error) {
	h, e := Verify(ctx, o.Input)
	if e != nil {
		return Imported{}, e
	}
	return ImportData(ctx, o, h, false)
}

// ImportData is the deterministic converter; fixtures bypass only the upstream lock.
func ImportData(ctx context.Context, o Options, inputHash string, fixture bool) (Imported, error) {
	if !slices.Contains(Forms, o.Form) || !slices.Contains(Tasks, o.Task) || o.InterTurns < 0 {
		return Imported{}, fmt.Errorf("invalid PrefEval form/task/inter-turns")
	}
	var fillers []Filler
	if e := readJSON(filepath.Join(o.Input, "filtered_inter_turns.json"), &fillers); e != nil {
		return Imported{}, e
	}
	flat := []Message{}
	for _, f := range fillers {
		flat = append(flat, f.Conversation...)
	}
	if o.InterTurns > len(flat)/2 {
		return Imported{}, fmt.Errorf("inter-turns exceeds %d available messages", len(flat))
	}
	prefix := flat[:o.InterTurns*2]
	if len(prefix) > 0 && prefix[len(prefix)-1].Role != "assistant" {
		return Imported{}, fmt.Errorf("filler prefix must end with assistant")
	}
	for _, m := range flat {
		if m.Role != "user" && m.Role != "assistant" {
			return Imported{}, fmt.Errorf("invalid filler role %q", m.Role)
		}
	}
	files, e := filepath.Glob(filepath.Join(o.Input, "explicit_preference", "*.json"))
	if e != nil {
		return Imported{}, e
	}
	if len(files) == 0 {
		return Imported{}, fmt.Errorf("no canonical topics")
	}
	formDir := map[string]string{"explicit": "explicit_preference", "implicit-choice": "implicit_preference/choice-based", "implicit-persona": "implicit_preference/persona-driven"}[o.Form]
	w, e := suite.NewWriter(o.Out)
	if e != nil {
		return Imported{}, e
	}
	defer w.Abort()
	yes, no := true, false
	id := fmt.Sprintf("prefeval-%s-%s-t%d@%s", o.Form, o.Task, o.InterTurns, Revision[:12])
	h := &suite.Suite{SchemaVersion: "3.0", SuiteID: id, Track: "memory-qa", ProtocolRef: "protocols/prefeval.json#" + o.Form + "-" + o.Task,
		License:     &suite.License{ID: "CC-BY-NC-4.0", Noncommercial: true, RedistributeNormalized: true, Source: "https://github.com/amazon-science/PrefEval"},
		Provenance:  map[string]any{"revision": Revision, "converter": Converter, "local_input_sha256": inputHash, "fixture_synthetic": fixture, "attribution": "PrefEval authors; Amazon Science; CC BY-NC 4.0", "source_url": "https://github.com/amazon-science/PrefEval"},
		Protocol:    suite.SuiteProtocol{Ingestion: "per_event", ResetPolicyAllowed: []string{"fresh_per_case"}, TimeExposure: "none", InjectCurrentDate: &no, TextOnly: &yes, Haystack: &suite.Haystack{FillerSource: "filtered_inter_turns.json (24 LMSYS conversations, official concatenation order)", ContextLadder: []int{0, 10, 300}}},
		Limitations: []string{"Official realtime assistant pref_generation acknowledgment is omitted; it is model output, not dataset history", "Official Bedrock Claude 3 Sonnet judge is declared only; replacement judge fidelity partial", "Native JSON objects/lists and Python literal strings are both accepted; annotations remain private", "Canonical topics come from explicit_preference; unmatched MCQ files are excluded and counted", "MCQ uses mcq_options preference/question per upstream load_files_mcq; differences from explicit_preference are counted"}}
	if o.Task == "mcq" {
		h.Limitations = append(h.Limitations, "MCQ order uses Go math/rand NewSource(41).Perm, sequential sorted topic/index order; differs from Python random.sample; each case records seed/order")
	}
	if fixture {
		h.SuiteID += "-fixture"
	}
	rng := rand.New(rand.NewSource(41))
	byTopic := map[string]int{}
	prefCounts := map[string]int{}
	native, literals, prefDifferences, questionCount, eventCount := 0, 0, 0, 0, 0
	canonical := map[string]bool{}
	for _, file := range files {
		if e = ctx.Err(); e != nil {
			return Imported{}, e
		}
		topic := strings.TrimSuffix(filepath.Base(file), ".json")
		canonical[topic] = true
		var base, rows, mcq []Row
		if e = readJSON(file, &base); e != nil {
			return Imported{}, e
		}
		if e = readJSON(filepath.Join(o.Input, formDir, topic+".json"), &rows); e != nil {
			return Imported{}, e
		}
		if len(rows) != len(base) {
			return Imported{}, fmt.Errorf("%s form alignment differs", topic)
		}
		if o.Task == "mcq" {
			if e = readJSON(filepath.Join(o.Input, "mcq_options", topic+".json"), &mcq); e != nil {
				return Imported{}, e
			}
			if len(mcq) != len(base) {
				return Imported{}, fmt.Errorf("%s MCQ alignment differs", topic)
			}
		}
		for i, row := range rows {
			if row.Question != base[i].Question {
				return Imported{}, fmt.Errorf("%s:%d question alignment differs", topic, i)
			}
			if o.Task == "mcq" {
				if mcq[i].Question != row.Question {
					return Imported{}, fmt.Errorf("MCQ question alignment differs")
				}
				if mcq[i].Preference != base[i].Preference {
					prefDifferences++
				}
				if o.Form == "explicit" {
					row = mcq[i]
				}
			}
			messages, lit, e := PreferenceMessages(o.Form, row)
			if e != nil {
				return Imported{}, fmt.Errorf("%s:%d: %w", topic, i, e)
			}
			if o.Form != "explicit" {
				if lit {
					literals++
				} else {
					native++
				}
			}
			epID := fmt.Sprintf("%s:%d", topic, i)
			ep := &suite.Episode{ID: epID, ClusterID: topic, Events: []suite.Event{}, Cases: []suite.Case{}}
			for _, segment := range []struct {
				msgs         []Message
				short, group string
			}{{messages, "p", "preference"}, {prefix, "f", "filler"}} {
				for j, m := range segment.msgs {
					ep.Events = append(ep.Events, suite.Event{SourceID: fmt.Sprintf("%s:%s%d", epID, segment.short, j), Owner: "user", Role: m.Role, Content: m.Content, TurnGroup: segment.group, Attributes: map[string]any{}})
				}
			}
			prefCounts[strconv.Itoa(len(messages))]++
			c := suite.Case{ID: epID + ":gen", ClusterID: topic, Kind: "rubric", Category: topic, QuestionType: topic, Query: row.Question, User: "user", Scope: []string{}, Cutoff: len(ep.Events), QueryPoint: "end_of_history", Attributes: map[string]any{"explanation": row.Explanation}, Gold: suite.Gold{Reference: row.Preference, Rubric: &suite.Rubric{Aggregate: "prefeval", Items: []suite.RubricItem{}}}}
			for _, probe := range []string{"acknow", "violate", "hallucinate", "helpful"} {
				c.Gold.Rubric.Items = append(c.Gold.Rubric.Items, suite.RubricItem{Text: probe, Polarity: "as_written", Weight: 1})
			}
			if o.Task == "mcq" {
				opts, raw, lit, e := OptionList(mcq[i].Options)
				if e != nil {
					return Imported{}, fmt.Errorf("%s:%d: %w", topic, i, e)
				}
				if lit {
					literals++
				} else {
					native++
				}
				order := rng.Perm(4)
				seed := 41
				c.ID = epID + ":mcq"
				c.Kind = "mcq"
				c.Options = map[string]string{}
				c.Gold = suite.Gold{MCQ: &suite.MCQSpec{Shuffle: true, Seed: &seed, Order: order, Parse: "prefeval_choice_tag"}}
				c.Attributes["options_raw"] = raw
				for j, k := range order {
					letter := string(rune('A' + j))
					c.Options[letter] = opts[k]
					if k == 0 {
						c.Gold.Choice = letter
					}
				}
			}
			ep.Cases = append(ep.Cases, c)
			if e = w.WriteEpisode(ep); e != nil {
				return Imported{}, e
			}
			byTopic[topic]++
			questionCount++
			eventCount += len(ep.Events)
		}
	}
	orphan := map[string]int{}
	mcqFiles, e := filepath.Glob(filepath.Join(o.Input, "mcq_options", "*.json"))
	if e != nil {
		return Imported{}, e
	}
	for _, file := range mcqFiles {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		if !canonical[name] {
			var rows []Row
			if e = readJSON(file, &rows); e != nil {
				return Imported{}, e
			}
			orphan[filepath.Base(file)] = len(rows)
		}
	}
	if e = w.Close(h); e != nil {
		return Imported{}, e
	}
	s, e := suite.Open(ctx, o.Out)
	if e != nil {
		return Imported{}, e
	}
	stats, e := importers.Complete(s, importers.Stats{Diagnostics: map[string]any{"form": o.Form, "task": o.Task, "inter_turns": o.InterTurns, "topics": len(byTopic), "tasks": questionCount, "tasks_by_topic": byTopic, "filler_conversations": len(fillers), "filler_messages_available": len(flat), "filler_messages_per_task": len(prefix), "preference_messages_distribution": prefCounts, "native_json_fields": native, "python_literal_fields": literals, "literal_parse_failures": 0, "orphan_mcq_files": orphan, "mcq_preference_differences_from_explicit": prefDifferences}}, Converter)
	if e != nil {
		return Imported{}, e
	}
	stats.Questions = questionCount
	stats.Events = eventCount
	stats.UpstreamScale.Declared = map[string]any{"topics": 20, "tasks_per_form": 1000, "filler_conversations": 24, "filler_messages": 632}
	stats.UpstreamScale.Explanation = "20 topics total 1,000 tasks, with unequal per-topic counts; one unmatched MCQ copy excluded. Fields in pinned release are native JSON; literal strings also supported."
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
