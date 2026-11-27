package locomo

import (
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"strings"
	"testing"
)

func TestVendoredLiterals(t *testing.T) {
	var want map[string]string
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());out={}
for n in m.body:
 if isinstance(n,ast.Assign):
  for v in n.targets:
   if isinstance(v,ast.Name) and v.id in ['CONV_START_PROMPT','QA_PROMPT','QA_PROMPT_CAT_5']:out[v.id]=ast.literal_eval(n.value)
print(json.dumps(out))`, nil, &want, testoracle.Upstream(t, "locomo/task_eval/gpt_utils.py"))
	for name, s := range map[string]string{"CONV_START_PROMPT": StartTemplate, "QA_PROMPT": QATemplate, "QA_PROMPT_CAT_5": Cat5Template} {
		if s != want[name] {
			t.Fatal(name, "literal drift")
		}
	}
}
func TestSourceOrderAndWholeSessionBudget(t *testing.T) {
	known := map[string]suite.Event{"x": {Content: "one", Session: "s1", TimeRaw: "raw 1", Timestamp: "2024-01-01T00:00:00Z", Attributes: map[string]any{"speaker": "A", "memeval_reader_sequence": 0}}, "y": {Content: "two", Session: "s2", TimeRaw: "raw 2", Timestamp: "2024-01-02T00:00:00Z", Attributes: map[string]any{"speaker": "B", "memeval_reader_sequence": 1}}}
	items := []protocol.Item{{SourceIDs: []string{"y", "x", "y"}}}
	in, e := Build(items, known, "question", 5, []string{"A", "B"}, 10000)
	if e != nil || strings.Index(in.Prompt, `"one"`) > strings.Index(in.Prompt, `"two"`) || strings.Count(in.Prompt, "two") != 2 || !strings.Contains(in.Prompt, strings.Replace(Cat5Template, "{}", "question", 1)) {
		t.Fatal(in, e)
	}
	in, e = Build(items, known, "question", 1, []string{"A", "B"}, 50)
	if e != nil || !in.Truncated || in.DroppedSessions != 1 || strings.Contains(in.Prompt, "one") {
		t.Fatal(in, e)
	}
}
