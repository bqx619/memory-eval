package suite

import (
	"context"
	"fmt"
)

type ValidateOptions struct {
	// nil means true; false skips only the line digest, never decoding/semantics.
	VerifyLines    *bool
	SkipLeakScan   bool
	LeakReport     *LeakReport
	ExpectedSHA256 string
}

func Validate(ctx context.Context, s *Suite, opts ValidateOptions) (Counts, error) {
	if err := ctx.Err(); err != nil {
		return Counts{}, err
	}
	if opts.ExpectedSHA256 != "" {
		h, err := SHA256(s)
		if err != nil {
			return Counts{}, err
		}
		if h != opts.ExpectedSHA256 {
			return Counts{}, fmt.Errorf("suite.json: expected suite_sha256 %s, got %s", opts.ExpectedSHA256, h)
		}
	}
	if s.bundle == nil {
		counts, err := validateSingle(s)
		if err != nil {
			return Counts{}, err
		}
		if !opts.SkipLeakScan {
			scan, err := LeakScan(s)
			if err != nil {
				return Counts{}, err
			}
			if opts.LeakReport != nil {
				*opts.LeakReport = scan
			}
			if len(scan.Problems) > 0 {
				return Counts{}, fmt.Errorf("suite leak scan: %s", scan.Problems[0])
			}
		}
		return counts, nil
	}
	counts := Counts{ByKind: map[string]int{}}
	cases := map[string]bool{}
	scan := LeakReport{Problems: []string{}, AnswerTextScan: "not_applicable"}
	verify := opts.VerifyLines == nil || *opts.VerifyLines
	err := s.iter(ctx, verify, func(ep *Episode) error {
		local := *s
		local.bundle = nil
		local.Episodes = []Episode{*ep}
		local.Splits = nil
		one, err := validateSingle(&local)
		if err != nil {
			return err
		}
		for _, c := range ep.Cases {
			if cases[c.ID] {
				return fmt.Errorf("duplicate case id %s across episodes", c.ID)
			}
			cases[c.ID] = true
		}
		counts.Episodes++
		counts.Events += one.Events
		counts.Cases += one.Cases
		counts.UnresolvedEvidenceRefs += one.UnresolvedEvidenceRefs
		for k, n := range one.ByKind {
			counts.ByKind[k] += n
		}
		if !opts.SkipLeakScan {
			r, err := LeakScan(&local)
			if err != nil {
				return err
			}
			scan.Checked += r.Checked
			scan.Problems = append(scan.Problems, r.Problems...)
			if len(r.Problems) > 0 {
				return fmt.Errorf("suite leak scan: %s", r.Problems[0])
			}
		}
		return nil
	})
	if opts.LeakReport != nil {
		*opts.LeakReport = scan
	}
	if err != nil {
		return Counts{}, err
	}
	used := map[string]bool{}
	for _, name := range sortedKeys(s.Splits) {
		for _, id := range s.Splits[name] {
			if !cases[id] || used[id] {
				return Counts{}, fmt.Errorf("split %s: unknown or repeated case id %s", name, id)
			}
			used[id] = true
		}
	}
	return counts, nil
}
