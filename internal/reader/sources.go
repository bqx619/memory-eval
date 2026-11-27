// Package reader provides source-only ordering shared by benchmark readers.
package reader

import (
	"fmt"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"sort"
)

func Order(e suite.Event) int {
	switch v := e.Attributes["memeval_reader_sequence"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}
func Sources(items []protocol.Item, known map[string]suite.Event) ([]suite.Event, error) {
	events := []suite.Event{}
	seen := map[string]bool{}
	for _, item := range items {
		for _, id := range item.SourceIDs {
			ev, ok := known[id]
			if !ok {
				return nil, fmt.Errorf("reader source does not resolve")
			}
			if !seen[id] {
				seen[id] = true
				events = append(events, ev)
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return Order(events[i]) < Order(events[j]) })
	return events, nil
}
