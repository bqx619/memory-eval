package report_test

import (
	"bytes"
	"encoding/json"
	"memeval/internal/adapter"
	"memeval/internal/identity"
	"memeval/internal/llm/decision"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/scoring/judge/inputlog"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resumeReport(t *testing.T) (string, *result.Report) {
	t.Helper()
	dir := t.TempDir()
	r := &result.Report{RunID: "fixture-run", RunSeed: strings.Repeat("aa", 32), SuiteID: "fixture", SuiteSHA256: strings.Repeat("bb", 32), Protocol: result.Protocol{Version: "paeval-v2"}, Mode: runner.Retrieval, Cases: []result.Case{{CaseID: "q", SampleID: identity.SampleID("q", 0), Status: result.Scored, PublicInputSHA256: strings.Repeat("cc", 32), Metrics: map[string]*float64{}}}}
	if e := report.Save(t.Context(), r, dir); e != nil {
		t.Fatal(e)
	}
	return dir, r
}

func TestResumeReportRejectsInconsistentArtifactsAndLocks(t *testing.T) {
	dir, r := resumeReport(t)
	x, e := report.LoadResume(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer x.Close()
	if e = x.Begin(); e != nil {
		t.Fatal(e)
	}
	y, e := report.LoadResume(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer y.Close()
	if e = y.Begin(); e == nil {
		t.Fatal("concurrent resume allowed")
	}
	path := filepath.Join(dir, "cases.jsonl")
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(b, b...), 0600); e != nil {
		t.Fatal(e)
	}
	if e = x.Commit(t.Context(), r, func(string) bool { return false }, []result.Status{result.Error}); e == nil {
		t.Fatal("concurrent source change accepted")
	}
	if _, e = report.LoadResume(t.Context(), dir); e == nil {
		t.Fatal("report/JSONL mismatch accepted")
	}
	if _, e = os.Stat(filepath.Join(dir, "resume-history")); !os.IsNotExist(e) {
		t.Fatal("failed commit wrote history")
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(dir, "report.json"), path); e != nil {
		t.Fatal(e)
	}
	if _, e = report.LoadResume(t.Context(), dir); e == nil {
		t.Fatal("artifact symlink accepted")
	}
}

func TestResumePreservesUnknownFieldsAndOriginalJSONL(t *testing.T) {
	dir := t.TempDir()
	s := &suite.Suite{SchemaVersion: "2.0", SuiteID: "fixture", Provenance: map[string]any{"source_url": "fixture://synthetic"}, Episodes: []suite.Episode{{ID: "ep", Events: []suite.Event{{SourceID: "source", Owner: "u", Content: "value"}}, Cases: []suite.Case{{ID: "q", Kind: "qa", User: "u", Query: "value", Cutoff: 1, Gold: suite.Gold{EvidenceIDs: []string{"source"}}}}}}}
	o := runner.DefaultOptions()
	o.RunSeed = strings.Repeat("ab", 32)
	a, _ := adapter.NewBaseline("null")
	r, e := runner.Run(t.Context(), s, a, o)
	if e != nil {
		t.Fatal(e)
	}
	if e = report.Save(t.Context(), r, dir); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "report.json"))
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]any
	if e = json.Unmarshal(b, &wire); e != nil {
		t.Fatal(e)
	}
	row := wire["cases"].([]any)[0].(map[string]any)
	row["future_extension"] = map[string]any{"retained": true}
	b, _ = json.Marshal(wire)
	if e = os.WriteFile(filepath.Join(dir, "report.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	line, _ := json.Marshal(row)
	line = append([]byte("  "), line...)
	line = append(line, '\n')
	if e = os.WriteFile(filepath.Join(dir, "cases.jsonl"), line, 0600); e != nil {
		t.Fatal(e)
	}
	x, e := report.LoadResume(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer x.Close()
	if e = x.Begin(); e != nil {
		t.Fatal(e)
	}
	p, e := runner.NewResume(x.Original, "error,invalid")
	if e != nil {
		t.Fatal(e)
	}
	o.Resume = p
	a, _ = adapter.NewBaseline("null")
	next, e := runner.Run(t.Context(), s, a, o)
	if e != nil {
		t.Fatal(e)
	}
	if e = x.Commit(t.Context(), next, p.Selected, p.Statuses); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "cases.jsonl"))
	if !bytes.Equal(got, line) {
		t.Fatal("JSONL formatting/unknown field changed")
	}
	if _, e = report.LoadResume(t.Context(), dir); e != nil {
		t.Fatal("merged report disagrees with JSONL", e)
	}
}

