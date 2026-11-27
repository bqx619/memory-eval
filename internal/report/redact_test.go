package report

import (
	"context"
	"encoding/json"
	"memeval/internal/config/redact"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStringRedactionPreservesNumericJSON(t *testing.T) {
	ctx := redact.With(context.Background(), redact.New([]string{"1"}))
	path := filepath.Join(t.TempDir(), "report.json")
	if err := WriteJSON(ctx, path, map[string]any{"secret": "1", "score": 1.25, "count": 10}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(b, &got) != nil || got["secret"] != "[REDACTED]" || got["score"] != 1.25 || got["count"] != float64(10) {
		t.Fatal(string(b))
	}
}
