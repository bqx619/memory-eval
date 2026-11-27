package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"memeval/internal/benchmark"
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

var beamEvidence = flag.String("beam-evidence-dir", "", "optional NEW scratch directory for BEAM CLI offline acceptance")

func TestBEAMCLIRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "beam")
	root := benchmark.Root()
	dir := t.TempDir()
	if *beamEvidence != "" {
		dir = *beamEvidence
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("evidence directory must be new")
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
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "beam-test-key")
	t.Chdir(root)
	envfile := filepath.Join(dir, "empty-config")
	os.WriteFile(envfile, nil, 0600)
	s, e := suite.Open(t.Context(), testoracle.Dataset(t, "normalized/beam-100k@3205395e897e"))
	if e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(dir, "two-conversations")
	w, e := suite.NewWriter(out)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Abort()
	questions := map[string]string{}
	goldScores := map[string]float64{}
	count := 0
	stop := errors.New("two conversations")
	e = s.Iter(t.Context(), func(ep *suite.Episode) error {
		if count == 2 {
			return stop
		}
		count++
		for _, c := range ep.Cases {
			answer := "fixture answer"
			if c.Category == "event_ordering" {
				answer = strings.Join(c.Gold.Sequence, "\n")
			}
			questions[c.Query] = answer
			sum := 0.
			for i, item := range c.Gold.Rubric.Items {
				v := float64(i%3) / 2
				key := item.Text + "\x00" + answer
				goldScores[key] = v
				sum += v
			}
			_ = sum
		}
		return w.WriteEpisode(ep)
	})
	if e != nil && !errors.Is(e, stop) {
		t.Fatal(e)
	}
	if e = w.Close(s); e != nil {
		t.Fatal(e)
	}
	var calls, dials atomic.Int64
	old := http.DefaultTransport
	transport := old.(*http.Transport).Clone()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("non-loopback forbidden")
		}
		return dial(ctx, network, address)
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var b struct {
			State     string                     `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
			Model     string                     `json:"model"`
			Messages  []llm.Message              `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
			return
		}
		if len(b.Questions) > 0 {
			var state map[string]string
			json.Unmarshal([]byte(b.State), &state)
			testdecision.Wire(w, b.Model, b.Questions, func(name, kind string) any {
				if kind == "noul" {
					if state["first_snippet"] == state["second_snippet"] {
						return 1.
					}
					return 0.
				}
				v, ok := goldScores[state["rubric_item"]+"\x00"+state["llm_response"]]
				if !ok {
					t.Error("rubric slots changed")
				}
				return int(v * 2)
			})
			return
		}
		text := ""
		last := b.Messages[len(b.Messages)-1].Content
		if b.Model == "gpt-6-luna" {
			_, query, ok := strings.Cut(last, "Question: ")
			var exists bool
			text, exists = questions[query]
			if !ok || !exists {
				t.Error("reader altered query")
				http.Error(w, "unknown query", 400)
				return
			}
		}

		json.NewEncoder(w).Encode(map[string]any{"model": b.Model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 20, CompletionTokens: 3}})
	}))
	defer srv.Close()
	execute := func(mode string) *result.Report {
		dest := filepath.Join(dir, mode)
		args := []string{"--env-file", envfile, "run", "--suite", out, "--sut", "bm25", "--mode", "fixed_reader", "--reader-model", "gpt-6-luna", "--reader-provider", "openai-compat", "--llm", mode, "--llm-base-url", srv.URL + "/v1", "--cassette-dir", filepath.Join(dir, "cassettes"), "--llm-retries", "0", "--run-seed", strings.Repeat("73", 32), "--out", dest}
		var stdout, stderr bytes.Buffer
		if e := runCommand(t.Context(), args, &stdout, &stderr); e != nil {
			t.Fatalf("%v %s %s", e, stdout.String(), stderr.String())
		}
		b, e := os.ReadFile(filepath.Join(dest, "report.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r result.Report
		if e = json.Unmarshal(b, &r); e != nil {
			t.Fatal(e)
		}
		if r.Protocol.ResetPolicy != "incremental_per_episode" {
			t.Fatal("BEAM default reset policy", r.Protocol.ResetPolicy)
		}
		return &r
	}
	a := execute("record")
	n := calls.Load()
	before := dials.Load()
	srv.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	b := execute("replay")
	if len(a.Cases) != 40 || len(b.Cases) != 40 {
		t.Fatal("expected all questions in two conversations", len(a.Cases))
	}
	phase := map[string]int{}
	for i, x := range a.Cases {
		y := b.Cases[i]
		for k := range x.Timings {
			phase[k]++
		}
		if x.Status != result.Scored || x.Metrics["beam.llm_judge_score"] == nil {
			t.Fatal(x.CaseID, x.Status, x.ErrorType)
		}
		if x.Category == "event_ordering" {
			if v := x.Metrics["beam.event_ordering.final"]; v == nil || *v != 1 {
				t.Fatal("ordering identity must be 1", v)
			}
		}
		x.Timings = nil
		y.Timings = nil
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("case %d replay mismatch", i)
		}
	}
	if calls.Load() != n || dials.Load() != before {
		t.Fatal("replay HTTP")
	}
	officialBytes, _ := json.Marshal(b.Scores.Official)
	var official result.Official
	json.Unmarshal(officialBytes, &official)
	if len(official.ByTypeMetrics) != 10 {
		t.Fatal("per-category metrics")
	}
	if phase["reset_ms"] != 2 || phase["ingest_ms"] != 2 || phase["ready_ms"] != 2 || phase["query_ms"] != 40 {
		t.Fatal(phase)
	}
	billingBytes, _ := json.Marshal(b.Billing.Evaluator)
	var bill llm.Billing
	json.Unmarshal(billingBytes, &bill)
	if bill.CostNew == nil || *bill.CostNew != 0 || bill.CassetteHits != int(n) {
		t.Fatal("new cost or cassette misses", bill)
	}
	proof := map[string]any{"cases": 40, "conversations": 2, "phase_counts": phase, "record_http": n, "replay_http": 0, "replay_dials": 0, "new_usd": 0, "case_differences_excluding_timings": 0, "official_metrics": official.Metrics, "by_category": official.ByTypeMetrics}
	bb, _ := json.MarshalIndent(proof, "", "  ")
	os.WriteFile(filepath.Join(dir, "proof.json"), append(bb, '\n'), 0600)
	t.Logf("two complete conversations / 40 cases; %d record calls; replay 0 HTTP/0 dial/0 new USD; zero case differences", n)
}
