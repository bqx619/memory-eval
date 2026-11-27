package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"memeval/internal/benchmark"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnswerInputCLIResumePreflight(t *testing.T) {
	testoracle.ProtocolSources(t, "memoryagentbench")
	t.Chdir(benchmark.Root())
	for _, mode := range []string{"sources", "items"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			env := f18Config(t, dir)
			t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-key")
			s := &suite.Suite{SchemaVersion: "3.0", SuiteID: "p12-synthetic", ProtocolRef: "protocols/memoryagentbench.json#accurate_retrieval", Provenance: map[string]any{"source_url": "fixture://synthetic"}, Episodes: []suite.Episode{{ID: "ep", Events: []suite.Event{{SourceID: "source", Owner: "user", Role: "user", Content: "blue box"}}, Cases: []suite.Case{{ID: "case", Kind: "qa", Query: "blue?", User: "user", Cutoff: 1, Attributes: map[string]any{"mab_source": "ruler_qa1_197K"}, Gold: suite.Gold{Answers: []string{"blue"}, Checker: &suite.Checker{Type: "mab_exact_match"}}}}}}}
			path := filepath.Join(dir, "suite")
			if err := suite.Pack(t.Context(), s, path); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "choices": []any{map[string]any{"message": map[string]string{"content": "blue"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1}})
			}))
			defer server.Close()
			out := filepath.Join(dir, "run")
			base := []string{"--env-file", env, "run", "--suite", path, "--llm-base-url", server.URL, "--cassette-dir", filepath.Join(dir, "cassettes"), "--llm-retries", "0"}
			args := append(append([]string{}, base...), "--out", out, "--sut", "bm25", "--mode", "fixed_reader", "--llm", "record-once", "--reader-model", "fixture", "--reader-input", mode, "--run-seed", strings.Repeat("ab", 32))
			if err := runResumeCLI(t, args); err != nil {
				t.Fatal(err)
			}
			old := readRun(t, out)
			if calls != 1 || old.Cases[0].Status != result.Scored {
				t.Fatal(calls, old.Cases)
			}
			// Compare the entire persisted run and cassette inventory: rejecting an
			// opposite input must precede Engine creation and resume staging.
			snapshot := func() map[string][32]byte {
				t.Helper()
				m := map[string][32]byte{}
				err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					b, err := os.ReadFile(p)
					if err == nil {
						m[p] = sha256.Sum256(b)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			before := snapshot()
			opposite := "items"
			if mode == "items" {
				opposite = "sources"
			}
			rejectedCassette := filepath.Join(dir, "must-not-create")
			wrong := append(append([]string{}, base...), "--resume", out, "--llm", "replay", "--reader-input", opposite, "--cassette-dir", rejectedCassette)
			if err := runResumeCLI(t, wrong); err == nil || !strings.Contains(err.Error(), "reader input mismatch") {
				t.Fatalf("wrong-input preflight: %v", err)
			}
			if !reflect.DeepEqual(before, snapshot()) || calls != 1 {
				t.Fatal("preflight changed files or made HTTP calls")
			}
			for _, p := range []string{rejectedCassette, filepath.Join(out, "resume-attempts"), filepath.Join(out, ".resume.lock")} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("unexpected side effect %s: %v", p, err)
				}
			}
			server.Close()
			t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
			same := append(append([]string{}, base...), "--resume", out, "--llm", "replay", "--resume-statuses", "scored")
			if err := runResumeCLI(t, same); err != nil {
				t.Fatalf("same-input CLI replay: %v", err)
			}
			next := readRun(t, out)
			if next.Protocol.ReaderInput != mode || next.ResumeCount != 1 || next.Cases[0].Status != result.Scored || calls != 1 {
				t.Fatal(next.ResumeHistory, next.Cases, calls)
			}
			b, _ := json.Marshal(next.ResumeHistory[0].Billing)
			if !bytes.Contains(b, []byte(`"cassette_hits":1`)) || !bytes.Contains(b, []byte(`"cost_usd_new":0`)) {
				t.Fatalf("not a zero-HTTP replay: %s", b)
			}
		})
	}
}
