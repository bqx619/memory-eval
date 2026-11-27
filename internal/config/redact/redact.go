// Package redact removes private configuration at artifact boundaries, not from requests.
package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"sort"
	"strings"
)

type Redactor struct{ Values []string }

func New(values []string) Redactor {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range values {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return Redactor{out}
}
func (r Redactor) Text(s string) string {
	for _, v := range r.Values {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
		encoded, _ := json.Marshal(v)
		s = strings.ReplaceAll(s, string(encoded[1:len(encoded)-1]), "[REDACTED]")
	}
	return s
}
func (r Redactor) Encoded(data []byte) []byte {
	s := string(data)
	for _, v := range r.Values {
		b, _ := json.Marshal(v)
		for _, form := range []string{string(b[1 : len(b)-1]), html.EscapeString(v), v} {
			s = strings.ReplaceAll(s, form, "[REDACTED]")
		}
	}
	return []byte(s)
}
func (r Redactor) JSON(data []byte) ([]byte, error) {
	if len(r.Values) == 0 {
		return data, nil
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return r.Text(x)
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, v := range x {
				out[r.Text(k)] = walk(v)
			}
			return out
		}
		return v
	}
	return json.Marshal(walk(v))
}
func (r Redactor) Marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return r.JSON(b)
}

type key struct{}

func With(ctx context.Context, r Redactor) context.Context { return context.WithValue(ctx, key{}, r) }
func From(ctx context.Context) Redactor                    { r, _ := ctx.Value(key{}).(Redactor); return r }

// StripANSI removes CSI/OSC and other terminal control sequences from plain
// diagnostics, retaining newlines for selecting the first meaningful line.
func StripANSI(s string) string {
	var out strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == '\x1b' {
			i++
			if i >= len(r) {
				break
			}
			c = r[i]
		} else if c == '\u009b' {
			c = '['
		} else if c == '\u009d' {
			c = ']'
		} else {
			if c >= ' ' && c != '\x7f' || c == '\n' || c == '\r' || c == '\t' {
				out.WriteRune(c)
			}
			continue
		}
		switch c {
		case '[':
			for i++; i < len(r); i++ {
				if r[i] >= 0x40 && r[i] <= 0x7e {
					break
				}
			}
		case ']', 'P', 'X', '^', '_':
			for i++; i < len(r); i++ {
				if r[i] == '\a' || r[i] == '\u009c' {
					break
				}
				if r[i] == '\x1b' && i+1 < len(r) && r[i+1] == '\\' {
					i++
					break
				}
			}
		default:
			for c >= 0x20 && c <= 0x2f && i+1 < len(r) {
				i++
				c = r[i]
			}
		}
	}
	return out.String()
}
