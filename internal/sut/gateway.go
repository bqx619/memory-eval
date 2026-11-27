package sut

import (
	"context"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// LLMGateway is evaluator configuration, not part of the manifest identity.
// Credentials never participate in report, profile or argv serialization.
type LLMGateway struct {
	BaseURL *url.URL          `json:"-"`
	APIKey  string            `json:"-"`
	Model   string            `json:"model"`
	Effort  string            `json:"reasoning_effort"`
	Source  map[string]string `json:"source"`
}

func ParseLLMGateway(baseURL, key, model, effort string) (*LLMGateway, error) {
	g := &LLMGateway{APIKey: key, Model: model, Effort: effort, Source: map[string]string{}}
	if g.Model == "" {
		g.Model = "gpt-6-luna"
		g.Source["MEMEVAL_SUT_LLM_MODEL"] = "default"
	}
	if g.Effort == "" {
		g.Effort = "low"
	}
	if baseURL == "" {
		return g, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return nil, fmt.Errorf("SUT LLM base URL requires http:// or https:// and a host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("SUT LLM base URL must not contain credentials, query or fragment")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("invalid SUT LLM gateway port")
	}
	// Validate host/port syntax without resolving names or making requests.
	if _, _, err = net.SplitHostPort(net.JoinHostPort(u.Hostname(), port)); err != nil {
		return nil, fmt.Errorf("invalid SUT LLM gateway address")
	}
	g.BaseURL = u
	return g, nil
}

func ReadLLMGateway(ctx context.Context) (*LLMGateway, error) {
	g, err := ParseLLMGateway(os.Getenv("MEMEVAL_SUT_LLM_BASE_URL"), os.Getenv("MEMEVAL_SUT_LLM_API_KEY"), os.Getenv("MEMEVAL_SUT_LLM_MODEL"), os.Getenv("MEMEVAL_SUT_LLM_REASONING_EFFORT"))
	if err != nil {
		return nil, err
	}
	for _, b := range dotenv.Bindings {
		if b.Env != "MEMEVAL_SUT_LLM_BASE_URL" && b.Env != "MEMEVAL_SUT_LLM_API_KEY" && b.Env != "MEMEVAL_SUT_LLM_MODEL" && b.Env != "MEMEVAL_SUT_LLM_REASONING_EFFORT" {
			continue
		}
		source := "default"
		if os.Getenv(b.Env) != "" {
			source = "env"
		}
		if settings := dotenv.From(ctx); settings != nil && settings.Sources[b.Env] != "" {
			source = settings.Sources[b.Env]
		}
		// An explicitly empty file value/flag still means no model was
		// configured; Parse's fallback must not disguise it as a user choice.
		if b.Env == "MEMEVAL_SUT_LLM_MODEL" && strings.TrimSpace(os.Getenv(b.Env)) == "" {
			source = "default"
		}
		g.Source[b.Env] = source
	}
	return g, nil
}

func (g *LLMGateway) Secrets() []string {
	if g == nil || g.APIKey == "" {
		return nil
	}
	return []string{g.APIKey}
}

// EffectiveGateway applies runtime-only overrides; changing model/effort does
// not rebuild an image. Explicit flags outrank manifest pins.
func (m *Manifest) EffectiveGateway() *LLMGateway {
	g := m.Gateway
	if g == nil {
		g, _ = ParseLLMGateway("", "", "", "")
	}
	copy := *g
	copy.Source = map[string]string{}
	for k, v := range g.Source {
		copy.Source[k] = v
	}
	for _, v := range []struct {
		key, pin string
		dst      *string
	}{{"MEMEVAL_SUT_LLM_MODEL", m.LLM.Model, &copy.Model}, {"MEMEVAL_SUT_LLM_REASONING_EFFORT", m.LLM.Effort, &copy.Effort}} {
		if v.pin != "" && copy.Source[v.key] != "flag" {
			*v.dst = v.pin
			copy.Source[v.key] = "manifest"
		}
	}
	return &copy
}
func (m *Manifest) validateLLM() error {
	if m.LLM.Concurrency != nil && *m.LLM.Concurrency < 1 {
		return fmt.Errorf("llm.concurrency must be >= 1")
	}
	for k, v := range m.LLM.Env {
		if !keyPattern.MatchString(k) || Forbidden(k) {
			return fmt.Errorf("forbidden llm environment key: %s", k)
		}
		rest := strings.NewReplacer("{llm.base_url}", "", "{llm.api_key}", "", "{llm.model}", "", "{llm.reasoning_effort}", "").Replace(v)
		if strings.ContainsAny(rest, "{}\x00\r\n") {
			return fmt.Errorf("invalid llm.env template for %s", k)
		}
		if credentialEnvKey.MatchString(k) && v != "{llm.api_key}" {
			return fmt.Errorf("llm credential %s must use {llm.api_key}", k)
		}
	}
	return nil
}
func (m *Manifest) applyLLM(env map[string]string) error {
	g := m.EffectiveGateway()
	base := ""
	if g.BaseURL != nil {
		base = g.BaseURL.String()
		if m.Sandbox.Network == sandbox.NetworkGateway {
			base = container.GatewayBaseURL(g.BaseURL)
		}
	}
	if m.LLM.Required && m.Sandbox.Network == sandbox.NetworkNone {
		return fmt.Errorf("该变体需要 gateway 或 open 网络")
	}
	if m.LLM.Required && base == "" {
		return fmt.Errorf("LLM variant requires --sut-llm-base-url")
	}
	expand := strings.NewReplacer("{llm.base_url}", base, "{llm.api_key}", g.APIKey, "{llm.model}", g.Model, "{llm.reasoning_effort}", g.Effort)
	for k, v := range m.LLM.Env {
		env[k] = expand.Replace(v)
	}
	return nil
}
func (m *Manifest) LLMDisclosure(config map[string]any) map[string]any {
	if !m.LLM.Required && len(m.LLM.Env) == 0 {
		return nil
	}
	g := m.EffectiveGateway()
	host := ""
	if g.BaseURL != nil {
		host = g.BaseURL.Host
	}
	source := "env"
	for _, s := range g.Source {
		if s == "manifest" && source != "flag" {
			source = "manifest"
		}
		if s == "flag" {
			source = "flag"
		}
	}
	// Describe owns the SDK facts. Copy only strings, without manufacturing
	// provider support claims for adapters that do not disclose llm_notes.
	notes := []string{}
	switch v := config["llm_notes"].(type) {
	case []string:
		notes = append(notes, v...)
	case []any: // JSON-RPC decoded arrays
		for _, item := range v {
			if text, ok := item.(string); ok {
				notes = append(notes, text)
			}
		}
	}
	return map[string]any{"gateway_host": host, "model": g.Model, "reasoning_effort": g.Effort, "source": source, "notes": notes}
}
