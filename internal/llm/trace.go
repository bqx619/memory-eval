package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// PromptTrace is evaluator-only, opt-in output. It never enters a cassette or
// the SUT environment; request headers (including credentials) are not included.
type PromptTrace struct {
	TS            string          `json:"ts"`
	RunID         string          `json:"run_id"`
	SampleID      string          `json:"sample_id"`
	Purpose       string          `json:"purpose"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	ModelReturned string          `json:"model_returned"`
	TemplateID    string          `json:"template_id"`
	RequestSHA    string          `json:"request_sha"`
	CassetteHit   bool            `json:"cassette_hit"`
	Messages      []Message       `json:"messages,omitempty"`
	RequestBody   json.RawMessage `json:"request_body,omitempty"`
	ResponseText  string          `json:"response_text"`
	RawResponse   string          `json:"raw_response"`
	Error         string          `json:"error,omitempty"`
}

func openPromptTrace(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("prompt trace must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// Tighten an existing file's mode before truncating/writing sensitive text.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err == nil {
		err = f.Truncate(0)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Called under e.mu, once per ledger row (including retries and cache hits).
func (e *Engine) trace(r Request, response Response, raw []byte, ledger LedgerEntry) error {
	if e.prompts == nil {
		return nil
	}
	row := PromptTrace{TS: ledger.TS, RunID: ledger.RunID, SampleID: r.SampleID, Purpose: r.Purpose, Provider: r.Provider, Model: r.Model, ModelReturned: response.ModelReturned, TemplateID: r.TemplateID, RequestSHA: response.RequestSHA, CassetteHit: response.CassetteHit, Messages: r.Messages, ResponseText: response.Text, RawResponse: string(raw), Error: ledger.Error}
	if r.Operation != nil && len(r.Operation.Body) > 0 {
		row.RequestBody = r.Operation.Body
		row.ResponseText = string(raw)
	} else {
		// Retain the reader's original text and whitespace; raw_response also
		// preserves the provider envelope.
		var chat struct {
			Choices []struct {
				Message struct {
					Content *string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &chat) == nil {
			if r.Provider == "anthropic" && chat.Content != nil {
				var text strings.Builder
				for _, part := range chat.Content {
					if part.Type == "text" {
						text.WriteString(part.Text)
					}
				}
				row.ResponseText = text.String()
			} else if len(chat.Choices) > 0 && chat.Choices[0].Message.Content != nil {
				row.ResponseText = *chat.Choices[0].Message.Content
			}
		}
	}
	data, err := e.cfg.Redactor.Marshal(row)
	if err != nil {
		return err
	}
	if _, err := e.prompts.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write prompt trace: %w", err)
	}
	return e.prompts.Sync()
}
