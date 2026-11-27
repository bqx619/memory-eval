package identity

import (
	"strings"
	"testing"
)

func TestDeterministicAndDomainSeparated(t *testing.T) {
	seed := strings.Repeat("42", 32)
	a, err := New(seed, SampleID("private-case", 0))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New(seed, SampleID("private-case", 0))
	c, _ := New(seed, SampleID("private-case", 1))
	x := a.Anon("event", "source")
	if x != b.Anon("event", "source") {
		t.Fatal("not deterministic")
	}
	if x == c.Anon("event", "source") || x == a.Anon("owner", "source") || x == a.Anon("event", "another") {
		t.Fatal("domain collision")
	}
	if len(x) != 36 || x[14] != '4' || !strings.ContainsRune("89ab", rune(x[19])) {
		t.Fatalf("not a UUIDv4: %s", x)
	}
	if strings.Contains(a.Namespace(), "private") {
		t.Fatal("private id leaked")
	}
}
func TestSeed(t *testing.T) {
	for _, s := range []string{"bad", strings.Repeat("g", 64), "00"} {
		if _, err := Seed(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	s, err := Seed("")
	if err != nil || len(s) != 64 {
		t.Fatal(s, err)
	}
}
