package personamem

import "testing"

func TestExtractBranches(t *testing.T) {
	for _, x := range []struct {
		response, gold, final string
		pass                  bool
	}{{"why (a) <final_answer>(c)</final_answer>", "(c)", "(c)", true}, {"<final_answer> C ", "c", "C", true}, {"(c)", "c", "(c)", true}, {"C", "(c)", "C", true}, {"<final_answer>(b) (c)", "c", "(b) (c)", false}, {"I select (c). <final_answer>unsure", "c", "unsure", true}, {"éaé", "a", "éaé", false}, {"<final_answer>(b)", "c", "(b)", false}} {
		got, raw, _ := Extract(x.response, x.gold)
		if got != x.pass || raw != x.final {
			t.Fatalf("%+v: %v %q", x, got, raw)
		}
	}
}
