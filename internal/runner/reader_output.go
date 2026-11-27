package runner

import (
	"context"
	"fmt"
	"memeval/internal/llm"
	"memeval/internal/pytext"
	"memeval/internal/reader"
	"memeval/internal/result"
)

func (f *FixedReaderOptions) reasoningMinimum() int {
	if f.MinTokensReasoning == nil {
		return 128
	}
	return *f.MinTokensReasoning
}

// These are output limits in the vendored official reader calls, not context
// budgets or judge limits. Nil means unset/provider- or dataset-configured:
// do not invent an official value from our existing framework default (512).
func officialReaderTokens(id, kind string, full bool, provider string) *int {
	switch id {
	case "prefeval":
		if kind == "mcq" {
			return new(5)
		}
		return new(300)
	case "locomo":
		return new(32) // gpt_utils.py, batch_size=1
	case "longmemeval":
		return new(500) // run_generation.py, without CoT
	case "memora":
		if !full {
			return new(500)
		}
	case "personamem":
		if provider == "anthropic" {
			return new(128000)
		}
	}
	return nil
}

type readerClient struct {
	llm.Client
	row      *result.Case
	official *int
	min      int
	input    string
}

// Preserve historical sources cassette keys. The existing template dimension
// separates items even when returned text happens to equal the source history.
func readerTemplateID(id, input string) string {
	if input == "items" {
		return id + "/input=items"
	}
	return id
}

func (c *readerClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Purpose != "reader" {
		return c.Client.Complete(ctx, req)
	}
	req.TemplateID = readerTemplateID(req.TemplateID, c.input)
	requested := req.MaxTokens
	reasoning := llm.Capabilities(req.Provider, req.Model).Reasoning
	deviations := []string{}
	if reasoning && requested != nil && *requested < c.min {
		req.MaxTokens = new(c.min)
		deviations = append(deviations, fmt.Sprintf("reader reasoning budget raised: requested_max_tokens=%d, effective_max_tokens=%d, reader_min_tokens_reasoning=%d", *requested, c.min, c.min))
	}
	if c.official != nil && req.MaxTokens != nil && *c.official != *req.MaxTokens {
		deviations = append(deviations, fmt.Sprintf("reader output budget differs from official: official_max_tokens=%d, effective_max_tokens=%d", *c.official, *req.MaxTokens))
	}
	resp, err := c.Client.Complete(ctx, req)
	resp.Deviations = append(resp.Deviations, deviations...)
	if c.row.QA == nil {
		c.row.QA = &result.QA{}
	}
	empty := err == nil && pytext.Strip(resp.Text) == ""
	c.row.QA.ReaderOutput = &result.ReaderOutput{OfficialMaxTokens: c.official, RequestedMaxTokens: requested, EffectiveMaxTokens: req.MaxTokens, Reasoning: reasoning, FinishReason: resp.FinishReason, CompletionTokens: resp.Usage.CompletionTokens, ReasoningTokens: resp.Usage.ReasoningTokens, Empty: empty}
	if empty {
		resp.Deviations = append(resp.Deviations, "empty reader response: invalid; excluded from official denominators (including finish_reason=length with no content)")
		return resp, &reader.EmptyResponseError{Response: resp}
	}
	return resp, err
}

func sameLimit(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Called after benchmark-specific aggregation so budget disclosure and invalid
// coverage are identical across every reader family, including deterministic QA.
func readerOutputMetadata(r *result.Report, o Options) {
	m, ok := r.Reader.(map[string]any)
	if !ok {
		return
	}
	type budget struct {
		Official  *int `json:"official_max_tokens"`
		Requested *int `json:"requested_max_tokens"`
		Effective *int `json:"effective_max_tokens"`
	}
	budgets := []budget{}
	attempts := 0
	for _, c := range r.Cases {
		if c.QA == nil || c.QA.ReaderOutput == nil {
			continue
		}
		attempts++
		x := c.QA.ReaderOutput
		seen := false
		for _, b := range budgets {
			seen = seen || sameLimit(b.Official, x.OfficialMaxTokens) && sameLimit(b.Requested, x.RequestedMaxTokens) && sameLimit(b.Effective, x.EffectiveMaxTokens)
		}
		if !seen {
			budgets = append(budgets, budget{x.OfficialMaxTokens, x.RequestedMaxTokens, x.EffectiveMaxTokens})
		}
	}
	m["official_max_tokens"], m["effective_max_tokens"] = nil, nil
	m["requested_max_tokens"], m["max_tokens"] = nil, nil
	m["reasoning"] = llm.Capabilities(o.FixedReader.Provider, o.FixedReader.Model).Reasoning
	m["min_tokens_reasoning"] = o.FixedReader.reasoningMinimum()
	if len(budgets) == 0 {
		// Fixed upstream answers (Memora no-memory path) make no provider call,
		// but still have a declared protocol budget. Effective stays null.
		var official *int
		seen, mixed := false, false
		for _, c := range r.Cases {
			if c.Kind == "memory_audit" {
				continue
			}
			v := officialReaderTokens(o.benchmarkID, c.Kind, o.fullContext, o.FixedReader.Provider)
			if seen && !sameLimit(official, v) {
				mixed = true
			}
			official, seen = v, true
		}
		if !mixed {
			m["official_max_tokens"] = official
		}
	} else if len(budgets) == 1 {
		b := budgets[0]
		m["official_max_tokens"], m["effective_max_tokens"], m["requested_max_tokens"], m["max_tokens"] = b.Official, b.Effective, b.Requested, b.Effective
	} else if len(budgets) > 1 {
		m["token_budgets"] = budgets
	}
	m["token_budget_note"] = "official_max_tokens null means unspecified, configured upstream, or mixed (see token_budgets); effective_max_tokens null means no explicit cap or no reader request"
	if off, ok := r.Scores.Official.(result.Official); ok {
		off.Invalid = r.Summary.Invalid
		if off.Invalid > 0 {
			off.QuestionSet.Complete = false
			off.Fidelity = "partial"
			off.Deviations = appendUnique(off.Deviations, "empty reader answers are invalid and excluded from official denominators")
		}
		r.Scores.Official = off
	}
	if r.Summary.ReaderEmptyAnswers > 0 && attempts > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("Empty reader answers: %d/%d reader attempts (%.2f%%); invalid, excluded from official denominators", r.Summary.ReaderEmptyAnswers, attempts, 100*float64(r.Summary.ReaderEmptyAnswers)/float64(attempts)))
	}
}
