// Package personamem imports PersonaMem v1, preserving the official exclusive prefix.
package personamem

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
	"sort"
	"strconv"
	"strings"
)

const Revision = "a8076d5608c93ba2a28983cd78aa99b01a163ae7"
const Converter = "memeval-import-personamem/1"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func Import(ctx context.Context, questions, contexts, variant, persona string) (*suite.Suite, importers.Stats, error) {
	stats := importers.Stats{}
	if variant != "32k" || (persona != "include" && persona != "exclude") {
		return nil, stats, fmt.Errorf("requires variant 32k and system-persona include|exclude")
	}
	qb, err := os.ReadFile(questions)
	if err != nil {
		return nil, stats, err
	}
	cb, err := os.ReadFile(contexts)
	if err != nil {
		return nil, stats, err
	}
	histories := map[string][]Message{}
	scan := bufio.NewScanner(bytes.NewReader(cb))
	scan.Buffer(make([]byte, 4096), 32<<20)
	for scan.Scan() {
		var line map[string][]Message
		if err = json.Unmarshal(scan.Bytes(), &line); err != nil {
			return nil, stats, err
		}
		for id, msgs := range line {
			if _, ok := histories[id]; ok {
				return nil, stats, fmt.Errorf("duplicate context %s", id)
			}
			histories[id] = msgs
		}
	}
	if err = scan.Err(); err != nil {
		return nil, stats, err
	}
	records, err := csv.NewReader(bytes.NewReader(qb)).ReadAll()
	if err != nil || len(records) < 2 {
		return nil, stats, fmt.Errorf("questions CSV missing rows: %v", err)
	}
	columns := map[string]int{}
	for i, k := range records[0] {
		if _, ok := columns[k]; ok {
			return nil, stats, fmt.Errorf("duplicate CSV column %s", k)
		}
		columns[k] = i
	}
	for _, k := range []string{"persona_id", "question_id", "question_type", "user_question_or_message", "correct_answer", "all_options", "shared_context_id", "end_index_in_shared_context"} {
		if _, ok := columns[k]; !ok {
			return nil, stats, fmt.Errorf("missing column %s", k)
		}
	}
	rows := []map[string]string{}
	owners := map[string]string{}
	seen := map[string]bool{}
	labels := map[string]string{}
	for _, r := range records[1:] {
		row := map[string]string{}
		for k, i := range columns {
			row[k] = r[i]
		}
		cid, pid, qid := row["shared_context_id"], row["persona_id"], row["question_id"]
		if cid == "" || pid == "" || qid == "" {
			return nil, stats, fmt.Errorf("empty identity")
		}
		if seen[qid] {
			return nil, stats, fmt.Errorf("duplicate question_id %s", qid)
		}
		seen[qid] = true
		if old, ok := owners[cid]; ok && old != pid {
			return nil, stats, fmt.Errorf("context %s belongs to multiple personas", cid)
		}
		if _, ok := histories[cid]; !ok {
			return nil, stats, fmt.Errorf("unknown context %s", cid)
		}
		owners[cid] = pid
		labels[row["question_type"]] = row["question_type"]
		rows = append(rows, row)
	}
	no := false
	ref := "protocols/personamem.json#official"
	id := "personamem-v1-32k@" + Revision[:12]
	if persona == "exclude" {
		ref = "protocols/personamem.json#memory_only"
		id += "-nopersona"
	}
	hashes := map[string]string{"questions": fmt.Sprintf("%x", sha256.Sum256(qb)), "contexts": fmt.Sprintf("%x", sha256.Sum256(cb))}
	hb, _ := suite.CanonicalJSON(hashes)
	s := &suite.Suite{SchemaVersion: "3.0", SuiteID: id, Track: "personalization-mcq", ProtocolRef: ref, License: &suite.License{ID: "MIT", RedistributeNormalized: true, Source: "https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1/blob/" + Revision + "/README.md"}, DataNature: "model-generated", Provenance: map[string]any{"source_url": "https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1", "revision": Revision, "converter": Converter, "cluster_basis": "persona", "input_sha256": hashes, "local_input_sha256": fmt.Sprintf("%x", sha256.Sum256(hb))}, Taxonomy: &suite.Taxonomy{OriginalField: "question_type", CanonicalMap: labels}, Protocol: suite.SuiteProtocol{Ingestion: "per_event", TimeExposure: "none", InjectCurrentDate: &no, ResetPolicyAllowed: []string{"fresh_per_case"}}, Limitations: []string{"Original User: / Assistant: content prefixes preserved; cutoff is exclusive end_index_in_shared_context", "options_raw is private CSV text, used only by evaluator reader; diagnostic CSV columns are aggregate-only"}}
	if persona == "exclude" {
		s.Limitations = append(s.Limitations, "All system persona messages removed, including repeated block-boundary messages; cutoff counts retained messages in original prefix; not official long-context setting")
	}
	for k := range labels {
		s.Taxonomy.Labels = append(s.Taxonomy.Labels, k)
	}
	sort.Strings(s.Taxonomy.Labels)
	ids := []string{}
	for cid := range owners {
		ids = append(ids, cid)
	}
	sort.Strings(ids)
	indexes := map[string]int{}
	prefixes := map[string][]int{}
	perPersona := map[string]int{}
	counts := map[string]int{"contexts": len(ids), "context_messages_min": int(^uint(0) >> 1), "end_index_min": int(^uint(0) >> 1), "question_equals_at_end": 0, "question_equals_before_end": 0, "question_equals_at_end_without_user_prefix": 0}
	for _, cid := range ids {
		ep := suite.Episode{ID: cid, ClusterID: owners[cid], Events: []suite.Event{}, Cases: []suite.Case{}}
		perPersona[owners[cid]]++
		msgs := histories[cid]
		counts["context_messages_min"] = min(counts["context_messages_min"], len(msgs))
		counts["context_messages_max"] = max(counts["context_messages_max"], len(msgs))
		if len(msgs) > 0 && msgs[0].Role == "system" {
			counts["contexts_starting_system"]++
		}
		pref := make([]int, len(msgs)+1)
		for i, m := range msgs {
			counts["messages"]++
			if m.Role == "system" {
				counts["system_messages"]++
			}
			if persona != "exclude" || m.Role != "system" {
				ep.Events = append(ep.Events, suite.Event{SourceID: fmt.Sprintf("%s:%d", cid, i), Owner: "user", Role: m.Role, Content: m.Content, Attributes: map[string]any{}})
			}
			pref[i+1] = len(ep.Events)
		}
		indexes[cid] = len(s.Episodes)
		prefixes[cid] = pref
		stats.Events += len(ep.Events)
		s.Episodes = append(s.Episodes, ep)
	}
	letters := map[string]int{}
	numeric := map[string]map[string]float64{}
	for _, row := range rows {
		if err = ctx.Err(); err != nil {
			return nil, stats, err
		}
		cid := row["shared_context_id"]
		end, e := strconv.Atoi(row["end_index_in_shared_context"])
		if e != nil || end < 0 || end > len(histories[cid]) {
			return nil, stats, fmt.Errorf("invalid end_index for %s", row["question_id"])
		}
		counts["end_index_min"] = min(counts["end_index_min"], end)
		counts["end_index_max"] = max(counts["end_index_max"], end)
		q := row["user_question_or_message"]
		if end < len(histories[cid]) && q == histories[cid][end].Content {
			counts["question_equals_at_end"]++
		}
		if end < len(histories[cid]) && q == strings.TrimPrefix(histories[cid][end].Content, "User: ") {
			counts["question_equals_at_end_without_user_prefix"]++
		}
		if end > 0 && q == histories[cid][end-1].Content {
			counts["question_equals_before_end"]++
		}
		opts, e := ParseOptions(row["all_options"])
		if e != nil || len(opts) != 4 {
			return nil, stats, fmt.Errorf("%s: expected four string options: %v", row["question_id"], e)
		}
		om := map[string]string{}
		for i, v := range opts {
			om[string(rune('A'+i))] = v
		}
		choice := strings.ToUpper(strings.Trim(row["correct_answer"], "() \t\r\n"))
		if _, ok := om[choice]; !ok {
			return nil, stats, fmt.Errorf("invalid correct_answer")
		}
		letters[strings.ToLower(choice)]++
		for k, v := range row {
			if strings.HasPrefix(k, "distance_") || strings.HasPrefix(k, "context_length_") || k == "num_irrelevant_tokens" {
				n, e := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
				if e != nil {
					return nil, stats, fmt.Errorf("invalid diagnostic %s", k)
				}
				a := numeric[k]
				if a == nil {
					a = map[string]float64{"min": n, "max": n}
					numeric[k] = a
				}
				if strings.HasSuffix(v, "%") {
					n /= 100
					if a["count"] == 0 {
						a["min"] = n
						a["max"] = n
					}
				}
				a["count"]++
				a["sum"] += n
				a["min"] = min(a["min"], n)
				a["max"] = max(a["max"], n)
			}
		}
		ep := &s.Episodes[indexes[cid]]
		ep.Cases = append(ep.Cases, suite.Case{ID: row["persona_id"] + ":" + row["question_id"], Kind: "mcq", Query: q, User: "user", Scope: []string{}, ClusterID: ep.ClusterID, Category: row["question_type"], QuestionType: row["question_type"], Cutoff: prefixes[cid][end], QueryPoint: "at_index", Options: om, Attributes: map[string]any{"options_raw": row["all_options"]}, Gold: suite.Gold{Choice: choice, MCQ: &suite.MCQSpec{Parse: "personamem_final_answer"}, EvidenceIDs: []string{}}})
	}
	counts["personas"] = len(perPersona)
	dist := map[string]int{}
	for _, n := range perPersona {
		dist[strconv.Itoa(n)]++
	}
	stats.Questions = len(rows)
	stats.Diagnostics = map[string]any{"counts": counts, "correct_answers": letters, "contexts_per_persona_distribution": dist, "csv_numeric": numeric}
	stats.UpstreamScale.Declared = map[string]any{"questions_32k": 589, "contexts": 37, "personas": 20}
	stats, err = importers.Complete(s, stats, Converter)
	return s, stats, err
}
