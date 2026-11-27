package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"memeval/internal/benchmark"
	"memeval/internal/llm"
	"memeval/internal/result"
	score "memeval/internal/scoring/convomem"
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

var convoMemEvidence = flag.String("convomem-evidence-dir", "", "optional NEW scratch directory for ConvoMem offline acceptance")

func TestConvoMemCLIRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "convomem")
	root := benchmark.Root()
	dir := t.TempDir()
	if *convoMemEvidence != "" {
		abs, e := filepath.Abs(*convoMemEvidence)
		if e != nil {
			t.Fatal(e)
		}
		rel, e := filepath.Rel(root, abs)
		if e != nil || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatal("evidence must be outside repository")
		}
		if _, e = os.Stat(abs); !os.IsNotExist(e) {
			t.Fatal("evidence directory must be new")
		}
		if e = os.MkdirAll(abs, 0700); e != nil {
			t.Fatal(e)
		}
		dir = abs
	}
	for _, env := range os.Environ() {
		k, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(k, "MEMEVAL_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "p5-fixture-key")
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "convomem-fake-openai")
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "convomem-fake-jev")
	t.Chdir(root)
	envfile := filepath.Join(dir, "empty-config")
	os.WriteFile(envfile, nil, 0600)
	paths := map[string]string{}
	cases := map[string][]suite.Case{}
	outputs := map[string]string{}
	prompts := map[string]int{}
	types := []string{"abstention", "assistant_facts", "changing", "implicit_connection", "preference"}
	for _, typ := range types {
		s, e := suite.Open(t.Context(), testoracle.Dataset(t, "normalized/convomem-"+typ+"@e3e9b39115b0"))
		if e != nil {
			t.Fatal(e)
		}
		dest := filepath.Join(dir, typ+"-subset")
		w, e := suite.NewWriter(dest)
		if e != nil {
			t.Fatal(e)
		}
		defer w.Abort()
		file := ""
		for _, ix := range s.Index() {
			ep, e := s.Episode(t.Context(), ix.ID)
			if e != nil {
				t.Fatal(e)
			}
			c := ep.Cases[0]
			f := c.Attributes["source_file"].(string)
			if file == "" {
				file = f
			}
			if f != file {
				break
			}
			i := int(sha256.Sum256([]byte(typ + "\x00" + c.Query))[0])
			cases[typ] = append(cases[typ], c)
			if typ == "abstention" || typ == "assistant_facts" {
				answer := "offline answer"
				outputs[c.Query] = answer
				template := "default"
				correct := strings.Join(c.Gold.Answers, "\n")
				if typ == "abstention" {
					template = "abstention"
				}
				prompts[template+"\x00"+c.Query+"\x00"+correct+"\x00"+answer] = i
			}
			if e = w.WriteEpisode(ep); e != nil {
				t.Fatal(e)
			}
		}
		if e = w.Close(s); e != nil {
			t.Fatal(e)
		}
		paths[typ] = dest
	}
	if len(cases["abstention"]) != 100 || len(cases["assistant_facts"]) != 100 {
		t.Fatal("one 1_evidence file must contain 100 items")
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
			Model       string        `json:"model"`
			Messages    []llm.Message `json:"messages"`
			State       string        `json:"state"`
			Temperature *float64      `json:"temperature"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			return
		}
		if r.URL.Path == "/v1/systemone" {
			var state map[string]string
			json.Unmarshal([]byte(body.State), &state)
			template := "default"
			if _, ok := state["abstention_rule"]; ok {
				template = "abstention"
			}
			i, ok := prompts[template+"\x00"+state["question"]+"\x00"+state["reference_answer"]+"\x00"+state["response"]]
			if !ok {
				t.Error("unknown decision prompt")
				http.Error(w, "bad prompt", 400)
				return
			}
			p := .1
			if i%3 != 0 {
				p = .9
			}
			if i%2 == 0 {
				p = .49
				if i%3 != 0 {
					p = .51
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "answers": map[string]any{"correct": map[string]any{"type": "noul", "noul": p}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0}})
			return
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Temperature != nil {
			t.Error("unexpected prompt envelope")
			http.Error(w, "bad envelope", 400)
			return
		}
		prompt := body.Messages[0].Content
		text := ""
		if body.Model == "gpt-6-luna" {
			start := strings.LastIndex(prompt, "\nQuestion: ")
			if start < 0 || !strings.HasSuffix(prompt, "\n\nAnswer:") {
				t.Error("reader template")
				return
			}
			q := strings.TrimSuffix(prompt[start+11:], "\n\nAnswer:")
			var ok bool
			text, ok = outputs[q]
			if !ok {
				t.Error("unknown question")
				return
			}
			if !strings.Contains(prompt, "Conversation 1:") {
				t.Error("conversation renderer")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": llm.Usage{PromptTokens: 10, CompletionTokens: 3}})
	}))
	defer srv.Close()
	type target struct{ name, typ, sut, kind string }
	targets := []target{}
	for _, typ := range []string{"abstention", "assistant_facts"} {
		for _, sut := range []string{"bm25", "fullcontext"} {
			for _, kind := range []string{"decision"} {
				targets = append(targets, target{typ + "-" + sut + "-" + kind, typ, sut, kind})
			}
		}
	}
	execute := func(mode string, x target) *result.Report {
		dest := filepath.Join(dir, x.name+"-"+mode)
		args := []string{"--env-file", envfile, "run", "--suite", paths[x.typ], "--sut", x.sut, "--mode", "fixed_reader", "--llm", mode, "--llm-base-url", srv.URL + "/v1", "--cassette-dir", filepath.Join(dir, "cassettes-"+x.name), "--llm-retries", "0", "--run-seed", strings.Repeat("94", 32), "--max-context-chars", "100000", "--top-k", "50", "--out", dest}
		var out, errOut bytes.Buffer
		if e := runCommand(t.Context(), args, &out, &errOut); e != nil {
			t.Fatalf("%s/%s: %v %s %s", x.name, mode, e, out.String(), errOut.String())
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
		if len(a.Cases) != 100 || len(b.Cases) != 100 {
			t.Fatal("case count")
		}
		expectedScore := 0.
		for i, c := range a.Cases {
			d := b.Cases[i]
			n := int(sha256.Sum256([]byte(x.typ + "\x00" + cases[x.typ][i].Query))[0])
			want := 0.
			if n%3 != 0 {
				want = 1
			}
			expectedScore += want / 100
			if c.Status != result.Scored || c.Metrics["convomem.correct"] == nil || *c.Metrics["convomem.correct"] != want {
				t.Fatalf("%s case %d: %+v", x.name, i, c)
			}
			raw, _ := json.Marshal(c.QA.JudgeDetails[0])
			var j score.Judgment
			json.Unmarshal(raw, &j)
			c.Timings = nil
			d.Timings = nil
			if !reflect.DeepEqual(c, d) {
				t.Fatalf("%s case %d replay differs", x.name, i)
			}
		}
		raw, _ := json.Marshal(b.Scores.Official)
		var off result.Official
		json.Unmarshal(raw, &off)
		if off.Score == nil || math.Abs(*off.Score-expectedScore) > 1e-12 || off.Errors != 0 || off.Judged != 100 || off.Fidelity != "partial" || math.Abs(*off.ByTypeMetrics[x.typ]["convomem.correct"]-expectedScore) > 1e-12 || math.Abs(*off.Metrics["convomem.correct.n_evidence.1"]-expectedScore) > 1e-12 {
			t.Fatal(x.name, off)
		}
		expected := 200
		raw, _ = json.Marshal(b.Billing.Evaluator)
		var bill llm.Billing
		json.Unmarshal(raw, &bill)
		if counts[x.name] != int64(expected) || bill.CassetteHits != expected || bill.CostNew == nil || *bill.CostNew != 0 {
			t.Fatal(x.name, bill, counts[x.name], expected)
		}
		proofs[x.name] = map[string]any{"cases": 100, "correct": expectedScore, "record_http": counts[x.name], "replay_http": 0, "replay_dials": 0, "new_usd": 0, "case_differences_excluding_timings": 0, "metrics": off.Metrics}
	}
	if n != calls.Load() || nd != dials.Load() {
		t.Fatal("replay touched HTTP")
	}
	// Five source-file retrieval projections, kept in scratch, with original evidence refs.
	retrieval := map[string]any{}
	for _, typ := range types {
		for _, sut := range []string{"null", "bm25"} {
			dest := filepath.Join(dir, typ+"-"+sut+"-retrieval")
			var out, errOut bytes.Buffer
			if e := runCommand(t.Context(), []string{"--env-file", envfile, "run", "--suite", paths[typ], "--sut", sut, "--mode", "retrieval", "--top-k", "10", "--run-seed", strings.Repeat("94", 32), "--out", dest}, &out, &errOut); e != nil {
				t.Fatalf("retrieval %v %s", e, errOut.String())
			}
			raw, e := os.ReadFile(filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			var r result.Report
			json.Unmarshal(raw, &r)
			sum := 0.
			count := 0
			for _, c := range r.Cases {
				if c.Status == result.Error || c.Status == result.Unsupported || c.Status == result.ContractError {
					t.Fatal(c)
				}
				if v := c.Metrics["recall"]; v != nil {
					sum += *v
					count++
				}
			}
			if count == 0 {
				t.Fatalf("%s/%s no evidence recall", typ, sut)
			}
			if sut == "null" && sum != 0 {
				t.Fatal("null recall")
			}
			retrieval[typ+"-"+sut] = map[string]any{"cases": len(r.Cases), "recall_n": count, "recall": sum / float64(count), "summary": r.Summary}
		}
	}
	raw, _ := json.MarshalIndent(map[string]any{"fixed_reader": proofs, "retrieval": retrieval}, "", "  ")
	if e := os.WriteFile(filepath.Join(dir, "proof.json"), append(raw, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("12 targets x 100 cases; record %d HTTP; replay 0 HTTP/dials/USD, 0 per-case differences; 10 retrieval runs", n)
}
