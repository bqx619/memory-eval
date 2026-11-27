package longmemeval

import (
	"memeval/internal/testoracle"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestVendoredLiteralTemplates(t *testing.T) {
	b, err := os.ReadFile(testoracle.Upstream(t, "longmemeval/src/evaluation/evaluate_qa.py"))
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)^\s*template = ("(?:[^"\\]|\\.)*")`).FindAllStringSubmatch(string(b), -1)
	values := Templates()
	if len(matches) != len(values) {
		t.Fatal("upstream template branch count changed")
	}
	for i, m := range matches {
		s, err := strconv.Unquote(m[1])
		if err != nil || s != values[i] {
			t.Fatalf("template %d drift: %v", i, err)
		}
	}
	if TemplateSHA256() != VendoredTemplateSHA256 {
		t.Fatal("vendored template digest drift")
	}
	for _, task := range []string{"single-session-user", "single-session-assistant", "multi-session", "temporal-reasoning", "knowledge-update", "single-session-preference"} {
		for _, abs := range []bool{false, true} {
			v, e := Template(task, abs)
			if e != nil || strings.Count(v, "{}") != 3 {
				t.Fatal(task, e)
			}
		}
	}
}
