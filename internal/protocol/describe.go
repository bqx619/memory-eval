package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// v2 historically uses a string capability list (membership is the boolean).
// Also accept explicit boolean maps, so dump:false stays unsupported. Marshaling
// retains the established list shape and the existing read_only_queries field.
func (d *Describe) UnmarshalJSON(b []byte) error {
	type plain Describe
	wire := struct {
		*plain
		Capabilities json.RawMessage `json:"capabilities"`
	}{plain: (*plain)(d)}
	if e := json.Unmarshal(b, &wire); e != nil {
		return e
	}
	raw := bytes.TrimSpace(wire.Capabilities)
	if len(raw) > 0 && raw[0] == '{' {
		var caps map[string]json.RawMessage
		if e := json.Unmarshal(raw, &caps); e != nil {
			return e
		}
		d.Capabilities = []string{}
		for k, v := range caps {
			if !bytes.Equal(v, []byte("true")) && !bytes.Equal(v, []byte("false")) {
				return fmt.Errorf("capability %s must be boolean", k)
			}
			if k == "read_only_queries" {
				d.ReadOnlyQueries = bytes.Equal(v, []byte("true"))
			}
			if bytes.Equal(v, []byte("true")) {
				d.Capabilities = append(d.Capabilities, k)
			}
		}
		slices.Sort(d.Capabilities)
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, &d.Capabilities)
}
