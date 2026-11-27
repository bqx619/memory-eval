package pytext

import (
	"fmt"
	"strings"
	"unicode"
)

func ReprString(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r == rune(quote) {
				b.WriteByte('\\')
				b.WriteRune(r)
			} else if !unicode.IsPrint(r) {
				if r <= 255 {
					fmt.Fprintf(&b, `\x%02x`, r)
				} else if r <= 65535 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					fmt.Fprintf(&b, `\U%08x`, r)
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte(quote)
	return b.String()
}
func ReprStrings(ss []string) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = ReprString(s)
	}
	return "[" + strings.Join(out, ", ") + "]"
}
