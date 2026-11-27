package judge_test

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/llm"
	"memeval/internal/llm/decision"
	"memeval/internal/scoring/convomem"
	"memeval/internal/scoring/halumem"
	"memeval/internal/scoring/judge"
	beam "memeval/internal/scoring/judge/beam"
	"memeval/internal/scoring/judge/inputlog"
	"memeval/internal/scoring/mab"
	"memeval/internal/scoring/memora"
	"memeval/internal/scoring/prefeval"
	"memeval/internal/testdecision"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

var updateGolden = flag.Bool("update-p5-golden", false, "explicitly regenerate P5 assembly golden (maintainers only)")

func families(ctx context.Context, r *judge.Router) (out []any, err error) {
	add := func(v any, e error) bool {
		out = append(out, v)
		if e != nil {
			err = e
		}
		return e == nil
	}
	for _, kind := range []string{"single-session-user", "single-session-assistant", "multi-session", "temporal-reasoning", "knowledge-update", "single-session-preference"} {
		v, e := r.Evaluate(ctx, judge.Input{SampleID: kind, QuestionType: kind, Question: "Q {literal}", Reference: "Reference", Response: "Response"})
		if !add(v, e) {
			return
		}
	}
	v, e := r.Evaluate(ctx, judge.Input{SampleID: "abstention", QuestionType: "multi-session", Question: "Q", Reference: "Unknown", Response: "I do not know", Abstention: true})
	if !add(v, e) {
		return
	}
	sv, e := beam.Rubric(ctx, r, "beam-rubric", "Criterion", "Response")
	if !add(sv, e) {
		return
	}
	v, e = beam.Equivalent(ctx, r, "beam-equivalence", "First", "Second")
	if !add(v, e) {
		return
	}
	mv, e := memora.Evaluate(ctx, r, "memora", "Evaluation question", "Response")
	if !add(mv, e) {
		return
	}
	pv, e := prefeval.Evaluate(ctx, r, "prefeval", "User query", "Response", "Preference")
	if !add(pv, e) {
		return
	}
	for _, kind := range []string{"default", "rubric", "abstention"} {
		v, e := convomem.Evaluate(ctx, r, "convo-"+kind, kind, "Question", "Reference", "Response")
		if !add(v, e) {
			return
		}
	}
	states := map[string]map[string]string{"integrity": {"memories": "Memories", "expected_memory_point": "Point"}, "accuracy": {"dialogue": "Dialogue", "golden_memories": "Golden", "candidate_memory": "Candidate"}, "update": {"memories": "Memories", "updated_memory": "Updated", "original_memory": "Original"}, "question": {"question": "Question", "reference_answer": "Reference", "key_memory_points": "Points", "response": "Response"}}
	for _, kind := range []string{"integrity", "accuracy", "update", "question"} {
		v, e := halumem.Evaluate(ctx, r, "halu-"+kind, kind, states[kind])
		if !add(v, e) {
			return
		}
	}
	v, e = mab.Fluency(ctx, r, "mab", " Text ")
	add(v, e)
	return
}
func TestFamilyAssemblyGolden(t *testing.T) {
	c := &testdecision.Client{P: 1}
	_, e := families(t.Context(), &judge.Router{Decision: c, DecisionModel: "jev-1.14.0"})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range c.Requests {
		if r.Purpose != "judge-decision" || !strings.Contains(r.TemplateID, "-v2@") {
			t.Fatal(r)
		}
		name, sha, _ := strings.Cut(r.TemplateID, "@")
		if judge.TemplateID(name, r.Questions) != name+"@"+sha {
			t.Fatal("instructions hash")
		}
		var state map[string]any
		if json.Unmarshal([]byte(r.State), &state) != nil || state["prompt"] != nil {
			t.Fatal("unstructured", r.State)
		}
	}
	data, _ := json.MarshalIndent(c.Requests, "", "  ")
	data = append(data, '\n')
	path := "testdata/assemblies-v2.json"
	if *updateGolden {
		if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, data, 0644); e != nil {
			t.Fatal(e)
		}
	}
	want, e := os.ReadFile(path)
	if e != nil || string(want) != string(data) {
		t.Fatal("assembly state/instructions/criteria/SHA drift", e)
	}
	for _, r := range c.Requests {
		if strings.HasPrefix(r.TemplateID, "prefeval/noul-hallucinate-v2@") {
			if r.State != `{"preference":"Preference","response":"Response","user_query":"User query"}` || !strings.Contains(r.Questions[0].Instructions, "response's restatement or attribution of a preference to the user") {
				t.Fatal(r)
			}
		}
	}
}
func TestAllFamiliesTypeSafeRecordReplay(t *testing.T) {
	t.Setenv("MEMEVAL_TYPESAFE_API_KEY", "fixture-private-key")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
				return 1
			default:
				return ""
			}
		})
	}))
	defer server.Close()
	cfg := llm.DefaultConfig()
	cfg.Mode = "record-once"
	cfg.CassetteDir = t.TempDir()
	cfg.BaseURL = server.URL
	cfg.PricesPath = ""
	cfg.MaxRetries = 0
	run := func(mode string) ([]any, []inputlog.Record) {
		cfg.Mode = mode
		cfg.LedgerPath = filepath.Join(t.TempDir(), "ledger.jsonl")
		engine, e := llm.New(cfg)
		if e != nil {
			t.Fatal(e)
		}
		defer engine.Close()
		engine.Start("fixture")
		dir := t.TempDir()
		rec, e := inputlog.New(t.Context(), dir)
		if e != nil {
			t.Fatal(e)
		}
		defer rec.Close()
		out, e := families(inputlog.WithRecorder(t.Context(), rec), &judge.Router{Decision: &decision.Client{Engine: engine}, DecisionModel: "jev-1.14.0"})
		if e != nil {
			t.Fatal(e)
		}
		meta, e := rec.Snapshot()
		if e != nil {
			t.Fatal(e)
		}
		rows, e := inputlog.Read(dir, *meta)
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) != 22 {
			t.Fatal(len(rows))
		}
		if mode == "replay" && *engine.Billing().CostNew != 0 {
			t.Fatal(engine.Billing())
		}
		for _, row := range rows {
			if !row.Decision.Valid || row.Decision.Kind != "decision" || row.Decision.RequestSHA == "" {
				t.Fatal(row)
			}
		}
		return out, rows
	}
	a, ar := run("record-once")
	n := calls.Load()
	server.Close()
	b, br := run("replay")
	if calls.Load() != n || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ar, br) {
		t.Fatal("record/replay changed")
	}
	t.Logf("7 families, %d calls; replay zero HTTP/new cost; full decision input rows equal", n)
}
