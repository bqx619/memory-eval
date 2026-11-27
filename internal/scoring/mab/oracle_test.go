package mab

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"memeval/internal/testoracle"
	"reflect"
	"strings"
	"testing"
)

type oracleInput struct {
	Prediction string   `json:"prediction"`
	Answers    []string `json:"answers"`
	Source     string   `json:"source"`
}
type oracleOutput struct {
	Normalize string             `json:"normalize"`
	F1        []float64          `json:"f1"`
	Exact     bool               `json:"exact"`
	Substring bool               `json:"substring"`
	Parsed    string             `json:"parsed"`
	Metrics   map[string]float64 `json:"metrics"`
	Movie     string             `json:"movie"`
	Clean     string             `json:"clean"`
}

// Only deterministic functions are compiled from the vendored AST: no top-level
// imports, download calls or .env loaders execute. Missing ROUGE is explicitly
// removed from calculate_metrics, never replaced with a fake implementation.
const oracleScript = `import ast,json,sys,re,string,importlib.util
from collections import Counter
path=sys.argv[1]
tree=ast.parse(open(path).read());nodes=[n for n in tree.body if isinstance(n,ast.FunctionDef)]
rouge=importlib.util.find_spec('rouge_score') is not None
ns={'re':re,'string':string,'Counter':Counter}
if rouge:
 from rouge_score import rouge_scorer
 ns['rouge_scorer_instance']=rouge_scorer.RougeScorer(['rougeL','rougeLsum'],use_stemmer=True)
else:
 for n in nodes:
  if n.name=='calculate_metrics':
   # Its first assignment uses the unmodified upstream three basic functions.
   first=next(x for x in n.body if isinstance(x,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='metrics' for t in x.targets))
   n.body=[first,ast.Return(ast.Name(id='metrics',ctx=ast.Load()))]
exec(compile(ast.fix_missing_locations(ast.Module(nodes,type_ignores=[])),path,'exec'),ns)
out=[]
for v in json.load(sys.stdin):
 p,g,source=v['prediction'],v['answers'],v['source'];m,info=ns['post_process']({'output':p},g,{'sub_dataset':source})
 out.append({'normalize':ns['normalize_answer'](p),'f1':ns['f1_score'](p,g[0]),'exact':ns['drqa_exact_match_score'](p,g[0]),'substring':ns['substring_exact_match_score'](p,g[0]),'parsed':ns['parse_output'](p),'metrics':{k:float(v) for k,v in m.items()},'movie':ns['extract_movie_name'](p),'clean':ns['clean_text_elements'](p)})
print(json.dumps({'rouge':rouge,'rows':out}))`

func TestScoringPythonOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(34719))
	vocab := []string{"The", "an", "a", "AND", "yes", "no", "noanswer", "cat", "cats", "running", "中文", "İ", "ΟΣ", "α", "é", "e\u0301", "foo-bar", "1.Star_(1999)", "x/y_z>", "\x1c", "\u00a0", "_", "?", "(nested (inner))"}
	sources := []string{"icl_banking77", "eventqa_full", "ruler_qa1", "factconsolidation_sh", "detective_qa", "infbench_sum_eng_shots2", "longmemeval_s*", "infbench_choice_eng", "ruler_niah", "mermory_merging"}
	word := func() string {
		var b []string
		for j := 0; j < rng.Intn(9)+1; j++ {
			b = append(b, vocab[rng.Intn(len(vocab))])
		}
		return strings.Join(b, " ")
	}
	input := []oracleInput{}
	for i := 0; i < 800; i++ {
		p, g := word(), word()
		if i%3 == 0 {
			g = p
		}
		if i%4 == 0 {
			p = "reasoning\nAnSwEr: Answer: " + p + "\nexplanation"
		}
		if i%17 == 0 {
			p = ""
		}
		input = append(input, oracleInput{p, []string{g, word()}, sources[i%len(sources)]})
	}
	var result struct {
		Rouge bool           `json:"rouge"`
		Rows  []oracleOutput `json:"rows"`
	}
	testoracle.Run(t, oracleScript, input, &result, testoracle.Upstream(t, "memoryagentbench/utils/eval_other_utils.py"))
	if len(result.Rows) != len(input) {
		t.Fatal("oracle count")
	}
	for i, v := range input {
		w := result.Rows[i]
		f, p, r := F1(v.Prediction, v.Answers[0])
		parsed, e := ParseOutput(v.Prediction, "Answer:")
		if e != nil {
			t.Fatal(e)
		}
		if Normalize(v.Prediction) != w.Normalize || !reflect.DeepEqual([]float64{f, p, r}, w.F1) || Exact(v.Prediction, v.Answers[0]) != binary(w.Exact) || Substring(v.Prediction, v.Answers[0]) != binary(w.Substring) || parsed != w.Parsed || MovieName(v.Prediction) != w.Movie || CleanText(v.Prediction) != w.Clean {
			t.Fatalf("case %d normalization/basic/parse/clean mismatch\n%+v\nwant=%+v\ngot=%q %v %q %q", i, v, w, Normalize(v.Prediction), []float64{f, p, r}, MovieName(v.Prediction), CleanText(v.Prediction))
		}
		got, e := PostProcess(v.Prediction, v.Answers, v.Source, nil)
		if e != nil {
			t.Fatal(e)
		}
		for k, v := range w.Metrics {
			if math.Abs(got.Metrics[k]-v) > 1e-15 {
				t.Fatalf("case %d %s: %.17g != %.17g", i, k, got.Metrics[k], v)
			}
		}
	}
	t.Logf("vendored scorer oracle: %d groups; basic functions, normalization, parse_output, per-subset post_process, movie cleaning exact", len(input))
	if !result.Rouge {
		t.Run("ROUGEOracle", func(t *testing.T) {
			t.Skip("rouge_score unavailable: calculate_metrics ROUGE branch and ROUGE-1/2/L oracle not executed; no placeholder oracle")
		})
	}
}
func TestMaxNestedOracle(t *testing.T) {
	values := []any{"cat", []string{"dog", "cat"}, [][]string{{"dog"}, {"cat", "a cat"}}}
	for _, v := range values {
		m, e := MaxOverGT(Exact, "cat", v)
		if e != nil || m != 1 {
			t.Fatal(v, m, e)
		}
	}
	if _, e := MaxOverGT(Exact, "cat", []string{}); e == nil {
		t.Fatal("empty max")
	}
}
func TestRecsysAndROUGE(t *testing.T) {
	c, e := ReadCandidates(strings.NewReader(`{"a/Star_Wars_(1977)":1,"b/Cats":2,"c/Dog":3}`))
	if e != nil {
		t.Fatal(e)
	}
	p, e := c.Score("Preference. 1.Star Wars\n2. Cats\n3. Dog", []string{"1", "2"})
	if e != nil || p.Metrics["recsys_recall@1"] != .5 || p.Metrics["recsys_recall@5"] != 1 {
		t.Fatal(p, e)
	}
	m, e := FindNearest("Bat", []string{"Cat", "Hat", "Hat"})
	if e != nil || m.Nearest != "Cat" || m.Distance != 1 {
		t.Fatal(m, e)
	}
	for _, kind := range []string{"rouge1", "rouge2", "rougeL", "rougeLsum"} {
		f, r := Rouge("the cats run\nThey run fast", "the cats run\nThey run fast", kind)
		if f != 1 || r != 1 {
			t.Fatal(kind, f, r)
		}
		f, r = Rouge("", "", kind)
		if f != 0 || r != 0 {
			t.Fatal(kind, f, r)
		}
	}
	f, r := Rouge("cat sat", "cat sat mat", "rougeL")
	if math.Abs(f-.8) > 1e-15 || r != 2./3 {
		t.Fatal(f, r)
	}
}
func TestRecommendationPythonOracle(t *testing.T) {
	candidates := map[string]int{}
	for i := 0; i < 12; i++ {
		candidates[fmt.Sprintf("/Movie_%03d_(2000)", i)] = i
	}
	b, _ := json.Marshal(candidates)
	c, e := ReadCandidates(strings.NewReader(string(b)))
	if e != nil {
		t.Fatal(e)
	}
	inputs := []oracleInput{}
	for i := 0; i < 600; i++ {
		pred := fmt.Sprintf("Preferences. 1.Movie %03d\n2.Movie %03d\n3.Movie %03d", i%12, (i+1)%12, (i+2)%12)
		if i%2 == 0 {
			pred = strings.ReplaceAll(pred, "Movie", "Movi")
		}
		inputs = append(inputs, oracleInput{Prediction: pred, Answers: []string{fmt.Sprint(i % 12), fmt.Sprint((i + 7) % 12)}, Source: "recsys_redial_full"})
	}
	var want []struct {
		Metrics map[string]float64 `json:"metrics"`
		Parsed  []string           `json:"parsed"`
		GT      []string           `json:"gt"`
	}
	testoracle.Run(t, `import ast,json,sys,re,string,io,numpy as np
from collections import Counter
from nltk.metrics.distance import edit_distance
# The installed NLTK Levenshtein primitive substitutes for absent editdistance;
# all candidate cleaning/matching/recall and dispatch code is vendored unchanged.
p=sys.argv[1]
nodes=[n for n in ast.parse(open(p).read()).body if isinstance(n,ast.FunctionDef)]
v=json.load(sys.stdin);mapping=v['candidates']
import os
ns={'re':re,'string':string,'Counter':Counter,'json':json,'np':np,'os':os,'edit_distance':edit_distance,'open':lambda path:io.StringIO(json.dumps(mapping))}
exec(compile(ast.Module(nodes,type_ignores=[]),p,'exec'),ns)
rows=[]
for x in v['rows']:
 m,info=ns['post_process']({'output':x['prediction']},x['answers'],{'sub_dataset':x['source']})
 rows.append({'metrics':m,'parsed':info['parsed_output'],'gt':info['gt_movies']})
print(json.dumps(rows))`, map[string]any{"candidates": candidates, "rows": inputs}, &want, testoracle.Upstream(t, "memoryagentbench/utils/eval_other_utils.py"))
	for i, v := range inputs {
		g, e := PostProcess(v.Prediction, v.Answers, v.Source, c)
		if e != nil || !reflect.DeepEqual(g.Metrics, want[i].Metrics) || !reflect.DeepEqual(g.Parsed, want[i].Parsed) || !reflect.DeepEqual(g.GTMovies, want[i].GT) {
			t.Fatal(i, g, want[i], e)
		}
	}
	t.Log("600 vendored recsys cases exact; installed nltk.edit_distance supplies Levenshtein; unique nearest candidates avoid upstream set-order ties")
}
