// Package beam streams the pinned CC BY-SA BEAM conversations into bundles.
package beam

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/parquet-go/parquet-go"
	"io"
	"memeval/internal/importers"
	"memeval/internal/pytext"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const Revision = "3205395e897e7318c7b094ef4e6047b9b82dbb03"
const Converter = "memeval-import-beam/1"

var Categories = []string{"abstention", "contradiction_resolution", "event_ordering", "information_extraction", "instruction_following", "knowledge_update", "multi_session_reasoning", "preference_following", "summarization", "temporal_reasoning"}

type Turn struct {
	ID         int     `json:"id"`
	Role       string  `json:"role"`
	Content    string  `json:"content"`
	TimeAnchor *string `json:"time_anchor"`
}
type Row struct {
	ID      string   `json:"conversation_id"`
	Chat    [][]Turn `json:"chat"`
	Probing string   `json:"probing_questions"`
}
type Question struct {
	Question      string   `json:"question"`
	Rubric        []string `json:"rubric"`
	Difficulty    string   `json:"difficulty"`
	IdealSummary  string   `json:"ideal_summary"`
	SourceChatIDs any      `json:"source_chat_ids"`
}
type Options struct{ Input, Variant, Out string }
type Imported struct {
	Path        string          `json:"path"`
	SuiteID     string          `json:"suite_id"`
	SuiteSHA256 string          `json:"suite_sha256"`
	Statistics  importers.Stats `json:"statistics"`
}

