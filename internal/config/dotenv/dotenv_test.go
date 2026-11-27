package dotenv

import (
	"bytes"
	"github.com/joho/godotenv"
	flag "github.com/spf13/pflag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseDataNotShell(t *testing.T) {
	text := "# header\nexport FIRST='a # b $HOME $(touch x)'\nSECOND=\"a\\n\\\"b\\\\c\" # comment\nEMPTY='' # blank\nURL=https://host/#part\nPLAIN=value  # note\n"
	got, err := Parse(strings.NewReader(text))
	want := map[string]string{"FIRST": "a # b $HOME $(touch x)", "SECOND": "a\n\"b\\c", "EMPTY": "", "URL": "https://host/#part", "PLAIN": "value"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"A=1\nA=2", "lower=x", "_KEY=x", "A='unterminated", "A=\"v\" trailing", "A=bad\x00", "#\x00", "no equals"} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted invalid syntax")
		}
	}
}
func TestRootPermissionsAndPrecedence(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0600)
	child := filepath.Join(root, "nested")
	os.Mkdir(child, 0700)
	t.Chdir(child)
	path := filepath.Join(root, ".env")
	os.WriteFile(path, []byte("MEMEVAL_LLM_MODE=record\nMEMEVAL_LLM_PARALLEL=bad\n"), 0644)
	os.Chmod(path, 0644)
	t.Setenv("MEMEVAL_LLM_MODE", "replay")
	var warning bytes.Buffer
	s, undo, err := Load("", &warning)
	if err != nil {
		t.Fatal(err)
	}
	defer undo()
	if s.Root != root || s.Sources["MEMEVAL_LLM_MODE"] != "env" || s.Sources["MEMEVAL_LLM_PARALLEL"] != ".env" || !strings.Contains(warning.String(), "chmod 600") {
		t.Fatal(s, warning.String())
	}
	f := flag.NewFlagSet("fixture", flag.ContinueOnError)
	mode := f.String("llm", "default", "")
	parallel := f.Int("llm-parallel", 4, "")
	args := []string{"--llm", "live", "--llm-parallel", "2"}
	if err := ApplyFlags(f, args); err != nil {
		t.Fatal("invalid shadowed env should not fail", err)
	}
	if err := f.Parse(args); err != nil || *mode != "live" || *parallel != 2 {
		t.Fatal(err, *mode, *parallel)
	}
	f = flag.NewFlagSet("fixture", flag.ContinueOnError)
	mode = f.String("llm", "default", "")
	if err := ApplyFlags(f, nil); err != nil || *mode != "replay" {
		t.Fatal(err, *mode)
	}
	f.Int("llm-parallel", 4, "")
	if err := ApplyFlags(f, nil); err == nil {
		t.Fatal("invalid effective env accepted")
	}
	os.Chmod(path, 0600)
	warning.Reset()
	if _, err := Read(path, &warning); err != nil || warning.Len() != 0 {
		t.Fatal(err, warning.String())
	}
	if values, err := Read(filepath.Join(root, "missing"), nil); err != nil || len(values) != 0 {
		t.Fatal(err)
	}
}

func TestGodotenvSemanticsAndMultilineDuplicates(t *testing.T) {
	for _, input := range []string{
		"export\u00a0A=one\r\nB=${A}\r\n",
		"BASE=world\nTEXT=\"hello ${BASE}\\nline\"\nSINGLE='${BASE}'\n",
		"A: value # comment\nB=\"first\nA=not an assignment\nlast\"\nC=done",
		"A=\"tab\\tcarriage\\r\" B=another\n",
		"EMPTY= # godotenv treats this as a literal value\n",
	} {
		want, err := godotenv.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(strings.NewReader(input))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("godotenv parity: got=%q want=%q err=%v", got, want, err)
		}
	}
	for _, input := range []string{
		"export\u00a0A=one\nA=two",
		"A=one\nexport A: two", "A=\"first\nB=inside\"\nA=second",
		"A=\"one\" A=two", "A='one'\n# ignored\nA='two'",
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatal("missed duplicate", err)
		}
	}
}
