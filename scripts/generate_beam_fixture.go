//go:build ignore

// Truncate a CC BY-SA BEAM row to its first batch and one question per category.
// References outside the retained batch are removed, explicitly for conformance.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"memeval/internal/importers/beam"
	"os"
	"sort"
)

func main() {
	input := flag.String("input", "data/upstream/beam/data/100K-00000-of-00001.parquet", "input")
	out := flag.String("out", "internal/importers/beam/testdata/fixture.json", "fixture")
	flag.Parse()
	stop := errors.New("done")
	var row beam.Row
	e := beam.ReadRows(context.Background(), *input, func(_ int, r beam.Row) error { row = r; return stop })
	if !errors.Is(e, stop) {
		panic(e)
	}
	row.Chat = row.Chat[:1]
	ids := map[float64]bool{}
	for _, t := range row.Chat[0] {
		ids[float64(t.ID)] = true
	}
	q, e := beam.Questions(row.Probing)
	if e != nil {
		panic(e)
	}
	for k, v := range q {
		v = v[:1]
		kept := []any{}
		var walk func(any)
		walk = func(x any) {
			switch a := x.(type) {
			case []any:
				for _, y := range a {
					walk(y)
				}
			case map[string]any:
				for _, y := range a {
					walk(y)
				}
			case float64:
				if ids[a] {
					kept = append(kept, a)
				}
			}
		}
		walk(v[0].SourceChatIDs)
		sort.Slice(kept, func(i, j int) bool { return kept[i].(float64) < kept[j].(float64) })
		v[0].SourceChatIDs = kept
		q[k] = v
	}
	b, e := json.Marshal(q)
	if e != nil {
		panic(e)
	}
	row.Probing = string(b)
	b, e = json.MarshalIndent([]beam.Row{row}, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(*out, append(b, '\n'), 0600); e != nil {
		panic(e)
	}
	fmt.Println(*out)
}
