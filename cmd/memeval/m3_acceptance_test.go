package main

import (
	"bytes"
	"encoding/json"
	"memeval/internal/llm"
	"memeval/internal/result"
	"memeval/internal/testdecision"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Retain the documented optional acceptance switches, now exercising only Jev.
func TestM2bOfflineAcceptance(t *testing.T) {
	if os.Getenv("MEMEVAL_M2B_ACCEPTANCE") != "1" {
		t.Skip("set MEMEVAL_M2B_ACCEPTANCE=1 for reader/Jev metadata and trace round-trip")
	}
	TestRunPromptTraceAndEvaluatorMetadata(t)
}
func TestM3OfflineAcceptance(t *testing.T) {
	if os.Getenv("MEMEVAL_M3_ACCEPTANCE") != "1" {
		t.Skip("set MEMEVAL_M3_ACCEPTANCE=1 for TypeSafe decide acceptance")
	}
	TestTypeSafeDecideCLIRecordReplay(t)
}
func TestTypeSafeDecideCLIRecordReplay(t *testing.T) {
	dir := t.TempDir()
	envfile := filepath.Join(dir, "empty-config")
	os.WriteFile(envfile, nil, 0600)
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "fixture-typesafe-key")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b struct {
			Model     string
			Questions map[string]json.RawMessage
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		testdecision.Wire(w, b.Model, b.Questions, func(_ string, kind string) any {
			switch kind {
			case "noul":
				return .8
			case "score":
				return 2
			default:
				return "keep"
			}
		})
	}))
	defer server.Close()
	invoke := func(mode string) *result.Report {
		out := filepath.Join(dir, mode)
		var stdout, stderr bytes.Buffer
		e := runCommand(t.Context(), []string{"--env-file", envfile, "decide", "--provider", "typesafe", "--model", "jev-1.14.0", "--input", "../../testdata/decision/closed-rubric.json", "--fixture", "--repeats", "3", "--run-seed", strings.Repeat("42", 32), "--llm", mode, "--llm-base-url", server.URL, "--cassette-dir", filepath.Join(dir, "cassettes"), "--out", out}, &stdout, &stderr)
		if e != nil {
			t.Fatal(e, stderr.String())
		}
		b, e := os.ReadFile(filepath.Join(out, "report.json"))
		if e != nil {
			t.Fatal(e)
		}
		var report result.Report
		if json.Unmarshal(b, &report) != nil {
			t.Fatal("report")
		}
		return &report
	}
	a := invoke("record")
	server.Close()
	b := invoke("replay")
	if calls != 3 || len(a.Cases) != 3 {
		t.Fatal(calls, len(a.Cases))
	}
	for i := range a.Cases {
		a.Cases[i].Timings = nil
		b.Cases[i].Timings = nil
	}
	if !reflect.DeepEqual(a.Cases, b.Cases) {
		t.Fatal("decide replay changed")
	}
	raw, _ := json.Marshal(b.Billing.Evaluator)
	var bill llm.Billing
	json.Unmarshal(raw, &bill)
	if bill.CassetteHits != 3 || bill.CostNew == nil || *bill.CostNew != 0 {
		t.Fatal(bill)
	}
}
