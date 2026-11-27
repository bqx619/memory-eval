package main

import (
	"bytes"
	"memeval/internal/sut"
	"memeval/internal/testoracle"
	"testing"
)

func TestEnvironmentCLIRegistrationAndMissingLock(t *testing.T) {
	testoracle.FakeDocker(t).Configure(t)
	var out bytes.Buffer
	for _, name := range []string{"install", "list", "check", "destroy"} {
		root := newRootCommand(t.Context(), &out, &out)
		cmd, _, e := root.Find([]string{"env", name})
		if e != nil || cmd.Name() != name {
			t.Fatal(name, e)
		}
	}
	t.Setenv("MEMEVAL_CACHE_DIR", t.TempDir())
	e := runCommand(t.Context(), []string{"env", "check", "../../suts/echo/manifest.json"}, &out, &out)
	if e == nil || exitCode(e) != 3 {
		t.Fatal("missing env must exit 3", e)
	}
	m, e := sut.Load("../../suts/echo/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sut.CheckLock(m); e == nil {
		t.Fatal("legacy source lock accepted without installed env")
	}
	for _, args := range [][]string{{"env", "destroy", "--all"}, {"env", "destroy", "missing"}, {"env", "list"}} {
		if e = runCommand(t.Context(), args, &out, &out); e != nil {
			t.Fatal(e)
		}
	}
	if e = runCommand(t.Context(), []string{"env", "destroy", "../unsafe"}, &out, &out); e == nil {
		t.Fatal("unsafe name accepted")
	}
}
