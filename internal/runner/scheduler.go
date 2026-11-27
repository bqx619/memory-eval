package runner

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/suite"
	"slices"
	"sync"
)

func ParallelWarning(parallel, cpus int) string {
	if parallel > cpus {
		return fmt.Sprintf("episode parallelism %d exceeds host CPU count %d; reduce --parallel if resource contention increases", parallel, cpus)
	}
	return ""
}

type episodeResult struct {
	cases    []result.Case
	warnings []string
	err      error
}
type episodeJob struct {
	ep  suite.Episode
	out *episodeResult
}

// The iterator owns selection, scheduling and result order. Workers own adapter
// and runtime episode state. Fresh cases share their read-only source history,
// but each case (with all its repeats) is an independent job. Incremental jobs
// retain the whole suite episode. Completed slots are read after Wait, never in
// completion order.
func scheduleEpisodes(ctx context.Context, s *suite.Suite, first adapter.Adapter, desc protocol.Describe, o Options, runID string) ([]*episodeResult, *result.Interruption, error) {
	count, selectedCases := 0, 0
	for _, ep := range s.Index() {
		cases := ep.Cases
		if o.Limit != nil {
			cases = min(cases, *o.Limit-selectedCases)
		}
		if cases > 0 {
			selectedCases += cases
			if o.ResetPolicy == IncrementalPerEpisode {
				count++
			} else {
				count += cases
			}
		}
		if count >= o.Parallel || (o.Limit != nil && selectedCases >= *o.Limit) {
			break
		}
	}
	count = min(o.Parallel, count)
	if count == 0 {
		return nil, nil, nil
	}
	workers := []adapter.Adapter{first}
	for len(workers) < count {
		a, err := o.NewAdapter(ctx)
		if err == nil && a == nil {
			err = fmt.Errorf("adapter factory returned nil")
		}
		if err != nil {
			for _, w := range workers[1:] {
				err = errors.Join(err, w.Close(context.WithoutCancel(ctx)))
			}
			return nil, nil, fmt.Errorf("adapter worker initialization failed: %w", err)
		}
		if b, ok := a.(interface{ BeginRun(string) }); ok {
			b.BeginRun(runID)
		}
		workers = append(workers, a)
	}
	available := make(chan chan episodeJob, count)
	queues := make([]chan episodeJob, count)
	closeErrors := make([]error, count)
	var wg sync.WaitGroup
	for i, a := range workers {
		queue := make(chan episodeJob)
		queues[i] = queue
		available <- queue
		wg.Go(func() {
			for job := range queue {
				job.out.cases, job.out.warnings = executeEpisode(ctx, a, desc, job.ep, o)
				// Release the last case's process before offering the worker again.
				job.out.err = a.Close(context.WithoutCancel(ctx))
				available <- queue
			}
			closeErrors[i] = a.Close(context.WithoutCancel(ctx))
		})
	}
	rows := []*episodeResult{}
	var interruption *result.Interruption
	dispatch := func(selected suite.Episode) error {
		row := &episodeResult{}
		rows = append(rows, row)
		needed := false
		for _, c := range selected.Cases {
			for repeat := 0; repeat < o.Repeats; repeat++ {
				needed = needed || o.Resume.Selected(identity.SampleID(c.ID, repeat))
			}
		}
		if needed && interruption == nil {
			var queue chan episodeJob
			select {
			case <-ctx.Done():
				interruption = &result.Interruption{Reason: "interrupted"}
			case queue = <-available:
			}
			// Check after a worker is free: do not prefetch jobs past a budget stop.
			if ctx.Err() != nil {
				interruption = &result.Interruption{Reason: "interrupted"}
			} else if o.StopScheduling != nil {
				if reason := o.StopScheduling(); reason != "" {
					interruption = &result.Interruption{Reason: reason}
				}
			}
			if interruption == nil {
				queue <- episodeJob{selected, row}
				return nil
			}
			if queue != nil {
				available <- queue
			}
		}
		// Reused rows survive interruption; only genuinely absent samples count
		// as unscheduled. No SDK/evaluator call runs for a skipped job.
		for _, c := range selected.Cases {
			for repeat := 0; repeat < o.Repeats; repeat++ {
				sample := identity.SampleID(c.ID, repeat)
				if o.Resume != nil {
					if old, ok := o.Resume.previous[sample]; ok {
						row.cases = append(row.cases, old)
						continue
					}
				}
				// A manual case filter can intentionally leave absent samples
				// unscheduled; preserve the interruption after enumeration.
			}
		}
		return nil
	}
	n := 0
	limitReached := errors.New("case limit reached")
	// Finish local enumeration after interruption: retain prior resume rows and
	// count absent samples without scheduling more SUT/evaluator work.
	err := s.Iter(context.WithoutCancel(ctx), func(ep *suite.Episode) error {
		if o.Limit != nil && n >= *o.Limit {
			return limitReached
		}
		selected := *ep
		if o.Limit != nil {
			selected.Cases = ep.Cases[:min(len(ep.Cases), *o.Limit-n)]
		}
		n += len(selected.Cases)
		if len(selected.Cases) == 0 {
			return nil
		}
		if o.ResetPolicy == IncrementalPerEpisode {
			return dispatch(selected)
		}
		for i := range selected.Cases {
			job := selected
			job.Cases = selected.Cases[i : i+1]
			if err := dispatch(job); err != nil {
				return err
			}
		}
		return nil
	})
	for _, queue := range queues {
		close(queue)
	}
	wg.Wait()
	if ctx.Err() != nil {
		interruption = &result.Interruption{Reason: "interrupted"}
	}
	if interruption == nil && o.Resume != nil && o.Resume.Original.Interruption != nil {
		completed := 0
		for _, row := range rows {
			completed += len(row.cases)
		}
		if completed < n*o.Repeats {
			interruption = &result.Interruption{Reason: o.Resume.Original.Interruption.Reason}
		}
	}
	if interruption != nil {
		interruption.Unscheduled = n * o.Repeats
		for _, row := range rows {
			interruption.Unscheduled -= len(row.cases)
		}
	}
	if errors.Is(err, limitReached) {
		err = nil
	}
	for _, row := range rows {
		err = errors.Join(err, row.err)
	}
	for _, e := range closeErrors {
		err = errors.Join(err, e)
	}
	return rows, interruption, err
}

