// Package result owns evaluator artifacts, never passed to adapters.
package result

import (
	"encoding/json"
	"fmt"
	"memeval/internal/episodelog"
	"memeval/internal/protocol"
	"memeval/internal/scoring/judge/inputlog"
)

type Status string

const (
	Scored        Status = "scored"
	Invalid       Status = "invalid"
	Unscored      Status = "unscored"
	Unsupported   Status = "unsupported"
	ContractError Status = "contract_error"
	Error         Status = "error"
	IngestFailed  Status = "ingest_failed"
)

type AnswerParse struct {
	RawFinal     string   `json:"raw_final"`
	OptionsFound []string `json:"options_found"`
}
type Case struct {
	Namespace       string              `json:"namespace,omitempty"`
	PreservedJSON   json.RawMessage     `json:"-"`
	PreviousStatus  Status              `json:"previous_status,omitempty"`
	Audit           json.RawMessage     `json:"audit,omitempty"`
	AnswerParse     *AnswerParse        `json:"answer_parse,omitempty"`
	PredictionInput *PredictionInput    `json:"prediction_input,omitempty"`
	Decision        map[string]any      `json:"decision,omitempty"`
	QA              *QA                 `json:"qa,omitempty"`
	CaseID          string              `json:"case_id"`
	EpisodeID       string              `json:"episode_id"`
	SampleID        string              `json:"sample_id"`
	RepeatIndex     int                 `json:"repeat_index"`
	Category        string              `json:"category"`
	ClusterID       string              `json:"cluster_id"`
	Kind            string              `json:"kind"`
	Cutoff          int                 `json:"cutoff"`
	Budget          protocol.Budget     `json:"budget"`
	Status          Status              `json:"status"`
	Passed          *bool               `json:"passed"`
	Metrics         map[string]*float64 `json:"metrics"`
	// Python's empty_expected is a boolean diagnostic, not a numeric metric.
	EmptyExpected      *bool                      `json:"-"`
	Failures           []string                   `json:"failures"`
	Assertions         int                        `json:"assertions"`
	AssertionsPassed   int                        `json:"assertions_passed"`
	RetrievedSourceIDs []string                   `json:"retrieved_source_ids"`
	ReturnedChars      *int                       `json:"returned_chars"`
	Diagnostics        map[string]int             `json:"diagnostics"`
	Timings            map[string]float64         `json:"timings"`
	ErrorType          string                     `json:"error_type"`
	ErrorDetail        string                     `json:"error_detail,omitempty"`
	Note               *string                    `json:"note"`
	PublicInputSHA256  string                     `json:"public_input_sha256"`
	Query              *string                    `json:"query,omitempty"`
	Response           *protocol.RetrieveResponse `json:"response,omitempty"`
}

