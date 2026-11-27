// Package memoryagentbench streams the locked MIT release into episode bundles.
package memoryagentbench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parquet-go/parquet-go"
	templates "memeval/internal/benchmark/mab"
	"memeval/internal/importers"
	"memeval/internal/pytext"
	"memeval/internal/suite"
)

const Revision = "7ea066982b140a19337e17e60d45d4076e042faf"
const Converter = "memeval-import-memoryagentbench/1"

var Splits = map[string]string{"accurate_retrieval": "Accurate_Retrieval", "conflict_resolution": "Conflict_Resolution", "long_range_understanding": "Long_Range_Understanding", "test_time_learning": "Test_Time_Learning"}
var Limitations = []string{
	"neurosnap/sentences English Punkt replaces nltk.sent_tokenize; sentence boundary equivalence not established; no online punkt download",
	"o200k_base uses embedded offline BPE; greedy budget sums sentence tokens, excluding joining spaces; upstream preserves oversized sentences and may emit an empty leading chunk",
	"memorize time_stamp is fixed to the empty string instead of the upstream wall clock; event time exposure is none",
	"Privileged metadata is evaluator-only; no fabricated chunk retrieval labels",
	"Recommendation edit-distance ties use sorted candidate names instead of Python set iteration order; tied upstream outcomes are not deterministic",
}

type Turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer"`
}
type Metadata struct {
	Source           string     `json:"source"`
	QuestionDates    []string   `json:"question_dates"`
	QuestionIDs      []string   `json:"question_ids"`
	QuestionTypes    []string   `json:"question_types"`
	QAPairIDs        []string   `json:"qa_pair_ids"`
	HaystackSessions [][][]Turn `json:"haystack_sessions"`
	Keypoints        []string   `json:"keypoints"`
	PreviousEvents   []string   `json:"previous_events"`
	Demo             string     `json:"demo"`
}
type Row struct {
	Context   string     `json:"context"`
	Questions []string   `json:"questions"`
	Answers   [][]string `json:"answers"`
	Metadata  Metadata   `json:"metadata"`
}
type RowFunc func(int, Row) error

// ReadRows keeps only one reconstructed parquet row alive. JSON arrays are used
// solely by checked-in, explicitly truncated conformance fixtures.
func ReadRows(ctx context.Context, path string, fn RowFunc) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	if filepath.Ext(path) == ".json" {
		d := json.NewDecoder(f)
		t, e := d.Token()
		if e != nil || t != json.Delim('[') {
			return fmt.Errorf("fixture must be a JSON array")
		}
		for i := 0; d.More(); i++ {
			if e = ctx.Err(); e != nil {
				return e
			}
			var r Row
			if e = d.Decode(&r); e != nil {
				return e
			}
			if e = fn(i, r); e != nil {
				return e
			}
		}
		_, e = d.Token()
		if e != nil {
			return e
		}
		if _, e = d.Token(); e != io.EOF {
			return fmt.Errorf("trailing fixture data")
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
		e = r.Read(&v)
		if e == io.EOF {
			return nil
		}
		if e != nil {
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
			return e
		}
	}
}
func digestFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil)), e
}

type Imported struct {
	Path        string          `json:"path"`
	SuiteID     string          `json:"suite_id"`
	SuiteSHA256 string          `json:"suite_sha256"`
	Statistics  importers.Stats `json:"statistics"`
}
type Options struct {
	Input, Split, Out string
	ChunkTokens       int
}

