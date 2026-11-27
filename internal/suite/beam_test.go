package suite

import (
	"testing"
)

func TestNumericEpisodeLeakScanAndAsWritten(t *testing.T) {
	s := minimalV3("rubric")
	s.Episodes[0].ID = "1"
	s.Episodes[0].Cases[0].Query = "What happened on day 1?"
	s.Episodes[0].Cases[0].Gold.Rubric.Items[0].Polarity = "as_written"
	if _, e := Validate(t.Context(), s, ValidateOptions{SkipLeakScan: true}); e != nil {
		t.Fatal(e)
	}
	scan, e := LeakScan(s)
	if e != nil || len(scan.Problems) > 0 {
		t.Fatal(scan, e)
	}
	s.Episodes[0].Cases[0].Query = "private-source-identifier"
	scan, e = LeakScan(s)
	if e != nil || len(scan.Problems) == 0 {
		t.Fatal("composite source identity scan was weakened")
	}
	s.Episodes[0].Cases[0].Query = "safe"
	s.Episodes[0].Events[0].Attributes = map[string]any{"conversation_seed": "private"}
	scan, e = LeakScan(s)
	if e != nil || len(scan.Problems) == 0 {
		t.Fatal("privileged BEAM field missed")
	}
}