func (c Case) MarshalJSON() ([]byte, error) {
	if len(c.PreservedJSON) > 0 {
		return c.PreservedJSON, nil
	}
	type plain Case
	metrics := map[string]any{}
	for k, v := range c.Metrics {
		metrics[k] = v
	}
	if c.EmptyExpected != nil {
		metrics["empty_expected"] = *c.EmptyExpected
	}
	return json.Marshal(struct {
		plain
		Metrics map[string]any `json:"metrics"`
	}{plain(c), metrics})
}
func (c *Case) UnmarshalJSON(data []byte) error {
	type plain Case
	var wire struct {
		*plain
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	wire.plain = (*plain)(c)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.Metrics = map[string]*float64{}
	c.EmptyExpected = nil
	for k, v := range wire.Metrics {
		if k == "empty_expected" {
			var b bool
			if err := json.Unmarshal(v, &b); err != nil {
				return err
			}
			c.EmptyExpected = &b
		} else {
			var f *float64
			if err := json.Unmarshal(v, &f); err != nil {
				return fmt.Errorf("metric %s: %w", k, err)
			}
			c.Metrics[k] = f
		}
	}
	return nil
}

type Metric struct {
	Mean *float64 `json:"mean"`
	N    int      `json:"n"`
}
type Timing struct {
	P50   *float64 `json:"p50"`
	P95   *float64 `json:"p95"`
	Total float64  `json:"total"`
	N     int      `json:"n"`
}
type Percentiles struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
}
type Billing struct {
	Evaluator          any            `json:"evaluator"`
	EvaluatorByPurpose map[string]any `json:"evaluator_by_purpose,omitempty"`
	SUT                any            `json:"sut"`
}
type Summary struct {
	Invalid             int               `json:"invalid"`
	ReaderEmptyAnswers  int               `json:"reader_empty_answers"`
	UntraceableHits     int               `json:"untraceable_hits"`
	UnenforcedIsolation bool              `json:"unenforced_isolation,omitempty"`
	Planned             int               `json:"planned"`
	StatusCounts        map[Status]int    `json:"status_counts"`
	NWithPassJudgment   int               `json:"n_with_pass_judgment"`
	Passed              int               `json:"passed"`
	PassRateOnScored    *float64          `json:"pass_rate_on_scored"`
	ScoringCoverage     *float64          `json:"scoring_coverage"`
	ExecutionErrors     int               `json:"execution_errors"`
	Unsupported         int               `json:"unsupported"`
	Metrics             map[string]Metric `json:"metrics"`
	Timings             map[string]Timing `json:"timings"`
	ReturnedChars       Percentiles       `json:"returned_chars"`
	Billing             Billing           `json:"billing"`
}
type BenchmarkRef struct {
	ProtocolID string `json:"protocol_id"`
	Version    string `json:"version"`
	Variant    string `json:"variant"`
	Ref        string `json:"ref"`
	SHA256     string `json:"sha256"`
	Source     string `json:"source"`
}
type Protocol struct {
	IngestRender     string          `json:"ingest_render,omitempty"`
	Benchmark        *BenchmarkRef   `json:"benchmark,omitempty"`
	Judge            *JudgeInfo      `json:"judge,omitempty"`
	ReaderInput      string          `json:"reader_input,omitempty"`
	QueryTimePolicy  string          `json:"query_time_policy,omitempty"`
	RetrievalMetrics []string        `json:"retrieval_metrics,omitempty"`
	Changes          []string        `json:"changes,omitempty"`
	Version          string          `json:"version"`
	Budget           protocol.Budget `json:"budget"`
	BatchSize        int             `json:"batch_size"`
	Ingestion        string          `json:"ingestion"`
	ResetPolicy      string          `json:"reset_policy"`
	Scoring          string          `json:"scoring"`
	CaseLimit        *int            `json:"case_limit"`
	GoldHidden       bool            `json:"gold_hidden_from_adapter"`
	KUnit            string          `json:"k_unit"`
	ChangesFromV1    []string        `json:"changes_from_v1"`
	TraceText        bool            `json:"trace_text"`
}
type FingerprintValue struct {
	Status string `json:"status"`
	Value  any    `json:"value"`
}
type SUT struct {
	SourceSnapshotSHA256 *string                     `json:"source_snapshot_sha256,omitempty"`
	LLM                  map[string]any              `json:"llm,omitempty"`
	EnvLockSHA256        string                      `json:"env_lock_sha256,omitempty"`
	Declared             map[string]any              `json:"declared,omitempty"`
	ManifestSHA256       string                      `json:"manifest_sha256,omitempty"`
	Lock                 any                         `json:"lock,omitempty"`
	Name                 string                      `json:"name"`
	Variant              string                      `json:"variant"`
	Describe             protocol.Describe           `json:"describe"`
	Fingerprint          map[string]FingerprintValue `json:"fingerprint"`
}
type Host struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kernel string `json:"kernel"`
}
type Isolation struct {
	EgressAllowlist      []string       `json:"egress_allowlist"`
	Container            any            `json:"container,omitempty"`
	HostExecutables      []string       `json:"host_executables"`
	ProcessInfo          map[string]any `json:"process_info,omitempty"`
	Signal               string         `json:"signal,omitempty"`
	AllowShell           bool           `json:"allow_shell"`
	ProcessIPC           any            `json:"process_ipc,omitempty"`
	VMBoundary           any            `json:"vm_boundary,omitempty"`
	GuestProcess         any            `json:"guest_process,omitempty"`
	Sidecars             any            `json:"sidecars,omitempty"`
	FDHygiene            string         `json:"fd_hygiene"`
	FileReadEnforced     bool           `json:"file_read_enforced"`
	FileWriteEnforced    bool           `json:"file_write_enforced"`
	DenyReadEnforced     bool           `json:"deny_read_enforced"`
	ProcessTreeEnforced  bool           `json:"process_tree_enforced"`
	Caps                 any            `json:"caps,omitempty"`
	EffectiveConfig      any            `json:"effective_config,omitempty"`
	Reason               string         `json:"reason,omitempty"`
	Tier                 string         `json:"tier"`
	Backend              string         `json:"backend"`
	NetworkMode          string         `json:"network_mode"`
	NetworkEnforced      bool           `json:"network_enforced"`
	SandboxProfileSHA256 *string        `json:"sandbox_profile_sha256"`
	Selftest             any            `json:"selftest"`
	Host                 Host           `json:"host"`
}
type Environment struct {
	GoVersion  string `json:"go_version"`
	Platform   string `json:"platform"`
	CodeSHA256 string `json:"code_sha256"`
}
type Counts struct {
	Episodes int `json:"episodes"`
	Events   int `json:"events"`
	Cases    int `json:"cases"`
}
type Scores struct {
	Official   any            `json:"official"`
	MemoryOnly map[string]any `json:"memory_only"`
}

