package personamem

import (
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"reflect"
	"testing"
)

func TestVendoredLiteralAndPrompt(t *testing.T) {
	var want string
	testoracle.Run(t, `import ast,json,sys
m=ast.parse(open(sys.argv[1]).read())
for n in ast.walk(m):
 if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='instructions' for t in n.targets) and isinstance(n.value,ast.Constant): print(json.dumps(ast.literal_eval(n.value))); break`, nil, &want, testoracle.Upstream(t, "personamem/inference.py"))
	if want != Instructions {
		t.Fatal("literal drift")
	}
	known := map[string]suite.Event{"s": {Role: "system", Content: "persona", Attributes: map[string]any{"memeval_reader_sequence": 0}}, "u": {Role: "user", Content: "User: raw", Attributes: map[string]any{"memeval_reader_sequence": 1}}}
	items := []protocol.Item{{SourceIDs: []string{"u", "s"}}}
	raw := `['(a) one', "(b) two"]`
	in, e := Build(items, known, "ask", raw, 100)
	wantMessages := []llm.Message{{Role: "system", Content: "persona"}, {Role: "user", Content: "User: raw"}, {Role: "user", Content: "ask\n\n" + Instructions + "\n\n" + raw}}
	if e != nil || !reflect.DeepEqual(in.Messages, wantMessages) {
		t.Fatal(in, e)
	}
	in, e = Build(items, known, "ask", raw, 10)
	if e != nil || in.DroppedMessages != 1 || !in.Truncated || in.Messages[0].Role != "user" {
		t.Fatal(in, e)
	}
}
