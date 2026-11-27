package runner_test

import (
	"context"
	"encoding/json"
	"memeval/internal/adapter"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	readerBEAM "memeval/internal/reader/beam"
	readerLME "memeval/internal/reader/longmemeval"
	readerMAB "memeval/internal/reader/mab"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type inputGuardSpy struct {
	*spy
	describes int
}

func (a *inputGuardSpy) Describe(ctx context.Context) (protocol.Describe, error) {
	a.describes++
	return a.spy.Describe(ctx)
}

func TestAnswerInputResumeCannotRelabelScoredRows(t *testing.T) {
	for _, original := range []string{"sources", "items"} {
		t.Run(original, func(t *testing.T) {
			s := answerInputMAB(t)
			first := fixedOpts(&fakeLLM{answer: "SECRET-GOLD"})
			first.FixedReader.Input = original
			old := run(t, s, newSpy(), first)
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			var persisted result.Report
			if err = json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			plan, err := runner.NewResume(&persisted, "error,invalid")
			if err != nil {
				t.Fatal(err)
			}
			client := &fakeLLM{answer: "SECRET-GOLD"}
			o := fixedOpts(client)
			o.Resume = plan
			o.FixedReader.Input = "items"
			if original == "items" {
				o.FixedReader.Input = "sources"
			}
			a := &inputGuardSpy{spy: newSpy()}
			_, err = runner.Run(t.Context(), s, a, o)
			if err == nil || !strings.Contains(err.Error(), "reader input") || a.describes != 0 || len(a.names) != 0 || len(client.requests) != 0 {
				t.Fatalf("changed resume input was not rejected before execution: err=%v describe=%d reset=%d reader=%d", err, a.describes, len(a.names), len(client.requests))
			}
			// An unchanged input can reuse the same persisted scored row.
			o.FixedReader.Input = original
			r := run(t, s, newSpy(), o)
			if r.Protocol.ReaderInput != original || r.Cases[0].Status != result.Scored || len(client.requests) != 0 {
				t.Fatal("same-input resume changed", r.Cases)
			}
		})
	}
}

func TestAnswerInputFullContextRejectsItems(t *testing.T) {
	for _, benchmark := range []string{"memoryagentbench", "longmemeval", "beam"} {
		t.Run(benchmark, func(t *testing.T) {
			s := answerInputMAB(t)
			if benchmark == "longmemeval" {
				testoracle.ProtocolSources(t, "longmemeval")
				s = qaToy()
			} else if benchmark == "beam" {
				s = answerInputBEAM(t)
			}
			client := &fakeLLM{answer: "SECRET-GOLD"}
			o := fixedOpts(client)
			o.FixedReader.Input = "items"
			a, _ := adapter.NewBaseline("fullcontext")
			_, err := runner.Run(t.Context(), s, a, o)
			if err == nil || !strings.Contains(err.Error(), "fullcontext") || !strings.Contains(err.Error(), "items") || len(client.requests) != 0 {
				t.Fatalf("fullcontext+items was not rejected before answering: err=%v calls=%d", err, len(client.requests))
			}
		})
	}
}

func answerInputMAB(t *testing.T) *suite.Suite {
	t.Helper()
	testoracle.ProtocolSources(t, "memoryagentbench")
	s := qaToy()
	s.ProtocolRef = "protocols/memoryagentbench.json#accurate_retrieval"
	c := &s.Episodes[0].Cases[0]
	c.Gold.Checker = &suite.Checker{Type: "mab_exact_match"}
	c.Attributes = map[string]any{"mab_source": "ruler_qa1_197K"}
	return s
}

func TestAnswerInputUnattributedMemory(t *testing.T) {
	for _, benchmark := range []string{"memoryagentbench", "longmemeval", "beam"} {
		for _, mode := range []string{"items", "sources", "retrieval"} {
			t.Run(benchmark+"/"+mode, func(t *testing.T) {
				s := answerInputMAB(t)
				if benchmark == "longmemeval" {
					testoracle.ProtocolSources(t, "longmemeval")
					s = qaToy()
				} else if benchmark == "beam" {
					s = answerInputBEAM(t)
				}
				client := &fakeLLM{answer: "SECRET-GOLD"}
				a := newSpy()
				a.retrieve = func(*spy) (protocol.RetrieveResponse, error) {
					return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "unattributed observation", SourceIDs: []string{}}}}, nil
				}
				o := fixedOpts(client)
				o.FixedReader.Input = mode
				want := result.Unsupported
				if mode == "retrieval" {
					o.Mode = runner.Retrieval
				} else if mode == "items" {
					want = result.Scored
				}
				r := run(t, s, a, o)
				if r.Cases[0].Status != want {
					t.Fatalf("status=%s want=%s", r.Cases[0].Status, want)
				}
				if mode == "items" {
					if len(client.requests) != 1 {
						t.Fatal("reader was not called")
					}
					prompt, _ := json.Marshal(client.requests[0].Messages)
					if !strings.Contains(string(prompt), "unattributed observation") || strings.Contains(string(prompt), "blue box") {
						t.Fatal(string(prompt))
					}
				} else if len(client.requests) != 0 {
					t.Fatal("unsupported response reached reader")
				}
			})
		}
	}
}

