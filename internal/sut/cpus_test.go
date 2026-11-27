package sut

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCPULimitValidation(t *testing.T) {
	m, _ := fixtureManifest(t, "uv-python")
	for _, tc := range []struct {
		value *float64
		bad   bool
	}{{nil, false}, {new(.5), false}, {new(2.5), false}, {new(0.0), true}, {new(-1.0), true}} {
		m.Sandbox.Limits.CPUs = tc.value
		raw, e := json.Marshal(m)
		if e != nil {
			t.Fatal(e)
		}
		writeFixture(t, m.Path, string(raw))
		loaded, e := Load(m.Path)
		if tc.bad {
			if e == nil || !strings.Contains(e.Error(), "cpus") {
				t.Fatal(e)
			}
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		if tc.value == nil {
			if loaded.Sandbox.Limits.CPUs != nil || strings.Contains(string(raw), `"cpus"`) {
				t.Fatal("omitted cpus changed identity")
			}
		} else if loaded.Sandbox.Limits.CPUs == nil || *loaded.Sandbox.Limits.CPUs != *tc.value {
			t.Fatal("cpus lost")
		}
	}
}
