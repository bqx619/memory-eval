package runner_test

import (
	"context"
	"encoding/json"
	"memeval/internal/importers/locomo"
	"memeval/internal/importers/personamem"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"strings"
	"testing"
)

func TestIngestRenderAndHash(t *testing.T) {
	s := toy()
	s.SchemaVersion = "3.0"
	s.Episodes[0].Cases[0].QueryPoint = "at_index"
	s.Episodes[0].Events[0].Attributes = map[string]any{"speaker": "Aster"}
	a := newSpy()
	r := run(t, s, a, opts())
	s.Protocol.SpeakerMapping = map[string]string{"ingest_render": "{speaker}: {content}"}
	b := newSpy()
	changed := run(t, s, b, opts())
	if r.Cases[0].PublicInputSHA256 == changed.Cases[0].PublicInputSHA256 || changed.Protocol.IngestRender != "{speaker}: {content}" {
		t.Fatal("render hash or disclosure")
	}
	wire, _ := json.Marshal(b.inputs)
	if !strings.Contains(string(wire), "Aster: ") {
		t.Fatal("not rendered")
	}
	s.Protocol.SpeakerMapping["ingest_render"] = "{gold}"
	if _, e := suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true}); e == nil {
		t.Fatal("unknown render placeholder")
	}
}
func TestP2DeterministicRoutes(t *testing.T) {
	for _, id := range []string{"locomo", "personamem"} {
		t.Run(id, func(t *testing.T) {
			testoracle.ProtocolSources(t, id)
			var s *suite.Suite
			var e error
			if id == "locomo" {
				s, _, e = locomo.Import(t.Context(), "../importers/locomo/testdata/synthetic_locomo.json")
			} else {
				s, _, e = personamem.Import(t.Context(), "../importers/personamem/testdata/questions.csv", "../importers/personamem/testdata/contexts.jsonl", "32k", "include")
			}
			if e != nil {
				t.Fatal(e)
			}
			client := &fakeLLM{answer: "<final_answer>(c)"}
			o := fixedOpts(client)
			o.FixedReader.DecisionClient = nil
			r := run(t, s, newSpy(), o)
			if len(client.requests) != len(r.Cases) {
				t.Fatal("judge called")
			}
			for _, q := range client.requests {
				if q.Purpose != "reader" {
					t.Fatal(q.Purpose)
				}
			}
			official := r.Scores.Official.(result.Official)
			if official.Judge != nil || r.Protocol.Judge != nil {
				t.Fatal("deterministic judge metadata")
			}
			for _, c := range r.Cases {
				if c.Status != result.Scored {
					t.Fatal(c)
				}
				if id == "personamem" && c.AnswerParse == nil {
					t.Fatal("missing parse audit")
				}
			}
		})
	}
}
func TestFixedReaderRequiresBinding(t *testing.T) {
	s := toy()
	_, e := runner.Run(t.Context(), s, newSpy(), fixedOpts(&fakeLLM{}))
	if e == nil || !strings.Contains(e.Error(), "bound benchmark") {
		t.Fatal(e)
	}
}
func TestDeclaredOwnerLabelLeakScan(t *testing.T) {
	s, _, e := locomo.Import(t.Context(), "../importers/locomo/testdata/synthetic_locomo.json")
	if e != nil {
		t.Fatal(e)
	}
	s.Episodes[0].Cases[0].Query = "What happened in this conversation?"
	scan, e := suite.LeakScan(s)
	if e != nil || len(scan.Problems) > 0 {
		t.Fatal(scan, e)
	}
	s.Episodes[0].Cases[0].Query += " synthetic-conv-0"
	scan, e = suite.LeakScan(s)
	if e != nil || len(scan.Problems) == 0 {
		t.Fatal("case/episode scan weakened", scan, e)
	}
	s.Episodes[0].Events[0].Attributes["adversarial_answer"] = "private"
	scan, _ = suite.LeakScan(s)
	if !strings.Contains(strings.Join(scan.Problems, " "), "adversarial_answer") {
		t.Fatal("privileged annotation not detected")
	}
}