func executeEpisode(ctx context.Context, a adapter.Adapter, desc protocol.Describe, ep suite.Episode, o Options) ([]result.Case, []string) {
	rows := []result.Case{}
	warnings := []string{}
	if o.ResetPolicy == IncrementalPerEpisode {
		byCase := make([][]result.Case, len(ep.Cases))
		for repeat := 0; repeat < o.Repeats; repeat++ {
			lastNeeded := -1
			for i, c := range ep.Cases {
				if o.Resume.Selected(identity.SampleID(c.ID, repeat)) {
					lastNeeded = i
				}
			}
			local := o
			if lastNeeded >= 0 && ctx.Err() == nil {
				if checkpointEpisode(&ep) {
					local.shared = prepareCheckpointEpisode(ctx, a, ep, repeat, o)
				} else {
					local.shared = prepareEpisode(ctx, a, ep, repeat, o)
				}
			}
			for i, c := range ep.Cases {
				if o.Resume != nil && !o.Resume.Selected(identity.SampleID(c.ID, repeat)) && o.Resume.Previous(identity.SampleID(c.ID, repeat)).SampleID == "" {
					continue
				}
				if ctx.Err() != nil {
					if o.Resume != nil {
						if old, ok := o.Resume.previous[identity.SampleID(c.ID, repeat)]; ok {
							byCase[i] = append(byCase[i], old)
						}
					}
					continue
				}
				if i > lastNeeded {
					byCase[i] = append(byCase[i], o.Resume.Previous(identity.SampleID(c.ID, repeat)))
					continue
				}
				if checkpointEpisode(&ep) {
					advanceCheckpoint(ctx, a, ep, c.Cutoff, local.shared, o)
				}
				if checkpointEpisode(&ep) && c.Kind == "memory_audit" && slices.Contains(desc.Capabilities, "dump") && !o.Resume.Selected(identity.SampleID(c.ID, repeat)) {
					restoreCheckpointDump(ctx, a, local.shared)
				}
				byCase[i] = append(byCase[i], resumedCase(ctx, a, desc, ep, c, repeat, local))
			}
		}
		for _, samples := range byCase {
			rows = append(rows, samples...)
		}
		return rows, warnings
	}
	for _, c := range ep.Cases {
		if suite.NewKind(c.Kind) && !mabSupported(c) && !beamSupported(c) && !memoraSupported(c) && !halumemSupported(c) && !prefEvalSupported(c) && !convoMemSupported(c) {
			warnings = appendUnique(warnings, "kind "+c.Kind+" scoring not implemented in this version; marked unsupported")
		}
		for repeat := 0; repeat < o.Repeats; repeat++ {
			if o.Resume != nil && !o.Resume.Selected(identity.SampleID(c.ID, repeat)) && o.Resume.Previous(identity.SampleID(c.ID, repeat)).SampleID == "" {
				continue
			}
			if ctx.Err() != nil {
				if o.Resume != nil {
					if old, ok := o.Resume.previous[identity.SampleID(c.ID, repeat)]; ok {
						rows = append(rows, old)
					}
				}
				continue
			}
			rows = append(rows, resumedCase(ctx, a, desc, ep, c, repeat, o))
		}
	}
	return rows, warnings
}
