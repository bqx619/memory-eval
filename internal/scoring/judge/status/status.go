// Package status holds the reviewed model lifecycle snapshot, with offline overrides.
package status

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type Entry struct {
	Policy              string `json:"policy,omitempty"`
	RetirementNotBefore string `json:"retirement_not_before,omitempty"`
	Provider            string `json:"provider"`
	Announced           string `json:"announced,omitempty"`
	Shutdown            string `json:"shutdown_date,omitempty"`
	Replacement         string `json:"replacement,omitempty"`
	Source              string `json:"source"`
	NoticeDays          int    `json:"notice_days,omitempty"`
}
type Info struct {
	Policy              string `json:"policy,omitempty"`
	RetirementNotBefore string `json:"retirement_not_before,omitempty"`
	Status              string `json:"status"`
	Shutdown            string `json:"shutdown_date,omitempty"`
	Source              string `json:"source,omitempty"`
	Replacement         string `json:"replacement,omitempty"`
}
type Table map[string]Entry

func Builtin() Table {
	t := Table{}
	for _, id := range []string{"gpt-4o-2024-08-06", "gpt-4o-2024-11-20", "gpt-4o-mini-2024-07-18", "gpt-4.1-2025-04-14", "gpt-4.1-mini-2025-04-14"} {
		t[id] = Entry{Provider: "openai-compat", Announced: "2026-10-14", Shutdown: "2027-01-14", Replacement: "gpt-6-luna", Source: "https://developers.openai.com/api/docs/deprecations"}
	}
	t["gpt-4o-2024-05-13"] = Entry{Provider: "openai-compat", Shutdown: "2026-10-23", Replacement: "gpt-6-luna", Source: "https://developers.openai.com/api/docs/deprecations"}
	for _, id := range []string{"jev-1.13.0", "jev-1.14.0"} {
		t[id] = Entry{Provider: "typesafe", Source: "https://docs.typesafe.ai/", NoticeDays: 60}
	}
	for id, e := range t {
		if excluded(id) {
			e.Policy = "excluded"
			t[id] = e
		}
	}
	for _, id := range []string{"gpt-6-luna", "gpt-6.1-sol"} {
		t[id] = Entry{Provider: "openai-compat", Source: "https://developers.openai.com/api/docs/models"}
	}
	t["claude-sonnet-5-5"] = Entry{Provider: "anthropic", RetirementNotBefore: "2027-09-28", Source: "https://platform.claude.com/docs/en/models/overview"}
	return t
}
func Load(path string) (Table, error) {
	t := Builtin()
	if path == "" {
		return t, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read judge status table")
	}
	var overrides Table
	if err := json.Unmarshal(b, &overrides); err != nil {
		return nil, fmt.Errorf("invalid judge status table")
	}
	for id, e := range overrides {
		if id == "" || e.Provider == "" || e.Source == "" {
			return nil, fmt.Errorf("judge status entry requires model/provider/source")
		}
		for _, d := range []string{e.Announced, e.Shutdown, e.RetirementNotBefore} {
			if d != "" {
				if _, err := time.Parse("2006-01-02", d); err != nil {
					return nil, fmt.Errorf("invalid judge status date")
				}
			}
		}
		if e.Announced != "" && e.Shutdown != "" && e.Announced > e.Shutdown {
			return nil, fmt.Errorf("announcement after shutdown")
		}
		if e.Policy != "" && e.Policy != "excluded" {
			return nil, fmt.Errorf("invalid judge policy")
		}
		t[id] = e
	}
	return t, nil
}
func (t Table) Lookup(provider, model string, now time.Time) Info {
	e, ok := t[model]
	if !ok || e.Provider != provider {
		if excluded(model) {
			return Info{Status: "unknown", Policy: "excluded"}
		}
		return Info{Status: "unknown"}
	}
	i := Info{Policy: e.Policy, RetirementNotBefore: e.RetirementNotBefore, Status: "available", Shutdown: e.Shutdown, Source: e.Source, Replacement: e.Replacement}
	if excluded(model) {
		i.Policy = "excluded"
	}
	date := now.UTC().Format("2006-01-02")
	if e.Shutdown != "" && date > e.Shutdown {
		i.Status = "retired"
	} else if e.Announced != "" && date >= e.Announced || e.Announced == "" && e.Shutdown != "" {
		i.Status = "deprecated"
	}
	return i
}

// Exclusion is user policy and cannot be removed by lifecycle overrides.
func excluded(model string) bool { return model == "gpt-4o" || strings.HasPrefix(model, "gpt-4o-") }
