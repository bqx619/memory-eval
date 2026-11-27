package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/scoring"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type outputClient struct {
	requests     []llm.Request
	text, finish string
}

func (c *outputClient) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	c.requests = append(c.requests, r)
	return llm.Response{Text: c.text, FinishReason: c.finish, Usage: llm.Usage{CompletionTokens: 5, ReasoningTokens: 5}}, nil
}

func TestReasoningReaderBudget(t *testing.T) {
	for _, tc := range []struct {
		model, provider          string
		requested, minimum, want int
		raised                   bool
	}{
		{"gpt-6-luna", "openai-compat", 5, 128, 128, true},
		{"gpt-6.1-sol", "openai-compat", 5, 256, 256, true},
		{"gpt-6", "openai-compat", 300, 128, 300, false},
		{"gpt-6-luna", "openai-compat", 5, 0, 5, false},
		{"fixture-nonreasoning", "openai-compat", 5, 128, 5, false},
		{"gpt-60-fixture", "openai-compat", 5, 128, 5, false},
		{"gpt-6-luna", "anthropic", 5, 128, 5, false},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.provider, tc.model, tc.minimum), func(t *testing.T) {
			base := &outputClient{text: "<choice>A</choice>"}
			row := result.Case{QA: &result.QA{}}
			c := readerClient{Client: base, row: &row, official: new(5), min: tc.minimum}
			resp, err := c.Complete(t.Context(), llm.Request{Purpose: "reader", Provider: tc.provider, Model: tc.model, MaxTokens: &tc.requested})
			if err != nil || len(base.requests) != 1 || *base.requests[0].MaxTokens != tc.want || *row.QA.ReaderOutput.OfficialMaxTokens != 5 || *row.QA.ReaderOutput.EffectiveMaxTokens != tc.want {
				t.Fatal(resp, err, row.QA)
			}
			if strings.Contains(strings.Join(resp.Deviations, " "), "reasoning budget raised") != tc.raised {
				t.Fatal(resp.Deviations)
			}
			// The same client wrapping a judge request must not change its budget.
			_, err = c.Complete(t.Context(), llm.Request{Purpose: "judge-decision", Provider: tc.provider, Model: tc.model, MaxTokens: &tc.requested})
			if err != nil || *base.requests[1].MaxTokens != tc.requested {
				t.Fatal("judge budget changed")
			}
		})
	}
	if (&FixedReaderOptions{}).reasoningMinimum() != 128 {
		t.Fatal("default")
	}
	base := &outputClient{text: "ok", finish: "length"}
	row := result.Case{QA: &result.QA{}}
	c := readerClient{Client: base, row: &row, min: 128}
	if _, err := c.Complete(t.Context(), llm.Request{Purpose: "reader", Provider: "openai-compat", Model: "gpt-6-luna"}); err != nil || base.requests[0].MaxTokens != nil || row.QA.ReaderOutput.Empty {
		t.Fatal("uncapped/nonempty length response changed", err)
	}
}