func ReadRows(ctx context.Context, path string, fn func(int, Row) error) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	if filepath.Ext(path) == ".json" {
		var rows []Row
		if e = json.NewDecoder(f).Decode(&rows); e != nil {
			return e
		}
		for i, r := range rows {
			if e = fn(i, r); e != nil {
				return e
			}
		}
		return nil
	}
	r := parquet.NewReader(f)
	defer r.Close()
	for i := 0; ; i++ {
		if e = ctx.Err(); e != nil {
			return e
		}
		v := map[string]any{}
		if e = r.Read(&v); e == io.EOF {
			return nil
		} else if e != nil {
			return e
		}
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		var row Row
		if e = json.Unmarshal(b, &row); e != nil {
			return e
		}
		if e = fn(i, row); e != nil {
			return fmt.Errorf("row %d: %w", i, e)
		}
	}
}
func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil)), e
}
func Import(ctx context.Context, o Options) (Imported, error) {
	if !slices.Contains([]string{"100K", "500K", "1M"}, o.Variant) {
		return Imported{}, fmt.Errorf("BEAM variant must be 100K|500K|1M")
	}
	b, e := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(o.Input)), "source.lock.json"))
	if e != nil {
		return Imported{}, e
	}
	var lock struct {
		Revision string
		Files    []struct{ Path, SHA256 string }
	}
	if e = json.Unmarshal(b, &lock); e != nil {
		return Imported{}, e
	}
	h, e := fileHash(o.Input)
	if e != nil {
		return Imported{}, e
	}
	ok := false
	for _, f := range lock.Files {
		if f.Path == "data/"+o.Variant+"-00000-of-00001.parquet" && f.SHA256 == h {
			ok = true
		}
	}
	if !ok || lock.Revision != Revision {
		return Imported{}, fmt.Errorf("BEAM input differs from pinned lock/variant")
	}
	return ImportFile(ctx, o, false)
}
func Questions(raw string) (map[string][]Question, error) {
	v, e := pytext.ParseLiteral(raw)
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var q map[string][]Question
	if e = json.Unmarshal(b, &q); e != nil {
		return nil, e
	}
	if len(q) != len(Categories) {
		return nil, fmt.Errorf("expected ten question categories")
	}
	for k := range q {
		if !slices.Contains(Categories, k) {
			return nil, fmt.Errorf("unknown BEAM category %q", k)
		}
	}
	return q, nil
}
func sourceIDs(v any) ([]int, error) {
	out := []int{}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case nil:
		case []any:
			for _, z := range x {
				if e := walk(z); e != nil {
					return e
				}
			}
		case map[string]any:
			ks := []string{}
			for k := range x {
				ks = append(ks, k)
			}
			slices.Sort(ks)
			for _, k := range ks {
				if e := walk(x[k]); e != nil {
					return e
				}
			}
		case float64:
			if x < 0 || float64(int(x)) != x {
				return fmt.Errorf("invalid source_chat_ids")
			}
			out = append(out, int(x))
		default:
			return fmt.Errorf("unsupported source_chat_ids %T", v)
		}
		return nil
	}
	e := walk(v)
	slices.Sort(out)
	return slices.Compact(out), e
}
func Episode(row Row) (*suite.Episode, int, error) {
	ep := &suite.Episode{ID: row.ID, ClusterID: row.ID, Events: []suite.Event{}, Cases: []suite.Case{}}
	if row.ID == "" {
		return nil, 0, fmt.Errorf("missing conversation_id")
	}
	ids := map[int][]string{}
	anchors := 0
	for bi, batch := range row.Chat {
		for _, t := range batch {
			if t.ID < 0 {
				return nil, 0, fmt.Errorf("negative turn id")
			}
			sid := row.ID + ":" + strconv.Itoa(t.ID)
			if len(ids[t.ID]) > 0 {
				sid += fmt.Sprintf(":occ%d", len(ids[t.ID])+1)
			}
			ids[t.ID] = append(ids[t.ID], sid)
			ev := suite.Event{SourceID: sid, Owner: "user", Role: t.Role, Content: t.Content, TurnGroup: fmt.Sprintf("batch%d", bi), Attributes: map[string]any{}}
			if t.TimeAnchor != nil && *t.TimeAnchor != "" {
				d, e := time.Parse("January-2-2006", *t.TimeAnchor)
				if e != nil {
					return nil, 0, fmt.Errorf("invalid time_anchor %q", *t.TimeAnchor)
				}
				ev.Timestamp = d.Format(time.RFC3339)
				ev.TimeRaw = *t.TimeAnchor
				anchors++
			}
			ep.Events = append(ep.Events, ev)
		}
	}
	qs, e := Questions(row.Probing)
	if e != nil {
		return nil, 0, e
	}
	for _, cat := range Categories {
		for i, q := range qs[cat] {
			g := suite.Gold{EvidenceIDs: []string{}, EvidenceUnit: "event", Rubric: &suite.Rubric{Items: []suite.RubricItem{}, Aggregate: "mean"}, Checker: &suite.Checker{Type: "beam_rubric"}}
			for _, r := range q.Rubric {
				g.Rubric.Items = append(g.Rubric.Items, suite.RubricItem{Text: r, Polarity: "as_written", Weight: 1})
			}
			if cat == "event_ordering" {
				g.Sequence = slices.Clone(q.Rubric)
				g.Checker.Type = "beam_event_ordering"
			}
			g.Reference = q.IdealSummary
			source, e := sourceIDs(q.SourceChatIDs)
			if e != nil {
				return nil, 0, e
			}
			for _, id := range source {
				sid, ok := ids[id]
				if !ok {
					return nil, 0, fmt.Errorf("question %s/%d missing source_chat_id %d", cat, i, id)
				}
				g.EvidenceIDs = append(g.EvidenceIDs, sid...)
			}
			ep.Cases = append(ep.Cases, suite.Case{ID: fmt.Sprintf("%s:%s:%d", row.ID, cat, i), ClusterID: row.ID, Kind: "rubric", Category: cat, QuestionType: cat, Query: q.Question, Difficulty: q.Difficulty, User: "user", Scope: []string{}, Cutoff: len(ep.Events), QueryPoint: "end_of_history", Gold: g})
		}
	}
	return ep, anchors, nil
}
func ImportFile(ctx context.Context, o Options, fixture bool) (Imported, error) {
	h, e := fileHash(o.Input)
	if e != nil {
		return Imported{}, e
	}
	w, e := suite.NewWriter(o.Out)
	if e != nil {
		return Imported{}, e
	}
	defer w.Abort()
	no, yes := false, true
	header := &suite.Suite{SchemaVersion: "3.0", SuiteID: "beam-" + strings.ToLower(o.Variant) + "@" + Revision[:12], Track: "memory-qa", ProtocolRef: "protocols/beam.json#" + o.Variant, License: &suite.License{ID: "CC-BY-SA-4.0", ShareAlike: true, RedistributeNormalized: true, Source: "https://huggingface.co/datasets/Mohammadta/BEAM"}, Provenance: map[string]any{"source_url": "https://huggingface.co/datasets/Mohammadta/BEAM", "revision": Revision, "converter": Converter, "local_input_sha256": h, "fixture_truncated": fixture, "attribution": "Mohammad Tavakoli et al., Beyond a Million Tokens (BEAM), 2025"}, Limitations: []string{"Reference answers and rubric are evaluator-only; no repair_json fallback", "Reader truncation uses Unicode characters rather than model tokens; replacement judges require agreement before equivalent fidelity"}, Protocol: suite.SuiteProtocol{Ingestion: "per_event", ResetPolicyAllowed: []string{"fresh_per_case", "incremental_per_episode"}, TimeExposure: "turn", QueryTimePolicy: "reference_only", InjectCurrentDate: &no, TextOnly: &yes}}
	if fixture {
		header.SuiteID += "-fixture"
	}
	rows, anchors, rubrics := 0, 0, 0
	duplicateTurns, ambiguousRefs := 0, 0
	duplicateConversations := map[string]int{}
	cats := map[string]int{}
	e = ReadRows(ctx, o.Input, func(_ int, r Row) error {
		ep, n, e := Episode(r)
		if e != nil {
			return e
		}
		rows++
		turns := map[int]int{}
		for _, batch := range r.Chat {
			for _, t := range batch {
				turns[t.ID]++
				if turns[t.ID] > 1 {
					duplicateTurns++
					duplicateConversations[r.ID]++
				}
			}
		}
		qs, err := Questions(r.Probing)
		if err != nil {
			return err
		}
		for _, group := range qs {
			for _, q := range group {
				refs, err := sourceIDs(q.SourceChatIDs)
				if err != nil {
					return err
				}
				for _, id := range refs {
					if turns[id] > 1 {
						ambiguousRefs++
					}
				}
			}
		}
		anchors += n
		for _, c := range ep.Cases {
			cats[c.Category]++
			rubrics += len(c.Gold.Rubric.Items)
		}
		return w.WriteEpisode(ep)
	})
	if e != nil {
		return Imported{}, e
	}
	if duplicateTurns > 0 {
		header.Limitations = append(header.Limitations, "Duplicate upstream turn IDs retained with stable :occN suffix; evidence ID references expand to ALL occurrences and are ambiguous")
	}
	if e = w.Close(header); e != nil {
		return Imported{}, e
	}
	s, e := suite.Open(ctx, o.Out)
	if e != nil {
		return Imported{}, e
	}
	stats, e := importers.Complete(s, importers.Stats{Diagnostics: map[string]any{"variant": o.Variant, "conversations": rows, "questions_by_category": cats, "turns_with_time_anchor": anchors, "probing_parse_failures": 0, "rubric_items": rubrics, "duplicate_turn_id_occurrences": duplicateTurns, "duplicate_conversations": duplicateConversations, "ambiguous_evidence_references": ambiguousRefs}}, Converter)
	if e != nil {
		return Imported{}, e
	}
	stats.Questions = stats.Counts.Cases
	stats.Events = stats.Counts.Events
	stats.UpstreamScale.Declared = map[string]any{"benchmark_conversations_all_variants": 100, "variant_conversations": map[string]int{"100K": 20, "500K": 35, "1M": 35}[o.Variant]}
	stats.UpstreamScale.Explanation = "README front matter: 100K=20, 500K=35, 1M=35; 100 conversations/2000 questions describes the whole benchmark including unfetched 10M"
	if e = importers.Check(s, stats); e != nil {
		return Imported{}, e
	}
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
