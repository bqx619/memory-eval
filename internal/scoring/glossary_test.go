package scoring_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"memeval/internal/llm"
	"memeval/internal/result"
	"memeval/internal/sandbox/container/governor"
	"memeval/internal/scoring"
	"memeval/internal/scoring/halumem"
	"memeval/internal/scoring/mab"
	"memeval/internal/scoring/prefeval"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestGlossaryCoversEmittedMetrics(t *testing.T) {
	defs := scoring.Glossary()
	ids := map[string]string{}
	add := func(id, source string) { ids[id] = source }
	checkKeys := func(prefix string, m any) {
		v := reflect.ValueOf(m)
		for _, k := range v.MapKeys() {
			add(prefix+k.String(), "scorer output")
		}
	}
	// Exercise the generators, rather than duplicating their dynamic key lists.
	for _, gold := range [][]string{nil, {"a"}} {
		m, e := scoring.Retrieval([]string{"a"}, gold, true, 10)
		if e != nil {
			t.Fatal(e)
		}
		checkKeys("", m.Values)
	}
	checkKeys("", scoring.LexicalAnswer("a", []string{"a"}))
	_, mcq, e := scoring.MCQ("a", map[string]string{"a": "A"}, "a")
	if e != nil {
		t.Fatal(e)
	}
	checkKeys("", mcq)
	checkKeys("", scoring.LongMemEval([]suite.Event{{SourceID: "a", Session: "s"}}, []string{"a"}, []string{"a"}, []string{"s"}))
	checkKeys("", halumem.Aggregate(halumem.NewRecords()))
	checkKeys("", halumem.Aggregate(halumem.Records{Integrity: []halumem.Integrity{{Type: "other type"}}}))
	checkKeys("", prefeval.Scores{}.Metrics())
	for _, source := range []string{"icl", "eventqa", "ruler_niah", "infbench_choice_eng", "other"} {
		v, e := mab.PostProcess("Answer: Blue", []string{"Blue", "A"}, source, nil)
		if e != nil {
			t.Fatal(e)
		}
		checkKeys("mab_", v.Metrics)
	}
	candidates := &mab.Candidates{ByID: map[int]string{1: "Movie"}, Names: []string{"Movie"}}
	rec, e := candidates.Score("1. Movie", []string{"1"})
	if e != nil {
		t.Fatal(e)
	}
	checkKeys("mab_", rec.Metrics)

	// Collect direct writes and literal maps in production scorer/runner code.
	// New dynamic expressions must be explicitly audited: no broad family wildcard
	// can silently make an unrecognised metric pass this test.
	dynamic := map[string][]string{
		`"decision." + name`: {"decision.action", "decision.correct", "decision.quality", "decision.user_defined"},
		`"locomo_official.f1.cat" + strconv.Itoa(category)`: {"locomo_official.f1.cat1", "locomo_official.f1.cat2", "locomo_official.f1.cat3", "locomo_official.f1.cat4", "locomo_official.f1.cat5"},
		`fmt.Sprintf("convomem.correct.n_evidence.%v", n)`:  {"convomem.correct.n_evidence.1", "convomem.correct.n_evidence.7"},
		`"mab_infbench_sum." + typ + "_f1"`:                 {"mab_infbench_sum.rouge1_f1", "mab_infbench_sum.rouge2_f1", "mab_infbench_sum.rougeL_f1"},
		`primary`:                                           {"mab_longmemeval_judge"}, // Other MAB primaries come from PostProcess above.
		`"mab_" + k`:                                        {},                        // PostProcess/Calculate/Score outputs checked above.
		`k`:                                                 {},                        // Copies from the typed scorer maps tested above.
	}
	fs := token.NewFileSet()
	for _, dir := range []string{".", "../runner", "../../cmd/memeval"} {
		files, e := filepath.Glob(filepath.Join(dir, "*.go"))
		if e != nil {
			t.Fatal(e)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "glossary.go" {
				continue
			}
			f, e := parser.ParseFile(fs, path, nil, 0)
			if e != nil {
				t.Fatal(e)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						idx, ok := lhs.(*ast.IndexExpr)
						if !ok {
							continue
						}
						sel, ok := idx.X.(*ast.SelectorExpr)
						if !ok || (sel.Sel.Name != "Metrics" && sel.Sel.Name != "Values") {
							continue
						}
						if lit, ok := idx.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							s, _ := strconv.Unquote(lit.Value)
							add(s, path)
							continue
						}
						var expr bytes.Buffer
						_ = format.Node(&expr, fs, idx.Index)
						expansions, ok := dynamic[expr.String()]
						if !ok {
							t.Errorf("unaudited metric expression %s: %s", path, expr.String())
						}
						for _, id := range expansions {
							add(id, path)
						}
					}
				case *ast.CompositeLit:
					typ, ok := node.Type.(*ast.MapType)
					if !ok {
						break
					}
					var elem bytes.Buffer
					_ = format.Node(&elem, fs, typ.Value)
					if elem.String() != "*float64" {
						break
					}
					for _, ent := range node.Elts {
						kv, ok := ent.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if lit, ok := kv.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							s, _ := strconv.Unquote(lit.Value)
							add(s, path)
						}
					}
				}
				return true
			})
		}
	}
	// Protocol primary IDs, including currently unsupported checkers (nugget).
	paths, _ := filepath.Glob("../../protocols/*.json")
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		var v struct {
			Scoring struct {
				Primary string `json:"primary"`
			} `json:"scoring"`
		}
		if e = json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		if v.Scoring.Primary != "" {
			add(v.Scoring.Primary, p)
		}
	}
	// Machine-report status, timing and billing tags are source-defined too.
	for _, v := range []struct {
		prefix string
		typ    reflect.Type
	}{{"cost.sut.", reflect.TypeFor[governor.Billing]()}, {"cost.evaluator.", reflect.TypeFor[llm.Billing]()}} {
		for _, field := range reflect.VisibleFields(v.typ) {
			if field.Type.Kind() == reflect.Slice {
				continue
			}
			add(v.prefix+strings.Split(field.Tag.Get("json"), ",")[0], v.typ.String())
		}
	}
	for _, field := range reflect.VisibleFields(reflect.TypeFor[result.Summary]()) {
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Type.Kind() != reflect.Struct && tag != "metrics" && tag != "timings" && tag != "failures" {
			add(tag, "result.Summary")
		}
	}
	for _, phase := range []string{"reset", "ingest", "ready", "query", "answer", "decision"} {
		add("timings."+phase+"_ms", "result.Case.Timings")
	}
	add("wall_seconds", "result.Report")
	for id, source := range ids {
		if !slices.ContainsFunc(defs, func(d scoring.MetricDef) bool { return scoring.MetricMatches(id, d.ID) }) {
			t.Errorf("no glossary for %q emitted by %s", id, source)
		}
	}
	t.Logf("covered %d emitted/declared metric IDs with %d definitions", len(ids), len(defs))
}
func TestGlossaryStableAndSpecific(t *testing.T) {
	defs := scoring.Glossary()
	for i, d := range defs {
		if d.ID == "" || d.NameZh == "" || d.DefinitionZh == "" || d.Group == "" || d.Range == "" || d.Aggregation == "" || d.Caveats == nil || d.AppliesTo == nil {
			t.Fatal("incomplete", d)
		}
		if i > 0 && defs[i-1].ID >= d.ID {
			t.Fatal("duplicate/unsorted", d.ID)
		}
	}
	a, _ := json.Marshal(defs)
	b, _ := json.Marshal(scoring.Glossary())
	if !bytes.Equal(a, b) {
		t.Fatal("nondeterministic glossary")
	}
	if !scoring.MetricMatches("longmemeval_official.turn.recall_any@50", "longmemeval_official.turn.recall_any@k") || scoring.MetricMatches("longmemeval_official.turn.recall_any@future", "longmemeval_official.turn.recall_any@k") {
		t.Fatal("@k matching")
	}
	for _, unknown := range []string{"unexpected_metric", "beam.new_score", "halumem.new_score"} {
		if slices.ContainsFunc(defs, func(d scoring.MetricDef) bool { return scoring.MetricMatches(unknown, d.ID) }) {
			t.Fatal("coverage incorrectly accepts", unknown)
		}
	}
}
