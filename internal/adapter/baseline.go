package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"memeval/internal/protocol"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const stopWords = "a an the is are was were be been to of for from in on at and or as by with my me i we our you your it this that these those how what why when where should would can could do does did about any all so use using us its into has have had tell give please"

var stop = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Fields(stopWords) {
		m[s] = true
	}
	return m
}()
var tokenRE = regexp.MustCompile(`[a-z0-9]+|[\x{3400}-\x{9fff}]`)

func Tokens(s string) []string {
	// Python lower expands dotted capital I into i + combining dot.
	s = strings.ReplaceAll(s, "İ", "i\u0307")
	out := []string{}
	for _, x := range tokenRE.FindAllString(strings.ToLower(s), -1) {
		if !stop[x] {
			out = append(out, x)
		}
	}
	return out
}
func BM25Scores(query string, documents []string) []float64 {
	if len(documents) == 0 {
		return []float64{}
	}
	docs := make([][]string, len(documents))
	counts := make([]map[string]int, len(documents))
	df := map[string]int{}
	length := 0
	for i, d := range documents {
		docs[i] = Tokens(d)
		length += len(docs[i])
		counts[i] = map[string]int{}
		for _, w := range docs[i] {
			counts[i][w]++
		}
		for w := range counts[i] {
			df[w]++
		}
	}
	avg := float64(length) / float64(len(docs))
	if avg == 0 {
		avg = 1
	}
	qt := map[string]bool{}
	for _, w := range Tokens(query) {
		qt[w] = true
	}
	words := make([]string, 0, len(qt))
	for w := range qt {
		words = append(words, w)
	}
	sort.Strings(words)
	out := make([]float64, len(docs))
	n := float64(len(docs))
	for i, doc := range docs {
		for _, w := range words {
			tf := float64(counts[i][w])
			if tf > 0 {
				idf := math.Log(1 + (n-float64(df[w])+.5)/(float64(df[w])+.5))
				out[i] += idf * tf * 2.5 / (tf + 1.5*(.25+.75*float64(len(doc))/avg))
			}
		}
	}
	return out
}

type Baseline struct {
	strategy string
	store    map[string][]protocol.PublicEvent
	seq      map[string]int
}

func NewBaseline(strategy string) (*Baseline, error) {
	if strategy != "bm25" && strategy != "recent" && strategy != "null" && strategy != "fullcontext" {
		return nil, fmt.Errorf("unknown baseline %q", strategy)
	}
	return &Baseline{strategy: strategy, store: map[string][]protocol.PublicEvent{}, seq: map[string]int{}}, nil
}
func (b *Baseline) FullContext() bool { return b.strategy == "fullcontext" }
func (b *Baseline) Describe(ctx context.Context) (protocol.Describe, error) {
	if b.FullContext() {
		return protocol.Describe{Name: "fullcontext", Version: "0.1.0", Capabilities: []string{"retrieve", "read_only_queries", "dump"}, ReadOnlyQueries: true, Limitations: []string{"all events returned; context budget enforced by fixed reader"}}, ctx.Err()
	}
	return protocol.Describe{Name: b.strategy, Version: "0.1.0", Capabilities: []string{"retrieve", "typed_context", "read_only_queries", "dump"}, ReadOnlyQueries: true, Limitations: []string{"as_of unsupported", "No answer model", "Source-specific typed metadata routing"}, Config: map[string]any{"k1": 1.5, "b": .75, "positive_score_only": true, "policy": "metadata owner/scope/state; no learned judgments"}}, ctx.Err()
}
func (b *Baseline) Reset(ctx context.Context, ns string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.store[ns] = []protocol.PublicEvent{}
	b.seq[ns] = 0
	return nil
}
func (b *Baseline) Ingest(ctx context.Context, ns string, events []protocol.PublicEvent) (protocol.IngestHandle, error) {
	if err := ctx.Err(); err != nil {
		return protocol.IngestHandle{}, err
	}
	old, ok := b.store[ns]
	if !ok {
		return protocol.IngestHandle{}, &ContractError{"reset namespace before ingest"}
	}
	data, err := json.Marshal(events)
	if err != nil {
		return protocol.IngestHandle{}, err
	}
	var clone []protocol.PublicEvent
	if err = json.Unmarshal(data, &clone); err != nil {
		return protocol.IngestHandle{}, err
	}
	index := map[string]int{}
	for i, e := range old {
		index[e.ID] = i
	}
	for _, e := range clone {
		if i, ok := index[e.ID]; ok {
			old[i] = e
		} else {
			index[e.ID] = len(old)
			old = append(old, e)
		}
	}
	b.store[ns] = old
	b.seq[ns]++
	return protocol.IngestHandle{BatchSeq: b.seq[ns], Accepted: len(events)}, nil
}
func (b *Baseline) Ready(ctx context.Context, ns string, h protocol.IngestHandle, deadline time.Time) (protocol.ReadyResult, error) {
	if err := ctx.Err(); err != nil {
		return protocol.ReadyResult{}, err
	}
	if !deadline.IsZero() && time.Now().After(deadline) {
		return protocol.ReadyResult{}, context.DeadlineExceeded
	}
	if _, ok := b.store[ns]; !ok || h.BatchSeq > b.seq[ns] {
		return protocol.ReadyResult{}, &ContractError{"unknown namespace/batch"}
	}
	return protocol.ReadyResult{Visible: true, Stages: []protocol.Stage{{Name: "memory", Status: "ok"}}, Warnings: []string{}}, nil
}
func (b *Baseline) Answer(ctx context.Context, _ string, _ protocol.RetrieveRequest) (protocol.AnswerResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.AnswerResponse{}, err
	}
	return protocol.AnswerResponse{}, ErrUnsupported
}
func (b *Baseline) Close(context.Context) error { clear(b.store); clear(b.seq); return nil }
func stringsOf(v any) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []any:
		out := []string{}
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func str(v any) string { s, _ := v.(string); return s }
func truth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case []any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}
func item(e protocol.PublicEvent, score *float64) protocol.Item {
	return protocol.Item{ID: e.ID, Text: e.Content, SourceIDs: []string{e.ID}, Score: score}
}

