package runner_test

import (
	"context"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/runner"
	"testing"
)

func TestInterruptedSchedulingRetainsResultsAndResume(t *testing.T) {
	for _, policy := range []string{runner.FreshPerCase, runner.IncrementalPerEpisode} {
		t.Run(policy, func(t *testing.T) {
			s := parallelSuite(4)
			for i := range s.Episodes {
				s.Episodes[i].Cases[0].Cutoff = len(s.Episodes[i].Events)
				c := s.Episodes[i].Cases[0]
				c.ID += "-second"
				s.Episodes[i].Cases = append(s.Episodes[i].Cases, c)
			}
			o := opts()
			limit := 5
			o.ResetPolicy, o.Repeats, o.Limit = policy, 2, &limit
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a := &writeSpy{spy: newSpy(), readOnly: true}
			calls := 0
			a.retrieve = func(s *spy) (protocol.RetrieveResponse, error) {
				calls++
				if calls == 2 {
					cancel()
					return protocol.RetrieveResponse{}, ctx.Err()
				}
				return s.Adapter.Retrieve(ctx, s.names[len(s.names)-1], s.request)
			}
			r, err := runner.Run(ctx, s, a, o)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || !a.closed || len(r.Cases) != 2 || r.Summary.StatusCounts[result.Scored] != 1 || r.Summary.StatusCounts[result.Error] != 1 {
				t.Fatalf("discarded work or scheduled after cancel: calls=%d, summary=%+v", calls, r.Summary)
			}
			if r.Interruption == nil || r.Interruption.Reason != "interrupted" || r.Interruption.Unscheduled != 8 {
				t.Fatal(r.Interruption)
			}
			o.Resume, err = runner.NewResume(r, "error")
			if err != nil {
				t.Fatal(err)
			}
			next, err := runner.Run(t.Context(), s, &writeSpy{spy: newSpy(), readOnly: true}, o)
			if err != nil {
				t.Fatal(err)
			}
			if next.Interruption != nil || len(next.Cases) != 10 || next.Summary.StatusCounts[result.Scored] != 10 || next.RunID != r.RunID {
				t.Fatalf("resume lost interrupted samples: %+v; %+v", next.Interruption, next.Summary)
			}
		})
	}
}
