package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"memeval/internal/testdecision"
	"memeval/internal/testoracle"
	"strings"
	"testing"
)

type fakeLLM struct {
	requests  []llm.Request
	decisions []decision.DecisionRequest
	fail      string
	answer    string
}

func (f *fakeLLM) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, r)
	if r.Purpose == f.fail {
		return llm.Response{}, errors.New("test error must be redacted")
	}
	text := f.answer
	return llm.Response{Text: text, RequestSHA: "fixture-" + r.Purpose}, nil
}
func (f *fakeLLM) Decide(ctx context.Context, r decision.DecisionRequest) (decision.DecisionResponse, error) {
	f.decisions = append(f.decisions, r)
	d := testdecision.Client{P: 1}
	if f.fail == "judge" {
		d.Err = errors.New("synthetic judge failure")
	}
	return d.Decide(ctx, r)
}
func fixedOpts(client llm.Client) runner.Options {
	o := opts()
	o.Mode = runner.FixedReader
	o.FixedReader = &runner.FixedReaderOptions{Client: client, Provider: "openai-compat", Model: "reader-fixture", Input: "sources", JudgeDecisionModel: "jev-1.14.0", DecisionClient: &testdecision.Client{P: 1}, MaxTokens: 512}
	if d, ok := client.(decision.DecisionClient); ok {
		o.FixedReader.DecisionClient = d
	}
	return o
}
func qaToy() *suite.Suite {
	s := toy()
	s.SchemaVersion = "3.0"
	s.ProtocolRef = "protocols/longmemeval.json#s"
	s.Episodes[0].Cases[0].QueryPoint = "at_index"
	s.Protocol.QueryTimePolicy = "reference_only"
	s.Episodes[0].Cases[0].QuestionType = "knowledge-update"
	s.Episodes[0].Cases[0].QueryTime = "2025-03-04T05:06:00Z"
	return s
}
func TestFixedReaderBoundaryAndReferenceDate(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	s := qaToy()
	client := &fakeLLM{answer: " \nABSTAIN\x1c"}
	a := newSpy()
	r := run(t, s, a, fixedOpts(client))
	if a.request.AsOf != "" || len(client.requests) != 1 || len(client.decisions) != 1 {
		t.Fatal("as_of or LLM call count")
	}
	reader, j := client.requests[0], client.decisions[0]
	if !strings.Contains(reader.Messages[0].Content, "Current Date: 2025/03/04 (Tue) 05:06") || strings.Contains(reader.Messages[0].Content, "SECRET-GOLD") || strings.Contains(reader.SampleID, "PRIVATE-QUESTION") {
		t.Fatal("reader date/private boundary")
	}
	if !strings.Contains(j.State, "SECRET-GOLD") {
		t.Fatal("judge did not receive evaluator-only answer")
	}
	wire, _ := json.Marshal([]any{a.inputs, a.request})
	if strings.Contains(string(wire), "SECRET-GOLD") || strings.Contains(string(wire), "has_answer") {
		t.Fatal("gold leaked to SUT")
	}
	c := r.Cases[0]
	if c.QA == nil || *c.QA.Hypothesis != "ABSTAIN" || !c.QA.ReaderAbstain || c.QA.SUTAbstain != nil || c.Passed == nil || !*c.Passed {
		t.Fatal(c)
	}
	official := r.Scores.Official.(result.Official)
	if official.Fidelity != "partial" || official.QuestionSet.Evaluated != 1 || official.JudgeCompatible == nil || *official.JudgeCompatible || *official.Score != 1 {
		t.Fatal(official)
	}
	// Randomized SUT namespaces must not invalidate evaluator cassettes.
	client2 := &fakeLLM{answer: "ABSTAIN"}
	o := fixedOpts(client2)
	o.RunSeed = strings.Repeat("ab", 32)
	run(t, s, newSpy(), o)
	if client2.requests[0].SampleID != reader.SampleID || client2.requests[0].Messages[0] != reader.Messages[0] {
		t.Fatal("replay identity not stable")
	}
}
func TestAbsAndErrorDenominators(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	for _, phase := range []string{"", "reader", "judge"} {
		s := qaToy()
		s.Episodes[0].Cases[0].ID = "private_abs_question"
		s.Episodes[0].Cases[0].Gold.Abstain = true
		client := &fakeLLM{fail: phase, answer: "not enough information"}
		r := run(t, s, newSpy(), fixedOpts(client))
		official := r.Scores.Official.(result.Official)
		if phase == "reader" {
			if official.QuestionSet.Evaluated != 0 || official.Score != nil || official.Errors != 1 {
				t.Fatal(official)
			}
		} else {
			if official.QuestionSet.Evaluated != 1 {
				t.Fatal(official)
			}
			if !strings.Contains(client.decisions[0].Questions[0].Instructions, "unanswerable question") {
				t.Fatal("wrong abstention template")
			}
		}
		if phase == "judge" && (*official.Score != 0 || official.Judged != 0 || official.Errors != 1) {
			t.Fatal(official)
		}
	}
}
func TestFullContextAndTruncation(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	s := qaToy()
	s.Episodes[0].Events = []suite.Event{
		{SourceID: "a", Owner: "u", Session: "old", Timestamp: "2023-01-01T00:00:00Z", Role: "user", Content: strings.Repeat("old", 50)},
		{SourceID: "b", Owner: "u", Session: "new", Timestamp: "2024-01-01T00:00:00Z", Role: "user", Content: "newest"},
	}
	s.Episodes[0].Cases[0].Cutoff = 2
	s.Episodes[0].Cases[0].Gold.EvidenceIDs = []string{"b"}
	client := &fakeLLM{answer: "answer"}
	o := fixedOpts(client)
	o.Budget.TopK = 0
	o.Budget.MaxContextChars = 100
	a, _ := adapter.NewBaseline("fullcontext")
	r := run(t, s, a, o)
	if len(r.Cases[0].RetrievedSourceIDs) != 2 || !r.Cases[0].QA.Truncated || r.Cases[0].QA.DroppedSessions != 1 || r.Cases[0].Status != result.Scored {
		t.Fatal(r.Cases)
	}
	p := client.requests[0].Messages[0].Content
	if strings.Contains(p, "oldold") || !strings.Contains(p, "newest") {
		t.Fatal(p)
	}
	if r.Scores.Official.(result.Official).Fidelity != "partial" || r.Isolation.Tier != "C" {
		t.Fatal(r.Scores)
	}
}

func TestDecisionOfficialAlwaysPartial(t *testing.T) {
	testoracle.ProtocolSources(t, "longmemeval")
	s := qaToy()
	s.SuiteID = "longmemeval-s-cleaned@unit-fixture"
	original := s.Episodes[0].Cases[0]
	s.Episodes[0].Cases = nil
	for i := 0; i < 500; i++ {
		c := original
		c.ID = fmt.Sprintf("q-%d", i)
		s.Episodes[0].Cases = append(s.Episodes[0].Cases, c)
	}
	for _, variant := range []string{"full", "items", "model", "subset"} {
		c := &fakeLLM{answer: "answer"}
		o := fixedOpts(c)
		switch variant {
		case "items":
			o.FixedReader.Input = "items"
		case "model":
			o.FixedReader.JudgeDecisionModel = "jev-2.0.0"
		case "subset":
			n := 499
			o.Limit = &n
		}
		a, _ := adapter.NewBaseline("null")
		r := run(t, s, a, o)
		official := r.Scores.Official.(result.Official)
		if official.Fidelity != "partial" {
			t.Fatal(variant, official)
		}
		if len(official.ByType) != 6 || official.ByType["multi-session"].Accuracy != nil {
			t.Fatal("empty question-type denominator")
		}
	}
}
