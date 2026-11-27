package suite

import (
	"memeval/internal/identity"
	"strings"
	"testing"
)

func TestLeakScanVerifiedIdentityPaths(t *testing.T) {
	s := minimalV3("qa")
	ep := &s.Episodes[0]
	ep.Events[0].Session = "c0"
	id, _ := identity.New(strings.Repeat("42", 32), "leak-scan")
	// A raw session deliberately equals a substring of a correctly derived UUID.
	token := id.Anon("owner", ep.Events[0].Owner)[:2]
	ep.Events[0].Session = token
	scan, e := LeakScan(s)
	if e != nil || len(scan.Problems) != 0 {
		t.Fatal(e, scan)
	}
	ep.Cases[0].Query = "Leaked session: " + token
	scan, e = LeakScan(s)
	if e != nil || len(scan.Problems) == 0 {
		t.Fatal("query exemption too broad", e, scan)
	}
	ep.Cases[0].Query = "What happened?"
	ep.Events[0].Attributes = map[string]any{"canonical_name": token}
	scan, e = LeakScan(s)
	if e != nil || len(scan.Problems) == 0 {
		t.Fatal("attribute exemption too broad", e, scan)
	}
}
