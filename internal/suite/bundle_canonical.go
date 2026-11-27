package suite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// The lexical canonical check avoids materializing a second copy of all text.
// JSON numbers retain their spelling, exactly as CanonicalJSON's UseNumber does.
type canonicalParser struct {
	data    []byte
	pos     int
	version string
}

var fieldCache sync.Map

func fieldsOf(t reflect.Type) map[string]reflect.Type {
	if v, ok := fieldCache.Load(t); ok {
		return v.(map[string]reflect.Type)
	}
	m := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		m[name] = f.Type
	}
	fieldCache.Store(t, m)
	return m
}
func (p *canonicalParser) str() (string, error) {
	start := p.pos
	p.pos++
	for p.pos < len(p.data) {
		n := bytes.IndexAny(p.data[p.pos:], `"\`)
		if n < 0 {
			return "", fmt.Errorf("unterminated string")
		}
		p.pos += n
		if p.data[p.pos] == '"' {
			p.pos++
			v := p.data[start:p.pos]
			if !bytes.ContainsRune(v, '\\') {
				return string(v[1 : len(v)-1]), nil
			}
			s, e := strconv.Unquote(string(v))
			return s, e
		}
		p.pos++
		if p.pos >= len(p.data) {
			break
		}
		c := p.data[p.pos]
		p.pos++
		if c == 'u' {
			if p.pos+4 > len(p.data) {
				break
			}
			hex := string(p.data[p.pos : p.pos+4])
			p.pos += 4
			if hex == "2028" || hex == "2029" {
				continue
			}
			n, e := strconv.ParseUint(hex, 16, 16)
			if e != nil || hex != strings.ToLower(hex) || n >= 32 || n == 8 || n == 9 || n == 10 || n == 12 || n == 13 {
				return "", fmt.Errorf("noncanonical Unicode escape")
			}
		} else if !strings.ContainsRune(`"\bfnrt`, rune(c)) {
			return "", fmt.Errorf("noncanonical escape")
		}
	}
	return "", fmt.Errorf("unterminated string")
}
func (p *canonicalParser) value(t reflect.Type) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		t = nil
	}
	if p.pos >= len(p.data) {
		return io.ErrUnexpectedEOF
	}
	switch p.data[p.pos] {
	case '{':
		p.pos++
		previous := ""
		first := true
		required := map[string]bool{}
		if t == reflect.TypeFor[Event]() {
			required = map[string]bool{"source_id": false, "owner": false, "content": false}
		}
		if t == reflect.TypeFor[Case]() {
			required = map[string]bool{"id": false, "query": false, "cutoff": false, "gold": false}
		}
		for p.pos < len(p.data) && p.data[p.pos] != '}' {
			if !first {
				if p.data[p.pos] != ',' {
					return fmt.Errorf("noncanonical object separator")
				}
				p.pos++
			}
			if p.data[p.pos] != '"' {
				return fmt.Errorf("noncanonical object key")
			}
			key, err := p.str()
			if err != nil {
				return err
			}
			if !first && key <= previous {
				return fmt.Errorf("noncanonical key order/duplicate %q", key)
			}
			previous = key
			first = false
			if p.pos >= len(p.data) || p.data[p.pos] != ':' {
				return fmt.Errorf("noncanonical colon")
			}
			p.pos++
			var child reflect.Type
			if t != nil && t.Kind() == reflect.Struct {
				var ok bool
				child, ok = fieldsOf(t)[key]
				if !ok {
					return fmt.Errorf("unknown field %q", key)
				}
				if p.version == "2.0" {
					for _, name := range v3Names[t] {
						if key == name {
							return fmt.Errorf("field %s requires schema_version 3.0", key)
						}
					}
				}
			} else if t != nil && t.Kind() == reflect.Map {
				child = t.Elem()
			}
			if _, ok := required[key]; ok {
				if p.pos >= len(p.data) || p.data[p.pos] == 'n' {
					return fmt.Errorf("missing required field %s", key)
				}
				required[key] = true
			}
			if err = p.value(child); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		if p.pos >= len(p.data) {
			return io.ErrUnexpectedEOF
		}
		p.pos++
		for name, present := range required {
			if !present {
				return fmt.Errorf("missing required field %s", name)
			}
		}
	case '[':
		p.pos++
		first := true
		var child reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			child = t.Elem()
		}
		for p.pos < len(p.data) && p.data[p.pos] != ']' {
			if !first {
				if p.data[p.pos] != ',' {
					return fmt.Errorf("noncanonical array separator")
				}
				p.pos++
			}
			first = false
			if err := p.value(child); err != nil {
				return err
			}
		}
		if p.pos >= len(p.data) {
			return io.ErrUnexpectedEOF
		}
		p.pos++
	case '"':
		// Values usually contain the large history strings. Validate escapes
		// without unquoting or allocating their decoded text a second time.
		p.pos++
		for {
			n := bytes.IndexAny(p.data[p.pos:], `"\`)
			if n < 0 {
				return io.ErrUnexpectedEOF
			}
			p.pos += n
			if p.data[p.pos] == '"' {
				p.pos++
				break
			}
			p.pos++
			c := p.data[p.pos]
			p.pos++
			if c == 'u' {
				hex := string(p.data[p.pos : p.pos+4])
				p.pos += 4
				if hex == "2028" || hex == "2029" {
					continue
				}
				n, e := strconv.ParseUint(hex, 16, 16)
				if e != nil || hex != strings.ToLower(hex) || n >= 32 || n == 8 || n == 9 || n == 10 || n == 12 || n == 13 {
					return fmt.Errorf("noncanonical Unicode escape")
				}
			} else if !strings.ContainsRune(`"\bfnrt`, rune(c)) {
				return fmt.Errorf("noncanonical escape")
			}
		}
	default:
		start := p.pos
		for p.pos < len(p.data) && !bytes.ContainsRune([]byte(",]}"), rune(p.data[p.pos])) {
			if p.data[p.pos] <= ' ' {
				return fmt.Errorf("noncanonical whitespace")
			}
			p.pos++
		}
		if start == p.pos {
			return fmt.Errorf("invalid value")
		}
	}
	return nil
}
func canonicalEpisodeCheck(data []byte, version string) error {
	if !json.Valid(data) || !utf8.Valid(data) {
		return fmt.Errorf("invalid JSON/UTF-8")
	}
	if bytes.Contains(data, []byte("\u2028")) || bytes.Contains(data, []byte("\u2029")) {
		return fmt.Errorf("noncanonical literal U+2028/U+2029")
	}
	p := canonicalParser{data: data, version: version}
	if err := p.value(reflect.TypeFor[Episode]()); err != nil {
		return fmt.Errorf("byte %d: %w", p.pos, err)
	}
	if p.pos != len(data) {
		return fmt.Errorf("noncanonical trailing bytes")
	}
	return nil
}

type eventWire Event
type caseWire Case
type episodeWire struct {
	ID        string      `json:"id"`
	ClusterID string      `json:"cluster_id"`
	Events    []eventWire `json:"events"`
	Cases     []caseWire  `json:"cases"`
}

func decodeCanonicalEpisode(data []byte, version string) (*Episode, error) {
	if err := canonicalEpisodeCheck(data, version); err != nil {
		return nil, err
	}
	var w episodeWire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	ep := &Episode{ID: w.ID, ClusterID: w.ClusterID, Events: make([]Event, len(w.Events)), Cases: make([]Case, len(w.Cases))}
	for i := range w.Events {
		ep.Events[i] = Event(w.Events[i])
	}
	for i := range w.Cases {
		ep.Cases[i] = Case(w.Cases[i])
	}
	return ep, nil
}
func uniqueJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key := k.(string)
				if seen[key] {
					return fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = true
				if e = walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case json.Delim('['):
			for d.More() {
				if e = walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON input")
	}
	return nil
}
