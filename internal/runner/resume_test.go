package runner_test

import (
	"encoding/json"
	"fmt"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResumeCaseListSelection(t *testing.T) {
	s, o := resumeFixture(), opts()
	o.Repeats = 2
	old := run(t, s, newSpy(), o)
	old.Cases[1].Status = result.Error
	first, second := old.Cases[0], old.Cases[2]
	for _, tt := range []struct {
		name, statuses, list string
		want                 []int
	}{
		{"case-all-repeats", "scored,error", first.CaseID, []int{0, 1}},
		{"sample", "scored,error", old.Cases[1].SampleID, []int{1}},
		{"intersection", "scored", first.CaseID, []int{0}},
		{"mixed", "scored,error", first.CaseID + "," + second.SampleID, []int{0, 1, 2}},
		{"no-status-match", "invalid", first.CaseID, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := runner.NewResume(old, tt.statuses)
			if err != nil {
				t.Fatal(err)
			}
			if err = p.SelectCases(tt.list); err != nil {
				t.Fatal(err)
			}
			if p.Count() != len(tt.want) {
				t.Fatal(p.Count())
			}
			selected := map[int]bool{}
			for _, i := range tt.want {
				selected[i] = true
			}
			for i, c := range old.Cases {
				if p.Selected(c.SampleID) != selected[i] {
					t.Fatal(i, c.SampleID)
				}
			}
			local := o
			local.Resume = p
			a := newSpy()
			calls := 0
			a.retrieve = func(*spy) (protocol.RetrieveResponse, error) {
				calls++
				return protocol.RetrieveResponse{Items: []protocol.Item{}}, nil
			}
			next := run(t, s, a, local)
			if calls != len(tt.want) {
				t.Fatal(calls)
			}
			for i, c := range next.Cases {
				if !selected[i] && !reflect.DeepEqual(c, old.Cases[i]) {
					t.Fatal("unselected row changed", i)
				}
			}
		})
	}
	file := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(file, []byte("# comment\n\n  "+first.CaseID+" \n"+second.SampleID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ := runner.NewResume(old, "scored,error")
	if err := p.SelectCases("@" + file); err != nil {
		t.Fatal(err)
	}
	if len(p.Cases) != 2 || p.Count() != 3 {
		t.Fatal(p.Cases, p.Count())
	}
	for _, list := range []string{"", ",", first.CaseID + ",", "unknown", first.CaseID + "#999", "@" + file + "-missing"} {
		p, _ := runner.NewResume(old, "scored")
		if err := p.SelectCases(list); err == nil {
			t.Fatal("accepted", list)
		}
	}
	if err := os.WriteFile(file, []byte("\n# only comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ParseResumeCases("@" + file); err == nil {
		t.Fatal("empty file accepted")
	}
}

func TestResumeCaseListPreservesUnscheduledAndLimit(t *testing.T) {
	for _, policy := range []string{runner.FreshPerCase, runner.IncrementalPerEpisode} {
		t.Run(policy, func(t *testing.T) {
			s, o := resumeFixture(), opts()
			o.ResetPolicy = policy
			if policy == runner.IncrementalPerEpisode {
				s = incrementalToy()
			}
			o.Limit = new(2)
			old := run(t, s, newSpy(), o)
			old.Cases = old.Cases[:1]
			old.Interruption = &result.Interruption{Reason: "interrupted", Unscheduled: 1}
			p, err := runner.NewResume(old, "scored")
			if err != nil {
				t.Fatal(err)
			}
			if err = p.SelectCases(old.Cases[0].CaseID); err != nil {
				t.Fatal(err)
			}
			o.Resume = p
			next := run(t, s, newSpy(), o)
			if len(next.Cases) != 1 || next.Interruption == nil || next.Interruption.Unscheduled != 1 {
				t.Fatal(len(next.Cases), next.Interruption)
			}
			if p.Selected("Q-invalid#0") {
				t.Fatal("outside original limit selected")
			}
		})
	}
}

func resumeFixture() *suite.Suite {
	s := toy()
	for i, id := range []string{"Q-error", "Q-invalid"} {
		c := s.Episodes[0].Cases[0]
		c.ID = id
		c.Query += strings.Repeat("?", i+1)
		s.Episodes[0].Cases = append(s.Episodes[0].Cases, c)
	}
	return s
}
func cloneReport(t *testing.T, r *result.Report) *result.Report {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var x result.Report
	if e = json.Unmarshal(b, &x); e != nil {
		t.Fatal(e)
	}
	return &x
}

func TestResumeSelectionMergeAndGuards(t *testing.T) {
	s, o := resumeFixture(), opts()
	old := run(t, s, newSpy(), o)
	old.Cases[1].Status, old.Cases[2].Status = result.Error, result.Invalid
	for _, statuses := range []string{"error,invalid", "invalid", "scored", "unsupported"} {
		t.Run(statuses, func(t *testing.T) {
			p, e := runner.NewResume(old, statuses)
			if e != nil {
				t.Fatal(e)
			}
			o.Resume = p
			a := newSpy()
			queries := 0
			a.retrieve = func(s *spy) (protocol.RetrieveResponse, error) {
				queries++
				return protocol.RetrieveResponse{Items: []protocol.Item{}}, nil
			}
			r := run(t, s, a, o)
			if queries != p.Count() || r.RunID != old.RunID || r.RunSeed != old.RunSeed || r.StartedAtUTC != old.StartedAtUTC {
				t.Fatal(queries, p.Count(), r.RunID)
			}
			for i, c := range r.Cases {
				if !p.Selected(c.SampleID) {
					if !reflect.DeepEqual(c, old.Cases[i]) {
						t.Fatal("reused row changed")
					}
				} else if c.PreviousStatus != old.Cases[i].Status || c.Status == result.Error || c.Status == result.Invalid {
					t.Fatal(c)
				}
			}
		})
	}
	for _, kind := range []string{"root", "hash", "seed", "budget", "limit", "missing", "order", "sut"} {
		t.Run("reject-"+kind, func(t *testing.T) {
			r := cloneReport(t, old)
			local := opts()
			switch kind {
			case "root":
				r.SuiteSHA256 = strings.Repeat("0", 64)
			case "hash":
				r.Cases[0].PublicInputSHA256 = strings.Repeat("0", 64)
			case "seed":
				local.RunSeed = strings.Repeat("1", 64)
			case "budget":
				local.Budget.TopK++
			case "limit":
				local.Limit = new(1)
			case "missing":
				r.Cases = r.Cases[:2]
			case "order":
				r.Cases[0], r.Cases[1] = r.Cases[1], r.Cases[0]
			case "sut":
				r.SUT.Describe.Name = "wrong"
			}
			p, e := runner.NewResume(r, "error,invalid")
			if e != nil {
				t.Fatal(e)
			}
			local.Resume = p
			a := newSpy()
			if _, e = runner.Run(t.Context(), s, a, local); e == nil {
				t.Fatal("accepted mismatch")
			}
			if len(a.names) > 0 {
				t.Fatal("called SUT before rejection")
			}
		})
	}
	if _, e := runner.NewResume(old, "typo"); e == nil {
		t.Fatal("bad status accepted")
	}
	duplicate := cloneReport(t, old)
	duplicate.Cases = append(duplicate.Cases, duplicate.Cases[0])
	if _, e := runner.NewResume(duplicate, "error"); e == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestResumeIncrementalRebuildsHistoryOnlyForSelectedEpisodes(t *testing.T) {
	s := incrementalToy()
	o := opts()
	o.ResetPolicy = runner.IncrementalPerEpisode
	o.Repeats = 2
	old := run(t, s, &writeSpy{spy: newSpy(), readOnly: true}, o)
	old.Cases[3].Status = result.Error
	p, e := runner.NewResume(old, "error,invalid")
	if e != nil {
		t.Fatal(e)
	}
	o.Resume = p
	a := &writeSpy{spy: newSpy(), readOnly: true}
	r := run(t, s, a, o)
	if len(a.names) != 1 || len(a.inputs) != 2 || len(a.queries) != 1 {
		t.Fatal(len(a.names), len(a.inputs), len(a.queries))
	}
	for i := 0; i < 3; i++ {
		if !reflect.DeepEqual(old.Cases[i], r.Cases[i]) {
			t.Fatal("reused incremental row changed")
		}
	}
	if r.Cases[3].PreviousStatus != result.Error {
		t.Fatal(r.Cases[3])
	}
}

func TestGovernorStopAndResumeUnscheduled(t *testing.T) {
	for _, limit := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s, o := resumeFixture(), opts()
			original := s.Episodes[0]
			s.Episodes = nil
			for i, c := range original.Cases {
				ep := original
				ep.ID = fmt.Sprintf("episode-%d", i)
				ep.Cases = []suite.Case{c}
				s.Episodes = append(s.Episodes, ep)
			}
			checks := 0
			o.StopScheduling = func() string {
				checks++
				if checks > limit {
					return "budget"
				}
				return ""
			}
			r := run(t, s, newSpy(), o)
			if r.Interruption == nil || len(r.Cases) != limit || r.Interruption.Unscheduled != 3-limit {
				t.Fatal(len(r.Cases), r.Interruption)
			}
			p, err := runner.NewResume(r, "error,invalid")
			if err != nil {
				t.Fatal(err)
			}
			o.Resume = p
			o.StopScheduling = nil
			next := run(t, s, newSpy(), o)
			if next.Interruption != nil || len(next.Cases) != 3 || next.RunID != r.RunID {
				t.Fatal(next.Interruption, len(next.Cases))
			}
			for i := 0; i < limit; i++ {
				if !reflect.DeepEqual(next.Cases[i], r.Cases[i]) {
					t.Fatal("preserved case changed")
				}
			}
			// Trusted roots and public-input hashes remain mandatory for partial runs.
			if limit > 0 {
				bad := cloneReport(t, r)
				bad.Cases[0].PublicInputSHA256 = "changed"
				o.Resume, _ = runner.NewResume(bad, "error")
				if _, err = runner.Run(t.Context(), s, newSpy(), o); err == nil {
					t.Fatal("partial public input mismatch accepted")
				}
			}
		})
	}
}
func TestGovernorStopKeepsIncrementalEpisodeIntact(t *testing.T) {
	s := incrementalToy()
	ep := s.Episodes[0]
	ep.ID += "-second"
	ep.Cases = append([]suite.Case(nil), ep.Cases...)
	for i := range ep.Cases {
		ep.Cases[i].ID += "-second"
	}
	s.Episodes = append(s.Episodes, ep)
	o := opts()
	o.ResetPolicy = runner.IncrementalPerEpisode
	o.Repeats = 2
	checks := 0
	o.StopScheduling = func() string {
		checks++
		if checks > 1 {
			return "exhausted"
		}
		return ""
	}
	r := run(t, s, &writeSpy{spy: newSpy(), readOnly: true}, o)
	if r.Interruption == nil || len(r.Cases) != 4 || r.Interruption.Unscheduled != 4 {
		t.Fatal(r.Interruption, len(r.Cases))
	}
	p, err := runner.NewResume(r, "error,invalid")
	if err != nil {
		t.Fatal(err)
	}
	o.Resume = p
	o.StopScheduling = nil
	next := run(t, s, &writeSpy{spy: newSpy(), readOnly: true}, o)
	if len(next.Cases) != 8 {
		t.Fatal(len(next.Cases))
	}
}
