package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"memeval/internal/adapter"
	"memeval/internal/identity"
	"memeval/internal/procexec"
	"memeval/internal/protocol"
	"memeval/internal/result"
	"memeval/internal/scoring"
	"memeval/internal/scoring/judge/inputlog"
	mabscore "memeval/internal/scoring/mab"
	"memeval/internal/stats"
	"memeval/internal/suite"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Retrieval             = "retrieval"
	FixedReader           = "fixed_reader"
	NativeAnswer          = "native_answer"
	FreshPerCase          = "fresh_per_case"
	IncrementalPerEpisode = "incremental_per_episode"
)

// InProcessWarning describes Run's default isolation metadata. External callers
// replacing that metadata must also remove this baseline-only disclosure.
const InProcessWarning = "In-process trusted baselines; no OS isolation or network enforcement"

type Options struct {
	NewAdapter                   func(context.Context) (adapter.Adapter, error) // Same configuration; fresh worker state.
	RecordNamespace              bool
	StopScheduling               func() string
	Resume                       *ResumePlan
	mabCandidates                *mabscore.Candidates
	shared                       *episodeState
	fullContext                  bool
	benchmarkID, ingestRender    string
	episodeSpeakers              map[string][]string
	RegistryDir                  string
	Mode, ResetPolicy, RunSeed   string
	Repeats, BatchSize, Parallel int
	Limit                        *int
	Budget                       protocol.Budget
	TraceText                    bool
	FixedReader                  *FixedReaderOptions
	suiteSHA                     string
	ingestion                    string
	queryTimePolicy              string
	retrievalMetrics             []string
}

func DefaultOptions() Options {
	return Options{Mode: Retrieval, ResetPolicy: FreshPerCase, Repeats: 1, BatchSize: 64, Parallel: 1, Budget: protocol.Budget{TopK: 10, MaxContextChars: 100000}}
}

func ValidateResponse(resp protocol.RetrieveResponse, known map[string]suite.Event, budget protocol.Budget, typed bool) ([]protocol.Item, int, error) {
	return validateResponse(resp, known, budget, typed, false)
}

func validateResponse(resp protocol.RetrieveResponse, known map[string]suite.Event, budget protocol.Budget, typed, allowUnattributed bool) ([]protocol.Item, int, error) {
	bad := func(s string) ([]protocol.Item, int, error) { return nil, 0, &adapter.ContractError{Reason: s} }
	validate := func(items []protocol.Item) error {
		if items == nil {
			return &adapter.ContractError{Reason: "items/tier must be a list"}
		}
		for _, i := range items {
			if i.SourceIDs == nil {
				return &adapter.ContractError{Reason: "source_ids must be a list"}
			}
			if len(i.SourceIDs) == 0 && !allowUnattributed {
				return adapter.ErrUnsupported
			}
			seen := map[string]bool{}
			for _, s := range i.SourceIDs {
				if _, ok := known[s]; !ok {
					return &adapter.ContractError{Reason: "invented/future citation"}
				}
				if seen[s] {
					return &adapter.ContractError{Reason: "repeated citations within an item"}
				}
				seen[s] = true
			}
		}
		return nil
	}
	items := []protocol.Item{}
	chars := 0
	if typed {
		if resp.Tiers == nil || resp.PersonaPrelude == nil {
			return bad("typed_context requires tiers and personaPrelude")
		}
		for _, tier := range [][]protocol.Item{resp.Tiers.PinnedFacts, resp.Tiers.RelevantBeliefs, resp.Tiers.OpenQuestions} {
			if err := validate(tier); err != nil {
				return nil, 0, err
			}
			for _, i := range tier {
				if len(i.SourceIDs) != 1 {
					return nil, 0, adapter.ErrUnsupported
				}
			}
			items = append(items, tier...)
		}
		if len(resp.Tiers.RelevantBeliefs) > budget.TopK {
			return bad("top_k exceeded")
		}
		if (budget.HasMaxBeliefs || budget.MaxBeliefs != 0) && len(items) > budget.MaxBeliefs {
			return bad("maxBeliefs exceeded")
		}
	} else {
		if err := validate(resp.Items); err != nil {
			return nil, 0, err
		}
		items = resp.Items
		if len(items) > budget.TopK {
			return bad("top_k exceeded")
		}
	}
	for _, i := range items {
		chars += utf8.RuneCountInString(i.Text)
	}
	if resp.PersonaPrelude != nil {
		chars += utf8.RuneCountInString(*resp.PersonaPrelude)
	}
	if chars > budget.MaxContextChars {
		return bad("max_context_chars exceeded; no hidden clipping")
	}
	return items, chars, nil
}

