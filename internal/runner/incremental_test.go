package runner_test

import (
	"context"
	"encoding/json"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"reflect"
	"strings"
	"testing"
)

type writeSpy struct {
	*spy
	readOnly bool
	queries  []protocol.RetrieveRequest
}

func (a *writeSpy) Describe(ctx context.Context) (protocol.Describe, error) {
	d, e := a.spy.Describe(ctx)
	d.ReadOnlyQueries = a.readOnly
	return d, e
}
func (a *writeSpy) Retrieve(ctx context.Context, ns string, r protocol.RetrieveRequest) (protocol.RetrieveResponse, error) {
	a.queries = append(a.queries, r)
	return a.spy.Retrieve(ctx, ns, r)
}
func incrementalToy() *suite.Suite {
	s := toy()
	s.SchemaVersion = "3.0"
	s.Protocol.Ingestion = "per_chunk:4096:o200k_base:mab_memorize"
	s.Protocol.ResetPolicyAllowed = []string{runner.IncrementalPerEpisode}
	s.Protocol.SpeakerMapping = map[string]string{"ingest_render": "mab_memorize"}
	for i := range s.Episodes[0].Events {
		s.Episodes[0].Events[i].Attributes = map[string]any{"mab_source": "ruler_qa1_197K"}
	}
	s.Episodes[0].Cases[0].Cutoff = 2
	c := s.Episodes[0].Cases[0]
	c.ID = "SECOND-QUESTION"
	c.Query = "different question"
	s.Episodes[0].Cases = append(s.Episodes[0].Cases, c)
	return s
}
func TestIncrementalWritesOnceAndQueriesReadOnly(t *testing.T) {
	s := incrementalToy()
	o := opts()
	o.ResetPolicy = runner.IncrementalPerEpisode
	o.Repeats = 2
	a := &writeSpy{spy: newSpy(), readOnly: true}
	r := run(t, s, a, o)
	if len(a.names) != 2 || !reflect.DeepEqual(a.batches, []int{1, 1, 1, 1}) || len(a.queries) != 4 || len(r.Cases) != 4 {
		t.Fatal(a.names, a.batches, len(a.queries), len(r.Cases))
	}
	for _, ev := range a.inputs {
		if !strings.HasPrefix(ev.Content, `Dialogue between User and Assistant \n<User>`) {
			t.Fatal(ev.Content)
		}
	}
	if a.queries[0].Query != s.Episodes[0].Cases[0].Query || a.queries[1].Query != s.Episodes[0].Cases[1].Query || a.queries[0].User != a.queries[1].User || a.queries[0].QueryInvocationID == a.queries[1].QueryInvocationID {
		t.Fatal(a.queries)
	}
	if r.Cases[0].PublicInputSHA256 != r.Cases[2].PublicInputSHA256 || r.Cases[1].PublicInputSHA256 != r.Cases[3].PublicInputSHA256 || r.Cases[0].PublicInputSHA256 == r.Cases[1].PublicInputSHA256 {
		t.Fatal("episode/repeat digest")
	}
	for _, i := range []int{2, 3} {
		if _, ok := r.Cases[i].Timings["ingest_ms"]; ok {
			t.Fatal("startup cost double counted")
		}
	}
	wire, _ := json.Marshal(a.inputs)
	if strings.Contains(string(wire), "mab_source") || strings.Contains(string(wire), "SECRET-GOLD") {
		t.Fatal("private fields leaked")
	}
}
func TestIncrementalGuardsAndFailure(t *testing.T) {
	s := incrementalToy()
	o := opts()
	o.ResetPolicy = runner.IncrementalPerEpisode
	a := &writeSpy{spy: newSpy()}
	if _, e := runner.Run(t.Context(), s, a, o); e == nil || !strings.Contains(e.Error(), "read_only_queries") {
		t.Fatal(e)
	}
	if len(a.names) > 0 {
		t.Fatal("reset before capability guard")
	}
	a = &writeSpy{spy: newSpy(), readOnly: true}
	a.visibility = false
	r := run(t, s, a, o)
	if len(a.names) != 1 || len(a.queries) != 0 {
		t.Fatal("ready failure was ignored")
	}
	for _, c := range r.Cases {
		if c.Status != result.IngestFailed {
			t.Fatal(c)
		}
	}
	s.Episodes[0].Cases[0].Cutoff = 1
	s.Episodes[0].Cases[0].QueryPoint = "at_index"
	if _, e := runner.Run(t.Context(), s, newSpy(), o); e == nil {
		t.Fatal("partial history incremental allowed")
	}
}
func TestIncrementalLimit(t *testing.T) {
	s := incrementalToy()
	s.Episodes = append(s.Episodes, s.Episodes[0])
	s.Episodes[1].ID = "ep-two"
	s.Episodes[1].Cases = append([]suite.Case{}, s.Episodes[1].Cases...)
	for i := range s.Episodes[1].Cases {
		s.Episodes[1].Cases[i].ID += "-second"
	}
	o := opts()
	o.ResetPolicy = runner.IncrementalPerEpisode
	n := 1
	o.Limit = &n
	a := &writeSpy{spy: newSpy(), readOnly: true}
	r := run(t, s, a, o)
	if len(r.Cases) != 1 || len(a.names) != 1 || len(a.inputs) != 2 {
		t.Fatal("limit rewrote/repeated history")
	}
}
