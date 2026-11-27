package llm

import (
	"context"
	"strings"
	"sync"
)

// ModelCapabilities records known model-family properties independently of the
// per-run wire negotiation cache. Unknown models are never inferred as reasoning.
type ModelCapabilities struct {
	Reasoning bool `json:"reasoning"`
}

var declaredModelCapabilities = []struct {
	provider, family string
	capabilities     ModelCapabilities
}{
	{"openai-compat", "gpt-6", ModelCapabilities{Reasoning: true}},
}

func Capabilities(provider, model string) ModelCapabilities {
	for _, entry := range declaredModelCapabilities {
		if provider == entry.provider && (model == entry.family || strings.HasPrefix(model, entry.family+"-") || strings.HasPrefix(model, entry.family+".")) {
			return entry.capabilities
		}
	}
	return ModelCapabilities{}
}

type chatModel struct{ provider, model string }

// Owned by one Engine/run. A cold request discovers only the fields it uses;
// concurrent callers wait for that probe, then retain normal HTTP parallelism.
type chatCapabilities struct {
	mu                               sync.Mutex
	probing                          chan struct{}
	temperatureKnown, tokensKnown    bool
	prefillKnown, omitPrefill        bool
	omitTemperature, legacyMaxTokens bool
}

type chatProbe struct {
	capabilities *chatCapabilities
	done         chan struct{}
}

func (e *Engine) prepareChat(ctx context.Context, r Request) (Request, []string, *chatProbe, error) {
	if r.Provider != "openai-compat" && r.Provider != "anthropic" {
		return r, nil, nil, nil
	}
	e.mu.Lock()
	key := chatModel{r.Provider, r.Model}
	c := e.capabilities[key]
	if c == nil {
		c = &chatCapabilities{}
		e.capabilities[key] = c
	}
	e.mu.Unlock()
	for {
		c.mu.Lock()
		unknown := r.Provider != "anthropic" && (r.Temperature != nil && !c.temperatureKnown || r.MaxTokens != nil && !c.tokensKnown) || hasAssistantPrefill(r) && !c.prefillKnown
		if unknown && c.probing != nil {
			done := c.probing
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return r, nil, nil, ctx.Err()
			case <-done:
			}
			continue
		}
		probe := &chatProbe{capabilities: c}
		if unknown {
			c.probing = make(chan struct{})
			probe.done = c.probing
		}
		deviations := []string{}
		if hasAssistantPrefill(r) && c.omitPrefill {
			r.Messages = r.Messages[:len(r.Messages)-1]
			deviations = append(deviations, AssistantPrefillOmitted)
		}
		if r.MaxTokens != nil && c.legacyMaxTokens {
			r.legacyMaxTokens = true
			deviations = append(deviations, "max_completion_tokens unavailable; using max_tokens")
		}
		if r.Temperature != nil && c.omitTemperature {
			r.Temperature = nil
			deviations = append(deviations, "temperature unavailable; provider default")
		}
		c.mu.Unlock()
		return r, deviations, probe, nil
	}
}

func (p *chatProbe) observe(sent, next Request, adjusted bool, status int) {
	if p == nil {
		return
	}
	c := p.capabilities
	c.mu.Lock()
	defer c.mu.Unlock()
	if adjusted {
		if hasAssistantPrefill(sent) && !hasAssistantPrefill(next) {
			c.prefillKnown, c.omitPrefill = true, true
		}
		if sent.Temperature != nil && next.Temperature == nil {
			c.omitTemperature, c.temperatureKnown = true, true
		}
		if next.legacyMaxTokens && !sent.legacyMaxTokens {
			c.legacyMaxTokens, c.tokensKnown = true, true
		}
	}
	if status >= 200 && status < 300 {
		if hasAssistantPrefill(sent) {
			c.prefillKnown = true
		}
		if sent.Temperature != nil {
			c.temperatureKnown = true
		}
		if sent.MaxTokens != nil {
			c.tokensKnown = true
		}
	}
}

func (p *chatProbe) finish() {
	if p == nil || p.done == nil {
		return
	}
	c := p.capabilities
	c.mu.Lock()
	close(p.done)
	c.probing = nil
	c.mu.Unlock()
}

const AssistantPrefillOmitted = "assistant prefill unavailable; omitted"

func hasAssistantPrefill(r Request) bool {
	return r.Provider == "anthropic" && len(r.Messages) > 0 && r.Messages[len(r.Messages)-1].Role == "assistant"
}
