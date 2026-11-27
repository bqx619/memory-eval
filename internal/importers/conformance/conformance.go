// Package conformance supplies the shared importer acceptance contract.
package conformance

import (
	"bytes"
	"context"
	"memeval/internal/importers"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"testing"
)

type Spec struct {
	Name                     string
	Import                   func(context.Context) (*suite.Suite, importers.Stats, error)
	GoldenSuite, GoldenStats string
	Update                   bool
}

func Run(t *testing.T, spec Spec) {
	t.Helper()
	var previous [][]byte
	for i := 0; i < 2; i++ {
		s, stats, err := spec.Import(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true}); err != nil {
			t.Fatal(err)
		}
		leak, err := suite.LeakScan(s)
		if err != nil || len(leak.Problems) > 0 {
			t.Fatalf("%s: leak scan %v: %v", spec.Name, err, leak.Problems)
		}
		if err = importers.Check(s, stats); err != nil {
			t.Fatal(err)
		}
		encoded := [][]byte{}
		for _, v := range []any{s, stats} {
			b, err := suite.CanonicalJSON(v)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, append(b, '\n'))
		}
		if i == 0 {
			previous = encoded
		} else {
			for k := range encoded {
				if !bytes.Equal(previous[k], encoded[k]) {
					t.Fatalf("%s: nondeterministic import %d", spec.Name, k)
				}
			}
		}
	}
	for i, path := range []string{spec.GoldenSuite, spec.GoldenStats} {
		if spec.Update {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, previous[i], 0600); err != nil {
				t.Fatal(err)
			}
		}
		golden, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(previous[i], golden) {
			t.Fatalf("%s: golden differs: %s (review changes before -update)", spec.Name, path)
		}
	}
}
