package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"memeval/internal/benchmark"
	importer "memeval/internal/importers/halumem"
	"memeval/internal/llm"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var halumemEvidence = flag.String("halumem-evidence-dir", "", "optional NEW scratch directory for HaluMem offline evidence")
var halumemRealInput = flag.String("halumem-real-input", "", "optional locked local ND input; select a user with updates in first three sessions, artifacts only in scratch")

func TestHaluMemCLIRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "halumem")
	root := benchmark.Root()
	dir := t.TempDir()
	if *halumemEvidence != "" {
		dir = *halumemEvidence
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("new evidence directory required")
		}
		rel, _ := filepath.Rel(root, dir)
		if filepath.IsLocal(rel) {
			t.Fatal("HaluMem evidence must stay outside repository")
		}
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, env := range os.Environ() {
		k, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(k, "MEMEVAL_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "p5-fixture-key")
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "halumem-fake-key")
	t.Chdir(root)
	envfile := filepath.Join(dir, "empty-config")
	os.WriteFile(envfile, nil, 0600)
	input := filepath.Join(root, "internal/importers/halumem/testdata/synthetic.jsonl")
	synthetic := true
	if *halumemRealInput != "" {
		absolute, e := filepath.Abs(*halumemRealInput)
		if e != nil {
			t.Fatal(e)
		}
		realInput := testoracle.Dataset(t, absolute)
		if _, e := importer.Verify(realInput, "medium"); e != nil {
			t.Fatal(e)
		}
		f, e := os.Open(realInput)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		dec := json.NewDecoder(f)
		var selected *importer.User
		for {
			var u importer.User
			e = dec.Decode(&u)
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			n := 0
			for _, ss := range u.Sessions[:min(3, len(u.Sessions))] {
				for _, p := range ss.Points {
					if p.IsUpdate == "True" && len(p.Originals) > 0 {
						n++
					}
				}
			}
			if n > 0 {
				u.Sessions = u.Sessions[:3]
				selected = &u
				break
			}
		}
		if selected == nil {
			t.Fatal("no user with update in first 3 sessions")
		}
		b, _ := json.Marshal(selected)
		input = filepath.Join(dir, "local-only-three-sessions.jsonl")
		if e = os.WriteFile(input, append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
		synthetic = false
	}
	imported, e := importer.ImportFile(t.Context(), importer.Options{Input: input, Variant: "medium", Out: filepath.Join(dir, "bundle")}, "acceptance-input", synthetic)
	if e != nil {
		t.Fatal(e)
	}
	s, e := suite.Open(t.Context(), imported.Path)
	if e != nil {
		t.Fatal(e)
	}
	interference := []string{}
	goldN, updates, questions := 0, 0, 0
	if e = s.Iter(t.Context(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			if c.Kind == "qa" {
				questions++
			}
			for _, p := range c.Gold.MemoryPoints {
				goldN++
				if p.IsUpdate && len(p.UpdateOf) > 0 {
					updates++
				}
				if p.Polarity == "should_not_exist" {
					interference = append(interference, p.Content)
				}
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	var calls, dials atomic.Int64
	old := http.DefaultTransport
	tr := old.(*http.Transport).Clone()
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("external network forbidden")
		}
		return dial(ctx, network, address)
	}
	http.DefaultTransport = tr
	defer func() { http.DefaultTransport = old; tr.CloseIdleConnections() }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var b struct {
			State       string                     `json:"state"`
			Questions   map[string]json.RawMessage `json:"questions"`
			Model       string
			Messages    []llm.Message
			Temperature *float64
			Tokens      *int `json:"max_completion_tokens"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
			return
		}
		if len(b.Questions) > 0 {
			var state map[string]string
			json.Unmarshal([]byte(b.State), &state)
			testdecision.Wire(w, b.Model, b.Questions, func(name, kind string) any {
				if name == "included" {
					return "true"
				}
				if _, ok := state["expected_memory_point"]; ok {
					for _, target := range interference {
						if state["expected_memory_point"] == target {
							return "0"
						}
					}
					return "2"
				}
				if _, ok := state["candidate_memory"]; ok {
					return "2"
				}
				return "Correct"
			})
			return
		}
		if len(b.Messages) != 1 {
			t.Error("single user prompt")
		}
		text := "simulated concise answer"
		json.NewEncoder(w).Encode(map[string]any{"model": b.Model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 20, CompletionTokens: 4}})
	}))
	defer srv.Close()
	execute := func(mode string) *result.Report {
		dest := filepath.Join(dir, mode)
		args := []string{"--env-file", envfile, "run", "--suite", imported.Path, "--sut", "bm25", "--mode", "fixed_reader", "--reset-policy", "incremental_per_episode", "--reader-model", "gpt-6-luna", "--reader-provider", "openai-compat", "--llm", mode, "--llm-base-url", srv.URL + "/v1", "--cassette-dir", filepath.Join(dir, "cassettes"), "--llm-retries", "0", "--run-seed", strings.Repeat("84", 32), "--out", dest}
		var out, errout bytes.Buffer
		if e := runCommand(t.Context(), args, &out, &errout); e != nil {
			t.Fatalf("%v %s %s", e, out.String(), errout.String())
		}
		b, e := os.ReadFile(filepath.Join(dest, "report.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r result.Report
		if e = json.Unmarshal(b, &r); e != nil {
			t.Fatal(e)
		}
		return &r
	}
	a := execute("record")
	n, nd := calls.Load(), dials.Load()
	srv.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	b := execute("replay")
	if len(a.Cases) != questions+3 || len(b.Cases) != len(a.Cases) {
		t.Fatal("case count")
	}
	phase := map[string]int{}
	newCounts := []int{}
	for i, c := range a.Cases {
		d := b.Cases[i]
		if c.Status != result.Scored {
			t.Fatal(i, c.CaseID, c.Status, c.ErrorType, c.Note)
		}
		for k := range c.Timings {
			phase[k]++
		}
		if c.Kind == "memory_audit" {
			var v struct {
				NewCount int `json:"new_count"`
			}
			json.Unmarshal(c.Audit, &v)
			newCounts = append(newCounts, v.NewCount)
		}
		c.Timings = nil
		d.Timings = nil
		if !reflect.DeepEqual(c, d) {
			t.Fatal("replay case differs", i)
		}
	}
	if phase["reset_ms"] != 1 || phase["ingest_ms"] != 3 || phase["ready_ms"] != 3 || phase["audit_ms"] != 3 || phase["query_ms"] != questions {
		t.Fatal(phase)
	}
	bb, _ := json.Marshal(b.Scores.Official)
	var official result.Official
	json.Unmarshal(bb, &official)
	if official.QuestionSet.Total != 3467 || official.QuestionSet.Complete {
		t.Fatal("subset must not claim complete benchmark", official.QuestionSet)
	}
	for _, k := range []string{"halumem.memory_integrity.recall(all)", "halumem.memory_accuracy.target_accuracy(all)", "halumem.memory_extraction_f1", "halumem.memory_update.correct_update_memory_ratio(all)", "halumem.question_answering.correct_qa_ratio(all)"} {
		if official.Metrics[k] == nil || *official.Metrics[k] != 1 {
			t.Fatal(k, official.Metrics[k])
		}
	}
	if synthetic {
		if *official.Metrics["halumem.memory_accuracy.interference_accuracy(all)"] != 1 || !reflect.DeepEqual(newCounts, []int{2, 2, 2}) {
			t.Fatal("synthetic hand score/diff")
		}
	}
	bb, _ = json.Marshal(b.Billing.Evaluator)
	var bill llm.Billing
	json.Unmarshal(bb, &bill)
	if bill.CostNew == nil || *bill.CostNew != 0 || bill.CassetteHits != int(n) || calls.Load() != n || dials.Load() != nd {
		t.Fatal("replay HTTP/cost", bill)
	}
	proof := map[string]any{"synthetic": synthetic, "sessions": 3, "cases": len(a.Cases), "questions": questions, "gold_points": goldN, "update_points": updates, "new_dump_counts": newCounts, "phase_counts": phase, "record_http": n, "replay_http": 0, "replay_dials": 0, "new_usd": 0, "case_differences_excluding_timings": 0, "metrics": official.Metrics, "by_question_type": official.ByTypeMetrics}
	bb, _ = json.MarshalIndent(proof, "", "  ")
	if e = os.WriteFile(filepath.Join(dir, "proof.json"), append(bb, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("synthetic=%v: 3 sessions / %d cases / %d gold points / %d updates; record %d calls; replay 0 HTTP/dials/new USD, 0 differences", synthetic, len(a.Cases), goldN, updates, n)
}
