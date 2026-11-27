package jsonrpc

import (
	"encoding/json"
	"strings"
)

func redactData(raw json.RawMessage, secrets []string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return json.RawMessage(`"[invalid error data]"`)
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case string:
			return Redact(x, secrets)
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
			return x
		case map[string]any:
			out := map[string]any{}
			for k, val := range x {
				lower := strings.ToLower(k)
				if strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") || lower == "token" || lower == "authorization" {
					val = "[REDACTED]"
				}
				out[Redact(k, secrets)] = clean(val)
			}
			return out
		default:
			return v
		}
	}
	b, _ := json.Marshal(clean(v))
	return b
}
