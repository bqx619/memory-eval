package memoryagentbench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The sibling locked LongMemEval input is optional for portable imports. This
// diagnostic never changes the suite's Merkle root. No 277 MB array is built.
func overlapIntersection(input string, ids []string) (map[string]any, error) {
	path := filepath.Join(filepath.Dir(filepath.Dir(input)), "..", "longmemeval-s", "longmemeval_s_cleaned.json")
	out := map[string]any{"reference": "longmemeval-s/longmemeval_s_cleaned.json", "available": false}
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		out["reason"] = "sibling reference unavailable; overlap IDs retained for external verification"
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	tok, e := d.Token()
	if e != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("invalid LongMemEval reference")
	}
	seen := map[string]bool{}
	for d.More() {
		var q struct {
			ID string `json:"question_id"`
		}
		if e = d.Decode(&q); e != nil {
			return nil, e
		}
		seen[q.ID] = true
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, fmt.Errorf("trailing LongMemEval reference")
	}
	n := 0
	for _, id := range ids {
		if seen[id] {
			n++
		}
	}
	out["available"] = true
	out["reference_questions"] = len(seen)
	out["intersection"] = n
	out["mab_distinct_questions"] = len(ids)
	return out, nil
}
