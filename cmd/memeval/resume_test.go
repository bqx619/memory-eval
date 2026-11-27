package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"memeval/internal/benchmark"
	"memeval/internal/llm"
	"memeval/internal/registry"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func runResumeCLI(t *testing.T, args []string) error {
	t.Helper()
	var out, stderr bytes.Buffer
	err := run(t.Context(), args, &out, &stderr)
	return err
}
func readRun(t *testing.T, dir string) *result.Report {
	t.Helper()
	r, e := report.Load(t.Context(), filepath.Join(dir, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestResumeCLIRecordOnceReplayAndHistory(t *testing.T) {
	testoracle.ProtocolSources(t, "prefeval")
	t.Chdir(benchmark.Root())
	dir := t.TempDir()
	env := f18Config(t, dir)
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-key")
	s := &suite.Suite{SchemaVersion: "3.0", SuiteID: "f19-synthetic", ProtocolRef: "protocols/prefeval.json#explicit-mcq", Provenance: map[string]any{"source_url": "fixture://synthetic"}}
	ep := suite.Episode{ID: "ep", Events: []suite.Event{{SourceID: "source", Owner: "user", Role: "user", Content: "one is preferred"}}}
	for _, id := range []string{"ok", "empty", "retry"} {
		ep.Cases = append(ep.Cases, suite.Case{ID: id, Kind: "mcq", Query: id + " question?", User: "user", Category: "fixture", Cutoff: 1, Options: map[string]string{"A": "one", "B": "two", "C": "three", "D": "four"}, Gold: suite.Gold{Choice: "A", MCQ: &suite.MCQSpec{Parse: "prefeval_choice_tag"}}})
	}
	s.Episodes = []suite.Episode{ep}
	path := filepath.Join(dir, "suite")
	if e := suite.Pack(t.Context(), s, path); e != nil {
		t.Fatal(e)
	}
	calls := 0
	recover429 := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := json.Marshal(body["messages"])
		if strings.Contains(string(messages), "retry question") && !recover429 {
			w.WriteHeader(429)
			return
		}
		text := "<choice>A</choice>"
		if strings.Contains(string(messages), "empty question") {
			text = ""
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2}})
	}))
	defer server.Close()
	out := filepath.Join(dir, "run")
	cass := filepath.Join(dir, "cassettes")
	base := []string{"--env-file", env, "run", "--suite", path, "--llm-base-url", server.URL, "--cassette-dir", cass, "--llm-retries", "0"}
	args := append(append([]string{}, base...), "--out", out, "--sut", "bm25", "--mode", "fixed_reader", "--llm", "record-once", "--reader-model", "fixture", "--run-seed", strings.Repeat("ab", 32))
	if e := runResumeCLI(t, args); e == nil || !strings.Contains(e.Error(), "execution errors") {
		t.Fatal(e)
	}
	old := readRun(t, out)
	beforeReport := readFile(t, filepath.Join(out, "report.json"))
	beforeCases := readFile(t, filepath.Join(out, "cases.jsonl"))
	beforeLedger := readFile(t, filepath.Join(out, "llm_ledger.jsonl"))
	if calls != 3 || old.Summary.StatusCounts[result.Scored] != 1 || old.Summary.Invalid != 1 || old.Summary.ExecutionErrors != 1 {
		t.Fatal(calls, old.Summary)
	}
	recover429 = true
	args = append(append([]string{}, base...), "--resume", out, "--llm", "record-once")
	if e := runResumeCLI(t, args); e != nil {
		t.Fatal(e)
	}
	next := readRun(t, out)
	if calls != 4 || next.ResumeCount != 1 || next.ResumeHistory[0].RedoneCases != 2 || next.RunID != old.RunID || next.RunSeed != old.RunSeed || next.Summary.Invalid != 1 || next.Summary.StatusCounts[result.Scored] != 2 {
		t.Fatal(calls, next.ResumeHistory, next.Summary)
	}
	if next.Cases[1].PreviousStatus != result.Invalid || next.Cases[2].PreviousStatus != result.Error {
		t.Fatal("missing previous_status")
	}
	if got := bytes.Split(readFile(t, filepath.Join(out, "cases.jsonl")), []byte("\n"))[0]; !bytes.Equal(got, bytes.Split(beforeCases, []byte("\n"))[0]) {
		t.Fatal("reused row not byte-identical")
	}
	for name, b := range map[string][]byte{"report.json": beforeReport, "cases.jsonl": beforeCases, "llm_ledger.jsonl": beforeLedger} {
		if !bytes.Equal(readFile(t, filepath.Join(out, "resume-history/0001", name)), b) {
			t.Fatal("history changed", name)
		}
	}
	bill := next.Billing.Evaluator.(map[string]any)
	if bill["calls"] != float64(5) || bill["http_429"] != float64(1) || bill["cassette_hits"] != float64(1) {
		t.Fatal(bill)
	}
	server.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	args = append(append([]string{}, base...), "--resume", out, "--llm", "replay", "--resume-statuses", "scored")
	if e := runResumeCLI(t, args); e != nil {
		t.Fatal(e)
	}
	last := readRun(t, out)
	if last.ResumeCount != 2 || last.ResumeHistory[1].RedoneCases != 2 || calls != 4 {
		t.Fatal(last.ResumeHistory, calls)
	}
	attempt := last.ResumeHistory[1].Billing.(map[string]any)
	if attempt["cassette_hits"] != float64(2) || attempt["cost_usd_new"] != float64(0) || attempt["http_429"] != float64(0) {
		t.Fatal(attempt)
	}
	before := readFile(t, filepath.Join(out, "report.json"))
	if e := runResumeCLI(t, append(args, "--top-k", "9")); e == nil || !strings.Contains(e.Error(), "mismatch") {
		t.Fatal("budget guard", e)
	}
	if !bytes.Equal(before, readFile(t, filepath.Join(out, "report.json"))) {
		t.Fatal("rejection modified original")
	}
	if _, e := os.Stat(filepath.Join(out, ".resume.lock")); !os.IsNotExist(e) {
		t.Fatal("stale lock")
	}
}