func TestAnswerInputMixedProvenanceHasNoRetrievalScore(t *testing.T) {
	for _, benchmark := range []string{"memoryagentbench", "longmemeval", "beam"} {
		t.Run(benchmark, func(t *testing.T) {
			s := answerInputMAB(t)
			if benchmark == "longmemeval" {
				testoracle.ProtocolSources(t, "longmemeval")
				s = qaToy()
				s.Protocol.RetrievalMetrics = []string{"longmemeval_official"}
			} else if benchmark == "beam" {
				s = answerInputBEAM(t)
			}
			client := &fakeLLM{answer: "SECRET-GOLD"}
			a := newSpy()
			a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
				return protocol.RetrieveResponse{Items: []protocol.Item{
					{Text: "traceable rewrite", SourceIDs: []string{a.inputs[0].ID}},
					{Text: "unattributed observation", SourceIDs: []string{}},
				}}, nil
			}
			o := fixedOpts(client)
			o.FixedReader.Input = "items"
			r := run(t, s, a, o)
			c := r.Cases[0]
			if c.Status != result.Scored || c.Passed == nil || !*c.Passed {
				t.Fatal("answer must still be scored", c)
			}
			for key, value := range c.Metrics {
				if !strings.HasPrefix(key, "mab_") && !strings.HasPrefix(key, "beam.") && key != "qa_accuracy" && value != nil {
					t.Fatalf("partial citations scored as complete retrieval: %s=%g", key, *value)
				}
			}
			if r.Summary.Metrics["recall"].N != 0 || c.EmptyExpected != nil || c.Diagnostics["untraceable_hits"] != 1 || r.Summary.UntraceableHits != 1 || c.Note == nil || !strings.Contains(*c.Note, "untraceable") {
				t.Fatalf("missing provenance disclosure: %+v; summary=%+v", c, r.Summary)
			}
			if len(c.RetrievedSourceIDs) != 1 || c.RetrievedSourceIDs[0] != "private-a" {
				t.Fatal("source diagnostic was lost or invented", c.RetrievedSourceIDs)
			}
		})
	}
}

func TestMABItemsRankUnicodeBudgetAndDisclosure(t *testing.T) {
	for _, budget := range []int{6, 4} {
		s := answerInputMAB(t)
		client := &fakeLLM{answer: "SECRET-GOLD"}
		a := newSpy()
		a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
			return protocol.RetrieveResponse{Items: []protocol.Item{
				{Text: "🙂甲", SourceIDs: []string{a.inputs[0].ID}},
				{Text: "乙", SourceIDs: []string{a.inputs[0].ID}},
				{Text: "丙", SourceIDs: []string{a.inputs[0].ID}},
			}}, nil
		}
		o := fixedOpts(client)
		o.FixedReader.Input, o.Budget.MaxContextChars = "items", budget
		r := run(t, s, a, o)
		if r.Cases[0].Status != result.Scored || len(client.requests) != 1 {
			t.Fatal(r.Cases)
		}
		want, dropped := "🙂甲\n乙\n丙\n\n", 0
		if budget == 4 {
			want, dropped = "🙂甲\n乙\n\n", 1
		}
		if !strings.HasPrefix(client.requests[0].Messages[1].Content, want) || r.Cases[0].QA.Truncated != (dropped > 0) || r.Cases[0].QA.DroppedMessages != dropped {
			t.Fatalf("rank/budget %d: prompt=%q QA=%+v", budget, client.requests[0].Messages[1].Content, r.Cases[0].QA)
		}
		official := r.Scores.Official.(result.Official)
		if r.Protocol.ReaderInput != "items" || r.Reader.(map[string]any)["input_mode"] != "items" || official.Fidelity == "full" || !strings.Contains(strings.Join(official.Deviations, " "), "reader_input=items") || !strings.Contains(strings.Join(r.Cases[0].QA.Deviations, " "), "reader_input=items") {
			t.Fatal("missing input deviation", r.Reader, official)
		}
		if v := r.Cases[0].Metrics["recall"]; v == nil || *v != 1 {
			t.Fatal("fully attributed diagnostics changed", r.Cases[0].Metrics)
		}
	}
}

