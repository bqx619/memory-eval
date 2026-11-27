package main

import (
	"bytes"
	"context"
	"encoding/json"
	flag "github.com/spf13/pflag"
	"io/fs"
	"memeval/internal/config/dotenv"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestEnvExampleCoverage(t *testing.T) {
	root := dotenv.Root(".")
	example, err := dotenv.Read(filepath.Join(root, ".env.example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	reads := map[string]bool{}
	patterns := []*regexp.Regexp{regexp.MustCompile(`(?:os\.(?:Getenv|LookupEnv)|dotenv\.Value)\(\s*"(MEMEVAL_[A-Z0-9_]+)"`), regexp.MustCompile(`Env:\s*"(MEMEVAL_[A-Z0-9_]+)"`)}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, pattern := range patterns {
				for _, m := range pattern.FindAllStringSubmatch(string(b), -1) {
					reads[m[1]] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key := range reads {
		if _, ok := example[key]; !ok {
			t.Errorf("code reads %s but .env.example omits it; ask Claude to update template", key)
		}
	}
	for key := range example {
		if strings.HasPrefix(key, "MEMEVAL_") {
			if !reads[key] {
				t.Errorf(".env.example has %s without a code read point", key)
			}
			continue
		}
		// SUT names are intentionally dynamic: prove the documented generic
		// manifest allowlist read point consumes each non-evaluator example key.
		m, err := sut.Load(filepath.Join(root, "suts/echo/manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		m.DefaultEnvFile = filepath.Join(t.TempDir(), ".env")
		m.Process.EnvFile = ""
		m.Process.EnvPassthrough = []string{key}
		if err := os.WriteFile(m.DefaultEnvFile, []byte(key+"=coverage-sut-value\n"), 0600); err != nil {
			t.Fatal(err)
		}
		env, err := m.Environment("/episode", "/venv")
		if err != nil || env[key] != "coverage-sut-value" {
			t.Errorf("no manifest read point for %s: %v", key, err)
		}
	}
}
func TestEnvCLIOriginsAndMaskedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.env")
	if err := os.WriteFile(path, []byte("MEMEVAL_OPENAI_API_KEY=uniquely-private-fixture-key\nMEMEVAL_LLM_MODE=record\nMEMEVAL_LLM_PARALLEL=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMEVAL_LLM_MODE", "replay")
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"env", "--env-file", path, "--llm-parallel", "3"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "uniquely-private-fixture-key") {
		t.Fatal("env printed full key")
	}
	var result struct {
		Configuration []struct {
			Key, Source, Prefix string
			Length              int
		}
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, row := range result.Configuration {
		sources[row.Key] = row.Source
		if row.Key == "MEMEVAL_OPENAI_API_KEY" && (row.Prefix != "uniq" || row.Length != len("uniquely-private-fixture-key")) {
			t.Fatal(row)
		}
	}
	if sources["MEMEVAL_OPENAI_API_KEY"] != ".env" || sources["MEMEVAL_LLM_MODE"] != "env" || sources["MEMEVAL_LLM_PARALLEL"] != "flag" {
		t.Fatal(sources)
	}
}

func TestSUTCheckShowsOnlyPassthroughKeys(t *testing.T) {
	testoracle.FakeDocker(t).Configure(t)
	root := dotenv.Root(".")
	dir := t.TempDir()
	// P6 requires an installed env; use an offline empty-dependency echo runtime.
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n"), 0600)
	t.Chdir(dir)
	m, err := sut.LoadWithEnv(filepath.Join(root, "suts/echo/manifest.json"), filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	m.Process.Command = []string{"{venv}/bin/python", "{project}/echo.py"}
	echo, err := os.ReadFile(filepath.Join(root, "suts/echo/echo_bridge.py"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "echo.py"), echo, 0600)
	m.Process.EnvPassthrough = []string{"OCTOPMEMORY_LLM_API_KEY"}
	m.Sandbox.Backend = "container"
	m.Runtime.Requirements = "requirements.in"
	m.Runtime.RequirementsLock = "requirements.lock.txt"
	os.WriteFile(filepath.Join(dir, "requirements.lock.txt"), nil, 0600)
	b, _ := json.Marshal(m)
	manifest := filepath.Join(dir, "manifest.json")
	os.WriteFile(manifest, b, 0600)
	os.WriteFile(filepath.Join(dir, "requirements.in"), nil, 0600)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("OCTOPMEMORY_LLM_API_KEY=private-sut-key\nMEMEVAL_OPENAI_API_KEY=private-evaluator-key\n"), 0600)
	installed, err := sut.LoadWithEnv(manifest, filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	t.Setenv("MEMEVAL_CACHE_DIR", cache)
	if _, err = sut.Install(t.Context(), installed, sut.InstallOptions{Cache: cache, Offline: true}); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if err := run(context.Background(), []string{"sut", "check", "--sut", manifest, "--cache-dir", cache}, &out, &errs); err != nil {
		t.Fatal(err, errs.String())
	}
	if strings.Contains(out.String(), "private-sut-key") || strings.Contains(out.String(), "private-evaluator-key") {
		t.Fatal("sut check leaked values")
	}
	var check struct {
		Valid bool
		Keys  []string `json:"env_passthrough"`
	}
	if json.Unmarshal(out.Bytes(), &check) != nil || !check.Valid || !reflect.DeepEqual(check.Keys, []string{"OCTOPMEMORY_LLM_API_KEY"}) {
		t.Fatal(out.String())
	}
	for _, key := range []string{"MEMEVAL_OPENAI_API_KEY", "HERDR_TOKEN", "CODEX_TOKEN", "CLAUDE_TOKEN", "ANTHROPIC_API_KEY"} {
		m.Process.EnvPassthrough = []string{key}
		b, _ = json.Marshal(m)
		os.WriteFile(manifest, b, 0600)
		if err := run(context.Background(), []string{"sut", "check", "--sut", manifest, "--cache-dir", cache}, &out, &errs); err == nil {
			t.Fatal("forbidden passthrough accepted", key)
		}
	}
}

func TestSection7DefaultsAndEffortPrecedence(t *testing.T) {
	for _, b := range dotenv.Bindings {
		t.Setenv(b.Env, "")
	}
	f := flag.NewFlagSet("fixture", flag.ContinueOnError)
	e := llmFlags(f)
	if err := parse(f, nil); err != nil {
		t.Fatal(err)
	}
	if e.reader.Model != "gpt-6-luna" || e.reader.JudgeDecisionModel != "jev-1.14.0" || e.config.AnthropicEffort != "low" {
		t.Fatal("section 7 defaults")
	}
	t.Setenv("MEMEVAL_ANTHROPIC_EFFORT", "high")
	f = flag.NewFlagSet("fixture", flag.ContinueOnError)
	e = llmFlags(f)
	if err := parse(f, nil); err != nil || e.config.AnthropicEffort != "high" {
		t.Fatal("effort env ignored", err)
	}
	if err := parse(f, []string{"--anthropic-effort", "low"}); err != nil || e.config.AnthropicEffort != "low" {
		t.Fatal("effort flag priority", err)
	}
}
