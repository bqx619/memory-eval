package halumem

import (
	"math"
	"math/rand/v2"
	"memeval/internal/testoracle"
	"reflect"
	"testing"
)

func ip(n int) *int       { return &n }
func sp(s string) *string { return &s }
func TestAggregateHand(t *testing.T) {
	r := Records{Integrity: []Integrity{{"system", "Persona Memory", 1, ip(2)}, {"secondary", "Persona Memory", .5, ip(1)}, {"system", "Event Memory", 1, nil}, {"interference", "Event Memory", 1, ip(0)}, {"interference", "Event Memory", 1, nil}}, Accuracy: []Accuracy{{ip(2), "true"}, {ip(1), "True"}, {nil, "true"}, {ip(0), false}}, Update: []Update{{"Relationship Memory", 1, sp("Correct")}, {"Relationship Memory", 1, nil}}, QA: []QA{{"Basic", sp("Omission")}, {"Basic", sp("Correct")}, {"Basic", nil}}}
	m := Aggregate(r)
	want := map[string]float64{"memory_integrity.recall(all)": 1. / 3, "memory_integrity.recall(valid)": .5, "memory_integrity.weighted_recall(all)": .5, "memory_integrity.weighted_recall(valid)": 1.25 / 1.5, "memory_accuracy.interference_accuracy(all)": .5, "memory_accuracy.interference_accuracy(valid)": 1, "memory_accuracy.target_accuracy(all)": .5, "memory_accuracy.target_accuracy(valid)": .75, "memory_accuracy.weighted_accuracy(all)": .375, "memory_accuracy.weighted_accuracy(valid)": .5, "memory_extraction_f1": .4, "memory_update.correct_update_memory_ratio(all)": .5, "memory_update.correct_update_memory_ratio(valid)": 1, "question_answering.correct_qa_ratio(all)": 1. / 3, "question_answering.correct_qa_ratio(valid)": .5, "memory_type_accuracy.Persona Memory.memory_acc": .5}
	for k, v := range want {
		p := m["halumem."+k]
		if p == nil || math.Abs(*p-v) > 1e-15 {
			t.Fatal(k, p, v)
		}
	}
	empty := Aggregate(NewRecords())
	if empty["halumem.memory_extraction_f1"] != nil || empty["halumem.memory_integrity.recall(all)"] != nil || *empty["halumem.memory_integrity.memory_num"] != 0 {
		t.Fatal(empty)
	}
}
func TestAggregatePythonOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 31))
	xs := []Records{}
	types := []string{"Persona Memory", "Relationship Memory", "Event Memory"}
	for range 500 {
		r := NewRecords()
		for _, typ := range types {
			r.Integrity = append(r.Integrity, Integrity{"system", typ, 1, ip(2)}, Integrity{"interference", typ, .5, ip(0)})
		}
		r.Accuracy = append(r.Accuracy, Accuracy{ip(2), "true"})
		r.Update = append(r.Update, Update{types[0], 1, sp("Correct")})
		r.QA = append(r.QA, QA{"Basic", sp("Correct")})
		for range 20 {
			var n *int
			if rng.IntN(4) > 0 {
				n = ip(rng.IntN(3))
			}
			source := []string{"system", "secondary", "interference"}[rng.IntN(3)]
			typ := types[rng.IntN(3)]
			importance := []float64{.5, .75, 1}[rng.IntN(3)]
			r.Integrity = append(r.Integrity, Integrity{source, typ, importance, n})
			r.Accuracy = append(r.Accuracy, Accuracy{n, []any{"true", "True", "false", true, nil}[rng.IntN(5)]})
			r.Update = append(r.Update, Update{typ, importance, []*string{nil, sp("Correct"), sp("Hallucination"), sp("Omission"), sp("Other"), sp("invalid")}[rng.IntN(6)]})
			r.QA = append(r.QA, QA{"Basic", []*string{nil, sp("Correct"), sp("Hallucination"), sp("Omission"), sp("invalid")}[rng.IntN(5)]})
		}
		xs = append(xs, r)
	}
	var want []map[string]float64
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());ns={}
exec(compile(ast.Module(body=[n for n in m.body if isinstance(n,ast.FunctionDef) and n.name in ('aggregate_eval_results','compute_f1')],type_ignores=[]),'vendored','exec'),ns)
out=[]
for r in json.load(sys.stdin):
 r['overall_score']={k:{} for k in ('memory_integrity','memory_accuracy','memory_update','question_answering')}
 r['overall_score']['memory_type_accuracy']={k:dict(memory_integrity_acc=0,memory_update_acc=0,total_num=0) for k in ('Persona Memory','Relationship Memory','Event Memory')}
 result=ns['aggregate_eval_results'](r)['overall_score'];flat={}
 def flatten(x,p='halumem'):
  for k,v in x.items():
   if isinstance(v,dict):flatten(v,p+'.'+k)
   else:flat[p+'.'+k]=v
 flatten(result);out.append(flat)
json.dump(out,sys.stdout)`, xs, &want, testoracle.Upstream(t, "halumem/eval/evaluation.py"))
	maxdiff := 0.
	for i, r := range xs {
		got := Aggregate(r)
		if len(got) != len(want[i]) {
			t.Fatal("metric keys", len(got), len(want[i]))
		}
		for k, v := range want[i] {
			p := got[k]
			if p == nil || math.Abs(*p-v) > 1e-14 {
				t.Fatal(i, k, p, v)
			}
			maxdiff = max(maxdiff, math.Abs(*p-v))
		}
	}
	t.Logf("500 record sets, %d metrics each, max diff %.3g", len(want[0]), maxdiff)
}
func TestTemplates(t *testing.T) {
	var want map[string]string
	testoracle.Run(t, `import ast,json,sys
out={}
for file in sys.argv[1:]:
 for n in ast.parse(open(file).read()).body:
  if isinstance(n,ast.Assign) and isinstance(n.targets[0],ast.Name) and (n.targets[0].id.startswith('EVALUATION_PROMPT_FOR_') or n.targets[0].id=='PROMPT_MEMZERO'):out[n.targets[0].id]=ast.literal_eval(n.value)
json.dump(out,sys.stdout)`, nil, &want, testoracle.Upstream(t, "halumem/eval/eval_tools.py"), testoracle.Upstream(t, "halumem/eval/prompts.py"))
	if !reflect.DeepEqual(want, Templates) {
		t.Fatal("literal drift")
	}
	input := map[string]string{"dialogue": "a {question} <tag>\n你好", "golden_memories": "b", "candidate_memory": "c"}
	var formatted string
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());s=next(ast.literal_eval(n.value) for n in m.body if isinstance(n,ast.Assign) and n.targets[0].id=='EVALUATION_PROMPT_FOR_MEMORY_ACCURACY');json.dump(s.format(**json.load(sys.stdin)),sys.stdout)`, input, &formatted, testoracle.Upstream(t, "halumem/eval/eval_tools.py"))
	if Format(Templates["EVALUATION_PROMPT_FOR_MEMORY_ACCURACY"], input) != formatted {
		t.Fatal("format parity")
	}
}