func TestMABAnswerInputUsesSelectedRepresentation(t *testing.T) {
	for _, mode := range []string{"items", "sources"} {
		t.Run(mode, func(t *testing.T) {
			s := answerInputMAB(t)
			client := &fakeLLM{answer: "SECRET-GOLD"}
			a := newSpy()
			a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
				return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "rewritten memory", SourceIDs: []string{a.inputs[0].ID}}}}, nil
			}
			o := fixedOpts(client)
			o.FixedReader.Input = mode
			r := run(t, s, a, o)
			if r.Cases[0].Status != result.Scored || r.Cases[0].Metrics["mab_exact_match"] == nil || *r.Cases[0].Metrics["mab_exact_match"] != 1 || len(client.requests) != 1 {
				t.Fatalf("answer did not score: %+v; reader calls %d", r.Cases[0], len(client.requests))
			}
			prompt, _ := json.Marshal(client.requests[0].Messages)
			want, absent := "rewritten memory", "blue box"
			if mode == "sources" {
				want, absent = absent, want
			}
			if !strings.Contains(string(prompt), want) || strings.Contains(string(prompt), absent) {
				t.Fatalf("reader %s did not use selected representation: %s", mode, prompt)
			}
			public, _ := json.Marshal([]any{a.inputs, a.request, client.requests})
			for _, private := range []string{"SECRET-GOLD", "FUTURE-FORBIDDEN", "has_answer", "PRIVATE-QUESTION"} {
				if strings.Contains(string(public), private) {
					t.Fatalf("private data leaked: %s", private)
				}
			}
		})
	}
}

func TestAnswerInputCassetteIdentity(t *testing.T) {
	for _, benchmark := range []string{"memoryagentbench", "longmemeval", "beam"} {
		t.Run(benchmark, func(t *testing.T) {
			s := answerInputMAB(t)
			historicalTemplate := readerMAB.PromptID + "@ebefa70772b3ac926f56f78a223a9c37a150ba40315e6344b3ae375b509d083d"
			if benchmark == "longmemeval" {
				testoracle.ProtocolSources(t, "longmemeval")
				s = qaToy()
				historicalTemplate = readerLME.PromptID
			} else if benchmark == "beam" {
				s = answerInputBEAM(t)
				historicalTemplate = readerBEAM.PromptID + "@dcc41b8b00bcc48701d6ac26ba9049cbdf60c2b5cabf8728aba5db493d691e9f"
			}
			var requests []llm.Request
			for _, mode := range []string{"sources", "items"} {
				client := &fakeLLM{answer: "SECRET-GOLD"}
				o := fixedOpts(client)
				o.FixedReader.Input = mode
				a := newSpy()
				a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
					return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "blue box", SourceIDs: []string{a.inputs[0].ID}}}}, nil
				}
				r := run(t, s, a, o)
				if len(client.requests) != 1 || r.Protocol.ReaderInput != mode {
					t.Fatal(r.Protocol, client.requests)
				}
				requests = append(requests, client.requests[0])
			}
			if requests[0].TemplateID != historicalTemplate {
				t.Fatal("sources historical template changed")
			}
			if benchmark == "memoryagentbench" && !reflect.DeepEqual(requests[0].Messages, requests[1].Messages) {
				t.Fatal("fixture must exercise identical prompts")
			}
			t.Setenv("MEMEVAL_OPENAI_API_KEY", "synthetic-key")
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				json.NewEncoder(w).Encode(map[string]any{"model": "reader-fixture", "choices": []any{map[string]any{"message": map[string]string{"content": "answer"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1}})
			}))
			defer server.Close()
			cfg := llm.DefaultConfig()
			cfg.BaseURL = server.URL
			cfg.CassetteDir = filepath.Join(t.TempDir(), "cassettes")
			cfg.PricesPath = ""
			cfg.MaxRetries = 0
			engine := func(mode string) *llm.Engine {
				t.Helper()
				c := cfg
				c.Mode = mode
				c.LedgerPath = filepath.Join(t.TempDir(), "ledger.jsonl")
				e, err := llm.New(c)
				if err != nil {
					t.Fatal(err)
				}
				if err = e.Start("p12"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { e.Close() })
				return e
			}
			record := engine("record-once")
			source, err := record.Complete(t.Context(), requests[0])
			if err != nil {
				t.Fatal(err)
			}
			replay := engine("replay")
			// Normalize messages as well: even if both readers produce identical wire
			// text, the input contract must remain a distinct existing key dimension.
			sameTextItems := requests[1]
			sameTextItems.Messages = requests[0].Messages
			if _, err = replay.Complete(t.Context(), sameTextItems); err == nil || err.Error() != "CassetteMiss" {
				t.Fatalf("sources cassette accepted items contract: %v", err)
			}
			item, err := record.Complete(t.Context(), requests[1])
			if err != nil {
				t.Fatal(err)
			}
			if source.RequestSHA == item.RequestSHA || calls != 2 {
				t.Fatal("mode identity collapsed", source.RequestSHA, item.RequestSHA, calls)
			}
			server.Close()
			t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
			for i, req := range requests {
				got, err := replay.Complete(t.Context(), req)
				want := []llm.Response{source, item}[i]
				if err != nil || !got.CassetteHit || got.Text != want.Text || got.RequestSHA != want.RequestSHA {
					t.Fatal(got, err)
				}
			}
			if calls != 2 || replay.Billing().CassetteHits != 2 || *replay.Billing().CostNew != 0 {
				t.Fatal("replay was not offline/zero cost", replay.Billing(), calls)
			}
		})
	}
}

