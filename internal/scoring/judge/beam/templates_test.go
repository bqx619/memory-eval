package beam

import (
	"memeval/internal/procexec"
	"memeval/internal/testoracle"
	"os/exec"
	"testing"
)

func TestVendoredLiterals(t *testing.T) {
	for _, path := range []string{"prompts.py", "evaluation/compute_metrics.py", "answer_probing_questions/long_term_memory_methods.py"} {
		testoracle.Upstream(t, "beam/src/"+path)
	}
	b, e := procexec.CombinedOutput(exec.Command("python3", "-B", "../../../../scripts/extract_beam_templates.py", "--check"))
	if e != nil {
		t.Fatalf("%s %v", b, e)
	}
	t.Logf("%s", b)
}
