package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"memeval/internal/adapter"
	"memeval/internal/importers/memoryagentbench"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/runner"
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

var mabEvidence = flag.String("mab-evidence-dir", "", "optional NEW scratch output directory for MAB record/replay acceptance")

func TestMABRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "memoryagentbench")
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "mab-fake-key")
	dir := t.TempDir()
	if *mabEvidence != "" {
		dir = *mabEvidence
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("evidence directory must be new")
		}
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	input := "../importers/memoryagentbench/testdata/accurate_retrieval.json"
	realPath := "../../data/upstream/memoryagentbench/data/Accurate_Retrieval-00000-of-00001.parquet"
	if _, e := os.Stat(realPath); e == nil {
		stop := errors.New("first row")
		var row memoryagentbench.Row
		e = memoryagentbench.ReadRows(t.Context(), testoracle.Dataset(t, "upstream/memoryagentbench/data/Accurate_Retrieval-00000-of-00001.parquet"), func(_ int, r memoryagentbench.Row) error { row = r; return stop })
		if !errors.Is(e, stop) {
			t.Fatal(e)
		}
		b, _ := json.Marshal([]memoryagentbench.Row{row})
		input = filepath.Join(dir, "ar-first-row.json")
		os.WriteFile(input, b, 0600)
		t.Log("AR first real parquet row, FULL context, 100 questions")
	}
	rs, e := memoryagentbench.ImportFile(t.Context(), input, "accurate_retrieval", filepath.Join(dir, "suite"), 4096, false)
	if e != nil {
		t.Fatal(e)
	}
	s, e := suite.Open(t.Context(), rs[0].Path)
	if e != nil {
		t.Fatal(e)
	}
	answersByQuery := map[string]string{}
	err := s.Iter(t.Context(), func(ep *suite.Episode) error {
		for i, c := range ep.Cases {
			answer := "deliberately incorrect fixture answer"
			if i%2 == 0 {
				answer = c.Gold.Answers[0]
			}
			answersByQuery[c.Query] = "Answer: " + answer
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls, dials atomic.Int64
	old := http.DefaultTransport
	tr := old.(*http.Transport).Clone()
	originalDial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		host, _, _ := net.SplitHostPort(address)
		if net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return nil, fmt.Errorf("non-loopback network forbidden in test")
		}
		return originalDial(ctx, network, address)
	}
	http.DefaultTransport = tr
	defer func() { http.DefaultTransport = old }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 2 {
			t.Error("reader request")
		}
		if !strings.Contains(body.Messages[1].Content, "Now Answer the Question:") {
			t.Error("wrong reader template")
		}
		prompt := body.Messages[1].Content
		prefix := "Now Answer the Question: "
		at := strings.LastIndex(prompt, prefix)
		if at < 0 {
			t.Error("missing query")
			http.Error(w, "bad prompt", 400)
			return
		}
		answer, ok := answersByQuery[prompt[at+len(prefix):]]
		if !ok {
			t.Error("query changed")
			http.Error(w, "unknown query", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "choices": []any{map[string]any{"message": map[string]string{"content": answer}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 4}})
	}))
	defer server.Close()
	cfg := llm.DefaultConfig()
	cfg.Mode = "record"
	cfg.BaseURL = server.URL + "/v1"
	cfg.CassetteDir = filepath.Join(dir, "cassettes")
	cfg.LedgerPath = filepath.Join(dir, "record", "llm_ledger.jsonl")
	cfg.TracePrompts = true
	cfg.MaxRetries = 0
	execute := func(c llm.Config) *result.Report {
		engine, e := llm.New(c)
		if e != nil {
			t.Fatal(e)
		}
		defer engine.Close()
		o := fixedOpts(engine)
		o.ResetPolicy = runner.IncrementalPerEpisode
		o.FixedReader.Model = "gpt-6-luna"
		o.FixedReader.JudgeDecisionModel = "jev-1.14.0"
		o.FixedReader.DecisionClient = &decision.Client{Engine: engine}
		a, _ := adapter.NewBaseline("bm25")
		r := run(t, s, a, o)
		for _, row := range r.Cases {
			if row.Status != result.Scored {
				t.Fatal(row.Status, row.ErrorType)
			}
			for _, k := range []string{"mab_f1", "mab_exact_match", "mab_substring_exact_match", "mab_rougeL_f1", "mab_rougeLsum_recall"} {
				if row.Metrics[k] == nil {
					t.Fatal("missing", k)
				}
			}
		}
		dest := filepath.Dir(c.LedgerPath)
		if e = report.Save(t.Context(), r, dest); e != nil {
			t.Fatal(e)
		}
		return r
	}
	a := execute(cfg)
	if v := a.Summary.Metrics["mab_substring_exact_match"].Mean; v == nil || *v <= 0 || *v >= 1 {
		t.Fatal("fake service must exercise correct and incorrect scores", v)
	}
	if calls.Load() != 100 {
		t.Fatal("expected 100 reader calls", calls.Load())
	}
	server.Close()
	beforeDials := dials.Load()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	cfg.Mode = "replay"
	cfg.LedgerPath = filepath.Join(dir, "replay", "llm_ledger.jsonl")
	b := execute(cfg)
	for i := range a.Cases {
		x, y := a.Cases[i], b.Cases[i]
		x.Timings = nil
		y.Timings = nil
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("case %d replay differs", i)
		}
	}
	billing := b.Billing.Evaluator.(llm.Billing)
	if calls.Load() != 100 || dials.Load() != beforeDials || billing.CostNew == nil || *billing.CostNew != 0 || billing.CassetteHits != 100 {
		t.Fatal("replay performed HTTP or added cost", billing, calls.Load(), dials.Load()-beforeDials)
	}
	t.Logf("record=%d HTTP; replay=0 HTTP/0 dial/0 new USD; %d cases identical; mab_* metrics present", calls.Load(), len(a.Cases))
}
func TestMABJudgeRecordReplay(t *testing.T) {
	testoracle.ProtocolSources(t, "memoryagentbench")
	for _, kind := range []string{"summary", "longmem"} {
		for _, judgeKind := range []string{"decision"} {
			t.Run(kind+"/"+judgeKind, func(t *testing.T) {
				t.Setenv("MEMEVAL_OPENAI_API_KEY", "fake")
				t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "fake")
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					raw, _ := io.ReadAll(r.Body)
					var body map[string]json.RawMessage
					json.Unmarshal(raw, &body)
					if _, ok := body["questions"]; ok {
						p := .5
						if judgeKind == "decision" {
							p = .9
						}
						fmt.Fprintf(w, `{"model":"jev-1.14.0","answers":{"correct":{"type":"noul","noul":%g}},"usage":{"input_tokens":5},"cost_usd":0}`, p)
						return
					}
					var model string
					json.Unmarshal(body["model"], &model)
					text := "The story is coherent."
					json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 3}})
				}))
				defer srv.Close()
				t.Setenv("MEMEVAL_TYPESAFE_BASE_URL", srv.URL+"/v1")
				s := incrementalToy()
				s.ProtocolRef = "protocols/memoryagentbench.json#long_range_understanding"
				s.SuiteID = "synthetic-mab-judge"
				s.Episodes[0].Cases = s.Episodes[0].Cases[:1]
				c := &s.Episodes[0].Cases[0]
				c.Kind = "summary"
				c.Gold = suite.Gold{Reference: "The story is coherent.", Answers: []string{"The story is coherent."}, Checker: &suite.Checker{Type: "mab_infbench_sum"}}
				source := "infbench_sum_eng_shots2"
				if kind == "longmem" {
					source = "longmemeval_s*"
					c.Kind = "qa"
					c.Gold.Checker = &suite.Checker{Type: "mab_longmemeval_judge", Params: json.RawMessage(`{"question_type":"single-session-user","abstention":false}`)}
					c.QuestionType = "single-session-user"
				}
				c.Attributes = map[string]any{"mab_source": source}
				for i := range s.Episodes[0].Events {
					s.Episodes[0].Events[i].Attributes = map[string]any{"mab_source": source}
				}
				cfg := llm.DefaultConfig()
				cfg.Mode = "record"
				cfg.BaseURL = srv.URL + "/v1"
				cfg.CassetteDir = filepath.Join(t.TempDir(), "cassettes")
				cfg.MaxRetries = 0
				var previous result.Case
				for i, mode := range []string{"record", "replay"} {
					cfg.Mode = mode
					if i == 1 {
						srv.Close()
						t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
						t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "")
					}
					engine, e := llm.New(cfg)
					if e != nil {
						t.Fatal(e)
					}
					o := fixedOpts(engine)
					o.ResetPolicy = runner.IncrementalPerEpisode
					o.FixedReader.Model = "gpt-6-luna"
					o.FixedReader.JudgeDecisionModel = "jev-1.14.0"
					o.FixedReader.JudgeDecisionModel = "jev-1.14.0"
					o.FixedReader.DecisionClient = &decision.Client{Engine: engine}
					r := run(t, s, newSpy(), o)
					engine.Close()
					got := r.Cases[0]
					if got.Status != result.Scored || got.QA.JudgeLabel == nil || !*got.QA.JudgeLabel {
						t.Fatalf("%s: %+v", mode, got)
					}
					if kind == "summary" && got.Metrics["mab_infbench_sum.fluency"] == nil {
						t.Fatal("fluency missing")
					}
					got.Timings = nil
					if i == 0 {
						previous = got
					} else if !reflect.DeepEqual(previous, got) {
						t.Fatal("judge replay differs")
					}
				}
				want := int64(2)
				if calls.Load() != want {
					t.Fatal("call count", calls.Load(), want)
				}
			})
		}
	}
}
