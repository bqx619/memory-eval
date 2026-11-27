package runner

import (
	"bufio"
	"context"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/identity"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/suite"
	"os"
	"reflect"
	"strings"
)

// ResumePlan retains the complete ordered experiment. Selection never changes
// the suite root, source limit, repeat identities or aggregate denominators.
type ResumePlan struct {
	Original       *result.Report
	Statuses       []result.Status
	Cases          []string // nil means no explicit case/sample filter.
	AllowSUTChange bool
	previous       map[string]result.Case
	selected       map[string]bool
}

func NewResume(r *result.Report, statuses string) (*ResumePlan, error) {
	if r == nil || r.RunID == "" || r.RunSeed == "" || len(r.Cases) == 0 && r.Interruption == nil {
		return nil, fmt.Errorf("resume requires a completed report with cases and run identity")
	}
	if j := r.Protocol.Judge; j != nil && j.Kind != "decision" {
		return nil, fmt.Errorf("resume rejects legacy judge kind %q; Jev-only requires protocol.judge.kind=decision", j.Kind)
	}
	p := &ResumePlan{Original: r, previous: map[string]result.Case{}, selected: map[string]bool{}}
	allowed := map[result.Status]bool{}
	for _, name := range strings.Split(statuses, ",") {
		s := result.Status(strings.TrimSpace(name))
		switch s {
		case result.Scored, result.Error, result.Invalid, result.Unsupported, result.Unscored, result.ContractError, result.IngestFailed:
		default:
			return nil, fmt.Errorf("invalid resume status %q", name)
		}
		if !allowed[s] {
			p.Statuses = append(p.Statuses, s)
		}
		allowed[s] = true
	}
	for _, c := range r.Cases {
		if c.SampleID == "" || c.SampleID != identity.SampleID(c.CaseID, c.RepeatIndex) {
			return nil, fmt.Errorf("invalid resume sample identity")
		}
		if _, ok := p.previous[c.SampleID]; ok {
			return nil, fmt.Errorf("duplicate resume sample %s", c.SampleID)
		}
		p.previous[c.SampleID] = c
		if allowed[c.Status] {
			p.selected[c.SampleID] = true
		}
	}
	return p, nil
}

// ParseResumeCases accepts a comma-separated list or an @file with one ID per
// line. A # inside an ID is the repeat separator, not an inline comment.
func ParseResumeCases(value string) ([]string, error) {
	var ids []string
	if strings.HasPrefix(value, "@") {
		file, err := os.Open(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, fmt.Errorf("resume cases: %w", err)
		}
		defer file.Close()
		s := bufio.NewScanner(file)
		for s.Scan() {
			id := strings.TrimSpace(s.Text())
			if id != "" && !strings.HasPrefix(id, "#") {
				ids = append(ids, id)
			}
		}
		if err := s.Err(); err != nil {
			return nil, fmt.Errorf("resume cases: %w", err)
		}
	} else {
		for _, part := range strings.Split(value, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				return nil, fmt.Errorf("resume cases list must not be empty or contain empty IDs")
			}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("resume cases list must not be empty")
	}
	return ids, nil
}

// SelectCases intersects the status selection with IDs in the recorded run.
// It never widens the original --limit or implicitly selects unscheduled rows.
func (p *ResumePlan) SelectCases(value string) error {
	ids, err := ParseResumeCases(value)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, c := range p.previous {
		known[c.CaseID], known[c.SampleID] = true, true
	}
	filter := map[string]bool{}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("unknown resume case or sample id %q in recorded run", id)
		}
		filter[id] = true
	}
	p.Cases = ids
	for sample := range p.selected {
		if !filter[sample] && !filter[p.previous[sample].CaseID] {
			delete(p.selected, sample)
		}
	}
	return nil
}

// RateLimitedOnly narrows an automatic retry without changing identities or
// the unscheduled suffix. Manual --resume keeps its existing status selection.
func (p *ResumePlan) RateLimitedOnly() {
	for sample := range p.selected {
		if !RateLimitedCase(p.previous[sample]) {
			delete(p.selected, sample)
		}
	}
}
func RateLimitedCase(c result.Case) bool {
	if c.Status != result.Error && c.Status != result.IngestFailed {
		return false
	}
	return c.ErrorType == "gateway_exhausted" || c.ErrorType == "rpc_timeout"
}

func (p *ResumePlan) Selected(sample string) bool {
	return p == nil || p.selected[sample] || p.Cases == nil && p.Original.Interruption != nil && p.previous[sample].SampleID == ""
}
func (p *ResumePlan) Count() int                         { return len(p.selected) }
func (p *ResumePlan) Previous(sample string) result.Case { return p.previous[sample] }

