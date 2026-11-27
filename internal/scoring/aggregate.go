package scoring

import "memeval/internal/result"

func mean(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	s := 0.
	for _, v := range xs {
		s += v
	}
	return Float(s / float64(len(xs)))
}
func Aggregate(rows []result.Case) result.Summary {
	s := result.Summary{Planned: len(rows), StatusCounts: map[result.Status]int{}, Metrics: map[string]result.Metric{}, Timings: map[string]result.Timing{}}
	values := map[string][]float64{}
	passed, chars := []float64{}, []float64{}
	scored := 0
	for _, r := range rows {
		s.StatusCounts[r.Status]++
		if r.Status == result.Invalid {
			s.Invalid++
			if r.QA != nil && r.QA.ReaderOutput != nil && r.QA.ReaderOutput.Empty {
				s.ReaderEmptyAnswers++
			}
		}
		s.UntraceableHits += r.Diagnostics["untraceable_hits"]
		if r.ReturnedChars != nil {
			chars = append(chars, float64(*r.ReturnedChars))
		}
		if r.Status != result.Scored {
			continue
		}
		scored++
		if r.Passed != nil {
			passed = append(passed, boolFloat(*r.Passed))
			if *r.Passed {
				s.Passed++
			}
		}
		for k, v := range r.Metrics {
			if _, ok := values[k]; !ok {
				values[k] = nil
			}
			if v != nil {
				values[k] = append(values[k], *v)
			}
		}
	}
	s.NWithPassJudgment = len(passed)
	s.PassRateOnScored = mean(passed)
	if len(rows) > 0 {
		s.ScoringCoverage = Float(float64(scored) / float64(len(rows)))
	}
	s.ExecutionErrors = s.StatusCounts[result.Error] + s.StatusCounts[result.ContractError] + s.StatusCounts[result.IngestFailed]
	s.Unsupported = s.StatusCounts[result.Unsupported]
	for k, v := range values {
		s.Metrics[k] = result.Metric{Mean: mean(v), N: len(v)}
	}
	for _, phase := range []string{"reset_ms", "ingest_ms", "ready_ms", "query_ms", "answer_ms"} {
		xs := []float64{}
		total := 0.
		for _, r := range rows {
			if v, ok := r.Timings[phase]; ok {
				xs = append(xs, v)
				total += v
			}
		}
		s.Timings[phase] = result.Timing{P50: Percentile(xs, .5), P95: Percentile(xs, .95), Total: total, N: len(xs)}
	}
	s.ReturnedChars = result.Percentiles{P50: Percentile(chars, .5), P95: Percentile(chars, .95)}
	return s
}
