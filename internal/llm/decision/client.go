package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"memeval/internal/llm"
	"net/http"
	"sort"
	"strings"
)

func Normalize(r DecisionRequest) (DecisionRequest, error) {
	if r.Purpose != "decision" && r.Purpose != "judge-decision" {
		return r, fmt.Errorf("decision purpose must be decision or judge-decision")
	}
	if r.Provider != "typesafe" {
		return r, fmt.Errorf("unknown decision provider")
	}
	if r.Model == "" || r.SampleID == "" || r.TemplateID == "" || len(r.Questions) == 0 {
		return r, fmt.Errorf("decision requires model, sample, template and questions")
	}
	r.Questions = append([]Question{}, r.Questions...)
	sort.Slice(r.Questions, func(i, j int) bool { return r.Questions[i].Name < r.Questions[j].Name })
	for i, q := range r.Questions {
		if q.Name == "" || i > 0 && q.Name == r.Questions[i-1].Name {
			return r, fmt.Errorf("empty or duplicate question name")
		}
		switch q.Type {
		case "noul":
			if len(q.Options) > 0 || q.Levels != 0 {
				return r, fmt.Errorf("noul does not accept options or levels")
			}
		case "choice":
			if len(q.Options) < 2 || len(q.Options) > 255 || q.Levels != 0 {
				return r, fmt.Errorf("choice needs 2..255 options")
			}
			seen := map[string]bool{}
			for _, v := range q.Options {
				if v == "" || seen[v] {
					return r, fmt.Errorf("empty or duplicate choice option")
				}
				seen[v] = true
			}
		case "score":
			if q.Levels < 2 || q.Levels > 10 || len(q.Options) > 0 {
				return r, fmt.Errorf("score needs 2..10 levels")
			}
		default:
			return r, fmt.Errorf("decision type must be noul, choice or score")
		}
		if r.Provider == "typesafe" {
			if err := validateTypeSafeCriteria(q); err != nil {
				return r, err
			}
		}
	}
	headers := map[string]string{}
	for k, v := range r.Headers {
		canonical := http.CanonicalHeaderKey(k)
		for _, ch := range canonical {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", ch)) {
				return r, fmt.Errorf("invalid experimental header name")
			}
		}
		if r.Provider != "typesafe" || !strings.HasPrefix(canonical, "Typesafe-Danger-Zone-") || len(canonical) == len("Typesafe-Danger-Zone-") || strings.ContainsAny(k+v, "\r\n\x00") {
			return r, fmt.Errorf("decision header outside TypeSafe-Danger-Zone-* allowlist")
		}
		if _, ok := headers[canonical]; ok {
			return r, fmt.Errorf("duplicate canonical header")
		}
		headers[canonical] = v
	}
	r.Headers = headers
	return r, nil
}
func (c *Client) Decide(ctx context.Context, r DecisionRequest) (DecisionResponse, error) {
	var out DecisionResponse
	r, err := Normalize(r)
	if err != nil {
		return out, err
	}
	if c.Engine == nil {
		return out, fmt.Errorf("decision engine missing")
	}

	op := &llm.Operation{APIType: "systemone", Headers: r.Headers, KeyData: struct {
		Version   string
		Questions []Question
	}{TypeSafeMappingVersion, r.Questions}}
	request := llm.Request{Purpose: r.Purpose, Provider: r.Provider, Model: r.Model, SampleID: r.SampleID, TemplateID: r.TemplateID, Operation: op}
	questions := map[string]any{}
	for _, q := range r.Questions {
		v := map[string]any{"type": q.Type, "instructions": q.Instructions}
		if q.Type != "noul" {
			v["criteria"] = q.Criteria
		}
		questions[q.Name] = v
	}
	op.Body, err = json.Marshal(map[string]any{"state": r.State, "model": r.Model, "questions": questions})
	if err != nil {
		return out, err
	}
	op.Parse = func(raw []byte, base llm.Response) (llm.Response, error) {
		answers, usage, model, err := decode(r, raw, base)
		base.Usage = usage
		base.ModelReturned = model
		if err != nil {
			return base, &llm.Error{Kind: "DecisionResponseInvalid"}
		}
		telemetry := &llm.DecisionTelemetry{Confidence: map[string]float64{}, CalculatedConfidence: map[string]float64{}, ConfidenceDifference: map[string]float64{}, MappingVerified: true}
		for _, q := range r.Questions {
			a := answers[q.Name]
			for _, d := range a.Deviations {
				telemetry.Deviations = append(telemetry.Deviations, q.Name+": "+d)
			}
			if a.Confidence != nil {
				telemetry.Confidence[q.Name] = *a.Confidence
			}
			if expected, ok := CalibratedConfidence(q, a); ok {
				telemetry.CalculatedConfidence[q.Name] = expected
				if a.Confidence != nil {
					telemetry.ConfidenceDifference[q.Name] = *a.Confidence - expected
				}
			}
		}
		encoded, err := json.Marshal(answers)
		if err != nil {
			return base, err
		}
		base.Text = string(encoded)
		base.Usage = usage
		base.ModelReturned = model
		base.Decision = telemetry
		return base, nil
	}
	response, err := c.Engine.Complete(ctx, request)
	out = DecisionResponse{ModelReturned: response.ModelReturned, Usage: response.Usage, LatencyMS: response.LatencyMS, CassetteHit: response.CassetteHit, RequestSHA: response.RequestSHA, Raw: response.Raw, Calibration: response.Decision}
	if err != nil {
		return out, err
	}
	if json.Unmarshal([]byte(response.Text), &out.Answers) != nil {
		return out, &llm.Error{Kind: "DecisionCassetteInvalid"}
	}
	return out, nil
}
func decode(r DecisionRequest, raw []byte, base llm.Response) (map[string]Answer, Usage, string, error) {
	model, usage := base.ModelReturned, base.Usage
	var envelope struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   *struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, usage, model, err
	}
	if envelope.Model == "" || envelope.Usage == nil || envelope.Usage.Input < 0 || envelope.Usage.Output < 0 {
		return nil, usage, model, fmt.Errorf("missing native metadata")
	}
	model = envelope.Model
	usage = Usage{PromptTokens: envelope.Usage.Input, CompletionTokens: envelope.Usage.Output}
	if len(envelope.Answers) != len(r.Questions) {
		return nil, usage, model, fmt.Errorf("answer count")
	}
	out := map[string]Answer{}
	for _, q := range r.Questions {
		data, ok := envelope.Answers[q.Name]
		if !ok {
			return nil, usage, model, fmt.Errorf("missing answer")
		}
		a, err := decodeTypeSafeAnswer(q, data)
		if err != nil {
			return nil, usage, model, err
		}
		out[q.Name] = a
	}
	return out, usage, model, nil
}
func validateAnswer(q Question, a Answer) error {
	if a.Confidence == nil || !probability(*a.Confidence) || len(a.Probabilities) == 0 {
		return fmt.Errorf("invalid confidence/probabilities")
	}
	if _, err := ValidateDistribution(a.Probabilities); err != nil {
		return err
	}

	if len(a.Probabilities) != len(q.Options) {
		return fmt.Errorf("choice distribution")
	}
	for _, key := range q.Options {
		if _, ok := a.Probabilities[key]; !ok {
			return fmt.Errorf("choice distribution")
		}
	}
	value, ok := a.Value.(string)
	if !ok {
		return fmt.Errorf("choice value")
	}
	if _, ok := a.Probabilities[value]; !ok {
		return fmt.Errorf("choice value")
	}

	return nil
}
func probability(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }
func CalibratedConfidence(q Question, a Answer) (float64, bool) {
	switch q.Type {
	case "noul":
		p, ok := TrueProbability(a)
		return math.Abs(2*p - 1), ok
	case "choice":
		n := len(q.Options)
		if n < 2 {
			return 0, false
		}
		maximum := 0.
		for _, p := range a.Probabilities {
			maximum = math.Max(maximum, p)
		}
		u := 1 / float64(n)
		return (maximum - u) / (1 - u), true
	}
	return 0, false
}

// MetricValue describes the native decision value; judge score mapping is discrete.
func MetricValue(a Answer) float64 {
	switch a.Type {
	case "noul":
		p, _ := TrueProbability(a)
		return p
	case "choice":
		return a.Probabilities[a.Value.(string)]
	case "score":
		return a.Value.(float64)
	}
	return 0
}
func TrueProbability(a Answer) (float64, bool) {
	if a.Noul == nil {
		return 0, false
	}
	return *a.Noul, probability(*a.Noul)
}
