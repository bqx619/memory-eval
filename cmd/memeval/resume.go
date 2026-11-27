package main

import (
	"fmt"
	flag "github.com/spf13/pflag"
	"memeval/internal/registry"
	"memeval/internal/report"
	"memeval/internal/runner"
	"path/filepath"
)

// Report settings fill omitted experiment flags. Explicit flags may adjust
// evaluator configuration; public-input-affecting changes fail preflight.
func restoreRunFlags(f *flag.FlagSet, x *report.ResumeOutput, o *runner.Options, statuses string) error {
	r := x.Original
	set := func(name string, value any) error {
		if f.Changed(name) {
			return nil
		}
		return f.Set(name, fmt.Sprint(value))
	}
	values := map[string]any{"mode": r.Mode, "run-seed": r.RunSeed, "repeats": r.Repeats, "batch-size": r.Protocol.BatchSize, "reset-policy": r.Protocol.ResetPolicy, "top-k": r.Protocol.Budget.TopK, "max-context-chars": r.Protocol.Budget.MaxContextChars, "trace-text": r.Protocol.TraceText}
	if r.Protocol.CaseLimit != nil {
		values["limit"] = *r.Protocol.CaseLimit
	}
	if r.SUT.ManifestSHA256 == "" {
		values["sut"] = r.SUT.Name
	} else if !f.Changed("sut") {
		return fmt.Errorf("resume external SUT requires --sut with the original manifest")
	}
	if r.SUT.ManifestSHA256 != "" {
		values["network"] = r.Isolation.NetworkMode
	}
	if reader, ok := r.Reader.(map[string]any); ok {
		for flag, key := range map[string]string{"reader-provider": "provider", "reader-model": "model", "reader-input": "input_mode", "prefeval-remind": "remind", "reader-min-tokens-reasoning": "min_tokens_reasoning"} {
			if v, ok := reader[key]; ok && v != nil {
				values[flag] = v
			}
		}
		for _, key := range []string{"requested_max_tokens", "max_tokens", "effective_max_tokens"} {
			if v, ok := reader[key]; ok && v != nil {
				values["reader-max-tokens"] = v
				break
			}
		}
	}
	if j := r.Protocol.Judge; j != nil {
		if j.Kind != "decision" {
			return fmt.Errorf("resume rejects legacy judge kind %q; Jev-only requires protocol.judge.kind=decision", j.Kind)
		}
		values["judge-decision-model"] = j.Model
	}
	for name, v := range values {
		if v != "" {
			if err := set(name, v); err != nil {
				return err
			}
		}
	}
	if !f.Changed("suite") {
		reg, err := registry.LoadDir(o.RegistryDir)
		if err != nil {
			return fmt.Errorf("resume suite lookup: %w (or supply --suite)", err)
		}
		_, ref, ok := reg.LookupSuite(r.SuiteID)
		if !ok {
			return fmt.Errorf("resume suite is not registered; supply --suite")
		}
		if ref.SuiteSHA256 != "" && ref.SuiteSHA256 != r.SuiteSHA256 {
			return fmt.Errorf("resume registry trusted root mismatch")
		}
		if err := set("suite", ref.Path); err != nil {
			return err
		}
	}
	var err error
	o.Resume, err = runner.NewResume(r, statuses)
	return err
}

func sameDir(a, b string) bool {
	x, e := filepath.Abs(a)
	y, f := filepath.Abs(b)
	return e == nil && f == nil && filepath.Clean(x) == filepath.Clean(y)
}
