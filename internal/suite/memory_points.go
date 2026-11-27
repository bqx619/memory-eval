package suite

import "encoding/json"

// Preserve an explicit empty audit target set; other gold serialization is unchanged.
func (g Gold) MarshalJSON() ([]byte, error) {
	type plain Gold
	if g.MemoryPoints != nil && len(g.MemoryPoints) == 0 {
		return json.Marshal(struct {
			plain
			MemoryPoints []MemoryPoint `json:"memory_points"`
		}{plain(g), g.MemoryPoints})
	}
	return json.Marshal(plain(g))
}
