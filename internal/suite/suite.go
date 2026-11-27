package suite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"memeval/internal/jsonstrict"
	"memeval/internal/protocol"
	"os"
	"time"
)

type Suite struct {
	SchemaVersion string              `json:"schema_version" jsonschema:"enum=3.0"`
	SuiteID       string              `json:"suite_id"`
	Track         string              `json:"track"`
	Provenance    map[string]any      `json:"provenance"`
	Limitations   []string            `json:"limitations"`
	Protocol      SuiteProtocol       `json:"protocol"`
	ProtocolRef   string              `json:"protocol_ref,omitempty"`
	License       *License            `json:"license,omitempty"`
	DataNature    string              `json:"data_nature,omitempty" jsonschema:"enum=,enum=real-user-logs,enum=real-derived,enum=human-authored,enum=model-generated,enum=structured-diagnostic,enum=agent-trajectory"`
	Taxonomy      *Taxonomy           `json:"taxonomy,omitempty"`
	Splits        map[string][]string `json:"splits,omitempty"`
	v3Fields      []string
	bundle        *bundleData
	episodeIDs    map[string]int

	Episodes []Episode `json:"episodes"`
}
type Episode struct {
	ID        string  `json:"id"`
	ClusterID string  `json:"cluster_id"`
	Events    []Event `json:"events"`
	Cases     []Case  `json:"cases"`
}
type Event struct {
	SourceID    string         `json:"source_id"`
	Owner       string         `json:"owner"`
	Content     string         `json:"content"`
	Role        string         `json:"role" jsonschema:"enum=user,enum=assistant,enum=system,enum=observation,enum=fact,enum=tool"`
	Session     string         `json:"session,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
	TimeRaw     string         `json:"time_raw,omitempty"`
	TurnGroup   string         `json:"turn_group,omitempty"`
	Modality    string         `json:"modality,omitempty" jsonschema:"enum=,enum=text,enum=image,enum=screenshot,enum=a11y_tree,enum=tool_result"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Attributes  map[string]any `json:"attributes"`
}
type Budget struct {
	TopK            *int `json:"top_k,omitempty"`
	MaxContextChars *int `json:"max_context_chars,omitempty"`
	MaxBeliefs      *int `json:"maxBeliefs,omitempty"`
}

func (b Budget) Resolve(base protocol.Budget) protocol.Budget {
	if b.TopK != nil {
		base.TopK = *b.TopK
	}
	if b.MaxContextChars != nil {
		base.MaxContextChars = *b.MaxContextChars
	}
	if b.MaxBeliefs != nil {
		base.MaxBeliefs = *b.MaxBeliefs
		base.HasMaxBeliefs = true
	}
	return base
}

type Case struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind" jsonschema:"enum=typed_context,enum=qa,enum=mcq,enum=retrieval,enum=rubric,enum=set_match,enum=ordering,enum=summary,enum=memory_audit,enum=recsys,enum=agentic"`
	Query             string            `json:"query"`
	User              string            `json:"user,omitempty"`
	Scope             []string          `json:"scope"`
	Category          string            `json:"category,omitempty"`
	ClusterID         string            `json:"cluster_id"`
	QueryTime         string            `json:"query_time,omitempty"`
	QuestionType      string            `json:"question_type,omitempty"`
	Budget            Budget            `json:"budget"`
	Options           map[string]string `json:"options,omitempty"`
	Cutoff            int               `json:"cutoff"`
	Attributes        map[string]any    `json:"attributes,omitempty"`
	QueryPoint        string            `json:"query_point,omitempty" jsonschema:"enum=,enum=end_of_history,enum=at_index,enum=after_session_k"`
	SessionCheckpoint string            `json:"session_checkpoint,omitempty"`
	InjectCurrentDate *bool             `json:"inject_current_date,omitempty"`
	Difficulty        string            `json:"difficulty,omitempty"`
	Gold              Gold              `json:"gold"`
}
type Gold struct {
	EvidenceRefs       []EvidenceRef   `json:"evidence_refs,omitempty"`
	AbstainReference   string          `json:"abstain_reference,omitempty"`
	MCQ                *MCQSpec        `json:"mcq,omitempty"`
	Rubric             *Rubric         `json:"rubric,omitempty"`
	PhraseSet          *PhraseSet      `json:"phrase_set,omitempty"`
	Sequence           []string        `json:"sequence,omitempty"`
	Reference          string          `json:"reference,omitempty"`
	MemoryPoints       []MemoryPoint   `json:"memory_points,omitempty"`
	ItemIDs            []string        `json:"item_ids,omitempty"`
	Checker            *Checker        `json:"checker,omitempty"`
	Disputed           *Disputed       `json:"disputed,omitempty"`
	Answers            []string        `json:"answers,omitempty"`
	Abstain            bool            `json:"abstain,omitempty"`
	EvidenceSessionIDs []string        `json:"evidence_session_ids,omitempty"`
	EvidenceIDs        []string        `json:"evidence_ids"`
	EvidenceUnit       string          `json:"evidence_unit,omitempty"`
	Exhaustive         bool            `json:"exhaustive,omitempty"`
	Choice             string          `json:"choice,omitempty"`
	Expect             json.RawMessage `json:"expect,omitempty"`
}
type Counts struct {
	ByKind                 map[string]int `json:"by_kind"`
	UnresolvedEvidenceRefs int            `json:"unresolved_evidence_refs"`
	Episodes               int            `json:"episodes"`
	Events                 int            `json:"events"`
	Cases                  int            `json:"cases"`
}

