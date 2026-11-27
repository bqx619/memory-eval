// Package protocol contains only public wire types. Gold never crosses this boundary.
package protocol

import "encoding/json"

type PublicEvent struct {
	ID         string         `json:"id"`
	Owner      string         `json:"owner"`
	Sequence   int            `json:"sequence"`
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Timestamp  string         `json:"timestamp,omitempty"`
	TimeRaw    string         `json:"time_raw,omitempty"`
	SessionID  string         `json:"session_id,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

type Budget struct {
	TopK            int `json:"top_k"`
	MaxContextChars int `json:"max_context_chars"`
	MaxBeliefs      int `json:"maxBeliefs,omitempty"`
	// Presence distinguishes an explicit zero budget from the baseline default 20.
	HasMaxBeliefs bool `json:"-"`
}

func (b Budget) MarshalJSON() ([]byte, error) {
	m := map[string]int{"top_k": b.TopK, "max_context_chars": b.MaxContextChars}
	if b.HasMaxBeliefs || b.MaxBeliefs != 0 {
		m["maxBeliefs"] = b.MaxBeliefs
	}
	return json.Marshal(m)
}

func (b *Budget) UnmarshalJSON(data []byte) error {
	type plain Budget
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*b = Budget(p)
	_, b.HasMaxBeliefs = m["maxBeliefs"]
	return nil
}

type RetrieveRequest struct {
	Query             string            `json:"query"`
	User              string            `json:"user"`
	Scope             []string          `json:"scope"`
	AsOf              string            `json:"as_of,omitempty"`
	Budget            Budget            `json:"budget"`
	Options           map[string]string `json:"options,omitempty"`
	QueryInvocationID string            `json:"query_invocation_id"`
	OutputFormat      string            `json:"output_format"`
}

type Item struct {
	Timestamp string   `json:"timestamp,omitempty"`
	Status    string   `json:"status,omitempty"`
	ID        string   `json:"id,omitempty"`
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_ids"`
	Score     *float64 `json:"score"`
}

// Reject a missing/null text at the transport boundary; an empty string is valid.
func (i *Item) UnmarshalJSON(data []byte) error {
	type plain Item
	var wire struct {
		*plain
		Text *string `json:"text"`
	}
	wire.plain = (*plain)(i)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Text == nil {
		return &json.UnmarshalTypeError{Value: "missing or null text", Type: stringType}
	}
	i.Text = *wire.Text
	return nil
}

type Tiers struct {
	PinnedFacts     []Item `json:"pinnedFacts"`
	RelevantBeliefs []Item `json:"relevantBeliefs"`
	OpenQuestions   []Item `json:"openQuestions"`
}
type RetrieveResponse struct {
	Items          []Item  `json:"items,omitempty"`
	Tiers          *Tiers  `json:"tiers,omitempty"`
	PersonaPrelude *string `json:"personaPrelude,omitempty"`
}
type AnswerResponse struct {
	Text    string `json:"text,omitempty"`
	Abstain *bool  `json:"abstain,omitempty"`
	Choice  string `json:"choice,omitempty"`
}
type IngestHandle struct {
	BatchSeq int `json:"batch_seq"`
	Accepted int `json:"accepted"`
	Filtered int `json:"filtered"`
	Failed   int `json:"failed"`
}
type Stage struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Warnings []string `json:"warnings,omitempty"`
}
type ReadyResult struct {
	Visible  bool     `json:"visible"`
	Stages   []Stage  `json:"stages"`
	Warnings []string `json:"warnings"`
}
type Describe struct {
	Name            string         `json:"name"`
	Version         string         `json:"version"`
	Capabilities    []string       `json:"capabilities"`
	Limitations     []string       `json:"limitations"`
	ReadOnlyQueries bool           `json:"read_only_queries"`
	Config          map[string]any `json:"config"`
}
