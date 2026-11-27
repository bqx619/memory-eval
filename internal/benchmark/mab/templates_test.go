package mab

import (
	"encoding/json"
	"memeval/internal/pytext"
	judge "memeval/internal/scoring/judge/longmemeval"
	"memeval/internal/testoracle"
	"reflect"
	"strings"
	"testing"
)

func TestVendoredLiterals(t *testing.T) {
	testoracle.Upstream(t, "memoryagentbench/utils/templates.py")
	testoracle.Upstream(t, "memoryagentbench/llm_based_eval/summarization_evaluate.py")
	var want map[string]json.RawMessage
	testoracle.Run(t, `import importlib.util,json,sys
sys.path.insert(0,'../../../scripts')
s=importlib.util.spec_from_file_location('extract','../../../scripts/extract_mab_templates.py');m=importlib.util.module_from_spec(s);s.loader.exec_module(m);print(json.dumps(m.extract()))`, nil, &want)
	for name, b := range map[string][]byte{"templates.json": templates, "summary.json": summary} {
		var a, c any
		json.Unmarshal(b, &a)
		json.Unmarshal(want[name], &c)
		if !reflect.DeepEqual(a, c) {
			t.Fatal(name)
		}
	}
}
func TestMABLongMemJudgeLiterals(t *testing.T) {
	var want []string
	types := []string{"single-session-user", "single-session-assistant", "multi-session", "temporal-reasoning", "knowledge-update", "single-session-preference", "abstention"}
	testoracle.Run(t, `import ast,json,sys
p=sys.argv[1]
n=next(n for n in ast.parse(open(p).read()).body if isinstance(n,ast.FunctionDef) and n.name=='get_anscheck_prompt');ns={};exec(compile(ast.Module([n],type_ignores=[]),p,'exec'),ns)
print(json.dumps([ns['get_anscheck_prompt'](t,'Q',['A',"it\'s"],'R',t=='abstention') for t in json.load(sys.stdin)]))`, types, &want, testoracle.Upstream(t, "memoryagentbench/llm_based_eval/longmem_qa_evaluate.py"))
	for i, typ := range types {
		template, e := judge.Template(typ, typ == "abstention")
		parts := strings.Split(template, "{}")
		g := parts[0] + "Q" + parts[1] + pytext.ReprStrings([]string{"A", "it's"}) + parts[2] + "R" + parts[3]
		if e != nil || g != want[i] {
			t.Fatal(typ, e)
		}
	}
}
func TestFormatInsertedBraces(t *testing.T) {
	if v := Format(`{{label}} {question}`, map[string]string{"question": "literal {context}"}); v != "{label} literal {context}" {
		t.Fatal(v)
	}
}
