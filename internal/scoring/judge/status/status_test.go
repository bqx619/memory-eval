package status

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleSnapshotAndOverride(t *testing.T) {
	table := Builtin()
	for _, tc := range []struct{ model, date, want string }{{"gpt-4o-2024-08-06", "2026-10-13", "available"}, {"gpt-4o-2024-08-06", "2026-10-14", "deprecated"}, {"gpt-4o-2024-08-06", "2027-01-14", "deprecated"}, {"gpt-4o-2024-08-06", "2027-01-15", "retired"}, {"gpt-4o-2024-05-13", "2026-10-24", "retired"}, {"unlisted", "2026-10-16", "unknown"}} {
		now, _ := time.Parse("2006-01-02", tc.date)
		if got := table.Lookup("openai-compat", tc.model, now); got.Status != tc.want {
			t.Fatal(tc, got)
		}
	}
	if info := table.Lookup("typesafe", "jev-1.14.0", time.Now()); info.Status != "available" || info.Shutdown != "" {
		t.Fatal(info)
	}
	p := filepath.Join(t.TempDir(), "models.json")
	os.WriteFile(p, []byte(`{"fixture":{"provider":"typesafe","shutdown_date":"2020-01-01","source":"fixture://status"}}`), 0600)
	table, err := Load(p)
	if err != nil || table.Lookup("typesafe", "fixture", time.Now()).Status != "retired" {
		t.Fatal(table, err)
	}
	os.WriteFile(p, []byte(`{"fixture":{"provider":"typesafe","shutdown_date":"bad","source":"fixture"}}`), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("bad lifecycle accepted")
	}
}

func TestReplacementAvailabilityAndExclusionPolicy(t *testing.T) {
	table := Builtin()
	for _, model := range []string{"gpt-4o", "gpt-4o-2024-08-06", "gpt-4o-mini", "gpt-4o-unlisted"} {
		if table.Lookup("openai-compat", model, time.Now()).Policy != "excluded" {
			t.Fatal(model)
		}
	}
	for _, model := range []string{"gpt-6-luna", "gpt-6.1-sol"} {
		if table.Lookup("openai-compat", model, time.Now()).Status != "available" {
			t.Fatal(model)
		}
	}
	// A minimum support date is not a shutdown announcement.
	info := table.Lookup("anthropic", "claude-sonnet-5-5", time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC))
	if info.Status != "available" || info.Shutdown != "" || info.RetirementNotBefore != "2027-09-28" {
		t.Fatal(info)
	}
	p := filepath.Join(t.TempDir(), "status.json")
	os.WriteFile(p, []byte(`{"gpt-4o-2024-08-06":{"provider":"openai-compat","source":"fixture://no-retirement"}}`), 0600)
	override, err := Load(p)
	if err != nil || override.Lookup("openai-compat", "gpt-4o-2024-08-06", time.Now()).Policy != "excluded" {
		t.Fatal("lifecycle override bypassed exclusion", err)
	}
}