func required(data []byte, keys ...string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for _, k := range keys {
		if v, ok := m[k]; !ok || bytes.Equal(v, []byte("null")) {
			return fmt.Errorf("missing required field %s", k)
		}
	}
	return nil
}
func (e *Event) UnmarshalJSON(data []byte) error {
	if err := required(data, "source_id", "owner", "content"); err != nil {
		return err
	}
	type plain Event
	return json.Unmarshal(data, (*plain)(e))
}
func (c *Case) UnmarshalJSON(data []byte) error {
	if err := required(data, "id", "query", "cutoff", "gold"); err != nil {
		return err
	}
	type plain Case
	return json.Unmarshal(data, (*plain)(c))
}
func validateSingle(s *Suite) (Counts, error) {
	counts := Counts{ByKind: map[string]int{}}
	bad := func(msg string) (Counts, error) { return Counts{}, fmt.Errorf("invalid suite: %s", msg) }
	if s.SchemaVersion != "2.0" && s.SchemaVersion != "3.0" {
		return bad("unsupported schema_version")
	}
	if err := validateVersionFields(s); err != nil {
		return Counts{}, err
	}
	if s.SuiteID == "" || s.Provenance == nil {
		return bad("suite_id and provenance required")
	}
	if u, ok := s.Provenance["source_url"].(string); !ok || u == "" {
		return bad("source_url required")
	}
	if len(s.Index()) == 0 {
		return bad("episodes must be nonempty")
	}
	if s.Protocol.Ingestion == "" {
		s.Protocol.Ingestion = "per_event"
	}
	if s.Protocol.Ingestion != "per_event" && s.Protocol.Ingestion != "per_session" && !(s.SchemaVersion == "3.0" && validIngestion(s.Protocol.Ingestion)) {
		return bad("unknown ingestion")
	}
	if s.Protocol.QueryTimePolicy != "" && s.Protocol.QueryTimePolicy != "reference_only" && !(s.SchemaVersion == "3.0" && s.Protocol.QueryTimePolicy == "as_of") {
		return bad("unknown query_time_policy")
	}
	for _, metric := range s.Protocol.RetrievalMetrics {
		if metric != "longmemeval_official" {
			return bad("unknown retrieval_metrics")
		}
	}
	if s.Limitations == nil {
		s.Limitations = []string{}
	}
	eps, cases := map[string]bool{}, map[string]bool{}
	err := s.Iter(context.Background(), func(e *Episode) error {
		if e.ID == "" || eps[e.ID] {
			return fmt.Errorf("invalid suite: %s", "empty/duplicate episode id")
		}
		eps[e.ID] = true
		if e.ClusterID == "" {
			e.ClusterID = e.ID
		}
		ids := map[string]bool{}
		for j := range e.Events {
			v := &e.Events[j]
			if v.SourceID == "" || ids[v.SourceID] {
				return fmt.Errorf("invalid suite: %s", "empty/duplicate event id")
			}
			ids[v.SourceID] = true
			if v.Owner == "" {
				return fmt.Errorf("invalid suite: %s", "event owner required")
			}
			if v.Role == "" {
				v.Role = "observation"
			}
			switch v.Role {
			case "user", "assistant", "system", "observation", "fact":
			case "tool":
				if s.SchemaVersion != "3.0" {
					return fmt.Errorf("invalid suite: %s", "unknown event role")
				}
			default:
				return fmt.Errorf("invalid suite: %s", "unknown event role")
			}
			if v.Timestamp != "" {
				if _, err := time.Parse(time.RFC3339Nano, v.Timestamp); err != nil {
					return fmt.Errorf("invalid suite: %s", "timestamp must be RFC3339")
				}
			}
			if v.Attributes == nil {
				v.Attributes = map[string]any{}
			}
		}
		for j := range e.Cases {
			c := &e.Cases[j]
			if c.ID == "" || cases[c.ID] {
				return fmt.Errorf("invalid suite: %s", "empty/duplicate case id")
			}
			cases[c.ID] = true
			if c.ClusterID == "" {
				c.ClusterID = e.ClusterID
			}
			if c.Scope == nil {
				c.Scope = []string{}
			}
			if c.Cutoff < 0 || c.Cutoff > len(e.Events) {
				return fmt.Errorf("invalid suite: %s", "cutoff outside exclusive prefix")
			}
			switch c.Kind {
			case "typed_context", "qa", "mcq", "retrieval":
			default:
				if s.SchemaVersion != "3.0" || !NewKind(c.Kind) {
					return fmt.Errorf("invalid suite: %s", "unknown case kind")
				}
			}
			if s.SchemaVersion == "2.0" && c.Kind == "retrieval" && c.Gold.EvidenceIDs == nil {
				return fmt.Errorf("invalid suite: %s", "retrieval requires evidence_ids array")
			}
			if s.SchemaVersion == "2.0" && c.Kind == "mcq" {
				if _, ok := c.Options[c.Gold.Choice]; !ok || len(c.Options) == 0 {
					return fmt.Errorf("invalid suite: %s", "MCQ choice must resolve to options")
				}
			}
			if c.QueryTime != "" {
				if _, err := time.Parse(time.RFC3339Nano, c.QueryTime); err != nil {
					return fmt.Errorf("invalid suite: %s", "query_time must be RFC3339")
				}
			}
			for _, v := range []*int{c.Budget.TopK, c.Budget.MaxContextChars, c.Budget.MaxBeliefs} {
				if v != nil && *v < 0 {
					return fmt.Errorf("invalid suite: %s", "negative case budget")
				}
			}
			if u := c.Gold.EvidenceUnit; u != "" && u != "event" && u != "session" {
				return fmt.Errorf("invalid suite: %s", "unknown evidence_unit")
			}
			valid := map[string]bool{}
			sessions := map[string]bool{}
			for _, v := range e.Events[:c.Cutoff] {
				key := v.SourceID
				sessions[v.Session] = v.Session != ""
				if c.Gold.EvidenceUnit == "session" && c.Gold.EvidenceSessionIDs == nil {
					key = v.Session
				}
				valid[key] = true
			}
			for _, id := range c.Gold.EvidenceSessionIDs {
				if !sessions[id] {
					return fmt.Errorf("invalid suite: %s", "missing/future gold session")
				}
			}
			counts.ByKind[c.Kind]++
			for _, id := range c.Gold.EvidenceIDs {
				if !valid[id] {
					return fmt.Errorf("invalid suite: %s", "missing/future gold evidence")
				}
			}
		}
		counts.Events += len(e.Events)
		counts.Cases += len(e.Cases)
		return nil
	})
	if err != nil {
		return Counts{}, err
	}
	if s.SchemaVersion == "3.0" {
		if err := validateV3(s, &counts); err != nil {
			return Counts{}, fmt.Errorf("invalid suite: %w", err)
		}
	}
	counts.Episodes = len(eps)
	return counts, nil
}