func Import(ctx context.Context, o Options) ([]Imported, error) {
	name, ok := Splits[o.Split]
	if !ok {
		return nil, fmt.Errorf("unknown MAB split")
	}
	if o.ChunkTokens != 512 && o.ChunkTokens != 4096 {
		return nil, fmt.Errorf("chunk-tokens must be 512 or 4096")
	}
	path := filepath.Join(o.Input, "data", name+"-00000-of-00001.parquet")
	var lock struct {
		Revision string `json:"revision"`
		Files    []struct{ Path, SHA256 string }
	}
	b, e := os.ReadFile(filepath.Join(o.Input, "source.lock.json"))
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &lock); e != nil {
		return nil, e
	}
	if lock.Revision != Revision {
		return nil, fmt.Errorf("unreviewed MAB revision")
	}
	hash, e := digestFile(path)
	if e != nil {
		return nil, e
	}
	verified := false
	for _, f := range lock.Files {
		if f.Path == "data/"+name+"-00000-of-00001.parquet" {
			verified = f.SHA256 == hash
		}
	}
	if !verified {
		return nil, fmt.Errorf("MAB parquet differs from source lock")
	}
	return ImportFile(ctx, path, o.Split, o.Out, o.ChunkTokens, false)
}
func suiteID(split string, tokens int, fixture bool) string {
	id := "mab-" + strings.ReplaceAll(split, "_", "-") + "@" + Revision[:12]
	if tokens != 4096 && split != "longmemeval-overlap" {
		id += "-c512"
	}
	if fixture {
		id += "-fixture"
	}
	return id
}
func header(split, hash string, tokens int, fixture bool) *suite.Suite {
	no, yes := false, true
	ingestion := fmt.Sprintf("per_chunk:%d:o200k_base:mab_memorize", tokens)
	if split == "longmemeval-overlap" {
		ingestion = "per_session"
	}
	s := &suite.Suite{SchemaVersion: "3.0", SuiteID: suiteID(split, tokens, fixture), Track: "memory-qa", ProtocolRef: "protocols/memoryagentbench.json#" + split, License: &suite.License{ID: "MIT", RedistributeNormalized: true, Source: "https://huggingface.co/datasets/ai-hyz/MemoryAgentBench"}, Provenance: map[string]any{"source_url": "https://huggingface.co/datasets/ai-hyz/MemoryAgentBench", "revision": Revision, "local_input_sha256": hash, "converter": Converter, "cluster_basis": "context", "fixture_truncated": fixture}, Limitations: slices.Clone(Limitations), Protocol: suite.SuiteProtocol{Ingestion: ingestion, ResetPolicyAllowed: []string{"incremental_per_episode"}, TimeExposure: "none", QueryTimePolicy: "reference_only", InjectCurrentDate: &no, TextOnly: &yes, SpeakerMapping: map[string]string{"ingest_render": templates.MemorizeID}}}
	if split == "longmemeval-overlap" {
		s.Limitations = append(s.Limitations, "Separate overlapping LongMemEval-S questions; excluded from the MAB main aggregate; repeated contexts are not independent questions")
	}
	return s
}

type sourceCount struct {
	Rows      int `json:"rows"`
	Questions int `json:"questions"`
}
type chunkStats struct {
	Episode        string `json:"episode"`
	Chunks         int    `json:"chunks"`
	Tokens         []int  `json:"tokens"`
	SentenceTokens []int  `json:"sentence_tokens"`
	OverBudget     int    `json:"over_budget"`
	Empty          int    `json:"empty"`
}
type sink struct {
	w                     *suite.Writer
	s                     *suite.Suite
	path                  string
	rows                  int
	sources               map[string]sourceCount
	chunks                []chunkStats
	ids                   map[string]bool
	sessions, abstentions int
}

