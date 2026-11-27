package report_test

import (
	"bytes"
	"encoding/json"
	"memeval/internal/report"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putResumeArtifact(t *testing.T, root, name, text string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func stageResumeArtifacts(t *testing.T, x *report.ResumeOutput, record string) *result.Report {
	t.Helper()
	next := *x.Original
	next.Cases = append([]result.Case{}, x.Original.Cases...)
	next.Logs = logFixture(t, x.StageDir, "retry")
	putResumeArtifact(t, x.StageDir, "selftest/"+record, `{"passed":true}`)
	next.Isolation.Selftest = map[string]any{"path": filepath.Join(x.StageDir, "selftest", record), "sha256": "retained-hash"}
	putResumeArtifact(t, x.StageDir, "governor/episode-ips.json", `[]`)
	putResumeArtifact(t, x.StageDir, "governor/attempts.jsonl", "")
	if err := report.Save(t.Context(), &next, x.StageDir); err != nil {
		t.Fatal(err)
	}
	return &next
}
func TestResumeCommitSelftestGovernorAndLogs(t *testing.T) {
	dir, _ := resumeReport(t)
	putResumeArtifact(t, dir, "selftest/old.json", `{"old":true}`)
	x, err := report.LoadResume(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if err = x.Begin(); err != nil {
		t.Fatal(err)
	}
	next := stageResumeArtifacts(t, x, "x.json")
	if err = x.Commit(t.Context(), next, func(string) bool { return false }, []result.Status{result.Error}); err != nil {
		t.Fatal(err)
	}
	x.Close()
	for _, p := range []string{"selftest/x.json", "selftest/old.json", "governor/resume-1/attempts.jsonl", "logs/resume-1/0001-retry/sut.stderr.log", "resume-history/0001/report.json"} {
		info, err := os.Stat(filepath.Join(dir, p))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatal(p, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "selftest/x.json"))
	if err != nil || string(b) != `{"passed":true}` {
		t.Fatal(string(b), err)
	}
	info, _ := os.Stat(filepath.Join(dir, "selftest/x.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	loaded, err := report.LoadResume(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.Original.ResumeCount != 1 || len(loaded.Original.ResumeHistory) != 1 {
		t.Fatal(loaded.Original.ResumeHistory)
	}
	raw, _ := json.Marshal(loaded.Original.Isolation.Selftest)
	var evidence map[string]string
	if err = json.Unmarshal(raw, &evidence); err != nil || evidence["path"] != filepath.Join(dir, "selftest/x.json") || evidence["sha256"] != "retained-hash" {
		t.Fatal(string(raw), err)
	}
	if _, err = os.Stat(x.StageDir); !os.IsNotExist(err) {
		t.Fatal("committed stage remains", err)
	}
}
func TestResumeRejectedStageRetainedAndRetry(t *testing.T) {
	for _, mode := range []string{"unknown-dir", "collision", "nested", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir, _ := resumeReport(t)
			beforeReport, _ := os.ReadFile(filepath.Join(dir, "report.json"))
			beforeCases, _ := os.ReadFile(filepath.Join(dir, "cases.jsonl"))
			x, err := report.LoadResume(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			if err = x.Begin(); err != nil {
				t.Fatal(err)
			}
			next := stageResumeArtifacts(t, x, "x.json")
			want := ""
			switch mode {
			case "unknown-dir":
				putResumeArtifact(t, x.StageDir, "unexpected/keep.txt", "diagnostic")
				want = "unexpected staged artifact: unexpected"
			case "collision":
				putResumeArtifact(t, dir, "selftest/x.json", "original record")
				want = "selftest record collision: x.json"
			case "nested":
				putResumeArtifact(t, x.StageDir, "selftest/nested/record.json", "{}")
				want = "unexpected staged artifact: selftest/nested"
			case "symlink":
				if err = os.Symlink(filepath.Join(dir, "report.json"), filepath.Join(x.StageDir, "selftest/link.json")); err != nil {
					t.Fatal(err)
				}
				want = "unexpected staged artifact: selftest/link.json"
			}
			err = x.Commit(t.Context(), next, func(string) bool { return false }, []result.Status{result.Error})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal(err, want)
			}
			stage := filepath.Base(x.StageDir)
			x.Close()
			for _, p := range []string{"report.json", "selftest/x.json", "governor/attempts.jsonl", "logs/0001-retry/sut.stderr.log"} {
				if _, err = os.Stat(filepath.Join(dir, "resume-attempts", stage, p)); err != nil {
					t.Fatal("failed stage lost", p, err)
				}
			}
			for _, p := range []string{".resume.lock", "resume-history/0001", "logs", "governor"} {
				if _, err = os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
					t.Fatal("failed commit published state", p, err)
				}
			}
			for p, want := range map[string][]byte{"report.json": beforeReport, "cases.jsonl": beforeCases} {
				got, _ := os.ReadFile(filepath.Join(dir, p))
				if !bytes.Equal(got, want) {
					t.Fatal("original changed", p)
				}
			}
			if mode == "collision" {
				b, _ := os.ReadFile(filepath.Join(dir, "selftest/x.json"))
				if string(b) != "original record" {
					t.Fatal("overwrote selftest")
				}
			}
			// Same output, new attempt: no stale lock/history or partially moved folders.
			retry, err := report.LoadResume(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer retry.Close()
			if err = retry.Begin(); err != nil {
				t.Fatal(err)
			}
			next = stageResumeArtifacts(t, retry, "next.json")
			if err = retry.Commit(t.Context(), next, func(string) bool { return false }, []result.Status{result.Error}); err != nil {
				t.Fatal(err)
			}
			retry.Close()
			loaded, err := report.LoadResume(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			if loaded.Original.ResumeCount != 1 || len(loaded.Original.ResumeHistory) != 1 || loaded.Original.ResumeHistory[0].HistoryPath != "resume-history/0001" {
				t.Fatal(loaded.Original.ResumeHistory)
			}
			history, _ := os.ReadFile(filepath.Join(dir, "resume-history/0001/report.json"))
			if !bytes.Equal(history, beforeReport) {
				t.Fatal("history disagrees with original")
			}
			for _, p := range []string{"selftest/next.json", "governor/resume-1/attempts.jsonl", "logs/resume-1/0001-retry/sut.stderr.log"} {
				if _, err = os.Stat(filepath.Join(dir, p)); err != nil {
					t.Fatal(err)
				}
			}
			// Retained diagnostics are not folded into committed billing/history.
			raw, _ := json.Marshal(loaded.Original.Billing.SUT)
			if !strings.Contains(string(raw), `"calls":0`) {
				t.Fatal(string(raw))
			}
		})
	}
}
