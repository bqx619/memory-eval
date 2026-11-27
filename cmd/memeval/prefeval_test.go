package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"memeval/internal/benchmark"
	"memeval/internal/llm"
	reader "memeval/internal/reader/prefeval"
	"memeval/internal/result"
	score "memeval/internal/scoring/prefeval"
	"memeval/internal/suite"
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

var prefEvalEvidence = flag.String("prefeval-evidence-dir", "", "optional NEW scratch directory for PrefEval offline acceptance")

func TestPrefEvalCLIRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "prefeval")
	root := benchmark.Root()
	dir := t.TempDir()
	if *prefEvalEvidence != "" {
		dir = *prefEvalEvidence
		abs, e := filepath.Abs(dir)
		if e != nil {
			t.Fatal(e)
		}
		rel, e := filepath.Rel(root, abs)
		if e != nil || rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatal("evidence must be outside repository")
		}
		if _, e = os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("evidence directory must be new")
		}
		if e = os.MkdirAll(dir, 0700); e != nil {
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
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "prefeval-fake-openai")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "prefeval-fake-jev")
	t.Chdir(root)
	envfile := filepath.Join(dir, "empty-config")
	os.WriteFile(envfile, nil, 0600)
	paths := map[string]string{}
	cases := map[string][]suite.Case{}
	readerOutputs := map[string]string{}
	type probeAnswer struct {
		probe string
		n     int
		yes   bool
	}
	probes := map[string]probeAnswer{}
	for _, task := range []string{"generation", "mcq"} {
		path := testoracle.Dataset(t, "normalized/prefeval-explicit-"+task+"-t10@50795054b5ff")
		s, e := suite.Open(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		dest := filepath.Join(dir, task+"-subset")
		w, e := suite.NewWriter(dest)
		if e != nil {
			t.Fatal(e)
		}
		defer w.Abort()
		e = s.Iter(t.Context(), func(ep *suite.Episode) error {
			c := ep.Cases[0]
			if task == "generation" && c.Category != "education_resources" && c.Category != "travel_transportation" {
				return nil
			}
			if task == "mcq" && (c.Category != "education_resources" || len(cases[task]) >= 50) {
				return nil
			}
			n := len(cases[task])
			cases[task] = append(cases[task], c)
			in, e := reader.Build(nil, nil, c, false, false, 999999)
			if e != nil {
				return e
			}
			msg := in.Messages[len(in.Messages)-1].Content
			if task == "mcq" {
				msg = in.Messages[len(in.Messages)-2].Content
			}
			answer := fmt.Sprintf("offline-answer-%d", n)
			if task == "mcq" {
				answer = "<choice>B</choice>"
			}
			readerOutputs[msg] = answer
			if task == "generation" {
				for bit, probe := range []string{"acknow", "violate", "hallucinate", "helpful"} {
					prompt := probe + "\x00" + c.Query + "\x00" + answer + "\x00" + c.Gold.Reference
					if _, exists := probes[prompt]; exists {
						t.Fatal("duplicate fake prompt")
					}
					probes[prompt] = probeAnswer{probe, n, n&(1<<bit) != 0}
				}
			}
			return w.WriteEpisode(ep)
		})
		if e != nil {
			t.Fatal(e)
		}
		if e = w.Close(s); e != nil {
			t.Fatal(e)
		}
		paths[task] = dest
	}
	if len(cases["generation"]) != 100 || len(cases["mcq"]) != 50 {
		t.Fatal("expected 54+46 generation and 50 MCQ")
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
		var body struct {
			Questions map[string]struct {
				Instructions string `json:"instructions"`
			} `json:"questions"`
			Model       string        `json:"model"`
			Messages    []llm.Message `json:"messages"`
			State       string        `json:"state"`
			Tokens      int           `json:"max_completion_tokens"`
			Temperature *float64      `json:"temperature"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			return
		}
		if r.URL.Path == "/v1/systemone" {
			var state map[string]string
			json.Unmarshal([]byte(body.State), &state)
			probe := ""
			for _, candidate := range score.Probes {
				if body.Questions["correct"].Instructions == score.Instructions(candidate) {
					probe = candidate
				}
			}
			p, ok := probes[probe+"\x00"+state["user_query"]+"\x00"+state["response"]+"\x00"+state["preference"]]
			if !ok {
				t.Error("unknown decision prompt")
				http.Error(w, "bad prompt", 400)
				return
			}
			probability := .1
			if p.yes {
				probability = .9
			}
			if p.n%2 == 0 {
				probability = .49
				if p.yes {
					probability = .51
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "answers": map[string]any{"correct": map[string]any{"type": "noul", "noul": probability}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0}})
			return
		}
		if len(body.Messages) < 2 {
			t.Error("missing messages")
			return
		}
		last := body.Messages[len(body.Messages)-1].Content
		text := ""
		if body.Model == "gpt-6-luna" {
			var ok bool
			text, ok = readerOutputs[last]
			if !ok {
				t.Error("unknown reader question")
				http.Error(w, "bad query", 400)
				return
			}
			tokens := 300
			if strings.HasPrefix(text, "<choice>") {
				tokens = 128 // F18 reasoning floor; the official MCQ cap remains 5.
			}
			if body.Messages[0].Content != score.Templates["reader_system"] || body.Tokens != tokens {
				t.Error("reader system/token defaults", body.Tokens, tokens)
			}
		}

		if body.Temperature == nil || *body.Temperature != 0 {
			t.Error("temperature")
		}
		json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 10, CompletionTokens: 3}})
	}))
	defer srv.Close()
	type target struct{ name, sut, kind, task string }
	targets := []target{}
	for _, task := range []string{"generation", "mcq"} {
		for _, sut := range []string{"bm25", "fullcontext"} {
			for _, kind := range []string{"decision"} {
				targets = append(targets, target{task + "-" + sut + "-" + kind, sut, kind, task})
			}
		}
	}
	execute := func(mode string, x target) *result.Report {
		dest := filepath.Join(dir, x.name+"-"+mode)
		args := []string{"--env-file", envfile, "run", "--suite", paths[x.task], "--sut", x.sut, "--mode", "fixed_reader", "--llm", mode, "--llm-base-url", srv.URL + "/v1", "--cassette-dir", filepath.Join(dir, "cassettes-"+x.name), "--llm-retries", "0", "--run-seed", strings.Repeat("94", 32), "--max-context-chars", "100000", "--top-k", "50", "--out", dest}
		var out, errOut bytes.Buffer
		if e := runCommand(t.Context(), args, &out, &errOut); e != nil {
			t.Fatalf("%s/%s %v %s %s", x.name, mode, e, out.String(), errOut.String())
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
	records := map[string]*result.Report{}
	counts := map[string]int64{}
	for _, x := range targets {
		before := calls.Load()
		records[x.name] = execute("record", x)
		counts[x.name] = calls.Load() - before
	}
	n, nd := calls.Load(), dials.Load()
	srv.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "")
	proofs := map[string]any{}
	for _, x := range targets {
		a, b := records[x.name], execute("replay", x)
		count := len(cases[x.task])
		if len(a.Cases) != count || len(b.Cases) != count {
			t.Fatal("case count")
		}
		sums := map[string]float64{}
		catSums := map[string]map[string]float64{}
		catN := map[string]int{}
		for i, c := range a.Cases {
			d := b.Cases[i]
			if c.Status != result.Scored {
				t.Fatalf("%s %s %s %s", x.name, c.CaseID, c.Status, c.ErrorType)
			}
			expected := map[string]float64{}
			if x.task == "generation" {
				ans := map[string]string{}
				for bit, probe := range []string{"acknow", "violate", "hallucinate", "helpful"} {
					ans[probe] = "No"
					if i&(1<<bit) != 0 {
						ans[probe] = "Yes"
					}
				}
				expected = score.Combine(ans).Metrics()
				if len(c.QA.JudgeDetails) != 4 {
					t.Fatal("probes")
				}
				for j, v := range c.QA.JudgeDetails {
					raw, _ := json.Marshal(v)
					var q score.Judgment
					json.Unmarshal(raw, &q)
					if q.Probe != score.Probes[j] {
						t.Fatal("probe order")
					}
				}
			} else {
				expected["mcq.correct"] = 0
				if cases[x.task][i].Gold.Choice == "B" {
					expected["mcq.correct"] = 1
				}
			}
			if catSums[c.Category] == nil {
				catSums[c.Category] = map[string]float64{}
			}
			catN[c.Category]++
			for k, v := range expected {
				if c.Metrics[k] == nil || *c.Metrics[k] != v {
					t.Fatal(x.name, i, k, c.Metrics, expected)
				}
				sums[k] += v
				catSums[c.Category][k] += v
			}
			c.Timings = nil
			d.Timings = nil
			if !reflect.DeepEqual(c, d) {
				t.Fatalf("%s case %d replay mismatch", x.name, i)
			}
		}
		raw, _ := json.Marshal(b.Scores.Official)
		var off result.Official
		json.Unmarshal(raw, &off)
		if off.Fidelity != "partial" || off.Errors != 0 || off.QuestionSet.Evaluated != count {
			t.Fatal(off)
		}
		for k, v := range sums {
			if off.Metrics[k] == nil || math.Abs(*off.Metrics[k]-v/float64(count)) > 1e-15 {
				t.Fatal("aggregate", k)
			}
		}
		for cat, ms := range catSums {
			for k, v := range ms {
				if off.ByTypeMetrics[cat][k] == nil || *off.ByTypeMetrics[cat][k] != v/float64(catN[cat]) {
					t.Fatal("topic aggregate", cat, k)
				}
			}
		}
		raw, _ = json.Marshal(b.Billing.Evaluator)
		var bill llm.Billing
		json.Unmarshal(raw, &bill)
		expectedCalls := count
		if x.task == "generation" {
			expectedCalls = count * 5
		}

		if counts[x.name] != int64(expectedCalls) || bill.CostNew == nil || *bill.CostNew != 0 || bill.CassetteHits != expectedCalls {
			t.Fatal(x.name, "billing/calls", bill, counts[x.name], expectedCalls)
		}
		proofs[x.name] = map[string]any{"cases": count, "record_http": counts[x.name], "replay_http": 0, "replay_dials": 0, "new_usd": 0, "case_differences_excluding_timings": 0, "metrics": off.Metrics, "by_topic": off.ByTypeMetrics}
	}
	if n != calls.Load() || nd != dials.Load() {
		t.Fatal("replay network")
	}
	raw, _ := json.MarshalIndent(proofs, "", "  ")
	os.WriteFile(filepath.Join(dir, "proof.json"), append(raw, '\n'), 0600)
	t.Logf("4 targets; generation=100, MCQ=50, bm25/fullcontext with Jev; record %d calls; replay 0 HTTP/dials/USD and 0 per-case differences", n)
}
