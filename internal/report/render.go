package report

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"memeval/internal/config/redact"
	"memeval/internal/result"
	"os"
	"strings"
)

// These assets are copied byte-for-byte from design/report-ui; edit the design
// source and resync, never modify the embedded copy independently.
//
//go:embed assets/report.css
var reportCSS string

//go:embed assets/report.js
var reportJS string

//go:embed assets/shell.html
var reportShell string

type pageData struct {
	Reports []*result.Report   `json:"reports"`
	Compare *result.Comparison `json:"compare"`
}

func HTML(ctx context.Context, reports []*result.Report, path string) error {
	return HTMLWithComparison(ctx, reports, nil, path)
}

func HTMLWithComparison(ctx context.Context, reports []*result.Report, compare *result.Comparison, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(reports) == 0 {
		return fmt.Errorf("at least one report is required")
	}
	for _, r := range reports {
		if r == nil {
			return fmt.Errorf("report must not be null")
		}
	}
	var data bytes.Buffer
	// The maintained UI expects a numeric repeats badge. Keep its view stable
	// while report.json exposes the new repeats object and flip statistics.
	views := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		var v map[string]any
		if err = json.Unmarshal(b, &v); err != nil {
			return err
		}
		if _, ok := v["repeats"].(map[string]any); ok {
			v["repeat_statistics"] = v["repeats"]
			v["repeats"] = r.Repeats
		}
		views = append(views, v)
	}
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{"reports": views, "compare": compare}); err != nil {
		return err
	}
	encoded := data.Bytes()
	if r := redact.From(ctx); len(r.Values) > 0 {
		var err error
		encoded, err = r.JSON(encoded)
		if err != nil {
			return err
		}
	}
	blob := strings.ReplaceAll(string(encoded), "</", `<\/`)
	// JSON has no \! escape: <\!-- would make JSON.parse fail. Escaping the
	// opening less-than as a Unicode escape preserves the exact original value
	// while preventing the HTML parser's script-comment state transition.
	blob = strings.ReplaceAll(blob, "<!--", `\u003c!--`)
	for _, marker := range []string{"/*CSS*/", "/*JS*/", "/*DATA*/"} {
		if strings.Count(reportShell, marker) != 1 {
			return fmt.Errorf("report shell must contain exactly one %s marker", marker)
		}
	}
	// Single-pass replacement never expands marker-like text in user JSON.
	html := strings.NewReplacer("/*CSS*/", reportCSS, "/*JS*/", reportJS, "/*DATA*/", blob).Replace(reportShell)
	return writeBytes(ctx, path, []byte(html))
}

func LoadComparison(ctx context.Context, path string) (*result.Comparison, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c result.Comparison
	if err = json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Left == "" || c.Right == "" || c.Metric == "" || c.PairedCases < 0 || c.Clusters < 0 || c.CI95 != nil && len(c.CI95) != 2 {
		return nil, fmt.Errorf("invalid comparison: missing identity/metric or invalid counts/interval")
	}
	return &c, nil
}
