package locomo

import (
	"math"
	"math/rand"
	"memeval/internal/testoracle"
	"testing"
)

func TestPorterPythonOracle(t *testing.T) {
	r := rand.New(rand.NewSource(712))
	words := []string{"sky", "skies", "dying", "lying", "tying", "news", "inning", "cannings", "howe", "proceed", "exceed", "succeed", "dies", "ties", "spied", "enjoy", "enjoyment", "happy", "fly", "geologi", "radicalli", "relational", "controll", "roll", "İSTANBUL", "ΟΣ", "café", "caféing"}
	suffixes := []string{"sses", "ies", "ss", "s", "ied", "eed", "ed", "ing", "y", "ational", "tional", "enci", "anci", "izer", "bli", "alli", "entli", "eli", "ousli", "ization", "ation", "ator", "alism", "iveness", "fulness", "ousness", "aliti", "iviti", "biliti", "fulli", "logi", "icate", "ative", "alize", "iciti", "ical", "ful", "ness", "al", "ance", "ence", "er", "ic", "able", "ible", "ant", "ement", "ment", "ent", "ion", "ou", "ism", "ate", "iti", "ous", "ive", "ize", "e", "ll"}
	for i := 0; i < 5000; i++ {
		b := make([]byte, r.Intn(15))
		for j := range b {
			b[j] = "aeiouyyyybcdfghjklmnpqrstvwxz"[r.Intn(27)]
		}
		words = append(words, string(b)+suffixes[r.Intn(len(suffixes))])
	}
	var want []string
	testoracle.Run(t, `import json,sys
from nltk.stem import PorterStemmer
p=PorterStemmer();print(json.dumps([p.stem(w) for w in json.load(sys.stdin)]))`, words, &want)
	for i, w := range words {
		if g := Stem(w); g != want[i] {
			t.Fatalf("%q: %q != %q", w, g, want[i])
		}
	}
	t.Logf("Porter oracle: %d words exact", len(words))
}
func TestLocomoOfficialPythonOracle(t *testing.T) {
	type row struct {
		Prediction, Gold string
		Category         int
	}
	cases := []row{}
	r := rand.New(rand.NewSource(834))
	parts := []string{"café and thé", "an apple", "another", "Andrea", "a", "the", "and", "", "42", "running", "skies", "red, blue", "café; thé", "hello—world", "(hi!)", "İSTANBUL", "ΟΣ", "a\u0301", "\u001c an\u00a0apple\u001f", "¿the?", "金and金", "and2", "and_", "\u200dand"}
	for i := 0; i < 1250; i++ {
		p := parts[r.Intn(len(parts))] + " " + parts[r.Intn(len(parts))]
		g := parts[r.Intn(len(parts))] + " " + parts[r.Intn(len(parts))]
		if i%5 == 0 {
			p = "Not mentioned " + p
		}
		cases = append(cases, row{p, g, 1 + i%5})
	}
	var got []struct {
		Score      float64
		Normalized string
	}
	testoracle.Run(t, `import json,sys,types,importlib.util,contextlib,io
b=types.ModuleType('bert_score');b.score=lambda *a,**k:None;sys.modules['bert_score']=b
sys.modules['rouge']=types.ModuleType('rouge')
spec=importlib.util.spec_from_file_location('ev',sys.argv[1]);ev=importlib.util.module_from_spec(spec);spec.loader.exec_module(ev)
out=[]
for r in json.load(sys.stdin):
 with contextlib.redirect_stdout(io.StringIO()):
  score=ev.eval_question_answering([dict(prediction=r['Prediction'],answer=r['Gold'],category=r['Category'])])[0][0]
 out.append(dict(Score=float(score),Normalized=ev.normalize_answer(r['Prediction'])))
print(json.dumps(out))`, cases, &got, testoracle.Upstream(t, "locomo/task_eval/evaluation.py"))
	for i, c := range cases {
		if n := Normalize(c.Prediction); n != got[i].Normalized {
			t.Fatalf("normalize %q: %q != %q", c.Prediction, n, got[i].Normalized)
		}
		if v := Score(c.Prediction, c.Gold, c.Category); math.Abs(v-got[i].Score) > 1e-12 {
			t.Fatalf("%+v: %g != %g", c, v, got[i].Score)
		}
	}
	t.Logf("LoCoMo oracle: %d cases within 1e-12", len(cases))
}
