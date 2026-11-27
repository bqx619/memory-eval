package runner_test

import (
	"encoding/json"
	"memeval/internal/runner"
	"strings"
	"testing"
)

func TestNamespaceIsOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		a := newSpy()
		o := opts()
		o.RecordNamespace = enabled
		r := run(t, toy(), a, o)
		if enabled && r.Cases[0].Namespace != a.names[0] {
			t.Fatal("namespace does not match SUT", r.Cases[0].Namespace, a.names)
		}
		if !enabled {
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), `"namespace"`) || strings.Contains(string(b), `"logs"`) {
				t.Fatal("baseline wire shape changed")
			}
		}
	}
}

func TestIncrementalNamespacesMatchSharedSUT(t *testing.T) {
	a := &writeSpy{spy: newSpy(), readOnly: true}
	o := opts()
	o.RecordNamespace = true
	o.ResetPolicy = runner.IncrementalPerEpisode
	o.Repeats = 2
	r := run(t, incrementalToy(), a, o)
	for _, c := range r.Cases {
		if c.Namespace != a.names[c.RepeatIndex] {
			t.Fatal("shared episode namespace mismatch", c.Namespace, a.names)
		}
	}
}
