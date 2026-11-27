package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"reflect"
	"slices"
	"strings"
)

type LeakReport struct {
	Checked        int      `json:"checked"`
	Problems       []string `json:"problems"`
	AnswerTextScan string   `json:"answer_text_scan"`
}

// LeakScan visits public DTO fields directly. Large event content is exempt by
// contract, so it is never serialized/copied just to discard it during scanning.
func LeakScan(s *Suite) (LeakReport, error) {
	r := LeakReport{Problems: []string{}, AnswerTextScan: "not_applicable"}
	gold := map[string]bool{}
	t := reflect.TypeFor[Gold]()
	for i := 0; i < t.NumField(); i++ {
		gold[strings.Split(t.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	for _, k := range []string{"gold", "answer", "answers", "label", "labels", "correct", "has_answer", "expect", "evidence", "evidence_ids", "evidence_session_ids", "rubric", "memory_points", "choice", "abstain", "adversarial_answer"} {
		gold[k] = true
	}
	if s.SchemaVersion == "3.0" {
		for _, key := range []string{"persona_info", "memory_source", "importance", "original_memories", "is_update", "reason", "is_generated_qa_session", "keypoints", "previous_events", "question_types", "demo", "question_ids", "qa_pair_ids", "conversation_seed", "narratives", "user_profile", "conversation_plan", "user_questions", "source_chat_ids", "ideal_summary", "probing_questions", "share_memory", "session_type", "operation", "operation_details", "evaluation", "evaluation_questions", "evaluation_type", "expected_answer", "memory_evidence", "forgetting_evidence", "date_range", "preference", "explanation", "persona", "implicit_query", "aligned_op", "classification_task_options", "options_raw", "violation_probability", "containsEvidence", "message_evidences", "scenario_description", "use_case_model_name", "core_model_name", "checkpoint", "conversation_ids"} {
			gold[key] = true
		}
	}
	ei := 0
	err := s.Iter(context.Background(), func(ep *Episode) error {
		original := map[string]bool{ep.ID: true}
		// Numeric episode identifiers are also ordinary text/numbers. The user
		// approved checking identity DTOs positively rather than substrings.
		if s.SchemaVersion == "3.0" && asciiDigits(ep.ID) {
			delete(original, ep.ID)
		}
		roles := []string{"user", "assistant", "system", "observation", "fact", "tool"}
		for _, e := range ep.Events {
			original[e.SourceID] = true
			original[e.Session] = true
			if !slices.Contains(roles, e.Owner) && e.Owner != s.Protocol.SpeakerMapping["owner"] {
				original[e.Owner] = true
			}
		}
		for _, c := range ep.Cases {
			original[c.ID] = true
		}
		delete(original, "")
		originals := newIdentifierMatcher(original)
		// Approved P4b: verified identity DTO paths cannot leak short identifiers
		// merely because an HMAC UUID happens to contain their characters.
		verifiedIdentities := map[string]string{}
		var walk func(reflect.Value, string, bool, map[string]int) error
		walk = func(v reflect.Value, path string, values bool, sequences map[string]int) error {
			for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
				if v.IsNil() {
					return nil
				}
				v = v.Elem()
			}
			if !v.IsValid() {
				return nil
			}
			if v.Type() == reflect.TypeFor[json.RawMessage]() {
				var x any
				if err := json.Unmarshal(v.Bytes(), &x); err != nil {
					return err
				}
				return walk(reflect.ValueOf(x), path, values, sequences)
			}
			check := func(key string, x reflect.Value) error {
				p := path + "." + key
				allowed := false
				if n, ok := sequences[p]; ok {
					u := x
					for u.IsValid() && u.Kind() == reflect.Interface {
						u = u.Elem()
					}
					allowed = u.IsValid() && ((u.Kind() == reflect.Int && u.Int() == int64(n)) || (u.Kind() == reflect.Float64 && u.Float() == float64(n)))
				}
				if gold[key] && !allowed {
					r.Problems = append(r.Problems, p+": gold key")
				}
				// Only a PublicEvent's own content field is exempt, not nested attributes.
				if values && key == "content" && strings.Contains(path, ".events[") && !strings.Contains(path, ".attributes") {
					return nil
				}
				return walk(x, p, values, sequences)
			}
			switch v.Kind() {
			case reflect.Struct:
				t := v.Type()
				for i := 0; i < v.NumField(); i++ {
					f := t.Field(i)
					if !f.IsExported() {
						continue
					}
					tag := strings.Split(f.Tag.Get("json"), ",")
					key := tag[0]
					if key == "-" {
						continue
					}
					if key == "" {
						key = f.Name
					}
					x := v.Field(i)
					if len(tag) > 1 && tag[1] == "omitempty" && jsonEmpty(x) {
						continue
					}
					if err := check(key, x); err != nil {
						return err
					}
				}
			case reflect.Map:
				keys := v.MapKeys()
				slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
				for _, k := range keys {
					if err := check(k.String(), v.MapIndex(k)); err != nil {
						return err
					}
				}
			case reflect.Slice, reflect.Array:
				for i := 0; i < v.Len(); i++ {
					if err := walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i), values, sequences); err != nil {
						return err
					}
				}
			case reflect.String:
				if values && v.Type() != reflect.TypeFor[json.Number]() {
					if expected, ok := verifiedIdentities[path]; ok && v.String() == expected {
						return nil
					}
					if originals.contains(v.String()) {
						r.Problems = append(r.Problems, path+": original identifier in public value")
					}
				}
			}
			return nil
		}
		for i, e := range ep.Events {
			if err := walk(reflect.ValueOf(e.Attributes), fmt.Sprintf("episodes[%d].events[%d].attributes", ei, i), false, nil); err != nil {
				return err
			}
		}
		for ci, c := range ep.Cases {
			if c.Cutoff < 0 || c.Cutoff > len(ep.Events) {
				return fmt.Errorf("leak scan requires a valid cutoff")
			}
			id, err := identity.New(strings.Repeat("42", 32), "leak-scan")
			if err != nil {
				return err
			}
			events := RenderPublicEvents(ep.Events[:c.Cutoff], id, s.Protocol.SpeakerMapping["ingest_render"])
			request := PublicRequest(c, id, protocol.Budget{})
			path := fmt.Sprintf("episodes[%d].cases[%d]", ei, ci)
			sequences := map[string]int{}
			verifiedIdentities = map[string]string{}
			for i, event := range events {
				sequences[fmt.Sprintf("%s.events[%d].sequence", path, i)] = i
				if event.ID != id.Anon("event", ep.Events[i].SourceID) || event.SessionID != id.Anon("session", ep.Events[i].Session) || event.Owner != id.Anon("owner", ep.Events[i].Owner) {
					r.Problems = append(r.Problems, path+": owner identity not anonymized")
				}
				verifiedIdentities[fmt.Sprintf("%s.events[%d].id", path, i)] = id.Anon("event", ep.Events[i].SourceID)
				verifiedIdentities[fmt.Sprintf("%s.events[%d].session_id", path, i)] = id.Anon("session", ep.Events[i].Session)
				verifiedIdentities[fmt.Sprintf("%s.events[%d].owner", path, i)] = id.Anon("owner", ep.Events[i].Owner)
				for _, key := range sortedKeys(event.Attributes) {
					if !slices.Contains(AttributeNames, key) {
						r.Problems = append(r.Problems, path+": attribute outside whitelist: "+key)
					}
				}
			}
			if request.User != id.Anon("owner", c.User) {
				r.Problems = append(r.Problems, path+": request user not anonymized")
			}
			verifiedIdentities[path+".request.user"] = id.Anon("owner", c.User)
			verifiedIdentities[path+".request.query_invocation_id"] = id.Anon("invocation", "")
			if request.QueryInvocationID != id.Anon("invocation", "") {
				r.Problems = append(r.Problems, path+": invocation identity not anonymized")
			}
			if err := walk(reflect.ValueOf(events), path+".events", true, sequences); err != nil {
				return err
			}
			if err := walk(reflect.ValueOf(request), path+".request", true, sequences); err != nil {
				return err
			}
			r.Checked++
		}
		ei++
		return nil
	})
	return r, err
}
func jsonEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}

func asciiDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
