package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	mab "memeval/internal/benchmark/mab"
	"memeval/internal/jsonstrict"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

var v3Names = map[reflect.Type][]string{
	reflect.TypeFor[Suite]():         {"protocol_ref", "license", "data_nature", "taxonomy", "splits"},
	reflect.TypeFor[SuiteProtocol](): {"reset_policy_allowed", "time_exposure", "inject_current_date", "speaker_mapping", "text_only", "haystack"},
	reflect.TypeFor[Event]():         {"turn_group", "modality", "attachments"},
	reflect.TypeFor[Case]():          {"query_point", "session_checkpoint", "inject_current_date", "difficulty"},
	reflect.TypeFor[Gold]():          {"evidence_refs", "abstain_reference", "mcq", "rubric", "phrase_set", "sequence", "reference", "memory_points", "item_ids", "checker", "disputed"},
}

// Retain explicit empty/null v3 fields for the v2 anti-smuggling check. These
// private presence bits never enter normalized bytes or suite hashes.
func (s *Suite) UnmarshalJSON(data []byte) error {
	type plain Suite
	if err := json.Unmarshal(data, (*plain)(s)); err != nil {
		return err
	}
	s.v3Fields = nil
	var walk func(json.RawMessage, reflect.Type, string) error
	walk = func(raw json.RawMessage, t reflect.Type, path string) error {
		if t == reflect.TypeFor[json.RawMessage]() {
			return nil
		}
		if t.Kind() == reflect.Slice {
			var rows []json.RawMessage
			if err := json.Unmarshal(raw, &rows); err != nil {
				return err
			}
			for i, row := range rows {
				if err := walk(row, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
			return nil
		}
		if t.Kind() != reflect.Struct {
			return nil
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			v, ok := m[name]
			if !ok {
				continue
			}
			p := name
			if path != "" {
				p = path + "." + name
			}
			if slices.Contains(v3Names[t], name) {
				s.v3Fields = append(s.v3Fields, p)
			}
			if err := walk(v, f.Type, p); err != nil {
				return err
			}
		}
		return nil
	}
	if s.SchemaVersion == "2.0" {
		return walk(data, reflect.TypeFor[Suite](), "")
	}
	return nil
}
func validateVersionFields(s *Suite) error {
	if s.SchemaVersion != "2.0" {
		return nil
	}
	if len(s.v3Fields) > 0 {
		return fmt.Errorf("field %s requires schema_version 3.0", s.v3Fields[0])
	}
	var walk func(reflect.Value, string) error
	walk = func(v reflect.Value, path string) error {
		if v.Kind() == reflect.Slice {
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
			return nil
		}
		if v.Kind() != reflect.Struct {
			return nil
		}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			p := name
			if path != "" {
				p = path + "." + name
			}
			if slices.Contains(v3Names[t], name) && !v.Field(i).IsZero() {
				return fmt.Errorf("field %s requires schema_version 3.0", p)
			}
			if err := walk(v.Field(i), p); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(*s), "")
}
func NewKind(kind string) bool {
	return slices.Contains([]string{"rubric", "set_match", "ordering", "summary", "memory_audit", "recsys", "agentic"}, kind)
}
func validIngestion(value string) bool {
	if slices.Contains([]string{"per_document", "per_trajectory", "per_file"}, value) {
		return true
	}
	parts := strings.Split(value, ":")
	if len(parts) == 2 && parts[0] == "per_turn_batch" || len(parts) == 4 && parts[0] == "per_chunk" && parts[2] != "" && parts[3] != "" {
		n, err := strconv.Atoi(parts[1])
		return err == nil && n > 0
	}
	return false
}
func validateV3(s *Suite, counts *Counts) error {
	render := s.Protocol.SpeakerMapping["ingest_render"]
	rest := strings.NewReplacer("{speaker}", "", "{content}", "").Replace(render)
	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("ingest_render only supports {speaker} and {content}")
	}

	enum := func(name, value string, allowed ...string) error {
		if value != "" && !slices.Contains(allowed, value) {
			return fmt.Errorf("unknown %s %q", name, value)
		}
		return nil
	}
	if err := enum("data_nature", s.DataNature, "real-user-logs", "real-derived", "human-authored", "model-generated", "structured-diagnostic", "agent-trajectory"); err != nil {
		return err
	}
	if err := enum("time_exposure", s.Protocol.TimeExposure, "none", "session", "turn"); err != nil {
		return err
	}
	for _, v := range s.Protocol.ResetPolicyAllowed {
		if v == "" {
			return fmt.Errorf("empty reset_policy_allowed")
		}
		if err := enum("reset_policy_allowed", v, "fresh_per_case", "incremental_per_episode", "per_cluster", "shared_haystack"); err != nil {
			return err
		}
	}
	ids := map[string]bool{}
	err := s.Iter(context.Background(), func(ep *Episode) error {
		for _, e := range ep.Events {
			if render == mab.MemorizeID {
				source, _ := e.Attributes["mab_source"].(string)
				if _, err := mab.For(source); err != nil {
					return err
				}
			}
			if err := enum("modality", e.Modality, "text", "image", "screenshot", "a11y_tree", "tool_result"); err != nil {
				return err
			}
		}
		for _, c := range ep.Cases {
			ids[c.ID] = true
			switch c.QueryPoint {
			case "", "end_of_history":
				if c.Cutoff != len(ep.Events) {
					return fmt.Errorf("case %s: end_of_history requires cutoff == len(events)", c.ID)
				}
			case "at_index":
			case "after_session_k":
				last := -1
				for i, e := range ep.Events {
					if c.SessionCheckpoint != "" && e.Session == c.SessionCheckpoint {
						last = i
					}
				}
				if last < 0 || c.Cutoff != last+1 {
					return fmt.Errorf("case %s: after_session_k requires cutoff at session_checkpoint last event + 1", c.ID)
				}
			default:
				return fmt.Errorf("unknown query_point %q", c.QueryPoint)
			}
			g := c.Gold
			if g.EvidenceRefs != nil && (g.EvidenceIDs != nil || g.EvidenceSessionIDs != nil) {
				return fmt.Errorf("evidence_refs and flat evidence are mutually exclusive")
			}
			events, sessions := map[string]bool{}, map[string]bool{}
			for _, e := range ep.Events[:c.Cutoff] {
				events[e.SourceID] = true
				if e.Session != "" {
					sessions[e.Session] = true
				}
			}
			for _, ref := range g.EvidenceRefs {
				switch ref.Kind {
				case "event":
					if !events[ref.Ref] {
						return fmt.Errorf("missing/future evidence_refs event")
					}
				case "session":
					if !sessions[ref.Ref] {
						return fmt.Errorf("missing/future evidence_refs session")
					}
				case "text", "trajectory":
					counts.UnresolvedEvidenceRefs++
				default:
					return fmt.Errorf("unknown evidence_refs kind %q", ref.Kind)
				}
			}
			valid := true
			switch c.Kind {
			case "qa":
				valid = len(g.Answers) > 0 || g.Abstain
			case "mcq":
				valid = validChoice(c)
			case "retrieval":
				valid = g.EvidenceIDs != nil || g.EvidenceSessionIDs != nil || g.EvidenceRefs != nil
			case "typed_context":
				valid = len(g.Expect) > 0 && !bytes.Equal(bytes.TrimSpace(g.Expect), []byte("null"))
			case "rubric":
				valid = g.Rubric != nil && len(g.Rubric.Items) > 0
			case "set_match":
				valid = g.PhraseSet != nil && len(g.PhraseSet.Items) > 0
			case "ordering":
				valid = len(g.Sequence) >= 2
			case "summary":
				valid = g.Reference != ""
			case "memory_audit":
				valid = g.MemoryPoints != nil && c.QueryPoint == "after_session_k"
			case "recsys":
				valid = len(g.ItemIDs) > 0
			case "agentic":
				valid = g.Checker != nil
			}
			if !valid {
				return fmt.Errorf("case %s: %s requires its minimum gold fields", c.ID, c.Kind)
			}
			if g.MCQ != nil {
				if g.MCQ.Order != nil {
					if len(g.MCQ.Order) != len(c.Options) {
						return fmt.Errorf("mcq.order must cover every option")
					}
					seen := map[int]bool{}
					for _, v := range g.MCQ.Order {
						if v < 0 || v >= len(c.Options) || seen[v] {
							return fmt.Errorf("mcq.order must be a permutation")
						}
						seen[v] = true
					}
				}

				if err := enum("mcq.parse", g.MCQ.Parse, "exact_option", "boxed", "letter", "personamem_final_answer", "prefeval_choice_tag"); err != nil {
					return err
				}
			}
			if g.Rubric != nil {
				if err := enum("rubric.aggregate", g.Rubric.Aggregate, "mean", "majority", "formula", "fama", "prefeval", "convomem_rubric"); err != nil {
					return err
				}
				for _, item := range g.Rubric.Items {
					if g.Rubric.Aggregate == "fama" && (!slices.Contains([]string{"memory_presence", "forgetting_absence"}, item.Group) || item.Polarity == "as_written" || item.Weight != 1) {
						return fmt.Errorf("fama requires presence/absence group, binary polarity and unit weight")
					}
					if !slices.Contains([]string{"must_contain", "must_not_contain", "as_written"}, item.Polarity) {
						return fmt.Errorf("unknown rubric polarity")
					}
				}
			}
			for _, point := range g.MemoryPoints {
				if !slices.Contains([]string{"should_exist", "should_not_exist"}, point.Polarity) {
					return fmt.Errorf("unknown memory point polarity")
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, name := range sortedKeys(s.Splits) {
		for _, id := range s.Splits[name] {
			if !ids[id] || used[id] {
				return fmt.Errorf("split %s: unknown or repeated case id %s", name, id)
			}
			used[id] = true
		}
	}
	return nil
}
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
func validChoice(c Case) bool {
	if len(c.Options) == 0 {
		return false
	}
	choice := c.Gold.Choice
	if c.Gold.MCQ != nil {
		switch c.Gold.MCQ.Parse {
		case "boxed":
			if !strings.HasPrefix(choice, `\boxed{`) || !strings.HasSuffix(choice, "}") {
				return false
			}
			choice = strings.TrimSuffix(strings.TrimPrefix(choice, `\boxed{`), "}")
		case "letter":
			choice = strings.TrimSpace(choice)
			if len(choice) != 1 {
				return false
			}
		}
	}
	_, ok := c.Options[choice]
	return ok
}

// UpgradeBytes preserves v2 source bytes except the version value; validation
// runs on a separate decoded object so its legacy defaults cannot rewrite input.
func UpgradeBytes(data []byte) ([]byte, error) {
	var header struct {
		Version string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	if header.Version == "1.0" {
		s, err := UpgradeV1(data)
		if err != nil {
			return nil, err
		}
		data, err = json.Marshal(s)
		if err != nil {
			return nil, err
		}
		header.Version = "2.0"
	}
	if header.Version != "2.0" && header.Version != "3.0" {
		return nil, fmt.Errorf("unsupported schema_version")
	}
	out := bytes.Clone(data)
	if header.Version == "2.0" {
		d := json.NewDecoder(bytes.NewReader(data))
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			var raw json.RawMessage
			if err = d.Decode(&raw); err != nil {
				return nil, err
			}
			if key == "schema_version" {
				end := int(d.InputOffset())
				start := end - len(raw)
				out = append(append(append([]byte{}, data[:start]...), []byte(`"3.0"`)...), data[end:]...)
				break
			}
		}
	}
	var s Suite
	if err := jsonstrict.Decode(out, &s); err != nil {
		return nil, err
	}
	if _, err := Validate(context.Background(), &s, ValidateOptions{SkipLeakScan: true}); err != nil {
		return nil, err
	}
	return out, nil
}
func Upgrade(data []byte) (*Suite, error) {
	b, err := UpgradeBytes(data)
	if err != nil {
		return nil, err
	}
	var s Suite
	err = jsonstrict.Decode(b, &s)
	return &s, err
}
