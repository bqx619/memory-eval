package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"memeval/internal/config/dotenv"
	"memeval/internal/llm"
	"memeval/internal/report"
	"memeval/internal/suite"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestObservedWireDecisionReportReplay(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0600)
	stageReaderProtocol(t, root)
	t.Chdir(root)
	for _, binding := range dotenv.Bindings {
		t.Setenv(binding.Env, "")
		os.Unsetenv(binding.Env)
	}
	for _, key := range []string{"MEMEVAL_OPENAI_API_KEY", "MEMEVAL_OPENAI_BASE_URL", "MEMEVAL_TYPESAFE_API_KEY", "MEMEVAL_TYPESAFE_BASE_URL"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	var openCalls, nativeCalls atomic.Int32
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openCalls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["n"] != float64(1) || body["max_tokens"] != nil || body["max_completion_tokens"] == nil {
			t.Error("chat wire shape")
		}
		if _, exists := body["temperature"]; exists {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"unsupported_value","param":"temperature"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": body["model"], "system_fingerprint": nil, "choices": []any{map[string]any{"message": map[string]string{"content": "yes"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "completion_tokens_details": map[string]int{"reasoning_tokens": 2}}})
	}))
	defer openai.Close()
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nativeCalls.Add(1)
		var body struct{ Questions map[string]map[string]any }
		json.NewDecoder(r.Body).Decode(&body)
		q := body.Questions["correct"]
		if len(q) != 2 || q["type"] != "noul" || q["instructions"] == nil {
			t.Error("noul request shape")
		}
		fmt.Fprint(w, `{"model":"jev-1.14.0","answers":{"correct":{"type":"noul","noul":0.4}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer native.Close()
	envPath := filepath.Join(root, "fixture.env")
	os.WriteFile(envPath, []byte(fmt.Sprintf("MEMEVAL_OPENAI_API_KEY=fixture-open\nMEMEVAL_OPENAI_BASE_URL=%s/v1\nMEMEVAL_TYPESAFE_API_KEY=fixture-native\nMEMEVAL_TYPESAFE_BASE_URL=%s\n", openai.URL, native.URL)), 0600)
	source := suite.Suite{SchemaVersion: "3.0", ProtocolRef: "protocols/longmemeval.json#s", SuiteID: "wire-fixture", Provenance: map[string]any{"source_url": "fixture://local"}, Episodes: []suite.Episode{{ID: "episode", Events: []suite.Event{{SourceID: "source", Owner: "user", Role: "user", Session: "session", Content: "history", Timestamp: "2024-01-01T00:00:00Z"}}, Cases: []suite.Case{{ID: "case", Kind: "qa", Query: "question", User: "user", QuestionType: "single-session-user", QueryTime: "2024-02-01T00:00:00Z", Cutoff: 1, Gold: suite.Gold{EvidenceIDs: []string{"source"}, Answers: []string{"reference"}}}}}}}
	sourcePath := filepath.Join(root, "suite.json")
	if err := report.WriteJSON(context.Background(), sourcePath, source); err != nil {
		t.Fatal(err)
	}
	invoke := func(mode string) {
		t.Helper()
		var out, errs bytes.Buffer
		args := []string{"--env-file", envPath, "run", "--suite", sourcePath, "--mode", "fixed_reader", "--sut", "null", "--run-seed", strings.Repeat("37", 32), "--llm", mode, "--llm-retries", "0", "--cassette-dir", filepath.Join(root, "cassettes"), "--out", filepath.Join(root, mode)}
		if err := run(context.Background(), args, &out, &errs); err != nil {
			t.Fatal(err, errs.String())
		}
	}
	invoke("record")
	if openCalls.Load() != 2 || nativeCalls.Load() != 1 {
		t.Fatal("expected one reader temperature negotiation", openCalls.Load(), nativeCalls.Load())
	}
	openai.Close()
	native.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "")
	invoke("replay")
	a, err := report.Load(context.Background(), filepath.Join(root, "record", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := report.Load(context.Background(), filepath.Join(root, "replay", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.Cases[0].Timings = nil
	b.Cases[0].Timings = nil
	if !reflect.DeepEqual(a.Cases, b.Cases) || !reflect.DeepEqual(a.Scores, b.Scores) {
		t.Fatal("negotiated report replay differs")
	}
	encoded, _ := json.Marshal(b.Scores.Official)
	if !strings.Contains(string(encoded), "temperature unavailable; provider default") {
		t.Fatal("missing official deviation")
	}
	for _, mode := range []string{"record", "replay"} {
		data, err := os.ReadFile(filepath.Join(root, mode, "llm_ledger.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		rows := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
		wantRows := 3
		if mode == "replay" {
			wantRows = 2
		}
		if len(rows) != wantRows {
			t.Fatal("ledger attempts", len(rows))
		}
		reasoning := 0
		for _, line := range rows {
			var entry llm.LedgerEntry
			if json.Unmarshal(line, &entry) != nil {
				t.Fatal("ledger JSON")
			}
			reasoning += entry.ReasoningTokens
			if mode == "replay" && (!entry.CassetteHit || entry.CostNew == nil || *entry.CostNew != 0) {
				t.Fatal("replay billing")
			}
			if entry.Provider == "typesafe" && (len(entry.Decision.Confidence) != 0 || len(entry.Decision.ConfidenceDifference) != 0 || math.Abs(entry.Decision.CalculatedConfidence["correct"]-.2) > 1e-12) {
				t.Fatal("noul calibration")
			}
		}
		if reasoning != 2 {
			t.Fatal("reasoning usage", reasoning)
		}
	}
	if openCalls.Load() != 2 || nativeCalls.Load() != 1 {
		t.Fatal("replay contacted a provider")
	}
}
