package protocol

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestDescribeDumpCapabilities(t *testing.T) {
	for _, x := range []struct {
		raw  string
		want bool
	}{{`{"capabilities":["retrieve","dump"]}`, true}, {`{"capabilities":{"dump":true,"read_only_queries":true}}`, true}, {`{"capabilities":{"dump":false,"retrieve":true}}`, false}, {`{"capabilities":["retrieve"]}`, false}} {
		var d Describe
		if e := json.Unmarshal([]byte(x.raw), &d); e != nil || slices.Contains(d.Capabilities, "dump") != x.want {
			t.Fatal(d, e)
		}
	}
	var d Describe
	if e := json.Unmarshal([]byte(`{"capabilities":{"dump":null}}`), &d); e == nil {
		t.Fatal("null bool accepted")
	}
}
