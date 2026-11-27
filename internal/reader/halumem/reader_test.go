package halumem

import (
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"testing"
)

func TestReaderPythonParity(t *testing.T) {
	items := []protocol.Item{{Text: "你好 🌍 <tag> {question}", SourceIDs: []string{"a"}}, {Text: "explicit", Timestamp: "original stamp"}}
	known := map[string]suite.Event{"a": {Timestamp: "2025-01-01T00:00:00Z"}}
	var want string
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read());template=next(ast.literal_eval(n.value) for n in m.body if isinstance(n,ast.Assign) and n.targets[0].id=='PROMPT_MEMZERO');lines=json.load(sys.stdin);context='Memories for user pseudonym:\n\n    '+json.dumps(lines,indent=4)+'\n';json.dump(template.format(context=context,question='question?'),sys.stdout)`, Lines(items, known), &want, testoracle.Upstream(t, "halumem/eval/prompts.py"))
	if Build(items, known, "pseudonym", "question?")[0].Content != want {
		t.Fatal("reader/context literal drift")
	}
	if pythonJSON([]string{}) != "[]" {
		t.Fatal("empty")
	}
}