func ImportFile(ctx context.Context, path, split, out string, tokens int, fixture bool) (results []Imported, err error) {
	hash, err := digestFile(path)
	if err != nil {
		return nil, err
	}
	ch, err := NewChunker()
	if err != nil {
		return nil, err
	}
	sinks := map[string]*sink{}
	defer func() {
		for _, x := range sinks {
			x.w.Abort()
		}
	}()
	total := 0
	err = ReadRows(ctx, path, func(i int, r Row) error {
		total++
		kind := split
		if strings.HasPrefix(r.Metadata.Source, "longmemeval_s") {
			kind = "longmemeval-overlap"
		}
		x := sinks[kind]
		if x == nil {
			dest := out
			if kind != split {
				dest = filepath.Join(filepath.Dir(out), suiteID(kind, tokens, fixture))
			}
			w, e := suite.NewWriter(dest)
			if e != nil {
				return e
			}
			x = &sink{w: w, s: header(kind, hash, tokens, fixture), path: dest, sources: map[string]sourceCount{}, ids: map[string]bool{}, chunks: []chunkStats{}}
			sinks[kind] = x
		}
		ep, cs, ns, e := episode(r, i, ch, tokens)
		if e != nil {
			return fmt.Errorf("row %d (%s): %w", i, r.Metadata.Source, e)
		}
		if e = x.w.WriteEpisode(ep); e != nil {
			return e
		}
		x.rows++
		x.sessions += ns
		x.chunks = append(x.chunks, cs)
		v := x.sources[r.Metadata.Source]
		v.Rows++
		v.Questions += len(r.Questions)
		x.sources[r.Metadata.Source] = v
		for _, id := range r.Metadata.QuestionIDs {
			x.ids[id] = true
		}
		for _, c := range ep.Cases {
			if c.Gold.Abstain {
				x.abstentions++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, kind := range sorted(sinks) {
		x := sinks[kind]
		if err = x.w.Close(x.s); err != nil {
			return nil, err
		}
		s, e := suite.Open(ctx, x.path)
		if e != nil {
			return nil, e
		}
		ids := sorted(x.ids)
		stats := importers.Stats{Diagnostics: map[string]any{"input_split": split, "input_rows": total, "selected_rows": x.rows, "sources": x.sources, "chunk_tokens": tokens, "tokenizer": "o200k_base", "chunk_distribution": x.chunks, "overlap_question_ids": ids, "overlap_distinct_questions": len(ids)}, LongMemEvalStats: importers.LongMemEvalStats{Sessions: x.sessions, Abstentions: x.abstentions}}
		if kind == "longmemeval-overlap" && !fixture {
			intersection, err := overlapIntersection(path, ids)
			if err != nil {
				return nil, err
			}
			stats.Diagnostics["longmemeval_s_overlap"] = intersection
		}
		stats, e = importers.Complete(s, stats, Converter)
		if e != nil {
			return nil, e
		}
		stats.Questions = stats.Counts.Cases
		stats.Events = stats.Counts.Events
		stats.UpstreamScale.Declared = map[string]any{"rows": total}
		stats.UpstreamScale.Explanation = "Parquet row counts are split totals; overlap is partitioned into its own bundle"
		if e = importers.Check(s, stats); e != nil {
			return nil, e
		}
		b, e := suite.CanonicalJSON(stats)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(filepath.Join(x.path, "import.json"), append(b, '\n'), 0600); e != nil {
			return nil, e
		}
		sha, e := suite.SHA256(s)
		if e != nil {
			return nil, e
		}
		results = append(results, Imported{x.path, s.SuiteID, sha, stats})
	}
	return results, nil
}
func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
func at(xs []string, i int) string {
	if i < len(xs) {
		return xs[i]
	}
	return ""
}
func sessionKey(ts []Turn) string {
	v := make([][2]string, len(ts))
	for i, t := range ts {
		v[i] = [2]string{t.Role, t.Content}
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func episode(r Row, index int, ch *Chunker, tokens int) (*suite.Episode, chunkStats, int, error) {
	source := r.Metadata.Source
	ep := &suite.Episode{ID: fmt.Sprintf("%s#%d", source, index), Events: []suite.Event{}, Cases: []suite.Case{}}
	ep.ClusterID = ep.ID
	cs := chunkStats{Episode: ep.ID, Tokens: []int{}, SentenceTokens: []int{}}
	if _, e := templates.For(source); e != nil {
		return nil, cs, 0, e
	}
	if len(r.Questions) != len(r.Answers) {
		return nil, cs, 0, fmt.Errorf("questions/answers length mismatch")
	}
	sessions := map[string][][]string{}
	ns := 0
	if strings.HasPrefix(source, "longmemeval_s") {
		v, e := pytext.ParseLiteral(r.Context)
		if e != nil {
			return nil, cs, 0, e
		}
		xs, ok := v.([]any)
		if !ok || len(xs)%2 != 0 {
			return nil, cs, 0, fmt.Errorf("invalid chat context")
		}
		for j := 0; j < len(xs); j += 2 {
			date, ok := xs[j].(string)
			if !ok || !strings.HasPrefix(date, "Chat Time:") {
				return nil, cs, 0, fmt.Errorf("invalid Chat Time")
			}
			b, e := json.Marshal(xs[j+1])
			if e != nil {
				return nil, cs, 0, e
			}
			var ts []Turn
			if e = json.Unmarshal(b, &ts); e != nil {
				return nil, cs, 0, e
			}
			ns++
			ids := []string{}
			for k, t := range ts {
				id := fmt.Sprintf("%s:s%d:t%d", ep.ID, j/2, k)
				ep.Events = append(ep.Events, suite.Event{SourceID: id, Owner: "user", Role: t.Role, Content: t.Content, Session: fmt.Sprintf("%s:s%d", ep.ID, j/2), Attributes: map[string]any{"mab_source": source}})
				ids = append(ids, id)
			}
			sessions[sessionKey(ts)] = append(sessions[sessionKey(ts)], ids)
		}
	} else {
		chunks, e := ch.Chunk(r.Context, tokens)
		if e != nil {
			return nil, cs, 0, e
		}
		for k, c := range chunks {
			ep.Events = append(ep.Events, suite.Event{SourceID: fmt.Sprintf("%s:c%d", ep.ID, k), Owner: "user", Role: "observation", Content: c.Text, TurnGroup: "chunk", Attributes: map[string]any{"mab_source": source}})
			cs.Tokens = append(cs.Tokens, c.Tokens)
			cs.SentenceTokens = append(cs.SentenceTokens, c.SentenceTokens)
			if c.Tokens > tokens {
				cs.OverBudget++
			}
			if c.Text == "" {
				cs.Empty++
			}
		}
	}
	cs.Chunks = len(ep.Events)
	for i, q := range r.Questions {
		kind, checker := "qa", "mab_exact_match"
		params := map[string]any{"source": source}
		g := suite.Gold{Answers: r.Answers[i], EvidenceIDs: []string{}, EvidenceUnit: "event"}
		switch {
		case strings.Contains(source, "ruler_qa") || strings.Contains(source, "factconsolidation_"):
			checker = "mab_substring_exact_match"
		case strings.Contains(source, "eventqa_"):
			checker = "mab_eventqa_recall"
		case strings.Contains(source, "recsys_"):
			kind, checker = "recsys", "mab_recsys_recall@5"
			g.ItemIDs = slices.Clone(r.Answers[i])
			params["candidates"] = "entity2id"
		case strings.Contains(source, "infbench_sum"):
			kind, checker = "summary", "mab_infbench_sum"
			g.Reference = strings.Join(r.Answers[i], "\n")
		case strings.HasPrefix(source, "longmemeval_s"):
			checker = "mab_longmemeval_judge"
			qid := at(r.Metadata.QuestionIDs, i)
			g.Abstain = strings.Contains(qid, "_abs")
			params["question_type"] = at(r.Metadata.QuestionTypes, i)
			params["abstention"] = g.Abstain
			if i >= len(r.Metadata.HaystackSessions) {
				return nil, cs, 0, fmt.Errorf("missing evidence sessions")
			}
			seen := map[string]bool{}
			for _, ts := range r.Metadata.HaystackSessions[i] {
				matches := sessions[sessionKey(ts)]
				for k, t := range ts {
					if !t.HasAnswer {
						continue
					}
					if len(matches) == 0 {
						return nil, cs, 0, fmt.Errorf("labeled evidence session absent from context")
					}
					for _, ids := range matches {
						if !seen[ids[k]] {
							seen[ids[k]] = true
							g.EvidenceIDs = append(g.EvidenceIDs, ids[k])
						}
					}
				}
			}
		}
		p, _ := json.Marshal(params)
		g.Checker = &suite.Checker{Type: checker, Params: p}
		attrs := map[string]any{"mab_source": source, "question_id": at(r.Metadata.QuestionIDs, i), "question_date": at(r.Metadata.QuestionDates, i), "qa_pair_id": at(r.Metadata.QAPairIDs, i)}
		if len(r.Metadata.Keypoints) > 0 {
			attrs["keypoints"] = r.Metadata.Keypoints
		}
		if r.Metadata.Demo != "" {
			attrs["demo"] = r.Metadata.Demo
		}
		if len(r.Metadata.PreviousEvents) > 0 {
			attrs["previous_events"] = r.Metadata.PreviousEvents
		}
		ep.Cases = append(ep.Cases, suite.Case{ID: fmt.Sprintf("%s:q%d", ep.ID, i), ClusterID: ep.ID, Kind: kind, Category: source, QuestionType: at(r.Metadata.QuestionTypes, i), Query: q, User: "user", Scope: []string{}, Cutoff: len(ep.Events), QueryPoint: "end_of_history", Gold: g, Attributes: attrs})
	}
	return ep, cs, ns, nil
}
