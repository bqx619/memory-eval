package result

import (
	"memeval/internal/reader/locomo"
	"memeval/internal/scoring/judge/inputlog"
)

type QA struct {
	ReaderOutput     *ReaderOutput         `json:"reader_output,omitempty"`
	LoCoMo           *locomo.QuestionAudit `json:"locomo,omitempty"`
	JudgeDetails     []any                 `json:"judge_details,omitempty"`
	Judge            any                   `json:"judge,omitempty"`
	QuestionType     string                `json:"question_type"`
	Hypothesis       *string               `json:"hypothesis"`
	JudgeLabel       *bool                 `json:"judge_label"`
	SUTAbstain       *bool                 `json:"sut_abstain"`
	ReaderAbstain    bool                  `json:"reader_abstain"`
	Truncated        bool                  `json:"truncated"`
	DroppedMessages  int                   `json:"dropped_messages,omitempty"`
	DroppedSessions  int                   `json:"dropped_sessions"`
	ReaderRequestSHA string                `json:"reader_request_sha"`
	JudgeRequestSHA  string                `json:"judge_request_sha"`
	Deviations       []string              `json:"deviations"`
}

type ReaderOutput struct {
	OfficialMaxTokens  *int   `json:"official_max_tokens"`
	RequestedMaxTokens *int   `json:"requested_max_tokens"`
	EffectiveMaxTokens *int   `json:"effective_max_tokens"`
	Reasoning          bool   `json:"reasoning"`
	FinishReason       string `json:"finish_reason"`
	CompletionTokens   int    `json:"completion_tokens"`
	ReasoningTokens    int    `json:"reasoning_tokens"`
	Empty              bool   `json:"empty"`
}
type Accuracy struct {
	N        int      `json:"n"`
	Correct  int      `json:"correct"`
	Accuracy *float64 `json:"accuracy"`
}
type JudgeInfo struct {
	Kind        string                       `json:"kind"`
	Provider    string                       `json:"provider"`
	Model       string                       `json:"model"`
	Cutoff      float64                      `json:"cutoff"`
	MultiValued string                       `json:"multi_valued"`
	Templates   map[string]inputlog.Template `json:"templates"`
}
type ReaderInfo struct {
	Model     string `json:"model"`
	PromptID  string `json:"prompt_id"`
	InputMode string `json:"input_mode"`
}
type QuestionSet struct {
	Total     int  `json:"total"`
	Evaluated int  `json:"evaluated"`
	Complete  bool `json:"complete"`
}
type Official struct {
	JudgeCompatible *bool                          `json:"judge_compatible,omitempty"`
	Invalid         int                            `json:"invalid,omitempty"`
	Metrics         map[string]*float64            `json:"metrics,omitempty"`
	ByTypeMetrics   map[string]map[string]*float64 `json:"by_type_metrics,omitempty"`
	Benchmark       string                         `json:"benchmark"`
	Judge           *JudgeInfo                     `json:"judge"`
	Reader          ReaderInfo                     `json:"reader"`
	QuestionSet     QuestionSet                    `json:"question_set"`
	Score           *float64                       `json:"score"`
	ByType          map[string]Accuracy            `json:"by_type"`
	Deviations      []string                       `json:"deviations"`
	Fidelity        string                         `json:"fidelity"`
	Errors          int                            `json:"errors"`
	Unsupported     int                            `json:"unsupported"`
	Judged          int                            `json:"judged"`
}
