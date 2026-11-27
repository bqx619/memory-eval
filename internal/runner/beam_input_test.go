package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/llm/decision"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"strings"
	"testing"
)

func answerInputBEAM(t *testing.T) *suite.Suite {
	t.Helper()
	testoracle.ProtocolSources(t, "beam")
	s := qaToy()
	s.ProtocolRef = "protocols/beam.json#100K"
	s.Episodes[0].Events[0].Role = "user"
	c := &s.Episodes[0].Cases[0]
	c.Kind, c.Category = "rubric", "information_extraction"
	c.Gold.Checker = &suite.Checker{Type: "beam_rubric"}
	c.Gold.Rubric = &suite.Rubric{Aggregate: "mean", Items: []suite.RubricItem{{Text: "SECRET-GOLD", Polarity: "as_written", Weight: 1}}}
	return s
}

type beamOrderingClient struct{ fakeLLM }

func (f *beamOrderingClient) Decide(ctx context.Context, r decision.DecisionRequest) (decision.DecisionResponse, error) {
	f.decisions = append(f.decisions, r)
	d := testdecision.Client{P: 1}
	if r.Questions[0].Type == "noul" {
		var state map[string]string
		if err := json.Unmarshal([]byte(r.State), &state); err != nil {
			return decision.DecisionResponse{}, err
		}
		if state["first_snippet"] != state["second_snippet"] {
			d.P = 0
		}
	}
	return d.Decide(ctx, r)
}

func TestBEAMItemsEventOrderingUsesAnswerAndPrivateReference(t *testing.T) {
	for _, mode := range []string{"sources", "items"} {
		for _, reversed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reversed=%v", mode, reversed), func(t *testing.T) {
				s := answerInputBEAM(t)
				c := &s.Episodes[0].Cases[0]
				c.Category, c.Gold.Checker.Type = "event_ordering", "beam_event_ordering"
				c.Gold.Sequence = []string{"PRIVATE-FIRST", "PRIVATE-SECOND"}
				text, tau, final := "PRIVATE-FIRST\nPRIVATE-SECOND", 1., 1.
				if reversed {
					text, tau, final = "PRIVATE-SECOND\nPRIVATE-FIRST", -1, 0
				}
				client := &beamOrderingClient{fakeLLM: fakeLLM{answer: text}}
				a := newSpy()
				a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
					ids := []string{a.inputs[0].ID}
					if mode == "items" {
						ids = []string{}
					}
					return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "remembered account", SourceIDs: ids}}}, nil
				}
				o := fixedOpts(client)
				o.FixedReader.Input = mode
				r := run(t, s, a, o)
				row := r.Cases[0]
				if row.Status != result.Scored || len(client.requests) != 1 {
					t.Fatal("ordering does not require source turn structure", row)
				}
				for metric, want := range map[string]float64{"precision": 1, "recall": 1, "f1": 1, "tau_b": tau, "final": final} {
					got := row.Metrics["beam.event_ordering."+metric]
					if got == nil || *got != want {
						t.Fatalf("%s=%v want %g", metric, got, want)
					}
				}
				if *row.Metrics["beam.llm_judge_score"] != 1 || r.Scores.Official.(result.Official).Fidelity == "full" {
					t.Fatal("rubric/fidelity changed", r.Scores.Official)
				}
				public, _ := json.Marshal([]any{a.inputs, a.request, client.requests})
				for _, private := range append([]string{"SECRET-GOLD", "FUTURE-FORBIDDEN"}, c.Gold.Sequence...) {
					if strings.Contains(string(public), private) {
						t.Fatalf("ordering reference leaked before judge: %s", private)
					}
				}
				if mode == "items" && (row.Metrics["recall"] != nil || row.Diagnostics["untraceable_hits"] != 1) {
					t.Fatal("ordering score invented source retrieval", row)
				}
				if len(client.decisions) < 3 {
					t.Fatal("alignment and rubric judges were not both called")
				}
			})
		}
	}
}

func TestBEAMAnswerInputUsesSelectedRepresentation(t *testing.T) {
	for _, mode := range []string{"items", "sources"} {
		t.Run(mode, func(t *testing.T) {
			s := answerInputBEAM(t)
			client := &fakeLLM{answer: "reader answer"}
			a := newSpy()
			a.retrieve = func(a *spy) (protocol.RetrieveResponse, error) {
				return protocol.RetrieveResponse{Items: []protocol.Item{{Text: "rewritten memory", SourceIDs: []string{a.inputs[0].ID}}}}, nil
			}
			o := fixedOpts(client)
			o.FixedReader.Input = mode
			r := run(t, s, a, o)
			if r.Cases[0].Status != result.Scored || len(client.requests) != 1 || len(client.decisions) != 1 || *r.Cases[0].Metrics["beam.llm_judge_score"] != 1 {
				t.Fatal("BEAM reader/rubric not scored", r.Cases)
			}
			prompt, _ := json.Marshal(client.requests[0].Messages)
			want, absent := "rewritten memory", "blue box"
			if mode == "sources" {
				want, absent = absent, want
			}
			if !strings.Contains(string(prompt), want) || strings.Contains(string(prompt), absent) {
				t.Fatalf("%s used wrong representation: %s", mode, prompt)
			}
			public, _ := json.Marshal([]any{a.inputs, a.request, client.requests})
			for _, private := range []string{"SECRET-GOLD", "FUTURE-FORBIDDEN", "has_answer", "PRIVATE-QUESTION"} {
				if strings.Contains(string(public), private) {
					t.Fatalf("private data leaked to SUT/reader: %s", private)
				}
			}
			if !strings.Contains(client.decisions[0].State, "SECRET-GOLD") {
				t.Fatal("rubric must remain available only to judge")
			}
		})
	}
}

func TestBEAMItemsRankUnicodeBudgetAndDisclosure(t *testing.T) {
	for _, budget := range []int{6, 5, 4} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			s := answerInputBEAM(t)
			client := &fakeLLM{answer: "reader answer"}
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
			want, dropped := "🙂甲\n乙\n丙", 0
			if budget < 6 {
				want, dropped = "🙂甲\n乙", 1
			}
			qa := r.Cases[0].QA
			messages := client.requests[0].Messages
			if len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != want || qa.Truncated != (dropped > 0) || qa.DroppedMessages != dropped {
				t.Fatalf("rank/Unicode/separator budget: %+v QA=%+v", messages, qa)
			}
			official := r.Scores.Official.(result.Official)
			if r.Protocol.ReaderInput != "items" || r.Reader.(map[string]any)["input_mode"] != "items" || official.Fidelity == "full" || !strings.Contains(strings.Join(official.Deviations, " "), "reader_input=items") || !strings.Contains(strings.Join(qa.Deviations, " "), "reader_input=items") {
				t.Fatal("missing input deviation", r.Reader, official, qa)
			}
			if !strings.HasSuffix(official.Reader.PromptID, "/input=items") || !strings.HasSuffix(r.Reader.(map[string]any)["prompt_id"].(string), "/input=items") {
				t.Fatal("reader identity not disclosed", r.Reader, official.Reader)
			}
			if dropped > 0 && !strings.Contains(strings.Join(qa.Deviations, " "), "lowest-ranked") {
				t.Fatal("missing item truncation disclosure", qa)
			}
			if v := r.Cases[0].Metrics["recall"]; v == nil || *v != 1 {
				t.Fatal("fully attributed diagnostics changed", r.Cases[0].Metrics)
			}
		})
	}
}
