//go:build ignore

// Generate the four MIT real-row fixtures without loading a complete parquet.
// Run from the repository root: GOPROXY=off go run scripts/generate_mab_fixtures.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/importers/memoryagentbench"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	license, e := os.ReadFile("design/upstream/memoryagentbench/LICENSE")
	if os.IsNotExist(e) {
		fmt.Fprintln(os.Stderr, "Upstream source unavailable: design/upstream/memoryagentbench/LICENSE; restore with python3 -B scripts/fetch_upstream_refs.py --only memoryagentbench")
		os.Exit(3)
	}
	if e != nil {
		panic(e)
	}
	if e = run(license); e != nil {
		panic(e)
	}
}
func run(license []byte) error {
	ch, e := memoryagentbench.NewChunker()
	if e != nil {
		return e
	}
	dest := "internal/importers/memoryagentbench/testdata"
	if e = os.MkdirAll(dest, 0755); e != nil {
		return e
	}
	names := []string{}
	for n := range memoryagentbench.Splits {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, split := range names {
		stop := errors.New("first row read")
		var row memoryagentbench.Row
		e = memoryagentbench.ReadRows(context.Background(), filepath.Join("data/upstream/memoryagentbench/data", memoryagentbench.Splits[split]+"-00000-of-00001.parquet"), func(_ int, r memoryagentbench.Row) error { row = r; return stop })
		if !errors.Is(e, stop) {
			return e
		}
		chunks, e := ch.Chunk(row.Context, 4096)
		if e != nil {
			return e
		}
		n := min(3, len(chunks))
		for {
			texts := []string{}
			for _, c := range chunks[:n] {
				texts = append(texts, c.Text)
			}
			row.Context = strings.Join(texts, " ")
			again, e := ch.Chunk(row.Context, 4096)
			if e != nil {
				return e
			}
			if len(again) <= 3 {
				break
			}
			n--
			if n == 0 {
				return fmt.Errorf("cannot truncate %s", split)
			}
		}
		b, e := json.MarshalIndent([]memoryagentbench.Row{row}, "", "  ")
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dest, split+".json"), append(b, '\n'), 0600); e != nil {
			return e
		}
		fmt.Printf("%s: 1 real row, %d retained chunks, %d questions\n", split, n, len(row.Questions))
	}
	return os.WriteFile(filepath.Join(dest, "LICENSE"), license, 0600)
}