// AutoResume records a committed automatic retry round; counts are case×repeat samples.
type AutoResume struct {
	Auto           bool    `json:"auto"`
	Round          int     `json:"round"`
	Reason         string  `json:"reason"`
	WaitedSeconds  float64 `json:"waited_s"`
	CasesRetried   int     `json:"cases_retried"`
	CasesRecovered int     `json:"cases_recovered"`
}

type SUTIdentity struct {
	ManifestSHA256       string  `json:"manifest_sha256"`
	EnvLockSHA256        string  `json:"env_lock_sha256"`
	Variant              string  `json:"variant"`
	SourceSnapshotSHA256 *string `json:"source_snapshot_sha256"`
}
type SUTChange struct {
	Previous SUTIdentity `json:"previous"`
	Current  SUTIdentity `json:"current"`
}
type ResumeAttempt struct {
	*AutoResume
	Cases        *int       `json:"cases"`
	SUTChange    *SUTChange `json:"sut_change,omitempty"`
	ResumedAtUTC string     `json:"resumed_at_utc"`
	RedoneCases  int        `json:"redone_cases"`
	Statuses     []Status   `json:"statuses"`
	HistoryPath  string     `json:"history_path"`
	Billing      any        `json:"billing_evaluator"`
	Reader       any        `json:"reader"`
	EvaluatorLLM any        `json:"evaluator_llm"`
}

type Interruption struct {
	Reason      string `json:"reason"`
	Unscheduled int    `json:"unscheduled_samples"`
}
type Report struct {
	Parallel          int                 `json:"parallel"`
	Interruption      *Interruption       `json:"interruption,omitempty"`
	DataFlow          *DataFlow           `json:"data_flow,omitempty"`
	Logs              *episodelog.Summary `json:"logs,omitempty"`
	ResumeCount       int                 `json:"resume_count"`
	ResumeHistory     []ResumeAttempt     `json:"resume_history,omitempty"`
	JudgeInputs       *inputlog.Artifact  `json:"judge_inputs,omitempty"`
	Limitations       []string            `json:"limitations,omitempty"`
	RepeatAnalysis    *RepeatSummary      `json:"-"`
	FrameworkVersion  string              `json:"framework_version"`
	RunID             string              `json:"run_id"`
	RunSeed           string              `json:"run_seed"`
	StartedAtUTC      string              `json:"started_at_utc"`
	WallSeconds       float64             `json:"wall_seconds"`
	SuiteID           string              `json:"suite_id"`
	SuiteSHA256       string              `json:"suite_sha256"`
	Source            map[string]any      `json:"source"`
	SourceCounts      Counts              `json:"source_counts"`
	SourceLimitations []string            `json:"source_limitations"`
	Mode              string              `json:"mode"`
	SUT               SUT                 `json:"sut"`
	Reader            any                 `json:"reader"`
	Protocol          Protocol            `json:"protocol"`
	Isolation         Isolation           `json:"isolation"`
	EvaluatorLLM      any                 `json:"evaluator_llm"`
	Billing           Billing             `json:"billing"`
	Environment       Environment         `json:"environment"`
	Summary           Summary             `json:"summary"`
	ByCategory        map[string]Summary  `json:"by_category"`
	Scores            Scores              `json:"scores"`
	Repeats           int                 `json:"repeats"`
	Cases             []Case              `json:"cases"`
	Warnings          []string            `json:"warnings"`
}

// ExcludedByStatus counts case×repeat samples, separately from ExcludedCases.
// Both counts a status once per sample; different allowed statuses on the two
// sides each get an entry, so its status counts can overlap.
type ExcludedByStatus struct {
	Left  map[Status]int `json:"left"`
	Right map[Status]int `json:"right"`
	Both  map[Status]int `json:"both"`
}

type Comparison struct {
	PairBy              string            `json:"pair_by,omitempty"`
	ProtocolDifferences []string          `json:"protocol_differences,omitempty"`
	LeftRepeats         *RepeatSummary    `json:"left_repeats,omitempty"`
	RightRepeats        *RepeatSummary    `json:"right_repeats,omitempty"`
	Left                string            `json:"left"`
	Right               string            `json:"right"`
	Metric              string            `json:"metric"`
	Delta               float64           `json:"delta_right_minus_left"`
	PairedCases         int               `json:"paired_cases"`
	PairedSamples       int               `json:"paired_samples"`
	Clusters            int               `json:"clusters"`
	ExcludedCases       []string          `json:"excluded_cases"`
	ExcludedByStatus    *ExcludedByStatus `json:"excluded_by_status,omitempty"`
	CI95                []float64         `json:"ci95"`
	Note                string            `json:"note"`
	Bootstrap           string            `json:"bootstrap"`
	Draws               int               `json:"draws"`
	Seed                uint64            `json:"seed"`
	ClusterOrder        []string          `json:"cluster_order"`
	ResampleIndices     [][]int           `json:"-"`
}

// PredictionInput is evaluator-only metadata for official prediction exports.
type PredictionInput struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Category int    `json:"category"`
}
