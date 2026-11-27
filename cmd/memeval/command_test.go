package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCobraLegacyCommandFlagsAndHelp(t *testing.T) {
	b, err := os.ReadFile("../../testdata/cli/legacy-help.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]map[string]string
	if err = json.Unmarshal(b, &expected); err != nil {
		t.Fatal(err)
	}
	for path, flags := range expected {
		var out bytes.Buffer
		root := newRootCommand(context.Background(), &out, io.Discard)
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		for name, help := range flags {
			f := cmd.Flags().Lookup(name)
			if f == nil || f.Usage != help {
				t.Errorf("%s --%s help changed", path, name)
			}
		}
		root.SetArgs(append(strings.Fields(path), "--help"))
		if err := root.Execute(); err != nil || !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%s help: %v", path, err)
		}
	}
}

func TestCobraLegacySingleDash(t *testing.T) {
	var out bytes.Buffer
	err := runCommand(context.Background(), []string{"run", "-sut", "null", "-limit=0", "--help"}, &out, io.Discard)
	if err != nil || !strings.Contains(out.String(), "--limit") {
		t.Fatal(err)
	}
}

func TestLegacyFlagLikeValues(t *testing.T) {
	root := newRootCommand(context.Background(), io.Discard, io.Discard)
	args := []string{"report", "input", "-out", "-sut", "--compare=-out", "--", "-out"}
	want := []string{"report", "input", "--out", "-sut", "--compare=-out", "--", "-out"}
	if got := legacyArgs(root, args); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
