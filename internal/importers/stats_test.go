package importers

import (
	"memeval/internal/suite"
	"testing"
)

func TestCountsMustMatchValidation(t *testing.T) {
	s := &suite.Suite{SchemaVersion: "2.0", SuiteID: "fixture", Provenance: map[string]any{"source_url": "fixture://local"}, Episodes: []suite.Episode{{ID: "episode", Cases: []suite.Case{{ID: "case", Kind: "qa", Category: "category", QuestionType: "question-type"}}}}}
	stats, err := Complete(s, Stats{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = Check(s, stats); err != nil {
		t.Fatal(err)
	}
	stats.Counts.Cases++
	if err = Check(s, stats); err == nil {
		t.Fatal("incorrect import counts accepted")
	}
}
