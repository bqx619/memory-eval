package main

import (
	"memeval/internal/runner"
	"testing"
)

func TestAliasValidation(t *testing.T) {
	// Actual data loading is covered only by the opt-in acceptance above.
	for _, path := range []string{"longmemeval-s@0", "longmemeval-s@501", "longmemeval-s@bad"} {
		o := runner.DefaultOptions()
		if _, err := suiteAlias(path, &o); err == nil {
			t.Fatal(path)
		}
	}
}
