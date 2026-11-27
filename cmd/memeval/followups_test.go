package main

import (
	"bytes"
	"context"
	"encoding/json"
	"memeval/internal/llm"
	"memeval/internal/report"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunPromptTraceAndEvaluatorMetadata(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "p5-fixture-key")
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "followup-fixture-key")
	dir := t.TempDir()
	source := &suite.Suite{
		SchemaVersion: "3.0", ProtocolRef: "protocols/longmemeval.json#s", SuiteID: "followup-fixture",
		Provenance: map[string]any{"source_url": "fixture://offline"},
		Episodes: []suite.Episode{{
			ID:     "episode",
			Events: []suite.Event{{SourceID: "source", Owner: "user", Role: "user", Session: "session", Content: "private-history-marker blue box", Timestamp: "2024-01-01T00:00:00Z"}},
			Cases:  []suite.Case{{ID: "question", Kind: "qa", Query: "What color is the box?", User: "user", QuestionType: "single-session-user", QueryTime: "2024-02-01T00:00:00Z", Cutoff: 1, Gold: suite.Gold{EvidenceIDs: []string{"source"}, Answers: []string{"private-gold-marker blue"}}}},
		}},
	}
	sourcePath := filepath.Join(dir, "suite.json")
	if err := report.WriteJSON(context.Background(), sourcePath, source); err != nil {
		t.Fatal(err)
	}
	requests := map[string][]llm.Message{}
	responses := map[string]string{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State     string                     `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
			Model     string                     `json:"model"`
			Messages  []llm.Message              `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if len(req.Questions) > 0 {
			requests[req.Model] = nil
			responses[req.Model] = req.State
			testdecision.Wire(w, req.Model, req.Questions, func(string, string) any { return 1. })
			return
		}
		requests[req.Model] = req.Messages
		answer := "  blue\n"
		responses[req.Model] = answer
		json.NewEncoder(w).Encode(map[string]any{"model": req.Model + "-fixed", "choices": []any{map[string]any{"message": llm.Message{Role: "assistant", Content: answer}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 10, CompletionTokens: 2}})
	}))
	defer s.Close()
	cassettes := filepath.Join(dir, "cassettes")
	args := []string{"run", "--suite", sourcePath, "--sut", "fullcontext", "--mode", "fixed_reader", "--reader-model", "fixture-reader", "--llm-base-url", s.URL + "/v1", "--cassette-dir", cassettes, "--run-seed", strings.Repeat("42", 32)}
	invoke := func(mode, dest string, trace bool) {
		t.Helper()
		command := append(append([]string{}, args...), "--llm", mode, "--out", dest)
		if trace {
			command = append(command, "--trace-prompts")
		}
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), command, &stdout, &stderr); err != nil {
			t.Fatal(err, stderr.String())
		}
	}
	recordDir, replayDir := filepath.Join(dir, "record"), filepath.Join(dir, "replay")
	invoke("record", recordDir, false)
	if _, err := os.Stat(filepath.Join(recordDir, "prompts.jsonl")); !os.IsNotExist(err) {
		t.Fatal("default wrote prompts", err)
	}
	s.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	invoke("replay", replayDir, true)
	a, err := report.Load(context.Background(), filepath.Join(recordDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := report.Load(context.Background(), filepath.Join(replayDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Cases {
		a.Cases[i].Timings = nil
		b.Cases[i].Timings = nil
	}
	if !reflect.DeepEqual(a.Cases, b.Cases) || !reflect.DeepEqual(a.Scores, b.Scores) {
		t.Fatal("tracing changed scoring")
	}
	ma, mb := a.EvaluatorLLM.(map[string]any), b.EvaluatorLLM.(map[string]any)
	want := []any{map[string]any{"provider": "openai-compat", "model": "fixture-reader", "model_returned": "fixture-reader-fixed"}, map[string]any{"provider": "typesafe", "model": "jev-1.14.0", "model_returned": "jev-1.14.0"}}
	if !reflect.DeepEqual(ma["models_pinned"], want) || !reflect.DeepEqual(mb["models_pinned"], want) || len(ma["cassette_set"].(string)) != 64 || ma["cassette_set"] != mb["cassette_set"] || mb["cassette_files"] != float64(2) || ma["trace_prompts"] != false || mb["trace_prompts"] != true {
		t.Fatal(ma, mb)
	}
	tracePath := filepath.Join(replayDir, "prompts.jsonl")
	info, err := os.Stat(tracePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(rows) != 2 || bytes.Contains(data, []byte("followup-fixture-key")) {
		t.Fatal("trace row count/key")
	}
	for i, line := range rows {
		var row llm.PromptTrace
		if json.Unmarshal(line, &row) != nil || !row.CassetteHit || !reflect.DeepEqual(row.Messages, requests[row.Model]) || (row.Purpose == "reader" && row.ResponseText != responses[row.Model]) || row.RunID != b.RunID {
			t.Fatal(row)
		}
		if row.Purpose != []string{"reader", "judge-decision"}[i] {
			t.Fatal(row.Purpose)
		}
	}
	if !strings.Contains(requests["fixture-reader"][0].Content, "private-history-marker") || !strings.Contains(responses["jev-1.14.0"], "private-gold-marker") {
		t.Fatal("fixture lacks full prompt evidence")
	}
	files, _ := filepath.Glob(filepath.Join(cassettes, "*/*/*.*"))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data, []byte("private-history-marker")) || bytes.Contains(data, []byte("private-gold-marker")) {
			t.Fatal("cassette acquired prompt", err)
		}
	}
}
