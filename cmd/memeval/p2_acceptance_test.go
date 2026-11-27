package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/importers/locomo"
	"memeval/internal/importers/personamem"
	"memeval/internal/llm"
	"memeval/internal/registry"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var p2Output = flag.String("p2-acceptance-out", "", "run full P2 data against httptest and preserve record/replay in a NEW directory")

func TestP2OfflineAcceptance(t *testing.T) {
	root := dotenv.Root(".")
	t.Chdir(root)
	out := t.TempDir()
	full := *p2Output != ""
	if full {
		out = *p2Output
		if _, e := os.Stat(out); !os.IsNotExist(e) {
			t.Fatal("acceptance destination must not exist", out)
		}
		if e := os.MkdirAll(out, 0700); e != nil {
			t.Fatal(e)
		}
	}
	envFile := filepath.Join(t.TempDir(), "empty-config")
	os.WriteFile(envFile, nil, 0600)
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "p2-local-test-only")
	for _, b := range dotenv.Bindings {
		if strings.HasPrefix(b.Env, "MEMEVAL_") {
			t.Setenv(b.Env, "")
			os.Unsetenv(b.Env)
		}
	}
	for _, task := range []struct{ name, sut string }{{"locomo10", "fullcontext"}, {"personamem-v1-32k", "fullcontext"}, {"personamem-v1-32k", "bm25"}} {
		name, sutName := task.name, task.sut
		label := name + "-" + sutName
		t.Run(label, func(t *testing.T) {
			id := "personamem"
			if name == "locomo10" {
				id = "locomo"
			}
			testoracle.ProtocolSources(t, id)
			path := filepath.Join(root, "data/normalized", name+".json")
			if full {
				r, err := registry.LoadDir(filepath.Join(root, "data/registry"))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range r {
					for _, ref := range entry.Suites {
						if strings.HasPrefix(ref.SuiteID, name+"@") && !strings.HasSuffix(ref.SuiteID, "-nopersona") {
							path = filepath.Join(root, ref.Path)
						}
					}
				}
				path = testoracle.Dataset(t, path)
			}
			expected := 1986
			if name != "locomo10" {
				expected = 589
			}
			if !full {
				path = filepath.Join(out, label+".json")
				if name == "locomo10" {
					s, stats, e := locomo.Import(t.Context(), "internal/importers/locomo/testdata/synthetic_locomo.json")
					if e != nil {
						t.Fatal(e)
					}
					if e = saveImport(t.Context(), path, s, stats); e != nil {
						t.Fatal(e)
					}
					expected = stats.Questions
				} else {
					s, stats, e := personamem.Import(t.Context(), "internal/importers/personamem/testdata/questions.csv", "internal/importers/personamem/testdata/contexts.jsonl", "32k", "include")
					if e != nil {
						t.Fatal(e)
					}
					if e = saveImport(t.Context(), path, s, stats); e != nil {
						t.Fatal(e)
					}
					expected = stats.Questions
				}
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" {
					t.Error("unexpected provider endpoint")
				}
				var q struct {
					Model    string        `json:"model"`
					Messages []llm.Message `json:"messages"`
				}
				if e := json.NewDecoder(r.Body).Decode(&q); e != nil {
					t.Error(e)
				}
				if q.Model != "p2-fixture-reader" {
					t.Error("judge or unexpected model called", q.Model)
				}
				answer := "blue, three; not mentioned"
				if name != "locomo10" {
					answer = "<final_answer>(c)"
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "model": q.Model, "choices": []any{map[string]any{"index": 0, "message": llm.Message{Role: "assistant", Content: answer}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 0, CompletionTokens: 0}, "system_fingerprint": "httptest-no-model"})
			}))
			defer server.Close()
			args := []string{"--env-file", envFile, "run", "--suite", path, "--sut", sutName, "--mode", "fixed_reader", "--reader-model", "p2-fixture-reader", "--reader-provider", "openai-compat", "--reader-input", "sources", "--reader-max-tokens", "512", "--max-context-chars", "1000000", "--run-seed", strings.Repeat("42", 32), "--llm-base-url", server.URL + "/v1", "--llm-retries", "0", "--cassette-dir", filepath.Join(out, label+"-cassettes")}
			paths := []string{}
			var all []*result.Report
			for _, mode := range []string{"record-once", "replay"} {
				if mode == "replay" {
					server.Close()
				}
				dest := filepath.Join(out, label+"-"+mode)
				paths = append(paths, dest)
				var stdout, stderr bytes.Buffer
				e := run(context.Background(), append(append([]string{}, args...), "--llm", mode, "--out", dest), &stdout, &stderr)
				if e != nil {
					t.Fatalf("%s: %v %s", mode, e, stderr.String())
				}
				os.WriteFile(filepath.Join(out, label+"-"+mode+".log"), stdout.Bytes(), 0600)
				r, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
				if e != nil {
					t.Fatal(e)
				}
				if len(r.Cases) != expected || r.Summary.ExecutionErrors != 0 {
					t.Fatalf("coverage/errors %+v", r.Summary)
				}
				for _, c := range r.Cases {
					if c.Status != result.Scored || c.QA == nil || c.QA.JudgeLabel != nil {
						t.Fatal("deterministic scoring not reached", c.Status)
					}
					if name != "locomo10" && (c.AnswerParse == nil || c.AnswerParse.RawFinal != "(c)") {
						t.Fatal("parse audit missing")
					}
				}
				all = append(all, r)
			}
			if calls.Load() != int64(expected) {
				t.Fatal("extra HTTP or missing reader calls", calls.Load(), expected)
			}
			for _, r := range all {
				for i := range r.Cases {
					r.Cases[i].Timings = nil
				}
			}
			if !reflect.DeepEqual(all[0].Cases, all[1].Cases) || !reflect.DeepEqual(all[0].Scores, all[1].Scores) {
				t.Fatal("record/replay per-case or score drift")
			}
			ledger, e := os.ReadFile(filepath.Join(paths[1], "llm_ledger.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			hits := 0
			for _, line := range bytes.Split(bytes.TrimSpace(ledger), []byte{'\n'}) {
				var row llm.LedgerEntry
				if e = json.Unmarshal(line, &row); e != nil {
					t.Fatal(e)
				}
				if !row.CassetteHit || row.Purpose != "reader" || row.Error != "" || row.CostNew == nil || *row.CostNew != 0 {
					t.Fatal("replay used HTTP, judge or failed", string(line))
				}
				hits++
			}
			if hits != expected {
				t.Fatal("cassette hits", hits)
			}
			summary := map[string]any{"suite": name, "sut": sutName, "cases": expected, "record_http_calls": calls.Load(), "replay_http_calls": 0, "replay_cassette_hits": hits, "per_case_equal": true, "scores_equal": true, "scores": all[1].Scores, "note": "httptest synthetic responses; no live model or real cost"}
			b, _ := json.MarshalIndent(summary, "", "  ")
			if e = os.WriteFile(filepath.Join(out, label+"-acceptance.json"), append(b, '\n'), 0600); e != nil {
				t.Fatal(e)
			}
			t.Logf("%s: %d/%d identical; HTTP record=%d replay=0; reader only", name, expected, expected, calls.Load())
		})
	}
	fmt.Fprintln(os.Stdout, "P2 offline artifacts:", out)
}
