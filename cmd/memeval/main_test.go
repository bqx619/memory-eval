package main

import (
	"bytes"
	"context"
	flag "github.com/spf13/pflag"
	"path/filepath"
	"testing"
)

func TestInterspersedFlags(t *testing.T) {
	f := flag.NewFlagSet("test", flag.ContinueOnError)
	metric := f.String("metric", "pass", "")
	trace := f.Bool("trace-text", false, "")
	if err := parse(f, []string{"left.json", "right.json", "--metric", "recall", "--trace-text"}); err != nil || *metric != "recall" || !*trace || f.NArg() != 2 {
		t.Fatal(f.Args(), err)
	}
}
func TestCLIImportValidateAndRun(t *testing.T) {
	dir := t.TempDir()
	normalized := filepath.Join(dir, "suite.json")
	var out, errs bytes.Buffer
	ctx := context.Background()
	if err := run(ctx, []string{"import", "pmb", "--input", "../../docs/data/pmb_projection.json", "--out", normalized}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"validate", normalized}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"run", "--suite", normalized, "--sut", "null", "--limit", "1", "--out", filepath.Join(dir, "run")}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"0", "17"} {
		if err := run(ctx, []string{"run", "--suite", normalized, "--parallel", n, "--out", filepath.Join(dir, "invalid")}, &out, &errs); err == nil {
			t.Fatal("invalid parallel accepted", n)
		}
	}
}
