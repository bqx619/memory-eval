package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/config/redact"
	"memeval/internal/scoring/judge/status"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func jsonNumber(n int) string { return strconv.Itoa(n) }

type Engine struct {
	cfg          Config
	http         *http.Client
	sem          chan struct{}
	mu           sync.Mutex
	ledger       *os.File
	prompts      *os.File
	runID        string
	entries      []LedgerEntry
	models       map[ModelPin]struct{}
	status       status.Table
	prices       map[string]Price
	locks        map[string]*sync.Mutex
	capabilities map[chatModel]*chatCapabilities
}

func New(c Config) (*Engine, error) {
	switch c.Mode {
	case "live", "record", "replay", "record-once":
	default:
		return nil, fmt.Errorf("invalid llm mode")
	}
	if c.Parallel < 1 || c.Timeout <= 0 || c.MaxRetries < 0 || c.MaxRetries > 10 || c.RetryDelay < 0 || c.RetryMaxWait < 0 {
		return nil, fmt.Errorf("invalid LLM concurrency/timeout/retry settings")
	}
	if c.RetryMaxWait == 0 {
		c.RetryMaxWait = 60 * time.Second
	}
	if c.Sleep == nil {
		c.Sleep = sleepContext
	}
	if c.CassetteDir == "" {
		h, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		c.CassetteDir = filepath.Join(h, ".cache/memeval/cassettes")
	}
	if c.AnthropicEffort == "" {
		c.AnthropicEffort = "low"
	}
	e := &Engine{cfg: c, sem: make(chan struct{}, c.Parallel), prices: map[string]Price{}, locks: map[string]*sync.Mutex{}, models: map[ModelPin]struct{}{}, capabilities: map[chatModel]*chatCapabilities{}}
	if e.cfg.Now == nil {
		e.cfg.Now = time.Now
	}
	var statusErr error
	e.status, statusErr = status.Load(c.ModelStatusPath)
	if statusErr != nil {
		return nil, statusErr
	}
	// Do not inherit proxy credentials, or forward evaluator keys on redirects.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	e.http = &http.Client{Transport: tr, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	if c.PricesPath != "" {
		b, err := os.ReadFile(c.PricesPath)
		if err == nil {
			if err = json.Unmarshal(b, &e.prices); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	for _, p := range e.prices {
		if p.PromptPerMillion < 0 || p.CompletionPerMillion < 0 {
			return nil, fmt.Errorf("negative token price")
		}
	}
	if c.BaseURL != "" {
		if _, _, err := e.endpoint("openai-compat"); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Start binds one engine and ledger to one run, before any calls are made.
func (e *Engine) Start(runID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runID != "" {
		return fmt.Errorf("LLM engine already bound to a run")
	}
	if e.cfg.TracePrompts && e.cfg.LedgerPath == "" {
		return fmt.Errorf("prompt tracing requires a run output ledger path")
	}
	if e.cfg.LedgerPath != "" {
		if err := os.MkdirAll(filepath.Dir(e.cfg.LedgerPath), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(e.cfg.LedgerPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		e.ledger = f
	}
	if e.cfg.TracePrompts {
		f, err := openPromptTrace(filepath.Join(filepath.Dir(e.cfg.LedgerPath), "prompts.jsonl"))
		if err != nil {
			e.ledger.Close()
			e.ledger = nil
			return err
		}
		e.prompts = f
	}
	e.runID = runID
	return nil
}
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.http.CloseIdleConnections()
	var err error
	if e.prompts != nil {
		err = e.prompts.Close()
		e.prompts = nil
	}
	if e.ledger != nil {
		err = errors.Join(err, e.ledger.Close())
		e.ledger = nil
	}
	return err
}
func (e *Engine) Configuration() map[string]any {
	return map[string]any{"mode": e.cfg.Mode, "client_version": ClientVersion, "parallel": e.cfg.Parallel, "timeout_seconds": e.cfg.Timeout.Seconds(), "max_retries": e.cfg.MaxRetries, "retry_max_wait_seconds": e.cfg.RetryMaxWait.Seconds(), "base_url_override": e.cfg.Redactor.Text(e.cfg.BaseURL), "trace_prompts": e.cfg.TracePrompts, "anthropic_effort": e.cfg.AnthropicEffort}
}

// CassetteDirectory exposes the effective path only to in-process discovery.
func (e *Engine) CassetteDirectory() string { return e.cfg.CassetteDir }
func (e *Engine) ModelStatus(provider, model string) status.Info {
	return e.status.Lookup(provider, model, e.cfg.Now())
}
func (e *Engine) BillingPurpose(purpose string) Billing {
	return BillingEntries(e.Entries(), purpose)
}
func (e *Engine) Entries() []LedgerEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]LedgerEntry{}, e.entries...)
}
func (e *Engine) Billing() Billing {
	return BillingEntries(e.Entries(), "")
}

// BillingEntries aggregates the append-only attempt ledger, including legacy HTTP errors.
func BillingEntries(entries []LedgerEntry, purpose string) Billing {
	b := Billing{CostNew: new(float64), CostOriginal: new(float64)}
	var costsNew, costsOriginal []float64
	for _, r := range entries {
		if purpose != "" && r.Purpose != purpose {
			continue
		}
		b.Calls++
		if !r.CassetteHit {
			status := r.HTTPStatus
			if status == 0 {
				_, _ = fmt.Sscanf(r.Error, "LLMHTTPError (HTTP %d)", &status)
			}
			if status == 429 {
				b.HTTP429++
			}
			if status >= 400 && status <= 499 {
				b.HTTP4xx++
			}
			if status >= 500 && status <= 599 {
				b.HTTP5xx++
			}
		}
		b.PromptTokens += r.PromptTokens
		b.CompletionTokens += r.CompletionTokens
		b.ReasoningTokens += r.ReasoningTokens
		if r.CassetteHit {
			b.CassetteHits++
		}
		if r.CostNew == nil {
			b.CostNew = nil
		} else {
			costsNew = append(costsNew, *r.CostNew)
		}
		if r.CostOriginal == nil {
			b.CostOriginal = nil
		} else {
			costsOriginal = append(costsOriginal, *r.CostOriginal)
		}
	}
	// Preserve the chronological ledger, but make floating point totals
	// independent of concurrent request completion order.
	stableCost(b.CostNew, costsNew)
	stableCost(b.CostOriginal, costsOriginal)
	return b
}
func stableCost(sum *float64, costs []float64) {
	if sum == nil {
		return
	}
	slices.Sort(costs)
	for _, cost := range costs {
		*sum += cost
	}
}
func (e *Engine) cost(r Request, u Usage) *float64 {
	p, ok := e.prices[r.Provider+"/"+r.Model]
	if r.Provider == "typesafe" {
		if !ok {
			p = Price{PromptPerMillion: .042}
			ok = true
		}
		p.CompletionPerMillion = 0
	}
	if !ok {
		return nil
	}
	n := (float64(u.PromptTokens)*p.PromptPerMillion + float64(u.CompletionTokens)*p.CompletionPerMillion) / 1e6
	return &n
}
func (e *Engine) log(r Request, resp Response, raw []byte, cost *float64, err error) error {
	entry := LedgerEntry{TS: time.Now().UTC().Format(time.RFC3339Nano), SampleID: r.SampleID, Purpose: r.Purpose, Provider: r.Provider, Model: r.Model, ModelReturned: resp.ModelReturned, SystemFingerprint: resp.SystemFingerprint, RequestSHA: resp.RequestSHA, CassetteHit: resp.CassetteHit, PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens, CostOriginal: cost, CostNew: cost, LatencyMS: resp.LatencyMS, Deviations: resp.Deviations}
	entry.ReasoningTokens = resp.Usage.ReasoningTokens
	entry.HTTPStatus = resp.HTTPStatus
	if resp.CassetteHit || e.cfg.Mode == "replay" {
		entry.CostNew = new(float64)
	}
	if r.Operation != nil {
		n := resp.Usage.PromptTokens
		entry.APIType = r.Operation.APIType
		entry.InputTokens = &n
		entry.CostUSD = entry.CostNew
		entry.Decision = resp.Decision
	}
	if err != nil {
		entry.Error = err.Error()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry.RunID = e.runID
	if e.ledger != nil {
		data, err := e.cfg.Redactor.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := e.ledger.Write(append(data, '\n')); err != nil {
			return err
		}
		if err := e.ledger.Sync(); err != nil {
			return err
		}
	}
	e.entries = append(e.entries, entry)
	return e.trace(r, resp, raw, entry)
}
func (e *Engine) endpoint(provider string) (string, string, error) {
	base := e.cfg.BaseURL
	api := "chat/completions"
	switch provider {
	case "openai-compat":
		if base == "" {
			base = os.Getenv("MEMEVAL_OPENAI_BASE_URL")
		}
		if base == "" {
			base = "https://api.openai.com/v1"
		}
	case "typesafe":
		api = "systemone"
		if base == "" {
			base = os.Getenv("MEMEVAL_TYPESAFE_BASE_URL")
		}
		if base == "" {
			base = "https://api.typesafe.ai"
		}
	case "anthropic":
		api = "messages"
		if base == "" {
			base = os.Getenv("MEMEVAL_ANTHROPIC_BASE_URL")
		}
		if base == "" {
			base = "https://api.anthropic.com/v1"
		}
	default:
		return "", "", fmt.Errorf("unsupported LLM provider")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("base URL must be HTTP(S), without credentials/query/fragment")
	}
	if (provider == "anthropic" || provider == "typesafe") && strings.TrimRight(u.Path, "/") == "" {
		u.Path = "/v1"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + api
	return u.String(), api, nil
}
func (e *Engine) key(r Request) (string, string, error) {
	if r.Model == "" || r.SampleID == "" || r.TemplateID == "" || (r.Operation == nil && (len(r.Messages) == 0 || r.N < 1)) {
		return "", "", fmt.Errorf("LLM request missing model/messages/sample/template/n")
	}
	endpoint, api, err := e.endpoint(r.Provider)
	if err != nil {
		return "", "", err
	}
	// encoding/json deterministically sorts all map keys, including Extra.
	wire := struct {
		Provider, Endpoint, API, Model, Template, Client, Sample, Purpose string
		Messages                                                          []Message
		Temperature                                                       *float64
		MaxTokens                                                         *int
		N                                                                 int
		Extra                                                             map[string]any
	}{r.Provider, endpoint, api, r.Model, r.TemplateID, ClientVersion, r.SampleID, r.Purpose, r.Messages, r.Temperature, r.MaxTokens, r.N, r.Extra}
	b, err := json.Marshal(wire)
	if err != nil {
		return "", "", err
	}
	if r.Operation != nil {
		if r.Operation.Parse == nil || r.Operation.APIType == "" {
			return "", "", fmt.Errorf("typed operation requires decoder and api type")
		}
		b, err = json.Marshal(struct {
			Request json.RawMessage
			APIType string
			Body    json.RawMessage
			KeyData any
			Headers map[string]string
		}{b, r.Operation.APIType, r.Operation.Body, r.Operation.KeyData, r.Operation.Headers})
		if err != nil {
			return "", "", err
		}
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), endpoint, nil
}
func (e *Engine) Complete(ctx context.Context, r Request) (Response, error) {
	// Normalize before hashing, transport, tracing, and replay lookup. This provider
	// uses adaptive thinking, not the unsupported temperature parameter.
	if r.Provider == "anthropic" {
		if _, exists := r.Extra["output_config"]; exists {
			return Response{}, &Error{Kind: "ReservedLLMExtraField"}
		}
		if _, exists := r.Extra["thinking"]; exists {
			return Response{}, &Error{Kind: "ReservedLLMExtraField"}
		}
		extra := make(map[string]any, len(r.Extra)+2)
		for k, v := range r.Extra {
			extra[k] = v
		}
		extra["output_config"] = map[string]any{"effort": e.cfg.AnthropicEffort}
		extra["thinking"] = map[string]any{"type": "adaptive"}
		r.Extra = extra
		r.Temperature = nil
	}
	sha, endpoint, err := e.key(r)
	if err != nil {
		return Response{}, err
	}
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return Response{}, &Error{Kind: "LLMCanceled"}
	}
	e.mu.Lock()
	lock := e.locks[sha]
	if lock == nil {
		lock = &sync.Mutex{}
		e.locks[sha] = lock
	}
	e.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	resp := Response{RequestSHA: sha}
	fail := func(err error) (Response, error) {
		if le := e.log(r, resp, nil, nil, err); le != nil {
			return resp, le
		}
		return resp, err
	}
	session, err := e.recording(r, sha)
	if err != nil {
		return fail(err)
	}
	if session.replay {
		replayed, replayErr := e.replay(ctx, r, sha, endpoint, session)
		if replayErr != nil {
			return fail(replayErr)
		}
		return replayed, nil
	}
	if e.ModelStatus(r.Provider, r.Model).Policy == "excluded" {
		return fail(&Error{Kind: "ModelExcluded"})
	}
	if e.ModelStatus(r.Provider, r.Model).Status == "retired" {
		return fail(&Error{Kind: "ModelRetired"})
	}
	apiKey := os.Getenv("MEMEVAL_OPENAI_API_KEY")
	if r.Provider == "anthropic" {
		apiKey = os.Getenv("MEMEVAL_ANTHROPIC_API_KEY")
	}
	if r.Provider == "typesafe" {
		apiKey = os.Getenv("MEMEVAL_TYPESAFE_API_KEY")
	}
	if apiKey == "" {
		return fail(&Error{Kind: "EvaluatorKeyMissing"})
	}
	session.redactor = redact.New(append(append([]string{}, e.cfg.Redactor.Values...), apiKey))
	var raw []byte
	var cost *float64
	effective, deviations, probe, err := e.prepareChat(ctx, r)
	if err != nil {
		return fail(&Error{Kind: "LLMCanceled"})
	}
	defer probe.finish()
	if r.Provider == "anthropic" {
		deviations = append(deviations, "anthropic adaptive thinking; effort="+e.cfg.AnthropicEffort+"; temperature omitted")
	}
	for retries := 0; ; {
		if err = ctx.Err(); err != nil {
			return fail(&Error{Kind: "LLMCanceled"})
		}
		var status int
		resp, raw, status, err = e.call(ctx, endpoint, apiKey, effective, session.client)
		if err != nil && ctx.Err() != nil {
			err = &Error{Kind: "LLMCanceled"}
		}
		// Capture transport-only state before redaction round-trips Response
		// through JSON; neither this flag nor the underlying error is persisted.
		retryable := resp.retryTransport || status == 429 || status >= 500 && status <= 599
		wait := retryWait(resp.retryAfter, e.cfg.Now(), e.cfg.RetryDelay, retries, e.cfg.RetryMaxWait)
		// Compatibility retries are bounded by the changed field, independently
		// of transient retries. Inspect only structured error codes/parameters.
		next := effective
		compat := err != nil && status == http.StatusBadRequest && negotiateChat(&next, raw, &deviations)
		probe.observe(effective, next, compat, status)
		resp.RequestSHA = sha
		resp.Deviations = append([]string(nil), deviations...)
		var sanitizeErr error
		resp, sanitizeErr = e.sanitizeResponse(resp)
		if sanitizeErr != nil {
			return fail(&Error{Kind: "LLMResponseInvalid"})
		}
		if len(e.cfg.Redactor.Values) > 0 {
			if json.Valid(raw) {
				raw, sanitizeErr = e.cfg.Redactor.JSON(raw)
			} else {
				raw = e.cfg.Redactor.Encoded(raw)
			}
			if sanitizeErr != nil {
				return fail(&Error{Kind: "LLMResponseInvalid"})
			}
		}
		cost = e.cost(r, resp.Usage)
		if le := e.log(effective, resp, raw, cost, err); le != nil {
			return resp, le
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return resp, &Error{Kind: "LLMCanceled"}
		}
		if compat {
			effective = next
			continue
		}
		if retries >= e.cfg.MaxRetries || !retryable {
			return resp, err
		}
		retries++
		if e.cfg.Sleep(ctx, wait) != nil {
			return resp, &Error{Kind: "LLMCanceled"}
		}
	}
	if err := session.save(sha, resp, cost); err != nil {
		return resp, err
	}
	return resp, nil
}

func negotiateChat(r *Request, raw []byte, deviations *[]string) bool {
	if hasAssistantPrefill(*r) {
		var body struct {
			Error struct{ Type, Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &body) == nil && body.Error.Type == "invalid_request_error" {
			message := strings.ToLower(body.Error.Message)
			if (strings.Contains(message, "prefill") || strings.Contains(message, "pre-fill")) && (strings.Contains(message, "not support") || strings.Contains(message, "not allow")) {
				r.Messages = r.Messages[:len(r.Messages)-1]
				*deviations = append(*deviations, AssistantPrefillOmitted)
				return true
			}
		}
	}

	if r.Provider != "openai-compat" {
		return false
	}
	var body struct {
		Error struct{ Code, Param string } `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	switch {
	case body.Error.Code == "unsupported_parameter" && body.Error.Param == "max_completion_tokens" && r.MaxTokens != nil && !r.legacyMaxTokens:
		r.legacyMaxTokens = true
		*deviations = append(*deviations, "max_completion_tokens unavailable; using max_tokens")
		return true
	case body.Error.Code == "unsupported_value" && body.Error.Param == "temperature" && r.Temperature != nil:
		r.Temperature = nil
		*deviations = append(*deviations, "temperature unavailable; provider default")
		return true
	}
	return false
}
func (e *Engine) sanitizeResponse(resp Response) (Response, error) {
	if len(e.cfg.Redactor.Values) == 0 {
		return resp, nil
	}
	b, err := e.cfg.Redactor.Marshal(resp)
	if err != nil {
		return resp, err
	}
	var safe Response
	if err = json.Unmarshal(b, &safe); err != nil {
		return resp, err
	}
	if safe.Text != resp.Text {
		safe.Deviations = append(safe.Deviations, "private configuration redacted from evaluator response")
	}
	return safe, nil
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cassette-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (e *Engine) call(ctx context.Context, endpoint, key string, r Request, client *http.Client) (resp Response, raw []byte, status int, err error) {
	start := time.Now()
	body := map[string]any{}
	for k, v := range r.Extra {
		body[k] = v
	}
	for _, k := range []string{"model", "messages", "temperature", "max_tokens", "max_completion_tokens", "n", "stream"} {
		if _, ok := body[k]; ok {
			return resp, nil, 0, &Error{Kind: "ReservedLLMExtraField"}
		}
	}
	body["model"] = r.Model
	body["messages"] = r.Messages
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.MaxTokens != nil {
		field := "max_tokens"
		if (r.Provider == "openai-compat") && !r.legacyMaxTokens {
			field = "max_completion_tokens"
		}
		body[field] = *r.MaxTokens
	}
	if r.Provider == "anthropic" {
		if r.N != 1 || r.MaxTokens == nil {
			return resp, nil, 0, &Error{Kind: "AnthropicRequiresN1AndMaxTokens"}
		}
		messages := []Message{}
		system := []string{}
		for _, m := range r.Messages {
			if m.Role == "system" {
				system = append(system, m.Content)
			} else {
				messages = append(messages, m)
			}
		}
		body["messages"] = messages
		if len(system) > 0 {
			body["system"] = strings.Join(system, "\n\n")
		}
	} else {
		body["n"] = r.N
	}
	b, err := json.Marshal(body)
	if err != nil {
		return resp, nil, 0, &Error{Kind: "LLMRequestInvalid"}
	}
	if r.Operation != nil && len(r.Operation.Body) > 0 {
		b = r.Operation.Body
	}
	wire, err := providerTransport(r.Provider).Do(ctx, client, endpoint, key, r, b)
	defer func() { e.observeModel(r, resp) }()
	resp.LatencyMS = float64(time.Since(start).Nanoseconds()) / 1e6
	if err != nil {
		resp.retryTransport = transientTransportError(err)
		return resp, nil, 0, &Error{Kind: "LLMTransportError"}
	}
	resp.HTTPStatus = wire.StatusCode
	resp.retryAfter = wire.Header.Get("Retry-After")
	defer wire.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(wire.Body, 32<<20))
	resp.LatencyMS = float64(time.Since(start).Nanoseconds()) / 1e6
	if err != nil {
		return resp, nil, wire.StatusCode, &Error{Kind: "LLMResponseReadError"}
	}
	if wire.StatusCode < 200 || wire.StatusCode >= 300 {
		return resp, raw, wire.StatusCode, &Error{Kind: "LLMHTTPError", Status: wire.StatusCode}
	}
	if r.Operation != nil && len(r.Operation.Body) > 0 {
		parsed, parseErr := r.Operation.Parse(raw, resp)
		parsed.Raw, _ = json.Marshal(json.RawMessage(raw))
		return parsed, raw, wire.StatusCode, parseErr
	}
	if r.Provider == "anthropic" {
		var v struct {
			Model      string `json:"model"`
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Content == nil {
			return resp, raw, wire.StatusCode, &Error{Kind: "LLMResponseInvalid"}
		}
		for _, c := range v.Content {
			if c.Type == "text" {
				resp.Text += c.Text
			}
		}
		resp.ModelReturned = v.Model
		resp.FinishReason = v.StopReason
		resp.Usage = Usage{PromptTokens: v.Usage.Input, CompletionTokens: v.Usage.Output}
	} else {
		var v struct {
			Model       string `json:"model"`
			Fingerprint string `json:"system_fingerprint"`
			Usage       struct {
				Usage
				Details struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
			Choices []struct {
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(raw, &v) != nil || len(v.Choices) == 0 || len(v.Choices[0].Message.Content) == 0 {
			return resp, raw, wire.StatusCode, &Error{Kind: "LLMResponseInvalid"}
		}
		var content *string
		if json.Unmarshal(v.Choices[0].Message.Content, &content) != nil {
			return resp, raw, wire.StatusCode, &Error{Kind: "LLMResponseInvalid"}
		}
		// A present null content is a valid empty response (e.g. a reasoning
		// budget exhausted before visible output). Preserve finish/usage so the
		// reader boundary can mark invalid instead of treating it as HTTP failure.
		if content != nil {
			resp.Text = *content
		}
		resp.ModelReturned = v.Model
		resp.SystemFingerprint = v.Fingerprint
		resp.Usage = v.Usage.Usage
		resp.Usage.ReasoningTokens = v.Usage.Details.ReasoningTokens
		resp.FinishReason = v.Choices[0].FinishReason
	}
	if r.Operation != nil {
		parsed, parseErr := r.Operation.Parse(raw, resp)
		parsed.Raw, _ = json.Marshal(json.RawMessage(raw))
		return parsed, raw, wire.StatusCode, parseErr
	}
	return resp, raw, wire.StatusCode, nil
}

// Both net/http and openai-go v1.12.0 preserve *url.Error on transport
// failures (the SDK returns HTTPClient.Do's error unchanged). Classify before
// replacing it with a safe Error; URLs and raw error text must never be logged.
func transientTransportError(err error) bool {
	var u *url.Error
	if !errors.As(err, &u) {
		return false
	}
	var n net.Error
	// Inspect the underlying error: url.Error itself implements net.Error,
	// including for non-transient failures such as CheckRedirect refusal.
	return u.Timeout() || errors.Is(u.Err, io.EOF) || errors.Is(u.Err, io.ErrUnexpectedEOF) ||
		errors.As(u.Err, &n) || errors.Is(u.Err, syscall.ECONNRESET) ||
		errors.Is(u.Err, syscall.EPIPE) || errors.Is(u.Err, syscall.ECONNREFUSED)
}