func publicDigest(ep suite.Episode, c suite.Case, repeat int, o Options) (string, error) {
	sample := identity.SampleID(c.ID, repeat)
	if o.ResetPolicy == IncrementalPerEpisode {
		id, err := identity.New(o.RunSeed, identity.SampleID("episode:"+ep.ID, repeat))
		if err != nil {
			return "", err
		}
		return suite.Digest(suite.RenderPublicEvents(ep.Events[:c.Cutoff], id, o.ingestRender))
	}
	id, err := identity.New(o.RunSeed, sample)
	if err != nil {
		return "", err
	}
	budget := c.Budget.Resolve(o.Budget)
	if halumemSupported(c) && c.Kind == "qa" {
		budget.TopK = 20
	}
	req := suite.PublicRequest(c, id, budget)
	if o.queryTimePolicy == "reference_only" {
		req.AsOf = ""
	}
	return suite.Digest(struct {
		Events  []protocol.PublicEvent   `json:"events"`
		Request protocol.RetrieveRequest `json:"request"`
	}{suite.RenderPublicEvents(ep.Events[:c.Cutoff], id, o.ingestRender), req})
}

// VerifyResume is read-only and must finish before any evaluator/SUT execution.
func VerifyResume(ctx context.Context, s *suite.Suite, o Options) error {
	if o.Resume == nil {
		return nil
	}
	p, old := o.Resume, o.Resume.Original
	sha, err := suite.SHA256(s)
	if err != nil {
		return err
	}
	if sha != old.SuiteSHA256 || s.SuiteID != old.SuiteID {
		return fmt.Errorf("resume suite trusted root mismatch")
	}
	if o.RunSeed != old.RunSeed || o.Mode != old.Mode || o.Repeats != old.Repeats || o.ResetPolicy != old.Protocol.ResetPolicy || !reflect.DeepEqual(o.Limit, old.Protocol.CaseLimit) || o.Budget != old.Protocol.Budget || o.BatchSize != old.Protocol.BatchSize || o.TraceText != old.Protocol.TraceText {
		return fmt.Errorf("resume run seed/mode/repeats/reset/limit/budget/batch/trace mismatch")
	}
	if o.Mode == FixedReader && (o.FixedReader == nil || o.FixedReader.Input != old.Protocol.ReaderInput) {
		return fmt.Errorf("resume reader input mismatch; sources and items results cannot be relabelled")
	}
	o.ingestRender, o.queryTimePolicy = s.Protocol.SpeakerMapping["ingest_render"], s.Protocol.QueryTimePolicy
	n, row := 0, 0
	err = s.Iter(ctx, func(ep *suite.Episode) error {
		cache := map[string]string{}
		for _, c := range ep.Cases {
			if c.Cutoff < 0 || c.Cutoff > len(ep.Events) {
				return fmt.Errorf("resume case cutoff is out of bounds")
			}
			if o.Limit != nil && n >= *o.Limit {
				break
			}
			for repeat := 0; repeat < o.Repeats; repeat++ {
				sample := identity.SampleID(c.ID, repeat)
				previous, ok := p.previous[sample]
				if !ok && old.Interruption != nil {
					if p.Cases == nil {
						p.selected[sample] = true
					}
					continue
				}
				if !ok || row >= len(old.Cases) || old.Cases[row].SampleID != sample || previous.EpisodeID != ep.ID || previous.Cutoff != c.Cutoff || previous.Kind != c.Kind {
					return fmt.Errorf("resume case selection/identity mismatch at %s", sample)
				}
				row++
				// Older unsupported new kinds never constructed a public request.
				if previous.Status == result.Unsupported && previous.PublicInputSHA256 == "" && !p.Selected(sample) {
					continue
				}
				key := fmt.Sprintf("%d:%d", repeat, c.Cutoff)
				digest := cache[key]
				if o.ResetPolicy != IncrementalPerEpisode || digest == "" {
					digest, err = publicDigest(*ep, c, repeat, o)
					if err != nil {
						return err
					}
					cache[key] = digest
				}
				if previous.PublicInputSHA256 == "" || digest != previous.PublicInputSHA256 {
					return fmt.Errorf("resume public input hash mismatch at %s", sample)
				}
			}
			n++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if row != len(old.Cases) {
		return fmt.Errorf("resume case count mismatch")
	}
	return nil
}

func resumedCase(ctx context.Context, a adapter.Adapter, desc protocol.Describe, ep suite.Episode, c suite.Case, repeat int, o Options) result.Case {
	sample := identity.SampleID(c.ID, repeat)
	if !o.Resume.Selected(sample) {
		return o.Resume.Previous(sample)
	}
	r := runCase(ctx, a, desc, ep, c, repeat, o)
	if o.Resume != nil {
		r.PreviousStatus = o.Resume.Previous(sample).Status
	}
	return r
}

// Rebuild checkpoint dump state even when its scored row is reused. No reader
// or judge is called here; later selected audits need the preceding snapshot.
func restoreCheckpointDump(ctx context.Context, a adapter.Adapter, state *episodeState) {
	if state.err != nil {
		return
	}
	d, ok := a.(adapter.Dumper)
	if !ok {
		return
	}
	dump, err := d.Dump(ctx, state.id.Namespace())
	if err == nil {
		err = adapter.ValidateDump(dump)
	}
	if err != nil {
		state.err = err
		return
	}
	state.previousDump = map[string]bool{}
	for _, item := range dump.Items {
		state.previousDump[item.ID] = true
	}
}
