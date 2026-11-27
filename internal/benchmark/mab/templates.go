// Package mab contains literal templates from the pinned MIT upstream release.
package mab

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

const MemorizeID = "mab_memorize"

//go:embed templates.json
var templates []byte

//go:embed summary.json
var summary []byte

type Template struct {
	System   string            `json:"system"`
	Memorize string            `json:"memorize"`
	Query    map[string]string `json:"query"`
}

var library struct {
	System string              `json:"SYSTEM_MESSAGE"`
	Base   map[string]Template `json:"BASE_TEMPLATES"`
}
var summaryPrompts map[string]string

func init() {
	if e := json.Unmarshal(templates, &library); e != nil {
		panic(e)
	}
	if e := json.Unmarshal(summary, &summaryPrompts); e != nil {
		panic(e)
	}
}
func TemplateSHA256() string { return fmt.Sprintf("%x", sha256.Sum256(templates)) }
func SummarySHA256() string  { return fmt.Sprintf("%x", sha256.Sum256(summary)) }
func For(source string) (Template, error) {
	for _, v := range []struct {
		parts []string
		key   string
	}{{[]string{"ruler_", "qa"}, "ruler_qa"}, {[]string{"icl_"}, "in_context_learning"}, {[]string{"infbench_", "sum"}, "infbench_sum"}, {[]string{"eventqa_"}, "eventqa"}, {[]string{"recsys_", "redial"}, "recsys_redial"}, {[]string{"longmemeval_"}, "longmemeval"}, {[]string{"factconsolidation_"}, "factconsolidation"}, {[]string{"detective_", "qa"}, "detective_qa"}} {
		ok := true
		for _, p := range v.parts {
			ok = ok && strings.Contains(source, p)
		}
		if ok {
			return library.Base[v.key], nil
		}
	}
	return Template{}, fmt.Errorf("unsupported MAB source %q", source)
}

// Format mirrors only the named/escaped braces used by the vendored literals.
// Replacements are simultaneous: braces in inserted dataset text are untouched.
func Format(template string, values map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(template); {
		if strings.HasPrefix(template[i:], "{{") {
			b.WriteByte('{')
			i += 2
			continue
		}
		if strings.HasPrefix(template[i:], "}}") {
			b.WriteByte('}')
			i += 2
			continue
		}
		if template[i] == '{' {
			if end := strings.IndexByte(template[i:], '}'); end >= 0 {
				if v, ok := values[template[i+1:i+end]]; ok {
					b.WriteString(v)
					i += end + 1
					continue
				}
			}
		}
		b.WriteByte(template[i])
		i++
	}
	return b.String()
}
func Render(source, content string) (string, error) {
	t, e := For(source)
	if e != nil {
		return "", e
	}
	return Format(t.Memorize, map[string]string{"context": content, "time_stamp": ""}), nil
}
func SummaryPrompt(kind string, values map[string]string) (string, error) {
	s, ok := summaryPrompts[kind+"_prompt_book"]
	if !ok {
		return "", fmt.Errorf("unknown summary rubric")
	}
	return Format(s, values), nil
}
