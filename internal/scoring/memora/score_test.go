package memora

import (
	"math"
	"memeval/internal/procexec"
	"memeval/internal/testoracle"
	"os/exec"
	"testing"
)

func TestFAMAHand(t *testing.T) {
	for _, x := range []struct {
		p, np, f, nf        int
		mpa, faa, lam, fama float64
	}{{1, 2, 0, 1, .5, 0, 1. / 3, 1. / 6}, {2, 2, 1, 2, 1, .5, .5, .75}, {0, 0, 1, 2, 0, .5, 1, 0}, {1, 2, 0, 0, .5, 1, 0, .5}, {0, 0, 0, 0, 0, 1, 0, 0}, {0, 1, 0, 1, 0, 0, .5, 0}} {
		v := FAMA(x.p, x.np, x.f, x.nf)
		if math.Abs(v.MPA-x.mpa) > 1e-14 || math.Abs(v.FAA-x.faa) > 1e-14 || math.Abs(v.Lambda-x.lam) > 1e-14 || math.Abs(v.FAMA-x.fama) > 1e-14 {
			t.Fatal(x, v)
		}
	}
}
func TestFAMAPythonOracle(t *testing.T) {
	xs := [][4]int{}
	for np := 0; np < 9; np++ {
		for nf := 0; nf < 9; nf++ {
			for p := 0; p <= np; p++ {
				for f := 0; f <= nf; f++ {
					xs = append(xs, [4]int{p, np, f, nf})
				}
			}
		}
	}
	var want []float64
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());ns={}
exec(compile(ast.Module(body=[n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='fama_score'],type_ignores=[]),'vendored','exec'),ns)
json.dump([ns['fama_score'](*v) for v in json.load(sys.stdin)],sys.stdout)`, xs, &want, testoracle.Upstream(t, "memora/evals/agent_eval/memory_to_answer.py"))
	for i, x := range xs {
		if FAMA(x[0], x[1], x[2], x[3]).FAMA != want[i] {
			t.Fatal(i, x, want[i])
		}
	}
	t.Logf("%d FAMA exact oracle cases", len(xs))
}

func TestTemplateLiteralParity(t *testing.T) {
	for _, path := range []string{"agent_eval/memory_to_answer.py", "model_eval/model_based_evaluator.py", "model_eval/config.py"} {
		testoracle.Upstream(t, "memora/evals/"+path)
	}
	b, e := procexec.CombinedOutput(exec.Command(testoracle.Python(t), "-B", "../../../scripts/extract_memora_templates.py", "--check"))
	if e != nil {
		t.Fatalf("%v %s", e, b)
	}
	t.Logf("%s", b)
}
