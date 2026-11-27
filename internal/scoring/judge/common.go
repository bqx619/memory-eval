package judge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/judge/inputlog"
	"strings"
)

// TemplateID hashes the exact instruction bytes, in question order, separated
// by a newline for multi-question calls. Criteria and state remain in request_sha.
func TemplateID(name string, qs []decision.Question) string {
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = q.Instructions
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return name + "@" + hex.EncodeToString(h[:])
}
func (r *Router) decide(ctx context.Context, sample string, recipe inputlog.Recipe) (decision.DecisionResponse, error) {
	if e := r.Validate(); e != nil {
		return decision.DecisionResponse{}, e
	}
	if e := recipe.Validate(); e != nil {
		return decision.DecisionResponse{}, e
	}
	return r.Decision.Decide(ctx, decision.DecisionRequest{Purpose: "judge-decision", Provider: "typesafe", Model: r.DecisionModel, State: recipe.State, Questions: recipe.Questions, SampleID: sample, TemplateID: recipe.TemplateID})
}
func verdict(r *Router, resp decision.DecisionResponse, e error) inputlog.Verdict {
	v := inputlog.Verdict{Kind: "decision", Provider: "typesafe", Model: r.DecisionModel, Valid: e == nil, Raw: resp.Raw, ModelReturned: resp.ModelReturned, RequestSHA: resp.RequestSHA}
	if e != nil {
		v.Error = e.Error()
	}
	return v
}
func record(ctx context.Context, sample, category string, recipe inputlog.Recipe, v inputlog.Verdict, e error) error {
	return errors.Join(e, inputlog.Append(ctx, sample, category, recipe, v))
}