type ranked struct {
	event protocol.PublicEvent
	score *float64
}

func (b *Baseline) rank(query string, events []protocol.PublicEvent) []ranked {
	out := []ranked{}
	if strings.TrimSpace(query) == "" {
		return out
	}
	if b.strategy == "recent" {
		for i := len(events) - 1; i >= 0; i-- {
			out = append(out, ranked{event: events[i]})
		}
		return out
	}
	docs := make([]string, len(events))
	for i, e := range events {
		parts := []string{e.Content, strings.ReplaceAll(str(e.Attributes["canonical_name"]), "_", " ")}
		parts = append(parts, stringsOf(e.Attributes["aliases"])...)
		docs[i] = strings.Join(parts, " ")
	}
	scores := BM25Scores(query, docs)
	indices := make([]int, len(events))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool { return scores[indices[i]] > scores[indices[j]] })
	for _, i := range indices {
		if scores[i] > 0 {
			out = append(out, ranked{event: events[i], score: &scores[i]})
		}
	}
	return out
}
func (b *Baseline) Retrieve(ctx context.Context, ns string, req protocol.RetrieveRequest) (protocol.RetrieveResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.RetrieveResponse{}, err
	}
	events, ok := b.store[ns]
	if !ok {
		return protocol.RetrieveResponse{}, &ContractError{"unknown namespace"}
	}
	if req.Budget.TopK < 0 || req.Budget.MaxContextChars < 0 || req.Budget.MaxBeliefs < 0 {
		return protocol.RetrieveResponse{}, &ContractError{"negative budget"}
	}
	eligible := []protocol.PublicEvent{}
	for _, e := range events {
		if e.Owner == req.User {
			eligible = append(eligible, e)
		}
	}
	k, cap := req.Budget.TopK, req.Budget.MaxContextChars
	response := protocol.RetrieveResponse{Items: []protocol.Item{}}
	if req.OutputFormat == "typed_context" {
		empty := ""
		response.PersonaPrelude = &empty
		response.Tiers = &protocol.Tiers{PinnedFacts: []protocol.Item{}, RelevantBeliefs: []protocol.Item{}, OpenQuestions: []protocol.Item{}}
	}
	if b.FullContext() {
		if req.OutputFormat == "typed_context" {
			return response, ErrUnsupported
		}
		for _, e := range events {
			response.Items = append(response.Items, item(e, nil))
		}
		return response, nil
	}
	if b.strategy == "null" {
		return response, nil
	}
	if req.OutputFormat != "typed_context" {
		used := 0
		for _, r := range b.rank(req.Query, eligible) {
			if len(response.Items) >= k {
				break
			}
			n := utf8.RuneCountInString(r.event.Content)
			if used+n <= cap {
				response.Items = append(response.Items, item(r.event, r.score))
				used += n
			}
		}
		return response, nil
	}
	universal, domain := []protocol.PublicEvent{}, []protocol.PublicEvent{}
	for _, e := range eligible {
		a := e.Attributes
		state := str(a["epistemic_status"])
		if state == "superseded" || state == "archived" || truth(a["resolved_at"]) || truth(a["superseded_by"]) {
			continue
		}
		scopes := stringsOf(a["scope"])
		if slices.Contains(scopes, "user:universal") {
			universal = append(universal, e)
		} else {
			for _, s := range req.Scope {
				if slices.Contains(scopes, s) {
					domain = append(domain, e)
					break
				}
			}
		}
	}
	parts := []string{}
	for _, e := range universal {
		parts = append(parts, e.Content)
	}
	runes := []rune(strings.Join(parts, "\n"))
	if len(runes) > cap {
		runes = runes[:cap]
	}
	prelude := string(runes)
	response.PersonaPrelude = &prelude
	remaining := cap - len(runes)
	pinned, questions, candidates := []ranked{}, []ranked{}, []protocol.PublicEvent{}
	for _, e := range domain {
		isQuestion := str(e.Attributes["type"]) == "open_question"
		isPinned := truth(e.Attributes["pinned"])
		if isPinned && !isQuestion {
			pinned = append(pinned, ranked{event: e})
		}
		if isPinned && isQuestion {
			questions = append(questions, ranked{event: e})
		}
		if !isPinned && !isQuestion {
			candidates = append(candidates, e)
		}
	}
	relevant := b.rank(req.Query, candidates)
	if len(relevant) > k {
		relevant = relevant[:k]
	}
	maxBeliefs := 20
	if req.Budget.HasMaxBeliefs || req.Budget.MaxBeliefs != 0 {
		maxBeliefs = req.Budget.MaxBeliefs
	}
	count := 0
	for _, group := range []struct {
		rows []ranked
		dest *[]protocol.Item
	}{{pinned, &response.Tiers.PinnedFacts}, {questions, &response.Tiers.OpenQuestions}, {relevant, &response.Tiers.RelevantBeliefs}} {
		for _, r := range group.rows {
			n := utf8.RuneCountInString(r.event.Content)
			if count < maxBeliefs && n <= remaining {
				*group.dest = append(*group.dest, item(r.event, r.score))
				count++
				remaining -= n
			}
		}
	}
	return response, nil
}
