package beam

import (
	"math"
	"math/rand/v2"
	"memeval/internal/procexec"
	"memeval/internal/testoracle"
	"os/exec"
	"testing"
)

func TestHandOrdering(t *testing.T) {
	for _, x := range []struct {
		r, s          []string
		f, tau, final float64
	}{{[]string{"A", "B", "C"}, []string{"A", "B", "C"}, 1, 1, 1}, {[]string{"A", "B", "C"}, []string{"C", "B", "A"}, 1, -1, 0}, {[]string{"A", "B", "C"}, []string{"A", "C", "B"}, 1, 1. / 3, 2. / 3}, {[]string{"A", "B", "C"}, []string{"A", "B", "X"}, 2. / 3, 2. / 3, 5. / 9}} {
		o := OrderingScore(x.r, x.s)
		if o.Tau == nil || math.Abs(*o.Tau-x.tau) > 1e-14 || math.Abs(o.F1-x.f) > 1e-14 || math.Abs(*o.Final-x.final) > 1e-14 {
			t.Fatal(o, x)
		}
	}
	if o := OrderingScore([]string{"A"}, []string{"A"}); o.Tau != nil || o.Final != nil {
		t.Fatal("undefined tau must be null", o)
	}
}
func TestKendallPythonOracle(t *testing.T) {
	py := testoracle.Python(t)
	if e := procexec.Run(exec.Command(py, "-B", "-c", "import scipy.stats")); e != nil {
		t.Skip("scipy absent: official Kendall tau_b oracle unavailable; hand-computed tied/undefined cases still run")
	}
	rng := rand.New(rand.NewPCG(17, 42))
	type row struct{ R, S []string }
	xs := []row{}
	for range 800 {
		x := row{[]string{}, []string{}}
		for range rng.IntN(10) {
			x.R = append(x.R, string(rune('a'+rng.IntN(7))))
		}
		for range rng.IntN(10) {
			x.S = append(x.S, string(rune('a'+rng.IntN(7))))
		}
		xs = append(xs, x)
	}
	var want []struct {
		Precision, Recall, F1 float64
		TauNorm               *float64 `json:"tau_norm"`
		Final                 *float64 `json:"final_score"`
	}
	script := `import ast,json,sys,math,warnings
from scipy.stats import kendalltau
from typing import List
warnings.filterwarnings('ignore')
m=ast.parse(open(sys.argv[1]).read());ns={'kendalltau':kendalltau,'List':List,'align_with_llm':lambda reference,system,llm:(reference,system)}
exec(compile(ast.Module(body=[n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='event_ordering_score'],type_ignores=[]),'vendored','exec'),ns)
out=[]
for x in json.load(sys.stdin):
 v=ns['event_ordering_score'](x['R'],x['S'],'llm',None)
 out.append({k:None if isinstance(n,float) and math.isnan(n) else n for k,n in v.items()})
json.dump(out,sys.stdout,allow_nan=False)`
	testoracle.Run(t, script, xs, &want, testoracle.Upstream(t, "beam/src/evaluation/compute_metrics.py"))
	close := func(a, b *float64) bool {
		if a == nil || b == nil {
			return a == b
		}
		return math.Abs(*a-*b) < 1e-14
	}
	for i, x := range xs {
		o := OrderingScore(x.R, x.S)
		w := want[i]
		if o.Precision != w.Precision || o.Recall != w.Recall || o.F1 != w.F1 || !close(o.TauNorm, w.TauNorm) || !close(o.Final, w.Final) {
			t.Fatalf("case %d: %+v %+v", i, o, w)
		}
	}
	t.Log("800 vendored ordering cases, including ties, duplicates and NaN, agree with scipy")
}

func TestRubricMeanHandExamples(t *testing.T) {
	for _, x := range []struct {
		v    []float64
		want float64
	}{{[]float64{0, .5, 1}, .5}, {[]float64{1, 1, 0, 0, .5}, .5}, {[]float64{.5, 1}, .75}} {
		v, e := RubricMean(x.v)
		if e != nil || v != x.want {
			t.Fatal(v, e)
		}
	}
	if _, e := RubricMean(nil); e == nil {
		t.Fatal("empty rubric accepted")
	}
}
