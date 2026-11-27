package locomo

import (
	"fmt"
	"memeval/internal/testoracle"
	"reflect"
	"strings"
	"testing"
)

func TestOfficialQuestionSuffixes(t *testing.T) {
	in, err := Build(nil, nil, "When?", 2, []string{"A", "B"}, 1000)
	if err != nil || !in.QuestionAudit.SuffixApplied || !strings.Contains(in.Prompt, "Question: When?"+TemporalSuffix+" Short answer:") {
		t.Fatal(in, err)
	}
	adversarial := "PRIVATE {distractor}"
	seed := strings.Repeat("42", 32)
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		opt := QuestionOptions{&adversarial, seed, fmt.Sprintf("synthetic-case-%d#0", i)}
		in, err := Build(nil, nil, "Where?", 5, []string{"A", "B"}, 1000, opt)
		if err != nil {
			t.Fatal(err)
		}
		a := in.QuestionAudit
		seen[a.OptionOrder[0]] = true
		want := fmt.Sprintf("Where? Select the correct answer: (a) %s (b) %s. ", a.Options["a"], a.Options["b"])
		if !a.SuffixApplied || !strings.Contains(in.Prompt, want) {
			t.Fatal(in)
		}
		draw, _ := cat5Draw(seed, opt.SampleID)
		if (a.Options["a"] == NotMentioned) != (draw < .5) {
			t.Fatal("branch differs")
		}
		for _, key := range []string{"a", "b"} {
			got, mapped := ProcessAnswer("("+strings.ToUpper(key)+")", a.Options)
			if got != a.Options[key] || mapped != key {
				t.Fatal(got, mapped, a)
			}
		}
		again, _ := Build(nil, nil, "Where?", 5, []string{"A", "B"}, 1000, opt)
		if !reflect.DeepEqual(in, again) {
			t.Fatal("nondeterministic order")
		}
	}
	if len(seen) != 2 {
		t.Fatal("both upstream branches not covered", seen)
	}
	legacy, err := Build(nil, nil, "Where?", 5, []string{"A", "B"}, 1000)
	if err != nil || legacy.QuestionAudit.SuffixApplied || len(legacy.Deviations) == 0 {
		t.Fatal("legacy suffix absence not disclosed", legacy, err)
	}
	if _, _, _, err := prepareQuestion("Q", 5, QuestionOptions{AdversarialAnswer: &adversarial}); err == nil {
		t.Fatal("unseeded choice allowed")
	}
}

func TestCat5MappingAndOracle(t *testing.T) {
	options := map[string]string{"a": "Not mentioned in the conversation", "b": "Private Distractor"}
	cases := []struct{ raw, want, key string }{
		{" a ", NotMentioned, "a"}, {"(A)", NotMentioned, "a"}, {"B", "Private Distractor", "b"}, {"(b)", "Private Distractor", "b"},
		{"c", "Private Distractor", "b"}, {"xyz", "Private Distractor", "b"}, {"☃", "Private Distractor", "b"},
		{"  Unknown RESPONSE  ", "unknown response", ""}, {"No information available", "no information available", ""}, {"", "", ""},
	}
	inputs := []any{}
	got := []string{}
	for _, c := range cases {
		text, key := ProcessAnswer(c.raw, options)
		if text != c.want || key != c.key {
			t.Fatalf("%q -> %q/%q", c.raw, text, key)
		}
		inputs = append(inputs, map[string]any{"raw": c.raw, "options": options})
		got = append(got, text)
	}
	var want []string
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());n=next(n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='get_cat_5_answer')
scope={};exec(compile(ast.Module(body=[n],type_ignores=[]),sys.argv[1],'exec'),scope)
print(json.dumps([scope['get_cat_5_answer'](x['raw'],x['options']).strip() for x in json.load(sys.stdin)]))`, inputs, &want, testoracle.Upstream(t, "locomo/task_eval/gpt_utils.py"))
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got, want)
	}
}

func TestQuestionSuffixVendoredLiterals(t *testing.T) {
	var literals []string
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());n=next(n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='get_gpt_answers')
print(json.dumps([x.value for x in ast.walk(n) if isinstance(x,ast.Constant) and isinstance(x.value,str)]))`, nil, &literals, testoracle.Upstream(t, "locomo/task_eval/gpt_utils.py"))
	for _, want := range []string{TemporalSuffix, Cat5Suffix, NotMentioned} {
		found := false
		for _, s := range literals {
			found = found || s == want
		}
		if !found {
			t.Fatal("official literal drift", want)
		}
	}
}