type ingestFailure = adapter.IngestError

func runCase(ctx context.Context, a adapter.Adapter, desc protocol.Describe, ep suite.Episode, c suite.Case, repeat int, o Options) result.Case {
	ctx = inputlog.WithCase(ctx, c.ID, ep.ID, c.ClusterID, repeat)
	sample := identity.SampleID(c.ID, repeat)
	id, _ := identity.New(o.RunSeed, sample)
	if o.shared != nil {
		id = o.shared.id
	}
	budget := c.Budget.Resolve(o.Budget)
	if halumemSupported(c) && c.Kind == "qa" {
		budget.TopK = 20
	}
	category := c.Category
	if category == "" {
		category = "unknown"
	}
	r := result.Case{CaseID: c.ID, EpisodeID: ep.ID, SampleID: sample, RepeatIndex: repeat, Category: category, ClusterID: c.ClusterID, Kind: c.Kind, Cutoff: c.Cutoff, Budget: budget, Status: result.Unscored, Metrics: map[string]*float64{}, Failures: []string{}, RetrievedSourceIDs: []string{}, Diagnostics: map[string]int{}, Timings: map[string]float64{}}
	if o.RecordNamespace {
		r.Namespace = id.Namespace()
	}
	if suite.NewKind(c.Kind) && !mabSupported(c) && !beamSupported(c) && !memoraSupported(c) && !halumemSupported(c) && !prefEvalSupported(c) && !convoMemSupported(c) {
		r.Status = result.Unsupported
		r.ErrorType = "unsupported"
		return r
	}
	events := ep.Events[:c.Cutoff]
	var public []protocol.PublicEvent
	if o.shared != nil {
		public = o.shared.public
	} else {
		public = suite.RenderPublicEvents(events, id, o.ingestRender)
	}
	req := suite.PublicRequest(c, id, budget)
	if o.shared != nil {
		req.QueryInvocationID = id.Anon("invocation", sample)
	}
	if o.queryTimePolicy == "reference_only" {
		req.AsOf = ""
	}
	if o.shared == nil {
		r.PublicInputSHA256, _ = suite.Digest(struct {
			Events  []protocol.PublicEvent   `json:"events"`
			Request protocol.RetrieveRequest `json:"request"`
		}{public, req})
	}
	if o.shared != nil {
		r.PublicInputSHA256 = o.shared.digest
	}
	known := map[string]suite.Event{}
	if o.shared == nil {
		for i, e := range events {
			// Reader-only ordering metadata; never added to public adapter events.
			e.Attributes = maps.Clone(e.Attributes)
			if e.Attributes == nil {
				e.Attributes = map[string]any{}
			}
			e.Attributes["memeval_reader_sequence"] = i
			known[public[i].ID] = e
		}
	}
	if o.shared != nil {
		known = o.shared.known
	}
	phase := func(name string, fn func() error) error {
		start := time.Now()
		err := fn()
		r.Timings[name] = float64(time.Since(start).Nanoseconds()) / 1e6
		return err
	}
	reset := false
	err := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		capability := "retrieve"
		if c.Kind == "typed_context" {
			capability = "typed_context"
		}
		if c.Kind == "memory_audit" && halumemSupported(c) {
			capability = "dump"
		}
		if !slices.Contains(desc.Capabilities, capability) {
			if capability == "dump" {
				recordUnsupportedAudit(&r, c)
			}
			return adapter.ErrUnsupported
		}
		ns := id.Namespace()
		if o.shared != nil {
			reset = true
			if !o.shared.timed {
				for k, v := range o.shared.timings {
					r.Timings[k] = v
				}
				o.shared.timed = true
				if checkpointEpisode(&ep) {
					o.shared.timings = map[string]float64{}
				}
			}
			if o.shared.err != nil {
				return o.shared.err
			}
		} else {
			if err := phase("reset_ms", func() error { return a.Reset(ctx, ns) }); err != nil {
				return err
			}
			reset = true
			h := protocol.IngestHandle{}
			if err := phase("ingest_ms", func() error {
				for off := 0; off < len(public); {
					end := min(off+o.BatchSize, len(public))
					if strings.HasPrefix(o.ingestion, "per_chunk:") {
						end = off + 1
					}
					if o.ingestion == "per_session" {
						end = off + 1
						for end < len(public) && public[end].SessionID == public[off].SessionID {
							end++
						}
					}
					if grouped, ok := a.(interface{ NeedsSessionBatching() bool }); ok && grouped.NeedsSessionBatching() {
						for j := off + 1; j < end; j++ {
							if public[j].SessionID != public[off].SessionID {
								end = j
								break
							}
						}
					}
					var err error
					h, err = a.Ingest(ctx, ns, public[off:end])
					if err != nil {
						return err
					}
					if h.Accepted < 0 || h.Filtered < 0 || h.Failed < 0 || h.Accepted+h.Filtered+h.Failed != end-off {
						return &adapter.ContractError{Reason: "ingest counts do not match batch"}
					}
					if h.Failed > 0 {
						return &ingestFailure{}
					}
					off = end
				}
				return nil
			}); err != nil {
				return err
			}
			if err := phase("ready_ms", func() error {
				deadline, _ := ctx.Deadline()
				ready, err := a.Ready(ctx, ns, h, deadline)
				if err != nil {
					return err
				}
				if !ready.Visible {
					return &ingestFailure{}
				}
				for _, s := range ready.Stages {
					if s.Status != "ok" {
						return &ingestFailure{}
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if halumemSupported(c) && c.Kind == "memory_audit" {
			return phase("audit_ms", func() error { return halumemAudit(ctx, a, &r, ep, c, known, o) })
		}
		var resp protocol.RetrieveResponse
		if err := phase("query_ms", func() error { var err error; resp, err = a.Retrieve(ctx, ns, req); return err }); err != nil {
			return err
		}
		validationBudget := budget
		if full, ok := a.(interface{ FullContext() bool }); ok && full.FullContext() {
			validationBudget.TopK = len(events)
			validationBudget.MaxContextChars = 0
			for _, ev := range public {
				validationBudget.MaxContextChars += utf8.RuneCountInString(ev.Content)
			}
		}
		allowUnattributed := o.Mode == FixedReader && o.FixedReader.Input == "items" && readerSupportsItems(o.benchmarkID) && c.Kind != "typed_context"
		items, chars, err := validateResponse(resp, known, validationBudget, c.Kind == "typed_context", allowUnattributed)
		if err != nil {
			return err
		}
		r.ReturnedChars = &chars
		raw := []string{}
		occurrences, untraceable := 0, 0
		for _, i := range items {
			if len(i.SourceIDs) == 0 {
				untraceable++
			}
			raw = append(raw, i.SourceIDs...)
			occurrences += len(i.SourceIDs)
		}
		raw = scoring.OrderedUnique(raw)
		cross := 0
		for _, s := range raw {
			e := known[s]
			r.RetrievedSourceIDs = append(r.RetrievedSourceIDs, e.SourceID)
			if e.Owner != c.User {
				cross++
			}
		}
		r.Diagnostics = map[string]int{"cross_user_source_count": cross, "duplicate_source_occurrences": occurrences - len(raw)}
		if untraceable > 0 {
			r.Diagnostics["untraceable_hits"] = untraceable
			note := "Retrieval metrics unavailable: untraceable items make the reported source subset incomplete; item text used for fixed_reader only"
			r.Note = &note
		} else if c.Kind == "typed_context" {
			convert := func(items []protocol.Item) []string {
				ids := []string{}
				for _, i := range items {
					ids = append(ids, known[i.SourceIDs[0]].SourceID)
				}
				return ids
			}
			out, err := scoring.ScoreTyped(c.Gold.Expect, scoring.TypedResponse{PinnedFacts: convert(resp.Tiers.PinnedFacts), RelevantBeliefs: convert(resp.Tiers.RelevantBeliefs), OpenQuestions: convert(resp.Tiers.OpenQuestions), PersonaPrelude: *resp.PersonaPrelude}, budget.TopK)
			if err != nil {
				return err
			}
			r.Status = out.Status
			r.Passed = out.Passed
			r.Assertions = out.Assertions
			r.AssertionsPassed = out.AssertionsPassed
			r.Failures = out.Failures
			r.Metrics = out.Metrics.Values
			r.EmptyExpected = out.Metrics.EmptyExpected
			r.Note = out.Note
		} else {
			gold := c.Gold.EvidenceIDs
			if convoMemSupported(c) {
				gold = nil
				for _, ref := range c.Gold.EvidenceRefs {
					if ref.Kind == "event" {
						gold = append(gold, ref.Ref)
					}
				}
				gold = scoring.OrderedUnique(gold)
			}
			if c.Gold.EvidenceUnit == "session" && c.Gold.EvidenceSessionIDs != nil {
				gold = c.Gold.EvidenceSessionIDs
			}
			pred := slices.Clone(r.RetrievedSourceIDs)
			if c.Gold.EvidenceUnit == "session" {
				pred = []string{}
				for _, s := range raw {
					pred = append(pred, known[s].Session)
				}
				pred = scoring.OrderedUnique(pred)
			}
			if ((!c.Gold.Abstain || convoMemSupported(c)) && len(gold) > 0) || (len(gold) == 0 && c.Gold.Exhaustive) {
				m, err := scoring.Retrieval(pred, gold, c.Gold.Exhaustive, budget.TopK)
				if err != nil {
					return err
				}
				r.Status = result.Scored
				r.Metrics = m.Values
				r.EmptyExpected = m.EmptyExpected
				note := "Source-unit retrieval diagnostics; citations are adapter-reported, not entailed-text verification"
				r.Note = &note
			} else {
				note := "No positive source labels; retrieval has no ground-truth score here"
				r.Note = &note
			}
		}
		if untraceable == 0 && !c.Gold.Abstain && slices.Contains(o.retrievalMetrics, "longmemeval_official") {
			r.Status = result.Scored
			for k, v := range scoring.LongMemEval(events, r.RetrievedSourceIDs, c.Gold.EvidenceIDs, c.Gold.EvidenceSessionIDs) {
				r.Metrics[k] = v
			}
		}
		if o.Mode == FixedReader {
			if err := phase("answer_ms", func() error { return answerCase(ctx, &r, c, items, known, o) }); err != nil {
				return err
			}
		}
		if o.TraceText {
			r.Query = &req.Query
			r.Response = &resp
		}
		return nil
	}()
	if err != nil {
		r.Metrics = map[string]*float64{}
		r.EmptyExpected = nil
		r.Passed = nil
		r.Status = result.Error
		r.ErrorType = fmt.Sprintf("%T", err)
		var typed interface{ ErrorType() string }
		if errors.As(err, &typed) {
			r.ErrorType = typed.ErrorType()
		}
		var detailed interface{ SafeDetail() string }
		if errors.As(err, &detailed) {
			r.ErrorDetail = detailed.SafeDetail()
		}
		note := "Operation failed; error_detail is SUT-reported error text or a stderr tail; credentials are redacted, dataset text is not redacted; full streams are retained in logs/ when episode logging is enabled (subject to the file cap)"
		r.Note = &note
		var contract *adapter.ContractError
		var failed *ingestFailure
		switch {
		case errors.Is(err, adapter.ErrUnsupported):
			r.Status = result.Unsupported
			r.ErrorType = "UnsupportedCapability"
			note = "Requested capability or evidence provenance is unsupported"
			var diagnostic interface{ UnsupportedNote() string }
			if errors.As(err, &diagnostic) && diagnostic.UnsupportedNote() != "" {
				note = diagnostic.UnsupportedNote()
			}
		case errors.As(err, &contract):
			r.Status = result.ContractError
			r.ErrorType = "ContractError"
		case errors.As(err, &failed):
			r.Status = result.IngestFailed
			r.ErrorType = "IngestFailed"
		}
	}
	if d, ok := a.(adapter.DiagnosticSource); ok && reset {
		for k, v := range d.Diagnostics() {
			if o.shared != nil {
				delta := v - o.shared.diagnostics[k]
				o.shared.diagnostics[k] = v
				r.Diagnostics[k] = max(r.Diagnostics[k], delta)
			} else {
				r.Diagnostics[k] = max(r.Diagnostics[k], v)
			}
		}
	}
	return r
}

func Run(ctx context.Context, s *suite.Suite, a adapter.Adapter, o Options) (report *result.Report, err error) {
	defer func() {
		if closeErr := a.Close(context.WithoutCancel(ctx)); err == nil && closeErr != nil {
			err = fmt.Errorf("adapter cleanup failed (%T)", closeErr)
		}
	}()
	if o.Mode != Retrieval && o.Mode != FixedReader {
		if o.Mode == FixedReader || o.Mode == NativeAnswer {
			return nil, fmt.Errorf("%s not implemented in M0", o.Mode)
		}
		return nil, fmt.Errorf("unknown mode")
	}
	if full, ok := a.(interface{ FullContext() bool }); ok && full.FullContext() {
		if o.Mode != FixedReader {
			return nil, fmt.Errorf("fullcontext requires fixed_reader mode")
		}
		if o.FixedReader != nil && o.FixedReader.Input == "items" {
			return nil, fmt.Errorf("fullcontext requires sources input; items is ambiguous")
		}
	}
	if o.ResetPolicy != FreshPerCase && o.ResetPolicy != IncrementalPerEpisode {
		return nil, fmt.Errorf("reset policy %s not implemented in M0", o.ResetPolicy)
	}
	if o.Repeats < 1 || o.BatchSize < 1 || o.Parallel < 1 || o.Parallel > 16 || o.Limit != nil && *o.Limit < 1 {
		return nil, fmt.Errorf("repeats/batch-size/limit must be positive; parallel must be between 1 and 16")
	}
	if o.Parallel > 1 && o.NewAdapter == nil {
		return nil, fmt.Errorf("parallel > 1 requires an independent adapter factory")
	}
	if o.Budget.TopK < 0 || o.Budget.MaxContextChars < 0 || o.Budget.MaxBeliefs < 0 {
		return nil, fmt.Errorf("negative budget")
	}
	counts, err := suite.Validate(ctx, s, suite.ValidateOptions{SkipLeakScan: true})
	if err != nil {
		return nil, err
	}
	if err := supportedExecution(s); err != nil {
		return nil, err
	}
	if s.SchemaVersion == "3.0" && len(s.Protocol.ResetPolicyAllowed) > 0 && !slices.Contains(s.Protocol.ResetPolicyAllowed, o.ResetPolicy) {
		return nil, fmt.Errorf("reset policy %s is not allowed by suite", o.ResetPolicy)
	}
	if o.ResetPolicy == IncrementalPerEpisode {
		if err = s.Iter(ctx, func(ep *suite.Episode) error {
			return validateIncremental(ep, s.Protocol.Ingestion)
		}); err != nil {
			return nil, err
		}
	}
	if o.ResetPolicy != IncrementalPerEpisode {
		if e := s.Iter(ctx, func(ep *suite.Episode) error {
			if checkpointEpisode(ep) {
				return fmt.Errorf("after_session_k requires incremental_per_episode")
			}
			return nil
		}); e != nil {
			return nil, e
		}
	}
	binding, err := bindBenchmark(s, o.RegistryDir)
	if err != nil {
		return nil, err
	}
	if o.Mode == FixedReader {
		o.benchmarkID, err = ValidateReaderBinding(s, o.RegistryDir)
		if err != nil {
			return nil, err
		}
		if !readerSupportsItems(o.benchmarkID) && o.FixedReader != nil && o.FixedReader.Input == "items" {
			return nil, fmt.Errorf("%s reader requires sources input", o.benchmarkID)
		}
		if err = o.FixedReader.validate(o.benchmarkID == "convomem" || o.benchmarkID == "prefeval" && NeedsPrefEvalJudge(s) || o.benchmarkID == "longmemeval" || o.benchmarkID == "beam" || o.benchmarkID == "memora" || o.benchmarkID == "halumem" || o.benchmarkID == "memoryagentbench" && needsMABJudge(s)); err != nil {
			return nil, err
		}
	}
	o.ingestRender = s.Protocol.SpeakerMapping["ingest_render"]
	if v, ok := s.Provenance["episode_speakers"]; ok {
		b, _ := json.Marshal(v)
		if err = json.Unmarshal(b, &o.episodeSpeakers); err != nil {
			return nil, err
		}
	}
	o.ingestion = s.Protocol.Ingestion
	o.queryTimePolicy = s.Protocol.QueryTimePolicy
	o.retrievalMetrics = s.Protocol.RetrievalMetrics
	o.RunSeed, err = identity.Seed(o.RunSeed)
	if err != nil {
		return nil, err
	}
	if err := VerifyResume(ctx, s, o); err != nil {
		return nil, err
	}
	runSeed, err := identity.Seed("")
	if err != nil {
		return nil, err
	}
	runID, _ := identity.New(runSeed, "")
	invocationID := runID.Anon("invocation", "run")
	if o.Resume != nil {
		invocationID = o.Resume.Original.RunID
	}
	if b, ok := a.(interface{ BeginRun(string) }); ok {
		b.BeginRun(invocationID)
	}
	desc, err := a.Describe(ctx)
	if err != nil {
		return nil, fmt.Errorf("adapter describe failed (%T)", err)
	}
	if o.Resume != nil && !o.Resume.AllowSUTChange && desc.Name != o.Resume.Original.SUT.Describe.Name {
		return nil, fmt.Errorf("resume SUT mismatch")
	}
	if desc.Capabilities == nil {
		return nil, fmt.Errorf("describe requires capabilities")
	}
	if o.ResetPolicy == IncrementalPerEpisode && !desc.ReadOnlyQueries {
		return nil, fmt.Errorf("incremental_per_episode requires describe.read_only_queries=true")
	}
	if full, ok := a.(interface{ FullContext() bool }); ok {
		o.fullContext = full.FullContext()
	}
	sha, err := suite.SHA256(s)
	if err != nil {
		return nil, err
	}
	if o.Mode == FixedReader && o.benchmarkID == "memoryagentbench" {
		if err := prepareMAB(s, &o); err != nil {
			return nil, err
		}
	}
	o.suiteSHA = sha
	start := time.Now()
	report = &result.Report{Parallel: o.Parallel, FrameworkVersion: "0.4.0-m2a", RunID: invocationID, RunSeed: o.RunSeed, StartedAtUTC: start.UTC().Format(time.RFC3339Nano), SuiteID: s.SuiteID, SuiteSHA256: sha, Source: s.Provenance, SourceCounts: result.Counts{Episodes: counts.Episodes, Events: counts.Events, Cases: counts.Cases}, SourceLimitations: s.Limitations, Mode: o.Mode, Repeats: o.Repeats, Cases: []result.Case{}, ByCategory: map[string]result.Summary{}, Warnings: []string{"Source-specific diagnostic; not an official upstream leaderboard score", InProcessWarning, "Repeated samples do not add independent source clusters"}}
	if o.Resume != nil {
		report.RunID, report.RunSeed, report.StartedAtUTC = o.Resume.Original.RunID, o.Resume.Original.RunSeed, o.Resume.Original.StartedAtUTC
	}
	if o.Mode == FixedReader {
		if c, ok := o.FixedReader.Client.(interface{ Start(string) error }); ok {
			if err := c.Start(report.RunID); err != nil {
				return nil, err
			}
		}
	}
	report.SUT = result.SUT{Name: desc.Name, Variant: desc.Name, Describe: desc, Fingerprint: map[string]result.FingerprintValue{}}
	for _, k := range []string{"model", "embedding", "prompt_hash", "sampling", "retry"} {
		report.SUT.Fingerprint[k] = result.FingerprintValue{Status: "unavailable", Value: nil}
	}
	report.Protocol = result.Protocol{Version: "paeval-v2", Budget: o.Budget, BatchSize: o.BatchSize, Ingestion: s.Protocol.Ingestion, ResetPolicy: o.ResetPolicy, Scoring: "deterministic-source-assertions-and-diagnostics-v2", CaseLimit: o.Limit, GoldHidden: true, KUnit: "items→events(dedup first-seen)", ChangesFromV1: []string{"Exhaustive empty evidence sets in retrieval/qa are scored with empty_result_correct", "Explicit contract_error and ingest_failed statuses", "Deterministic per-sample pseudonyms from run_seed"}, TraceText: o.TraceText}
	report.Protocol.Benchmark = binding
	if binding != nil && binding.ProtocolID == "halumem" {
		report.Limitations = append(report.Limitations, halumemCostNotice())
	}
	if binding != nil && binding.ProtocolID == "memoryagentbench" && binding.Variant == "longmemeval-overlap" {
		report.SourceLimitations = append(slices.Clone(report.SourceLimitations), "Raw upstream MAB questions already contain a Current Date wrapper; preserved verbatim, with no additional date injection")
	}
	report.Protocol.IngestRender = o.ingestRender
	if o.ResetPolicy == IncrementalPerEpisode {
		report.Protocol.Changes = append(report.Protocol.Changes, "Reset/ingest/ready once per episode and repeat; read_only_queries declaration required; public_input_sha256 hashes episode public history and is shared by its cases; startup timings charged once")
	}
	if binding == nil {
		report.Warnings = append(report.Warnings, "no benchmark protocol declaration bound")
	}
	if s.License != nil {
		if s.License.Noncommercial {
			report.SourceLimitations = append(append([]string{}, report.SourceLimitations...), "Dataset license restricts commercial use")
		}
		if s.License.NoDerivatives {
			report.SourceLimitations = append(append([]string{}, report.SourceLimitations...), "Dataset license prohibits redistribution of normalized derivatives")
		}
	}
	report.Protocol.QueryTimePolicy = s.Protocol.QueryTimePolicy
	report.Protocol.RetrievalMetrics = slices.Clone(s.Protocol.RetrievalMetrics)
	if o.queryTimePolicy == "reference_only" {
		report.Protocol.Changes = append(report.Protocol.Changes, "query_time retained as evaluator metadata only; no as_of filtering requested")
	}
	report.Isolation = result.Isolation{FDHygiene: procexec.Policy, Tier: "C", Backend: "in-process", NetworkMode: "inherit", Host: host(ctx)}
	report.Environment = environment()
	if full, ok := a.(interface{ FullContext() bool }); ok && full.FullContext() {
		report.Protocol.KUnit = "all source events; reader applies character budget"
		unit := "complete sessions"
		if o.benchmarkID == "memoryagentbench" {
			unit = "earliest complete blocks"
		}
		report.Protocol.Changes = append(report.Protocol.Changes, "fullcontext returns all events without top_k/character filtering; character budget is applied to "+unit+" by the reader")
	}
	rows, interruption, scheduleErr := scheduleEpisodes(ctx, s, a, desc, o, invocationID)
	report.Interruption = interruption
	for _, row := range rows {
		report.Cases = append(report.Cases, row.cases...)
		for _, warning := range row.warnings {
			report.Warnings = appendUnique(report.Warnings, warning)
		}
	}
	if warning := ParallelWarning(o.Parallel, runtime.NumCPU()); warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}
	report.Summary = scoring.Aggregate(report.Cases)
	groups := map[string][]result.Case{}
	for _, r := range report.Cases {
		groups[r.Category] = append(groups[r.Category], r)
	}
	for k, rs := range groups {
		report.ByCategory[k] = scoring.Aggregate(rs)
	}
	report.Scores.MemoryOnly = scoring.MemoryOnly(report.Summary.Metrics, o.retrievalMetrics)
	if o.Mode == FixedReader {
		if err := fixedMetadata(report, s, o); err != nil {
			return nil, err
		}
		decisionJudgeMetadata(report, o)
		readerOutputMetadata(report, o)
	}
	if o.Repeats > 1 {
		report.FrameworkVersion = "0.6.0-m3"
		report.RepeatAnalysis = stats.Repeats(report.Cases, o.Repeats)
	}
	if recorder := inputlog.From(ctx); recorder != nil {
		if report.Protocol.Judge != nil {
			report.Protocol.Judge.Templates = recorder.Templates()
		}
		report.JudgeInputs, err = recorder.Snapshot()
		if err != nil {
			return nil, err
		}
	}
	report.WallSeconds = time.Since(start).Seconds()
	return report, scheduleErr
}
