package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"memeval/internal/report"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareCLIExcludeStatus(t *testing.T) {
	dir := t.TempDir()
	leftPath, rightPath := filepath.Join(dir, "left.json"), filepath.Join(dir, "right.json")
	deltaPath := filepath.Join(dir, "comparison", "delta.json")
	zero, one := 0., 1.
	left := &result.Report{SuiteSHA256: "same", RunSeed: "same", Mode: "fixed_reader", Protocol: result.Protocol{Version: "2"}, Repeats: 1, SUT: result.SUT{Name: "left"}, Cases: []result.Case{
		{CaseID: "a", ClusterID: "a", Status: result.Invalid},
		{CaseID: "b", ClusterID: "b", Status: result.Scored, Metrics: map[string]*float64{"mcq.correct": &zero}},
		{CaseID: "c", ClusterID: "c", Status: result.Scored, Metrics: map[string]*float64{"mcq.correct": &zero}},
	}}
	right := *left
	right.SUT.Name = "right"
	right.Cases = append([]result.Case(nil), left.Cases...)
	right.Cases[0].Status = result.Scored
	right.Cases[0].Metrics = map[string]*float64{"mcq.correct": &one}
	right.Cases[1].Metrics = map[string]*float64{"mcq.correct": &one}
	write := func(r *result.Report, path string) {
		t.Helper()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(left, leftPath)
	write(&right, rightPath)
	args := []string{"compare", leftPath, rightPath, "--metric", "mcq.correct", "--out", deltaPath}
	var out bytes.Buffer
	if err := runCommand(context.Background(), args, &out, io.Discard); err == nil || !strings.Contains(err.Error(), "do not drop failed cases") {
		t.Fatal("default must reject mismatched invalid", err)
	}
	if _, err := os.Stat(deltaPath); !os.IsNotExist(err) {
		t.Fatal("failed comparison wrote artifact", err)
	}
	if err := runCommand(context.Background(), append(args, "--exclude-status", "invalid"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	c, err := report.LoadComparison(context.Background(), deltaPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Delta != .5 || c.PairedCases != 2 || c.Clusters != 2 || c.ExcludedByStatus.Left[result.Invalid] != 1 {
		t.Fatalf("wrong CLI result: %+v", c)
	}
	if err = runCommand(context.Background(), append(args, "--exclude-status", "error"), &out, io.Discard); err == nil || !strings.Contains(err.Error(), "do not drop failed cases") {
		t.Fatal("unsafe status option accepted", err)
	}
	left.Cases[0].Status = result.Error
	write(left, leftPath)
	if err = runCommand(context.Background(), append(args, "--exclude-status", "invalid"), &out, io.Discard); err == nil || !strings.Contains(err.Error(), "do not drop failed cases") {
		t.Fatal("error/scored accepted", err)
	}
}
