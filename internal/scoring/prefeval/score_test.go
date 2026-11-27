package prefeval

import (
	"errors"
	"memeval/internal/procexec"
	"memeval/internal/testoracle"
	"os/exec"
	"testing"
)

func TestTruthTable(t *testing.T) {
	for n := 0; n < 16; n++ {
		m := map[string]string{}
		for i, k := range []string{"acknow", "violate", "hallucinate", "helpful"} {
			m[k] = "No"
			if n&(1<<i) != 0 {
				m[k] = "Yes"
			}
		}
		v := Combine(m)
		expected := n&8 != 0 && n&2 == 0
		if v.Accuracy != expected {
			t.Fatal(n, v)
		}
		errors := 0
		for _, x := range []bool{v.Unhelpful, v.Inconsistent, v.HallucinationViolation, v.UnawareViolation} {
			if x {
				errors++
			}
		}
		if (!v.Accuracy && errors != 1) || (v.Accuracy && errors != 0) {
			t.Fatal(n, v)
		}
	}
}
func TestFormulaPythonOracle(t *testing.T) {
	inputs := []map[string]string{}
	for n := 0; n < 16; n++ {
		m := map[string]string{}
		for i, k := range []string{"acknow", "violate", "hallucinate", "helpful"} {
			m[k] = "No"
			if n&(1<<i) != 0 {
				m[k] = "Yes"
			}
		}
		inputs = append(inputs, m)
	}
	inputs = append(inputs, map[string]string{}, map[string]string{"acknow": "yesterday YES", "violate": "YES but NO", "helpful": "nope", "hallucinate": "Yes"})
	var wants []map[string]int
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());ns={'tqdm':lambda x:x}
exec(compile(ast.Module(body=[n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='analyze_errors'],type_ignores=[]),'vendored','exec'),ns)
json.dump([ns['analyze_errors']([{'evaluation_error_analysis':{k:{'answer':v} for k,v in row.items()}}])[0] for row in json.load(sys.stdin)],sys.stdout)`, inputs, &wants, testoracle.Upstream(t, "prefeval/generation_task/get_preference_following_accuracy_generation_task.py"))
	for i, m := range inputs {
		v := Combine(m)
		for k, b := range map[string]bool{"preference_adherence_accuracy": v.Accuracy, "error_unhelpful": v.Unhelpful, "error_inconsistent": v.Inconsistent, "hallucination_of_preference_violation": v.HallucinationViolation, "preference_unaware_violation": v.UnawareViolation} {
			n := 0
			if b {
				n = 1
			}
			if n != wants[i][k] {
				t.Fatal(i, k, n, wants[i])
			}
		}
	}
	t.Log("16 combinations + 2 substring/missing cases match official analyze_errors")
}
func TestChoicePythonOracle(t *testing.T) {
	xs := []string{"A", "<choice>A</choice>", "<choice>BC</choice>", "<choice>bad</choice>", "<choice> B </choice><choice>A</choice>", "<CHOICE>C</CHOICE>", "<choice>D", "<choice><b>A</b></choice>", "<choice>A<b>B</b></choice>", "<choice><!--D--></choice>", "<choice>&#65;</choice>", "<choice></choice>", "<choice>CAD</choice>"}
	var want []*string
	testoracle.Run(t, `import ast,json,sys,re
from bs4 import BeautifulSoup
m=ast.parse(open(sys.argv[1]).read());ns={'BeautifulSoup':BeautifulSoup,'re':re}
exec(compile(ast.Module(body=[n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='extract_choice'],type_ignores=[]),'vendored','exec'),ns)
json.dump([ns['extract_choice'](s) for s in json.load(sys.stdin)],sys.stdout)`, xs, &want, testoracle.Upstream(t, "prefeval/utils/utils_mcq.py"))
	for i, s := range xs {
		w := ""
		if want[i] != nil {
			w = *want[i]
		}
		if ExtractChoice(s) != w {
			t.Fatal(i, s, ExtractChoice(s), w)
		}
	}
	t.Logf("%d extract_choice cases match BeautifulSoup oracle", len(xs))
}
func TestTemplateLiteralParity(t *testing.T) {
	testoracle.Upstream(t, "prefeval/error_type/check_acknowledge.txt")
	b, e := procexec.CombinedOutput(exec.Command(testoracle.Python(t), "-B", "../../../scripts/extract_prefeval_templates.py", "--check"))
	var exit *exec.ExitError
	if errors.As(e, &exit) && exit.ExitCode() == 3 {
		t.Skipf("%s", b) // A partially restored source tree can lack other literals.
	}
	if e != nil {
		t.Fatalf("%v %s", e, b)
	}
	t.Logf("%s", b)
}
