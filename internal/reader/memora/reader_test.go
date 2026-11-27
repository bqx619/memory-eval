package memora

import (
	"context"
	"encoding/json"
	"memeval/internal/llm"
	"memeval/internal/protocol"
	"memeval/internal/testoracle"
	"reflect"
	"strings"
	"testing"
)

func TestSourcesExactAndEmpty(t *testing.T) {
	score := .125
	in, e := Build([]protocol.Item{{Text: "A", Score: &score}, {Text: "B"}}, nil, "Q", "2025-06-07", "gpt-6-luna", false, 1000)
	if e != nil || !strings.Contains(in.Messages[1].Content, "1. A (relevance: 0.12)\n2. B (relevance: 0.00)") || len(in.Deviations) != 1 {
		t.Fatal(in, e)
	}
	empty, e := Build(nil, nil, "Q", "", "gpt-6-luna", false, 0)
	if e != nil {
		t.Fatal(e)
	}
	r, e := Answer(t.Context(), nil, empty, "openai-compat", "gpt-6-luna", "sample", 512)
	if e != nil || r.Text != NoMemories {
		t.Fatal(r, e)
	}
}
func TestContextPythonOracle(t *testing.T) {
	sessions := []Session{}
	for i := 1; i <= 7; i++ {
		sessions = append(sessions, Session{i, "2025-06-01", []llm.Message{{Role: "user", Content: strings.Repeat("工作 item ", i*25)}, {Role: "assistant", Content: strings.Repeat("answer ", i*10)}}})
	}
	type req struct {
		Sessions []Session `json:"sessions"`
		Model    string    `json:"model"`
		Budget   int       `json:"budget"`
	}
	xs := []req{}
	for _, m := range []string{"gpt-6-luna", "gpt-4", "qwen3-32b", "claude-sonnet-4.5"} {
		for _, b := range []int{0, 10, 180, 400, 800, 2000, 10000} {
			xs = append(xs, req{sessions, m, b})
		}
	}
	var want []struct {
		Context  string          `json:"context"`
		Metadata ContextMetadata `json:"metadata"`
		Messages []llm.Message   `json:"messages"`
	}
	testoracle.Run(t, `import ast,json,sys,typing,contextlib,io
m=ast.parse(open(sys.argv[1]).read());cfg=ast.parse(open(sys.argv[2]).read());ns=dict(vars(typing));ns.update(TIKTOKEN_AVAILABLE=False,MAX_CONTEXT_TOKENS_HARD_CAP=200000)
# Token counts provided by the Go offline tokenizer isolate the exact Python
# truncation/rendering algorithm without downloading Python BPE assets.
counts=json.load(sys.stdin);inputs=counts['inputs'];table=counts['counts']
ns['MODEL_CONTEXT_LIMITS']=next(ast.literal_eval(n.value) for n in cfg.body if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='MODEL_CONTEXT_LIMITS' for t in n.targets))
nodes=[n for n in m.body if isinstance(n,ast.FunctionDef) and n.name=='get_model_context_limit']
cls=next(n for n in m.body if isinstance(n,ast.ClassDef) and n.name=='PromptBuilder');nodes.append(cls)
exec(compile(ast.Module(body=nodes,type_ignores=[]),'vendored','exec'),ns)
ns['count_tokens']=lambda s,model_name='gpt-4':table[model_name][s]
out=[]
for x in inputs:
 p=ns['PromptBuilder'](x['model']);convs=[{'session_id':s['id'],'date':s['date'],'conversation':[{'speaker':'ai_agent' if t['role']=='assistant' else 'user','message':t['content']} for t in s['turns']]} for s in x['sessions']]
 with contextlib.redirect_stdout(io.StringIO()):
  c,meta=p.build_conversation_context(convs,x['budget'] or None);a,b=p.build_question_prompt(c,'Q{question}','2025-06-07')
 out.append({'context':c,'metadata':meta,'messages':[{'role':'system','content':a},{'role':'user','content':b}]})
json.dump(out,sys.stdout)`, oracleContextInput(xs), &want, testoracle.Upstream(t, "memora/evals/model_eval/model_based_evaluator.py"), testoracle.Upstream(t, "memora/evals/model_eval/config.py"))
	for i, x := range xs {
		c, m := ConversationContext(x.Sessions, x.Model, x.Budget)
		msgs, _ := QuestionPrompt(c, "Q{question}", "2025-06-07", x.Model)
		if c != want[i].Context || m != want[i].Metadata || !reflect.DeepEqual(msgs, want[i].Messages) {
			t.Fatalf("context oracle %d budget%d: meta%+v want%+v", i, x.Budget, m, want[i].Metadata)
		}
	}
	t.Logf("%d context/partial-session/date prompt oracle cases", len(xs))
}

// Build a transcript of token-count inputs, then let the Python algorithm run
// independently using those counts. Tokenizer itself is checked separately.
func oracleContextInput(v any) any {
	b, _ := json.Marshal(v)
	var xs []struct {
		Sessions []Session
		Model    string
		Budget   int
	}
	json.Unmarshal(b, &xs)
	table := map[string]map[string]int{}
	for _, x := range xs {
		if table[x.Model] == nil {
			table[x.Model] = map[string]int{}
		}
		count := func(s string) int { n := CountTokens(s, x.Model); table[x.Model][s] = n; return n }
		c, _ := conversationContext(x.Sessions, x.Model, x.Budget, count)
		questionPrompt(c, "Q{question}", "2025-06-07", x.Model, count)
	}
	return map[string]any{"inputs": v, "counts": table}
}

type fakeClient func(llm.Request) (llm.Response, error)

func (f fakeClient) Complete(_ context.Context, r llm.Request) (llm.Response, error) { return f(r) }
func TestAnswerTrackParametersAndWhitespace(t *testing.T) {
	for _, full := range []bool{false, true} {
		client := fakeClient(func(r llm.Request) (llm.Response, error) {
			if *r.Temperature != .7 || (!full && (r.MaxTokens == nil || *r.MaxTokens != 500)) || (full && r.MaxTokens != nil) {
				t.Fatal(r)
			}
			return llm.Response{Text: "  answer \n"}, nil
		})
		r, e := Answer(t.Context(), client, Input{FullContext: full}, "openai-compat", "model", "sample", 512)
		want := "answer"
		if full {
			want = "  answer \n"
		}
		if e != nil || r.Text != want {
			t.Fatal(r, e)
		}
	}
	_, e := Answer(t.Context(), fakeClient(func(llm.Request) (llm.Response, error) { return llm.Response{Text: "  "}, nil }), Input{FullContext: true}, "openai-compat", "model", "sample", 512)
	if e == nil {
		t.Fatal("Track 1 empty answer accepted")
	}
}
