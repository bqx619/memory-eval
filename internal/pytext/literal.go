// Python literal grammar for benchmark data. No evaluation, calls or names are executed.
package pytext

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type literal struct {
	s     string
	i     int
	depth int
}

func ParseLiteral(s string) (any, error) {
	p := literal{s: s}
	v, e := p.value()
	p.space()
	if e == nil && p.i != len(s) {
		e = fmt.Errorf("trailing Python literal at byte %d", p.i)
	}
	return v, e
}
func StringList(s string) ([]string, error) {
	var raw []any
	if !(strings.HasPrefix(strings.TrimSpace(s), "[") && json.Unmarshal([]byte(s), &raw) == nil && raw != nil) {
		v, e := ParseLiteral(s)
		if e != nil {
			return nil, e
		}
		var ok bool
		raw, ok = v.([]any)
		if !ok {
			return nil, fmt.Errorf("expected string list")
		}
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		var ok bool
		out[i], ok = v.(string)
		if !ok {
			return nil, fmt.Errorf("option must be a string")
		}
	}
	return out, nil
}
func (p *literal) space() {
	for p.i < len(p.s) && strings.ContainsRune(" \r\n\t", rune(p.s[p.i])) {
		p.i++
	}
}
func (p *literal) take(c byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}
func (p *literal) value() (any, error) {
	p.space()
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 256 || p.i >= len(p.s) {
		return nil, fmt.Errorf("invalid literal at byte %d", p.i)
	}
	switch p.s[p.i] {
	case '\'', '"':
		return p.str()
	case '[':
		p.i++
		a := []any{}
		if !p.take(']') {
			for {
				v, e := p.value()
				if e != nil {
					return nil, e
				}
				a = append(a, v)
				if p.take(']') {
					break
				}
				if !p.take(',') {
					return nil, fmt.Errorf("expected comma at byte %d", p.i)
				}
				if p.take(']') {
					break
				}
			}
		}
		return a, nil
	case '{':
		p.i++
		m := map[string]any{}
		if !p.take('}') {
			for {
				k, e := p.str()
				if e != nil {
					return nil, e
				}
				if !p.take(':') {
					return nil, fmt.Errorf("expected colon")
				}
				v, e := p.value()
				if e != nil {
					return nil, e
				}
				m[k] = v
				if p.take('}') {
					break
				}
				if !p.take(',') {
					return nil, fmt.Errorf("expected comma")
				}
				if p.take('}') {
					break
				}
			}
		}
		return m, nil
	}
	start := p.i
	for p.i < len(p.s) && !strings.ContainsRune(" ,]}\r\n\t", rune(p.s[p.i])) {
		p.i++
	}
	v := p.s[start:p.i]
	switch v {
	case "None":
		return nil, nil
	case "True":
		return true, nil
	case "False":
		return false, nil
	}
	if n, e := strconv.ParseInt(v, 10, 64); e == nil {
		return n, nil
	}
	if strings.ContainsAny(v, ".eE") {
		if n, e := strconv.ParseFloat(v, 64); e == nil {
			return n, nil
		}
	}
	return nil, fmt.Errorf("invalid Python literal token at byte %d", start)
}
func (p *literal) str() (string, error) {
	p.space()
	if p.i >= len(p.s) || (p.s[p.i] != '\'' && p.s[p.i] != '"') {
		return "", fmt.Errorf("expected string at byte %d", p.i)
	}
	quote := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		if c == quote {
			return b.String(), nil
		}
		if c == '\n' || c == '\r' {
			return "", fmt.Errorf("unescaped newline")
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if p.i == len(p.s) {
			break
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case '\\', '\'', '"':
			b.WriteByte(e)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'a':
			b.WriteByte(7)
		case 'v':
			b.WriteByte(11)
		case '\n':
		case 'x', 'u', 'U':
			n := 2
			if e == 'u' {
				n = 4
			}
			if e == 'U' {
				n = 8
			}
			if p.i+n > len(p.s) {
				return "", fmt.Errorf("short hex escape")
			}
			v, err := strconv.ParseUint(p.s[p.i:p.i+n], 16, 32)
			if err != nil || !utf8.ValidRune(rune(v)) {
				return "", fmt.Errorf("invalid hex escape")
			}
			b.WriteRune(rune(v))
			p.i += n
		default:
			if e >= '0' && e <= '7' {
				digits := string(e)
				for len(digits) < 3 && p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '7' {
					digits += string(p.s[p.i])
					p.i++
				}
				v, _ := strconv.ParseUint(digits, 8, 32)
				b.WriteRune(rune(v))
			} else {
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		}
	}
	return "", fmt.Errorf("unterminated string")
}
