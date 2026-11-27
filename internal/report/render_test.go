package report

import (
	"bytes"
	"context"
	"encoding/json"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAssetsMatchDesign(t *testing.T) {
	for name, embedded := range map[string]string{"report.css": reportCSS, "report.js": reportJS, "shell.html": reportShell} {
		source, err := os.ReadFile(filepath.Join("../../design/report-ui", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(source, []byte(embedded)) {
			t.Fatalf("%s diverges from designer source", name)
		}
	}
}
func TestJSONInjectionRoundTrip(t *testing.T) {
	value := "</ScRiPt><img src=x onerror=alert(1)><!--<script>/*CSS*//*JS*//*DATA*/ 中文 & \u2028"
	r := &result.Report{SUT: result.SUT{Name: value}, Cases: []result.Case{}}
	c := &result.Comparison{Left: value, Right: "right", Metric: "recall", CI95: nil}
	for _, compare := range []*result.Comparison{nil, c} {
		path := filepath.Join(t.TempDir(), "report.html")
		if err := HTMLWithComparison(context.Background(), []*result.Report{r}, compare, path); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		html := string(b)
		start := `<script type="application/json" id="memeval-data">`
		_, rest, ok := strings.Cut(html, start)
		if !ok {
			t.Fatal("missing data node")
		}
		blob, _, ok := strings.Cut(rest, "</script>")
		if !ok {
			t.Fatal("unclosed data node")
		}
		if strings.Contains(blob, "</") || strings.Contains(blob, "<!--") || !strings.Contains(blob, `<\/ScRiPt>`) || !strings.Contains(blob, `\u003c!--`) {
			t.Fatal("unsafe JSON payload")
		}
		var decoded pageData
		if err = json.Unmarshal([]byte(blob), &decoded); err != nil {
			t.Fatal("JSON was quoted or invalid", err)
		}
		if len(decoded.Reports) != 1 || decoded.Reports[0].SUT.Name != value || (decoded.Compare == nil) != (compare == nil) {
			t.Fatal("payload changed")
		}
		if compare != nil && decoded.Compare.Left != value {
			t.Fatal("comparison changed")
		}
		if !strings.Contains(html, reportCSS) || !strings.Contains(html, reportJS) {
			t.Fatal("asset bytes changed")
		}
	}
}
func TestInvalidComparison(t *testing.T) {
	for _, blob := range []string{`null`, `{}`, `{"left":"l","right":"r","metric":"recall","ci95":[0]}`} {
		p := filepath.Join(t.TempDir(), "delta.json")
		if err := os.WriteFile(p, []byte(blob), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadComparison(context.Background(), p); err == nil {
			t.Fatal("accepted", blob)
		}
	}
}

func TestRepeatStatisticsKeepNumericUIBadge(t *testing.T) {
	rate := 0.
	r := &result.Report{SUT: result.SUT{Name: "repeat-fixture"}, Repeats: 3, RepeatAnalysis: &result.RepeatSummary{Count: 3, FlipRate: &rate}}
	dest := filepath.Join(t.TempDir(), "report.html")
	if err := HTML(context.Background(), []*result.Report{r}, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"repeats":3`) || !strings.Contains(string(data), `"repeat_statistics"`) {
		t.Fatal("UI repeats badge incompatibility")
	}
}