func TestAnswerInputItemsRetainStrictContracts(t *testing.T) {
	for _, benchmark := range []string{"memoryagentbench", "longmemeval", "beam"} {
		for _, kind := range []string{"unknown", "future", "duplicate", "nil-sources", "nil-items", "top-k", "characters", "unsupported", "mixed-invalid"} {
			t.Run(benchmark+"/"+kind, func(t *testing.T) {
				s := answerInputMAB(t)
				if benchmark == "longmemeval" {
					testoracle.ProtocolSources(t, "longmemeval")
					s = qaToy()
				} else if benchmark == "beam" {
					s = answerInputBEAM(t)
				}
				client := &fakeLLM{answer: "SECRET-GOLD"}
				o := fixedOpts(client)
				o.FixedReader.Input = "items"
				o.Budget.TopK = 1
				o.Budget.MaxContextChars = 10
				a := newSpy()
				a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
					i := protocol.Item{Text: "memory", SourceIDs: []string{a.inputs[0].ID}}
					items := []protocol.Item{i}
					switch kind {
					case "unknown", "future":
						items[0].SourceIDs = []string{kind + "-event"}
					case "duplicate":
						items[0].SourceIDs = append(items[0].SourceIDs, a.inputs[0].ID)
					case "nil-sources":
						items[0].SourceIDs = nil
					case "nil-items":
						items = nil
					case "top-k":
						items = append(items, protocol.Item{Text: "x", SourceIDs: []string{}})
					case "characters":
						items[0].Text = strings.Repeat("🙂", 11)
						items[0].SourceIDs = []string{}
					case "unsupported":
						return protocol.RetrieveResponse{}, adapter.ErrUnsupported
					case "mixed-invalid":
						items = []protocol.Item{{Text: "x", SourceIDs: []string{}}, {Text: "y", SourceIDs: []string{"unknown"}}}
					}
					return protocol.RetrieveResponse{Items: items}, nil
				}
				r := run(t, s, a, o)
				want := result.ContractError
				if kind == "unsupported" {
					want = result.Unsupported
				}
				if r.Cases[0].Status != want || len(client.requests) != 0 || len(r.Cases[0].Metrics) != 0 {
					t.Fatalf("invalid response reached reader/scorer: %+v calls=%d", r.Cases[0], len(client.requests))
				}
			})
		}
	}
}

func TestAnswerInputUnsupportedReadersRejectBeforeExecution(t *testing.T) {
	for name, variant := range map[string]string{"locomo": "text_only", "personamem": "official", "memora": "weekly", "halumem": "medium", "prefeval": "explicit-generation", "convomem": "assistant_facts"} {
		t.Run(name, func(t *testing.T) {
			testoracle.ProtocolSources(t, name)
			s := qaToy()
			s.ProtocolRef = "protocols/" + name + ".json#" + variant
			client := &fakeLLM{answer: "SECRET-GOLD"}
			o := fixedOpts(client)
			o.FixedReader.Input = "items"
			a := &inputGuardSpy{spy: newSpy()}
			_, err := runner.Run(t.Context(), s, a, o)
			if err == nil || !strings.Contains(err.Error(), name+" reader requires sources input") || a.describes != 0 || len(client.requests) != 0 {
				t.Fatalf("unsupported reader accepted items: err=%v describe=%d reader=%d", err, a.describes, len(client.requests))
			}
		})
	}
}
