package suite

import "encoding/json"

type License struct {
	ID                     string `json:"id"`
	Noncommercial          bool   `json:"noncommercial"`
	NoDerivatives          bool   `json:"no_derivatives"`
	ShareAlike             bool   `json:"share_alike"`
	RedistributeNormalized bool   `json:"redistribute_normalized"`
	Source                 string `json:"source"`
}
type Taxonomy struct {
	OriginalField string            `json:"original_field"`
	Labels        []string          `json:"labels"`
	CanonicalMap  map[string]string `json:"canonical_map,omitempty"`
}
type Haystack struct {
	FillerSource  string `json:"filler_source,omitempty"`
	ContextLadder []int  `json:"context_ladder,omitempty"`
}
type SuiteProtocol struct {
	Ingestion          string            `json:"ingestion"`
	QueryTimePolicy    string            `json:"query_time_policy,omitempty" jsonschema:"enum=,enum=reference_only,enum=as_of"`
	RetrievalMetrics   []string          `json:"retrieval_metrics,omitempty"`
	ResetPolicyAllowed []string          `json:"reset_policy_allowed,omitempty" jsonschema:"enum=fresh_per_case,enum=incremental_per_episode,enum=per_cluster,enum=shared_haystack"`
	TimeExposure       string            `json:"time_exposure,omitempty" jsonschema:"enum=,enum=none,enum=session,enum=turn"`
	InjectCurrentDate  *bool             `json:"inject_current_date,omitempty"`
	SpeakerMapping     map[string]string `json:"speaker_mapping,omitempty"`
	TextOnly           *bool             `json:"text_only,omitempty"`
	Haystack           *Haystack         `json:"haystack,omitempty"`
}
type Attachment struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Caption string `json:"caption,omitempty"`
}
type EvidenceRef struct {
	Kind string `json:"kind" jsonschema:"enum=event,enum=session,enum=text,enum=trajectory"`
	Ref  string `json:"ref"`
}
type MCQSpec struct {
	Seed    *int   `json:"seed,omitempty"`
	Order   []int  `json:"order,omitempty"`
	Shuffle bool   `json:"shuffle"`
	Parse   string `json:"parse" jsonschema:"enum=exact_option,enum=boxed,enum=letter,enum=personamem_final_answer,enum=prefeval_choice_tag"`
}
type Rubric struct {
	Items     []RubricItem `json:"items"`
	Aggregate string       `json:"aggregate" jsonschema:"enum=mean,enum=majority,enum=formula,enum=fama,enum=prefeval,enum=convomem_rubric"`
	Lambda    *float64     `json:"lambda,omitempty"`
}
type RubricItem struct {
	Group    string  `json:"group,omitempty"`
	Text     string  `json:"text"`
	Polarity string  `json:"polarity" jsonschema:"enum=must_contain,enum=must_not_contain,enum=as_written"`
	Weight   float64 `json:"weight"`
}
type PhraseSet struct {
	Items   []string   `json:"items"`
	Ordered bool       `json:"ordered"`
	Norm    PhraseNorm `json:"norm"`
}
type PhraseNorm struct {
	Lower      bool     `json:"lower"`
	StripPunct bool     `json:"strip_punct"`
	Separators []string `json:"separators,omitempty"`
}
type MemoryPoint struct {
	IsUpdate   bool     `json:"is_update,omitempty"`
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Type       string   `json:"type"`
	Polarity   string   `json:"polarity" jsonschema:"enum=should_exist,enum=should_not_exist"`
	Importance string   `json:"importance"`
	UpdateOf   []string `json:"update_of,omitempty"`
}
type Checker struct {
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}
type Disputed struct {
	Note       string   `json:"note"`
	AltAnswers []string `json:"alt_answers,omitempty"`
}
