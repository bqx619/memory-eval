// Package importers defines deterministic, shared import diagnostics.
package importers

import (
	"context"
	"fmt"
	"memeval/internal/suite"
	"reflect"
)

// LongMemEvalStats preserves the original serialized diagnostic, including field
// order, embedded in v2 limitations. Do not serialize the expanded Stats there.
type LongMemEvalStats struct {
	RepeatedSessionOccurrences int            `json:"repeated_session_occurrences"`
	Questions                  int            `json:"questions"`
	Abstentions                int            `json:"abstentions"`
	QuestionTypes              map[string]int `json:"question_types"`
	Sessions                   int            `json:"sessions"`
	Events                     int            `json:"events"`
	SessionTimeFailures        int            `json:"session_time_parse_failures"`
	QuestionTimeFailures       int            `json:"question_time_parse_failures"`
}
type Stats struct {
	Diagnostics map[string]any `json:"diagnostics,omitempty"`
	LongMemEvalStats
	Converter         string         `json:"converter"`
	InputSHA256       string         `json:"input_sha256"`
	SchemaVersion     string         `json:"schema_version"`
	Counts            suite.Counts   `json:"counts"`
	ByKind            map[string]int `json:"by_kind"`
	ByCategory        map[string]int `json:"by_category"`
	ByQuestionType    map[string]int `json:"by_question_type"`
	TimeParseFailures int            `json:"time_parse_failures"`
	Skipped           []Skipped      `json:"skipped"`
	UpstreamScale     UpstreamScale  `json:"upstream_scale"`
	Limitations       []string       `json:"limitations"`
}
type Skipped struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}
type UpstreamScale struct {
	Declared    map[string]any `json:"declared"`
	Observed    map[string]any `json:"observed"`
	Explanation string         `json:"explanation"`
}

func Complete(s *suite.Suite, stats Stats, converter string) (Stats, error) {
	counts, err := suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true})
	if err != nil {
		return stats, err
	}
	stats.Converter = converter
	stats.SchemaVersion = s.SchemaVersion
	stats.InputSHA256, _ = s.Provenance["local_input_sha256"].(string)
	stats.Counts = counts
	stats.ByKind = counts.ByKind
	stats.ByCategory = map[string]int{}
	stats.ByQuestionType = map[string]int{}
	err = s.Iter(context.Background(), func(ep *suite.Episode) error {
		for _, c := range ep.Cases {
			stats.ByCategory[c.Category]++
			if c.QuestionType != "" {
				stats.ByQuestionType[c.QuestionType]++
			}
		}
		return nil
	})
	if err != nil {
		return stats, err
	}
	stats.TimeParseFailures = stats.SessionTimeFailures + stats.QuestionTimeFailures
	if stats.Skipped == nil {
		stats.Skipped = []Skipped{}
	}
	stats.Limitations = append([]string{}, s.Limitations...)
	if stats.UpstreamScale.Declared == nil {
		stats.UpstreamScale.Declared = map[string]any{}
	}
	stats.UpstreamScale.Observed = map[string]any{"episodes": counts.Episodes, "events": counts.Events, "cases": counts.Cases}
	return stats, nil
}
func Check(s *suite.Suite, stats Stats) error {
	counts, err := suite.Validate(context.Background(), s, suite.ValidateOptions{SkipLeakScan: true})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(counts, stats.Counts) {
		return fmt.Errorf("import statistics counts differ from suite.Validate")
	}
	return nil
}