func TestResumeAbortRetainsAttemptLedger(t *testing.T) {
	dir, _ := resumeReport(t)
	before, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	x, err := report.LoadResume(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.Begin(); err != nil {
		t.Fatal(err)
	}
	ledger := []byte("{\"run_id\":\"fixture-run\",\"error\":\"interrupted\"}\n")
	if err = os.WriteFile(filepath.Join(x.StageDir, "llm_ledger.jsonl"), ledger, 0600); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Base(x.StageDir)
	x.Close()
	got, err := os.ReadFile(filepath.Join(dir, "resume-attempts", stage, "llm_ledger.jsonl"))
	if err != nil || !bytes.Equal(got, ledger) {
		t.Fatal("lost uncommitted attempt", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil || !bytes.Equal(got, before) {
		t.Fatal("abort changed report", err)
	}
}

func TestResumeMergesJudgeInputsWithTrustedHash(t *testing.T) {
	dir, r := resumeReport(t)
	second := r.Cases[0]
	second.CaseID = "q2"
	second.SampleID = identity.SampleID("q2", 0)
	r.Cases = append(r.Cases, second)
	writeInputs := func(dest string, ids []string, label bool) *inputlog.Artifact {
		rec, err := inputlog.New(t.Context(), dest)
		if err != nil {
			t.Fatal(err)
		}
		defer rec.Close()
		for _, id := range ids {
			ctx := inputlog.WithCase(inputlog.WithRecorder(t.Context(), rec), id, "ep", "cluster", 0)
			err = inputlog.Append(ctx, id, "memora_eval", inputlog.Recipe{Kind: "binary", State: `{"question":"synthetic"}`, Questions: []decision.Question{{Name: "correct", Type: "noul"}}, TemplateID: "synthetic"}, inputlog.Verdict{Kind: "decision", Provider: "typesafe", Model: "jev-1.14.0", Valid: true, Label: &label})
			if err != nil {
				t.Fatal(err)
			}
		}
		meta, err := rec.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		return meta
	}
	r.JudgeInputs = writeInputs(dir, []string{"q", "q2"}, true)
	if err := report.Save(t.Context(), r, dir); err != nil {
		t.Fatal(err)
	}
	x, err := report.LoadResume(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if err = x.Begin(); err != nil {
		t.Fatal(err)
	}
	oldMeta := *r.JudgeInputs
	r.JudgeInputs = writeInputs(x.StageDir, []string{"q"}, false)
	r.Cases[0].PreviousStatus = result.Scored
	if err = x.Commit(t.Context(), r, func(sample string) bool { return sample == identity.SampleID("q", 0) }, []result.Status{result.Scored}); err != nil {
		t.Fatal(err)
	}
	rows, err := inputlog.Read(dir, *r.JudgeInputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatal(len(rows))
	}
	for _, row := range rows {
		if *row.Decision.Label != (row.CaseID == "q2") {
			t.Fatal(row)
		}
	}
	prior, err := inputlog.Read(filepath.Join(dir, "resume-history/0001"), oldMeta)
	if err != nil || len(prior) != 2 || !*prior[0].Decision.Label {
		t.Fatal(prior, err)
	}
}

func TestPartialResumeCommitAndGovernorHistory(t *testing.T) {
	dir, r := resumeReport(t)
	r.Interruption = &result.Interruption{Reason: "budget", Unscheduled: 1}
	if e := report.Save(t.Context(), r, dir); e != nil {
		t.Fatal(e)
	}
	writeMeter := func(root string) {
		t.Helper()
		p := filepath.Join(root, "governor")
		os.MkdirAll(p, 0700)
		os.WriteFile(filepath.Join(p, "episode-ips.json"), []byte(`[{"IP":"172.22.0.2","NS":"ns"}]`), 0600)
		os.WriteFile(filepath.Join(p, "attempts.jsonl"), []byte("{\"t\":\"2026-10-19T00:00:00Z\",\"episode_ip\":\"172.22.0.2\",\"attempt\":1,\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3}}\n"), 0600)
	}
	writeMeter(dir)
	x, e := report.LoadResume(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer x.Close()
	if e = x.Begin(); e != nil {
		t.Fatal(e)
	}
	writeMeter(x.StageDir)
	fresh := *r
	fresh.Interruption = nil
	c := r.Cases[0]
	c.CaseID = "q2"
	c.SampleID = identity.SampleID(c.CaseID, 0)
	fresh.Cases = append(append([]result.Case(nil), r.Cases...), c)
	fresh.Isolation.Container = map[string]any{"governor": map[string]any{"meter_file": "governor/attempts.jsonl"}}
	if e = x.Commit(t.Context(), &fresh, func(s string) bool { return s == c.SampleID }, []result.Status{result.Error}); e != nil {
		t.Fatal(e)
	}
	loaded, e := report.LoadResume(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer loaded.Close()
	if len(loaded.Original.Cases) != 2 || loaded.Original.Interruption != nil {
		t.Fatal(loaded.Original.Interruption)
	}
	raw, _ := json.Marshal(loaded.Original.Billing.SUT)
	if !strings.Contains(string(raw), `"prompt_tokens":24`) || !strings.Contains(string(raw), `"calls":2`) {
		t.Fatal(string(raw))
	}
	for _, p := range []string{"governor/attempts.jsonl", "governor/resume-1/attempts.jsonl", "resume-history/0001/report.json"} {
		if _, e := os.Stat(filepath.Join(dir, p)); e != nil {
			t.Fatal(e)
		}
	}
}
