// Package locomo imports the text-only LoCoMo release without privileged annotations.
package locomo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const Revision = "3eb6f2c585f5e1699204e3c3bdf7adc5c28cb376"
const Converter = "memeval-import-locomo/2"

var Categories = map[string]string{"1": "multi-hop", "2": "temporal", "3": "open-domain", "4": "single-hop", "5": "adversarial"}

type question struct {
	Question          string   `json:"question"`
	Answer            any      `json:"answer"`
	AdversarialAnswer *string  `json:"adversarial_answer"`
	Category          int      `json:"category"`
	Evidence          []string `json:"evidence"`
}
type sample struct {
	ID           string                     `json:"sample_id"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	QA           []question                 `json:"qa"`
}

func ParseDate(raw string) (string, error) {
	t, e := time.Parse("3:04 pm on 2 January, 2006", raw)
	if e != nil {
		return "", e
	}
	return t.UTC().Format(time.RFC3339), nil
}
func Import(ctx context.Context, path string) (*suite.Suite, importers.Stats, error) {
	stats := importers.Stats{}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, stats, err
	}
	var rows []sample
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = d.Decode(&rows); err != nil {
		return nil, stats, err
	}
	no := false
	speakers := map[string][]string{}
	s := &suite.Suite{SchemaVersion: "3.0", SuiteID: "locomo10@" + Revision[:12], Track: "history-qa", ProtocolRef: "protocols/locomo.json#text_only", DataNature: "model-generated", License: &suite.License{ID: "CC-BY-NC-4.0", Noncommercial: true, RedistributeNormalized: true, Source: "https://github.com/snap-research/locomo/blob/" + Revision + "/LICENSE.txt"}, Taxonomy: &suite.Taxonomy{OriginalField: "category", Labels: []string{"1", "2", "3", "4", "5"}, CanonicalMap: Categories}, Provenance: map[string]any{"source_url": "https://github.com/snap-research/locomo", "revision": Revision, "local_input_sha256": fmt.Sprintf("%x", sha256.Sum256(b)), "converter": Converter, "cluster_basis": "conversation", "episode_speakers": speakers}, Protocol: suite.SuiteProtocol{Ingestion: "per_session", TimeExposure: "session", InjectCurrentDate: &no, TextOnly: &no, ResetPolicyAllowed: []string{"fresh_per_case"}, SpeakerMapping: map[string]string{"owner": "conversation", "role": "user", "ingest_render": "{speaker}: {content}", "reader_render": "official"}}}
	yes := true
	s.Protocol.TextOnly = &yes
	// Preserve the already-published v1 bundle/root; this converter retains new
	// evaluator-only inputs required by the official reader protocol.
	s.SuiteID += "-reader-v2"
	counts := map[string]int{"conversations": len(rows)}
	missing := map[string]int{}
	removed := map[string]int{}
	corrections := []map[string]any{}
	splits := []map[string]any{}
	dropped := []map[string]any{}
	for _, row := range rows {
		if err = ctx.Err(); err != nil {
			return nil, stats, err
		}
		ep := suite.Episode{ID: row.ID, ClusterID: row.ID, Events: []suite.Event{}, Cases: []suite.Case{}}
		var a, z string
		if err = json.Unmarshal(row.Conversation["speaker_a"], &a); err != nil {
			return nil, stats, err
		}
		if err = json.Unmarshal(row.Conversation["speaker_b"], &z); err != nil {
			return nil, stats, err
		}
		speakers[row.ID] = []string{a, z}
		nums := []int{}
		for key := range row.Conversation {
			if strings.HasPrefix(key, "session_") && !strings.HasSuffix(key, "_date_time") {
				n, e := strconv.Atoi(strings.TrimPrefix(key, "session_"))
				if e != nil {
					return nil, stats, e
				}
				nums = append(nums, n)
			}
		}
		sort.Ints(nums)
		known := map[string]bool{}
		for _, n := range nums {
			key := fmt.Sprintf("session_%d", n)
			var raw string
			if err = json.Unmarshal(row.Conversation[key+"_date_time"], &raw); err != nil {
				return nil, stats, err
			}
			stamp, e := ParseDate(raw)
			if e != nil {
				stats.SessionTimeFailures++
			}
			counts["session_dates"]++
			if strings.HasPrefix(raw, "12:") {
				counts["twelve_hour_boundaries"]++
			}
			stats.Sessions++
			var turns []map[string]json.RawMessage
			if err = json.Unmarshal(row.Conversation[key], &turns); err != nil {
				return nil, stats, err
			}
			for _, turn := range turns {
				var id, text, speaker string
				for k, dst := range map[string]*string{"dia_id": &id, "text": &text, "speaker": &speaker} {
					if err = json.Unmarshal(turn[k], dst); err != nil {
						return nil, stats, fmt.Errorf("%s/%s: %w", key, k, err)
					}
				}
				if known[id] {
					return nil, stats, fmt.Errorf("duplicate source %s", id)
				}
				known[id] = true
				for _, k := range []string{"img_url", "blip_caption", "query", "re-download"} {
					if _, ok := turn[k]; ok {
						removed[k]++
					}
				}
				ep.Events = append(ep.Events, suite.Event{SourceID: id, Owner: "conversation", Role: "user", Session: key, Timestamp: stamp, TimeRaw: raw, Content: text, Attributes: map[string]any{"speaker": speaker}})
			}
		}
		for i, q := range row.QA {
			cat := strconv.Itoa(q.Category)
			label, ok := Categories[cat]
			if !ok {
				return nil, stats, fmt.Errorf("unknown category %d", q.Category)
			}
			answers := []string{}
			switch v := q.Answer.(type) {
			case nil:
				counts["missing_answers"]++
				missing[cat]++
			case string:
				answers = append(answers, v)
			case json.Number:
				if _, e := v.Int64(); e != nil {
					return nil, stats, e
				}
				counts["integer_answers"]++
				answers = append(answers, string(v))
			default:
				return nil, stats, fmt.Errorf("invalid answer type")
			}
			evidence := []string{}
			evidenceNotes := []string{}
			for _, ref := range q.Evidence {
				counts["evidence_references_raw"]++
				if strings.Contains(ref, ";") {
					counts["semicolon_evidence_values"]++
				}
				parts := strings.Fields(strings.ReplaceAll(ref, ";", " "))
				actions := []string{}
				if len(parts) > 1 {
					splits = append(splits, map[string]any{"sample_id": row.ID, "question_index": i, "original": ref, "parts": parts})
					actions = append(actions, fmt.Sprintf("split into %q", parts))
				}
				if len(parts) > 1 && !strings.Contains(ref, ";") {
					counts["whitespace_evidence_values"]++
				}
				for _, rawID := range parts {
					id := NormalizeEvidence(rawID)
					counts["evidence_references_parsed"]++
					if id != rawID {
						counts["canonicalized_evidence_ids"]++
						corrections = append(corrections, map[string]any{"sample_id": row.ID, "question_index": i, "original": rawID, "normalized": id})
						actions = append(actions, fmt.Sprintf("normalized %q to %q", rawID, id))
					}
					if !known[id] {
						counts["dropped_evidence_references"]++
						dropped = append(dropped, map[string]any{"sample_id": row.ID, "question_index": i, "reference": rawID, "reason": "no matching source event"})
						actions = append(actions, fmt.Sprintf("dropped %q: no matching source event", rawID))
						continue
					}
					evidence = append(evidence, id)
					counts["evidence_references_retained"]++
				}
				if len(actions) > 0 {
					evidenceNotes = append(evidenceNotes, fmt.Sprintf("evidence %q: %s", ref, strings.Join(actions, "; ")))
				}
			}
			var attributes map[string]any
			if q.Category == 5 {
				adversarial := q.AdversarialAnswer
				if adversarial != nil {
					counts["cat5_adversarial_answer_field"]++
					if len(answers) > 0 && answers[0] != *adversarial {
						counts["cat5_answer_field_conflicts"]++
					}
				} else if len(answers) > 0 {
					adversarial = &answers[0]
					counts["cat5_answer_field_fallback"]++
				}
				if adversarial != nil {
					attributes = map[string]any{"adversarial_answer": *adversarial}
					counts["cat5_private_answers"]++
				} else {
					counts["cat5_missing_private_answers"]++
				}
				answers = []string{}
				stats.Abstentions++
			}
			params, _ := json.Marshal(map[string]int{"category": q.Category})
			var disputed *suite.Disputed
			if len(evidenceNotes) > 0 {
				disputed = &suite.Disputed{Note: strings.Join(evidenceNotes, "\n")}
			}
			ep.Cases = append(ep.Cases, suite.Case{ID: fmt.Sprintf("%s:%d", row.ID, i), Kind: "qa", Query: q.Question, User: "conversation", Scope: []string{}, Cutoff: len(ep.Events), ClusterID: row.ID, Category: label, QuestionType: cat, Attributes: attributes, Gold: suite.Gold{Answers: answers, Abstain: q.Category == 5, EvidenceIDs: evidence, EvidenceUnit: "event", Disputed: disputed, Checker: &suite.Checker{Type: "locomo_official", Params: params}}})
		}
		stats.Questions += len(ep.Cases)
		stats.Events += len(ep.Events)
		s.Episodes = append(s.Episodes, ep)
	}
	stats.Diagnostics = map[string]any{"counts": counts, "missing_answers_by_category": missing, "removed_multimodal_fields": removed, "evidence_corrections": corrections, "evidence_splits": splits, "dropped_evidence": dropped}
	if n := counts["dropped_evidence_references"]; n > 0 {
		stats.Skipped = []importers.Skipped{{Reason: "evidence reference: no matching source event (question retained)", Count: n}}
	}
	stats.UpstreamScale.Declared = map[string]any{"conversations": 10, "source": "README.MD: ten conversations"}
	s.Limitations = []string{fmt.Sprintf("text-only: omitted multimodal fields (img_url=%d, blip_caption=%d, query=%d, re-download=%d turns)", removed["img_url"], removed["blip_caption"], removed["query"], removed["re-download"]), fmt.Sprintf("Split %d semicolon evidence values; raw references=%d, parsed=%d", counts["semicolon_evidence_values"], counts["evidence_references_raw"], counts["evidence_references_parsed"]), "CC BY-NC 4.0: upstream SNAP Research LoCoMo; noncommercial use and attribution required", "Speaker order follows conversation.speaker_a/speaker_b, not upstream unordered set", "Official cat2/cat5 suffixes are reader-only; cat5 adversarial_answer is private case.attributes data, never a SUT option or query. Letter mapping precedes unchanged official phrase scoring", fmt.Sprintf("Cat5 private distractors=%d; adversarial_answer takes precedence over answer (%d conflicting answer fields); answer-only fallback=%d; missing=%d (reader fidelity partial)", counts["cat5_private_answers"], counts["cat5_answer_field_conflicts"], counts["cat5_answer_field_fallback"], counts["cat5_missing_private_answers"])}
	s.Limitations = append(s.Limitations, fmt.Sprintf("Evidence normalization: split %d whitespace lists, canonicalized %d IDs, dropped %d unresolved references; see import diagnostics (no event or answer synthesis)", counts["whitespace_evidence_values"], counts["canonicalized_evidence_ids"], counts["dropped_evidence_references"]))
	stats, err = importers.Complete(s, stats, Converter)
	return s, stats, err
}

var evidencePattern = regexp.MustCompile(`^D:?(\d+):(\d+)$`)

// NormalizeEvidence repairs only unambiguous punctuation/zero-padding; never guesses a turn.
func NormalizeEvidence(raw string) string {
	m := evidencePattern.FindStringSubmatch(raw)
	if m == nil {
		return raw
	}
	session, e := strconv.Atoi(m[1])
	if e != nil {
		return raw
	}
	turn, e := strconv.Atoi(m[2])
	if e != nil {
		return raw
	}
	return fmt.Sprintf("D%d:%d", session, turn)
}
