package llm

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// providerHTTP is the SDK boundary. The evaluator owns logical requests,
// recording, compatibility negotiation, retries and accounting. Anthropic can
// replace its HTTP implementation here without changing any of those contracts.
type providerHTTP interface {
	Do(context.Context, *http.Client, string, string, Request, []byte) (*http.Response, error)
}

type openAIHTTP struct{}

func (openAIHTTP) Do(ctx context.Context, client *http.Client, endpoint, key string, r Request, body []byte) (*http.Response, error) {
	base := strings.TrimSuffix(endpoint, "chat/completions")
	var wire *http.Response
	opts := []option.RequestOption{
		option.WithBaseURL(base), option.WithAPIKey(key), option.WithHTTPClient(client),
		option.WithMaxRetries(0), option.WithResponseInto(&wire), option.WithResponseBodyInto(&wire),
	}
	if r.Operation != nil {
		for k, v := range r.Operation.Headers {
			opts = append(opts, option.WithHeader(k, v))
		}
	}
	// Construct services with explicit options, avoiding NewClient's ambient
	// OPENAI_* defaults. Only evaluator MEMEVAL_* configuration may be used.
	api := openai.NewChatCompletionService(opts...)
	_, err := api.New(ctx, openai.ChatCompletionNewParams{}, option.WithRequestBody("application/json", body))
	// The SDK returns APIError for >=400 and restores Body. Our outer loop must
	// inspect it for negotiation and account for every attempt exactly once.
	if wire != nil && wire.StatusCode >= 400 {
		return wire, nil
	}
	return wire, err
}

type rawProviderHTTP struct{}

func (rawProviderHTTP) Do(ctx context.Context, client *http.Client, endpoint, key string, r Request, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Operation != nil {
		for k, v := range r.Operation.Headers {
			req.Header.Set(k, v)
		}
	}
	if r.Provider == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return client.Do(req)
}

func providerTransport(provider string) providerHTTP {
	if provider == "openai-compat" {
		return openAIHTTP{}
	}
	return rawProviderHTTP{}
}
