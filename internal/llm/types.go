// Package llm is the evaluator's own client. It is never a SUT proxy.
package llm

import (
	"context"
	"encoding/json"
	"memeval/internal/config/redact"
	"time"
)

const ClientVersion = "memeval-llm-v1"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	legacyMaxTokens bool           // Negotiated transport detail; logical cassette key stays stable.
	Operation       *Operation     `json:"-"`
	Purpose         string         `json:"purpose"`
	Provider        string         `json:"provider"`
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Temperature     *float64       `json:"temperature"`
	MaxTokens       *int           `json:"max_tokens"`
	N               int            `json:"n"`
	Extra           map[string]any `json:"extra,omitempty"`
	SampleID        string         `json:"sample_id"`
	TemplateID      string         `json:"template_id"`
}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"` // Subset of completion tokens, not an extra charge.
}
type Response struct {
	retryAfter        string
	retryTransport    bool
	HTTPStatus        int                `json:"http_status,omitempty"`
	Raw               json.RawMessage    `json:"raw,omitempty"`
	Decision          *DecisionTelemetry `json:"decision,omitempty"`
	Text              string             `json:"text"`
	FinishReason      string             `json:"finish_reason"`
	Usage             Usage              `json:"usage"`
	SystemFingerprint string             `json:"system_fingerprint"`
	ModelReturned     string             `json:"model_returned"`
	LatencyMS         float64            `json:"latency_ms"`
	CassetteHit       bool               `json:"cassette_hit"`
	RequestSHA        string             `json:"request_sha"`
	Deviations        []string           `json:"deviations,omitempty"`
}
type Client interface {
	Complete(context.Context, Request) (Response, error)
}
type Price struct {
	PromptPerMillion     float64 `json:"prompt_per_million"`
	CompletionPerMillion float64 `json:"completion_per_million"`
}
type Config struct {
	AnthropicEffort                                    string
	Mode, BaseURL, CassetteDir, LedgerPath, PricesPath string
	TracePrompts                                       bool
	Redactor                                           redact.Redactor
	ModelStatusPath                                    string
	Now                                                func() time.Time
	Parallel                                           int
	Timeout                                            time.Duration
	MaxRetries                                         int
	RetryDelay                                         time.Duration
	RetryMaxWait                                       time.Duration
	Sleep                                              func(context.Context, time.Duration) error
}

func DefaultConfig() Config {
	return Config{Mode: "replay", Parallel: 4, Timeout: 120 * time.Second, MaxRetries: 5, RetryDelay: time.Second, RetryMaxWait: 60 * time.Second}
}

type LedgerEntry struct {
	HTTPStatus        int                `json:"http_status,omitempty"`
	APIType           string             `json:"api_type,omitempty"`
	InputTokens       *int               `json:"input_tokens,omitempty"`
	CostUSD           *float64           `json:"cost_usd,omitempty"`
	Decision          *DecisionTelemetry `json:"decision,omitempty"`
	TS                string             `json:"ts"`
	RunID             string             `json:"run_id"`
	SampleID          string             `json:"sample_id"`
	Purpose           string             `json:"purpose"`
	Provider          string             `json:"provider"`
	Model             string             `json:"model"`
	ModelReturned     string             `json:"model_returned"`
	SystemFingerprint string             `json:"system_fingerprint"`
	RequestSHA        string             `json:"request_sha"`
	CassetteHit       bool               `json:"cassette_hit"`
	PromptTokens      int                `json:"prompt_tokens"`
	CompletionTokens  int                `json:"completion_tokens"`
	ReasoningTokens   int                `json:"reasoning_tokens,omitempty"`
	CostOriginal      *float64           `json:"cost_usd_original"`
	CostNew           *float64           `json:"cost_usd_new"`
	LatencyMS         float64            `json:"latency_ms"`
	Error             string             `json:"error"`
	Deviations        []string           `json:"deviations,omitempty"`
}
type Billing struct {
	HTTP429          int      `json:"http_429"`
	HTTP4xx          int      `json:"http_4xx"`
	HTTP5xx          int      `json:"http_5xx"`
	Calls            int      `json:"calls"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	ReasoningTokens  int      `json:"reasoning_tokens,omitempty"`
	CostNew          *float64 `json:"cost_usd_new"`
	CostOriginal     *float64 `json:"cost_usd_original"`
	CassetteHits     int      `json:"cassette_hits"`
}
type cassette struct {
	Version      string          `json:"client_version"`
	RequestSHA   string          `json:"request_sha"`
	Response     Response        `json:"response"`
	RawResponse  json.RawMessage `json:"raw_response"`
	CostOriginal *float64        `json:"cost_usd_original"`
}
type Error struct {
	Kind   string
	Status int
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return e.Kind + " (HTTP " + jsonNumber(e.Status) + ")"
	}
	return e.Kind
}
func (e *Error) ErrorType() string  { return e.Kind }
func (e *Error) SafeDetail() string { return e.Error() }

// Operation extends the same cassette, retry and ledger engine to typed APIs.
// Parse is an evaluator-owned decoder; its version belongs in KeyData.
type Operation struct {
	APIType string
	Body    json.RawMessage
	KeyData any
	Headers map[string]string
	Parse   func([]byte, Response) (Response, error)
}
type DecisionTelemetry struct {
	Deviations           []string           `json:"deviations,omitempty"`
	Confidence           map[string]float64 `json:"confidence"`
	CalculatedConfidence map[string]float64 `json:"calculated_confidence"`
	ConfidenceDifference map[string]float64 `json:"confidence_difference"`
	MappingVerified      bool               `json:"mapping_verified"`
}
