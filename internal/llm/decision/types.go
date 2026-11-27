// Package decision provides evaluator-side closed-rubric decisions, not a SUT.
package decision

import (
	"context"
	"encoding/json"
	"memeval/internal/llm"
)

type Question struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     any      `json:"criteria"`
	Options      []string `json:"options,omitempty"`
	Levels       int      `json:"levels,omitempty"`
}
type DecisionRequest struct {
	Purpose    string            `json:"purpose"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	State      string            `json:"state"`
	Questions  []Question        `json:"questions"`
	SampleID   string            `json:"sample_id"`
	TemplateID string            `json:"template_id"`
	Headers    map[string]string `json:"headers,omitempty"`
}
type Answer struct {
	Type          string             `json:"type"`
	Value         any                `json:"value"`
	Noul          *float64           `json:"noul,omitempty"` // Native P(true); no invented distribution/confidence.
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Deviations    []string           `json:"deviations,omitempty"`
}
type Usage = llm.Usage
type DecisionResponse struct {
	ModelReturned string                 `json:"model_returned"`
	Answers       map[string]Answer      `json:"answers"`
	Usage         Usage                  `json:"usage"`
	LatencyMS     float64                `json:"latency_ms"`
	CassetteHit   bool                   `json:"cassette_hit"`
	RequestSHA    string                 `json:"request_sha"`
	Raw           json.RawMessage        `json:"raw"`
	Calibration   *llm.DecisionTelemetry `json:"calibration"`
}
type DecisionClient interface {
	Decide(context.Context, DecisionRequest) (DecisionResponse, error)
}

type Client struct{ Engine *llm.Engine }

const TypeSafeMappingVersion = "memeval-typesafe-observed-v2"