var f19Run = flag.String("f19-run", "", "opt-in copied historical run in scratch (modified by resume)")
var f19Cassettes = flag.String("f19-cassettes", "", "opt-in copied historical cassettes in scratch")

func TestF19HistoricalResumeReplay(t *testing.T) {
	if *f19Run == "" || *f19Cassettes == "" {
		t.Skip("explicit scratch copy required")
	}
	root := benchmark.Root()
	t.Chdir(root)
	for _, p := range []string{*f19Run, *f19Cassettes} {
		abs, e := filepath.Abs(p)
		if e != nil || !filepath.IsAbs(p) || abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			t.Fatal("historical verification requires scratch paths")
		}
	}
	env := f18Config(t, t.TempDir())
	old := readRun(t, *f19Run)
	entries, err := registry.LoadDir(filepath.Join(root, "data/registry"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ref, ok := entries.LookupSuite(old.SuiteID); ok {
		testoracle.Dataset(t, filepath.Join(root, ref.Path))
	}
	if old.Summary.ExecutionErrors != 47 || len(old.Cases) != 1000 {
		t.Fatal("wrong historical input")
	}
	oldLines := bytes.Split(bytes.TrimSuffix(readFile(t, filepath.Join(*f19Run, "cases.jsonl")), []byte("\n")), []byte("\n"))
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	var dials atomic.Int64
	tr.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("F19 verification forbids HTTP")
	}
	original := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = original; tr.CloseIdleConnections() })
	err = runResumeCLI(t, []string{"--env-file", env, "run", "--resume", *f19Run, "--llm", "replay", "--cassette-dir", *f19Cassettes})
	if err != nil && !strings.Contains(err.Error(), "execution errors") {
		t.Fatal(err)
	}
	r := readRun(t, *f19Run)
	if r.ResumeCount != 1 || r.RunID != old.RunID || r.RunSeed != old.RunSeed || r.SuiteSHA256 != old.SuiteSHA256 || len(r.Cases) != 1000 || r.ResumeHistory[0].RedoneCases != 47 {
		t.Fatal("resume identity/selection", r.ResumeHistory)
	}
	newLines := bytes.Split(bytes.TrimSuffix(readFile(t, filepath.Join(*f19Run, "cases.jsonl")), []byte("\n")), []byte("\n"))
	reused := 0
	for i, c := range r.Cases {
		if c.PublicInputSHA256 != old.Cases[i].PublicInputSHA256 {
			t.Fatal("public input changed")
		}
		if old.Cases[i].Status == result.Scored {
			reused++
			if !bytes.Equal(oldLines[i], newLines[i]) {
				t.Fatal("reused line changed", i)
			}
		} else {
			if c.PreviousStatus != old.Cases[i].Status {
				t.Fatal("previous status missing")
			}
			if c.QA == nil || old.Cases[i].QA == nil || c.QA.ReaderRequestSHA != old.Cases[i].QA.ReaderRequestSHA {
				t.Fatal("historical reader cassette key changed", c.CaseID)
			}
		}
	}
	var bill llm.Billing
	b, _ := json.Marshal(r.ResumeHistory[0].Billing)
	json.Unmarshal(b, &bill)
	if reused != 953 || dials.Load() != 0 || bill.Calls != 47 || bill.CostNew == nil || *bill.CostNew != 0 {
		t.Fatal(reused, dials.Load(), bill)
	}
	evidence := map[string]any{"total": len(r.Cases), "reused_byte_identical": reused, "redone": 47, "http_dials": dials.Load(), "attempt_billing": bill, "statuses": r.Summary.StatusCounts, "suite_sha256": r.SuiteSHA256, "run_id_preserved": r.RunID == old.RunID, "run_seed_preserved": r.RunSeed == old.RunSeed}
	if err := report.WriteJSON(t.Context(), filepath.Join(filepath.Dir(*f19Run), "historical-resume-evidence.json"), evidence); err != nil {
		t.Fatal(err)
	}
	t.Logf("1000 cases; 953 byte-identical reused; 47 retried; HTTP=%d; new cost=0; remaining errors=%d", dials.Load(), r.Summary.ExecutionErrors)
}