func loadSingle(ctx context.Context, path string) (*Suite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var version struct {
		Version string `json:"schema_version"`
	}
	if err = json.Unmarshal(data, &version); err != nil {
		return nil, err
	}
	if version.Version == "1.0" {
		return UpgradeV1(data)
	}
	var s Suite
	if version.Version == "3.0" {
		err = jsonstrict.Decode(data, &s)
	} else {
		err = json.Unmarshal(data, &s)
	}
	if err != nil {
		return nil, err
	}
	_, err = validateSingle(&s)
	return &s, err
}
func UpgradeV1(data []byte) (*Suite, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m["schema_version"] != "1.0" {
		return nil, fmt.Errorf("UpgradeV1 requires schema_version 1.0")
	}
	m["schema_version"] = "2.0"
	eps, _ := m["episodes"].([]any)
	for _, ep := range eps {
		e, ok := ep.(map[string]any)
		if !ok {
			continue
		}
		events, _ := e["events"].([]any)
		for _, v := range events {
			ev, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := ev["time"].(string); ok && s != "" {
				if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
					ev["timestamp"] = s
				} else {
					ev["time_raw"] = s
				}
			}
			delete(ev, "time")
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var s Suite
	if err = json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	_, err = validateSingle(&s)
	return &s, err
}

// CanonicalJSON sorts every object, including structs, without HTML escaping.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = d.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if err = e.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
func Digest(v any) (string, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func SHA256(s *Suite) (string, error) {
	if s.bundle != nil {
		return s.bundle.digest(s)
	}
	if _, err := validateSingle(s); err != nil {
		return "", err
	}
	return Digest(s)
}