func TestEmptyReaderAllFamiliesRecordReplay(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "f18-local-only")
	for _, family := range []struct{ id, variant string }{
		{"longmemeval", "s"}, {"locomo", "text_only"}, {"personamem", "official"}, {"memoryagentbench", "accurate_retrieval"}, {"beam", "100K"}, {"memora", "weekly"}, {"halumem", "medium"}, {"convomem", "assistant_facts"}, {"prefeval", "explicit-mcq"},
	} {
		for _, response := range []struct {
			name, finish string
			text         any
		}{{"whitespace", "stop", " \n\t\u001c"}, {"empty", "length", ""}, {"null", "length", nil}} {
			t.Run(family.id+"/"+response.name, func(t *testing.T) {
				testoracle.ProtocolSources(t, family.id)
				finish := response.finish
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					json.NewEncoder(w).Encode(map[string]any{"model": "gpt-6-luna", "choices": []any{map[string]any{"message": map[string]any{"content": response.text}, "finish_reason": finish}}, "usage": map[string]any{"completion_tokens": 5, "prompt_tokens": 3, "completion_tokens_details": map[string]int{"reasoning_tokens": 5}}})
				}))
				defer server.Close()
				cfg := llm.DefaultConfig()
				cfg.BaseURL = server.URL + "/v1"
				cfg.CassetteDir = t.TempDir()
				cfg.MaxRetries = 0
				var previous string
				for _, mode := range []string{"record-once", "replay"} {
					if mode == "replay" {
						server.Close()
					}
					cfg.Mode = mode
					engine, err := llm.New(cfg)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { engine.Close() })
					o := DefaultOptions()
					o.suiteSHA = strings.Repeat("42", 32)
					o.RunSeed = strings.Repeat("ab", 32)
					o.benchmarkID = family.id
					o.episodeSpeakers = map[string][]string{"ep": {"A", "B"}}
					o.FixedReader = &FixedReaderOptions{Client: engine, Provider: "openai-compat", Model: "gpt-6-luna", Input: "sources", MaxTokens: 5, JudgeDecisionModel: "jev-1.14.0"}
					id, _ := identity.New(o.RunSeed, "fixture")
					o.shared = &episodeState{id: id}
					c := suite.Case{ID: "case", Kind: "qa", Query: "question", User: "user", Category: "fixture", QuestionType: "knowledge-update", Gold: suite.Gold{Answers: []string{"answer"}, Checker: &suite.Checker{}}}
					switch family.id {
					case "locomo":
						c.QuestionType = "5"
						c.Gold.Checker = &suite.Checker{Type: "locomo_official", Params: json.RawMessage(`{"category":5}`)}
						c.Attributes = map[string]any{"adversarial_answer": "private"}
					case "personamem":
						c.Kind = "mcq"
						c.Gold.MCQ = &suite.MCQSpec{Parse: "personamem_final_answer"}
						c.Attributes = map[string]any{"options_raw": "['a','b','c','d']"}
					case "memoryagentbench":
						c.Gold.Checker.Type = "mab_f1"
						c.Attributes = map[string]any{"mab_source": "ruler_qa"}
					case "beam":
						c.Kind = "rubric"
						c.Gold.Checker.Type = "beam_rubric"
						c.Gold.Rubric = &suite.Rubric{}
					case "memora":
						c.Kind = "rubric"
						c.Gold.Checker.Type = "memora_fama"
						c.Gold.Rubric = &suite.Rubric{Aggregate: "fama"}
					case "halumem":
						c.Gold.Checker.Type = "halumem_qa_judge"
					case "convomem":
						c.Gold.Checker.Type = "convomem_judge"
					case "prefeval":
						c.Kind = "mcq"
						c.Gold.MCQ = &suite.MCQSpec{Parse: "prefeval_choice_tag"}
						c.Options = map[string]string{"A": "a", "B": "b", "C": "c", "D": "d"}
					}
					event := suite.Event{SourceID: "raw", Session: "c0", Role: "user", Content: "context", Attributes: map[string]any{"speaker": "A", "memeval_reader_sequence": 0}}
					items := []protocol.Item{{Text: "context", SourceIDs: []string{"public"}}}
					row := result.Case{CaseID: c.ID, SampleID: "case#0", EpisodeID: "ep", Kind: c.Kind, Category: c.Category, Budget: protocol.Budget{MaxContextChars: 100000}, Status: result.Scored, Metrics: map[string]*float64{"old-retrieval-metric": new(1.)}}
					if err := answerCase(t.Context(), &row, c, items, map[string]suite.Event{"public": event}, o); err != nil {
						t.Fatal(err)
					}
					if row.Status != result.Invalid || row.Passed != nil || len(row.Metrics) != 0 || row.QA.Hypothesis == nil || row.QA.JudgeLabel != nil || row.QA.ReaderOutput.FinishReason != finish || row.QA.ReaderOutput.ReasoningTokens != 5 {
						t.Fatal(row)
					}
					entries := engine.Entries()
					if len(entries) != 1 || entries[0].ReasoningTokens != 5 || entries[0].CassetteHit != (mode == "replay") {
						t.Fatal("judge/retry/usage", entries)
					}
					ref := "protocols/" + family.id + ".json#" + family.variant
					r := &result.Report{Cases: []result.Case{row}, Summary: scoring.Aggregate([]result.Case{row}), Protocol: result.Protocol{Benchmark: &result.BenchmarkRef{Ref: ref, Variant: family.variant}}}
					s := &suite.Suite{Episodes: []suite.Episode{{Cases: []suite.Case{c}}}}
					if err := fixedMetadata(r, s, o); err != nil {
						t.Fatal(err)
					}
					readerOutputMetadata(r, o)
					off := r.Scores.Official.(result.Official)
					if off.QuestionSet.Evaluated != 0 || off.Invalid != 1 || r.Summary.Invalid != 1 || r.Summary.ReaderEmptyAnswers != 1 || r.Summary.ExecutionErrors != 0 || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "100.00%") {
						t.Fatal(off, r.Summary, r.Warnings)
					}
					// HaluMem must also exclude invalid QA from its all-denominator counters.
					if family.id == "halumem" {
						for k, v := range off.Metrics {
							if strings.Contains(k, "question_answering") && v != nil && *v != 0 {
								t.Fatal(k, *v)
							}
						}
					}
					b, _ := json.Marshal(row)
					if mode == "replay" && string(b) != previous {
						t.Fatal("record/replay drift")
					}
					previous = string(b)
					if mode == "replay" && (engine.Billing().CostNew == nil || *engine.Billing().CostNew != 0) {
						t.Fatal("replay cost")
					}
				}
				if calls != 1 {
					t.Fatal("extra HTTP or ConvoMem retried terminal empty response", calls)
				}
			})
		}
	}
}
