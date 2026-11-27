package convomem

import (
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestScalaTemplates(t *testing.T) {
	base := "convomem/src/main/scala/com/salesforce/crmmembench"
	read := func(p string) string {
		b, e := os.ReadFile(testoracle.Upstream(t, filepath.Join(base, p)))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	extract := func(pattern, s string) string {
		m := regexp.MustCompile(pattern).FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("missing pattern %s", pattern)
		}
		return m[1]
	}
	src := read("questions/evidence/generation/AnsweringEvaluation.scala")
	for key, obj := range map[string]string{"default": "Default", "rubric": "RubricBased", "abstention": "Abstention"} {
		block := strings.SplitN(src, "object "+obj+"AnsweringEvaluation extends AnsweringEvaluation {", 2)[1]
		guide := extract(`(?s)val guidelines = """(.*?)"""`, block)
		body := extract(`(?s)s"""(.*?)"""`, block)
		want := strings.ReplaceAll(body, "$guidelines", guide)
		if want != Templates[key] {
			t.Fatalf("%s literal drift", key)
		}
		if strings.Contains(want, "evidenceMessages") {
			t.Fatal("unsupported evidence interpolation")
		}
	}
	criteria := extract(`(?s)def getJudgeEvaluationCriteria: String = \{\s*"""(.*?)"""`, read("evaluation/memory/MemoryPromptUtils.scala"))
	body := extract(`(?s)s"""(You are an assistant.*?)"""`, read("evaluation/memory/LongContextMemoryAnswerer.scala"))
	if strings.ReplaceAll(body, "${MemoryPromptUtils.getJudgeEvaluationCriteria}", criteria) != Templates["reader"] {
		t.Fatal("reader literal drift")
	}
	utils := read("Utils.scala")
	if extract(`ANSWER_VERIFICATION_MAX_RETRIES: Int = (\d+)`, utils) != "20" || extract(`MODEL_ANSWER_MAX_RETRIES: Int = (\d+)`, utils) != "20" {
		t.Fatal("retry constant drift")
	}
	for _, s := range []string{"Try(f)", "case Failure(exception) if retries > 0", "loop(retries - 1, nextDelay)", "loop(maxRetries, initialDelay)"} {
		if !strings.Contains(utils, s) {
			t.Fatal("retry semantics changed")
		}
	}
	if !strings.Contains(read("questions/evidence/generators/ImplicitConnectionEvidenceGenerator.scala"), "RubricBasedAnsweringEvaluation") {
		t.Fatal("implicit mapping drift")
	}
	prompt := Render("default", map[string]string{"question": "$correctAnswer", "correctAnswer": "$modelAnswer", "modelAnswer": "private"})
	if !strings.Contains(prompt, "Question: $correctAnswer\nCorrect Answer: $modelAnswer\nModel Response: private") {
		t.Fatal("recursive interpolation")
	}
}
