package pytext

import (
	"encoding/json"
	"memeval/internal/testoracle"
	"reflect"
	"testing"
)

func TestNestedLiteralOracle(t *testing.T) {
	ss := []string{`['x', {'role':'user', 'content':'it\'s\nokay', 'count':-123, 'real':1.25e-4, 'yes':True, 'no':False,'none':None}]`, `{'a': [[],{}, [1,+2,-3]], 'b': '\x41\u03a3\U0001f642',}`, `[0,1.,.5,-2e3,False,None]`, `{'same': 1, 'same': 2}`}
	var want []any
	testoracle.Run(t, `import ast,json,sys; print(json.dumps([ast.literal_eval(s) for s in json.load(sys.stdin)]))`, ss, &want)
	for i, s := range ss {
		v, e := ParseLiteral(s)
		if e != nil {
			t.Fatal(s, e)
		}
		b, _ := json.Marshal(v)
		var generic any
		json.Unmarshal(b, &generic)
		if !reflect.DeepEqual(generic, want[i]) {
			t.Fatal(s, v, want[i])
		}
	}
}
func TestLiteralRejectsExecution(t *testing.T) {
	for _, s := range []string{`__import__('os').system('true')`, `[x for x in []]`, `{'x': lambda: 1}`, `[1] junk`, `1e999`, string([]byte{0}), `(1,2)`} {
		if _, e := ParseLiteral(s); e == nil {
			t.Fatal(s)
		}
	}
}
func TestReprOracle(t *testing.T) {
	v := []string{"simple", "it's", "both '\"", "\x00\x1c\u00a0\nΣİ🙂"}
	var want string
	testoracle.Run(t, `import json,sys;print(json.dumps(repr(json.load(sys.stdin))))`, v, &want)
	if got := ReprStrings(v); got != want {
		t.Fatalf("%q != %q", got, want)
	}
}
