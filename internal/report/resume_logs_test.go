package report_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"memeval/internal/episodelog"
	"memeval/internal/report"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func logFixture(t *testing.T, dir, ns string) *episodelog.Summary {
	t.Helper()
	r, e := episodelog.New(t.Context(), dir, episodelog.Options{})
	if e != nil {
		t.Fatal(e)
	}
	ep, e := r.Episode(ns)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprintln(ep.Writer("sut.stderr.log"), "diagnostic "+ns)
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	s := r.Summary()
	return &s
}
func TestResumeEpisodeLogsMerge(t *testing.T) {
	dir, original := resumeReport(t)
	original.Logs = logFixture(t, dir, "describe")
	if e := report.Save(t.Context(), original, dir); e != nil {
		t.Fatal(e)
	}
	oldPath := filepath.Join(dir, "logs/0001-describe/sut.stderr.log")
	old, _ := os.ReadFile(oldPath)
	for attempt := 1; attempt <= 3; attempt++ {
		x, e := report.LoadResume(t.Context(), dir)
		if e != nil {
			t.Fatal(e)
		}
		if e = x.Begin(); e != nil {
			t.Fatal(e)
		}
		next := *x.Original
		next.Cases = append([]result.Case{}, x.Original.Cases...)
		if attempt < 3 {
			next.Logs = logFixture(t, x.StageDir, "retry")
			next.Isolation.EffectiveConfig = []map[string]any{{"ns": "retry", "log_dir": "logs/0001-retry"}}
		} else {
			next.Logs = &episodelog.Summary{Disabled: true}
		} // --no-logs preserves existing history.
		if e = x.Commit(t.Context(), &next, func(string) bool { return false }, []result.Status{result.Error}); e != nil {
			t.Fatal(e)
		}
		x.Close()
		got, e := report.Load(t.Context(), filepath.Join(dir, "report.json"))
		if e != nil {
			t.Fatal(e)
		}
		if got.ResumeCount != attempt || got.Logs.Disabled || got.Logs.Episodes != min(attempt+1, 3) {
			t.Fatal(got.ResumeCount, got.Logs)
		}
		if attempt < 3 {
			p := filepath.Join(dir, "logs", fmt.Sprintf("resume-%d", attempt), "0001-retry/sut.stderr.log")
			if _, e = os.Stat(p); e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(got.Isolation.EffectiveConfig)
			if !strings.Contains(string(raw), fmt.Sprintf("logs/resume-%d/0001-retry", attempt)) {
				t.Fatal(string(raw))
			}
		}
		kept, _ := os.ReadFile(oldPath)
		if !bytes.Equal(kept, old) {
			t.Fatal("original logs changed")
		}
		b, _ := os.ReadFile(filepath.Join(dir, "logs/index.jsonl"))
		lines := bytes.Split(bytes.TrimSpace(b), []byte{'\n'})
		if len(lines) != min(attempt+1, 3) {
			t.Fatal("merged index rows", len(lines))
		}
		for _, line := range lines {
			var row episodelog.Record
			if e = json.Unmarshal(line, &row); e != nil {
				t.Fatal(e)
			}
			if _, e = os.Stat(filepath.Join(dir, "logs", row.Dir, "episode.json")); e != nil {
				t.Fatal(e)
			}
		}
		files := 0
		var size int64
		e = filepath.WalkDir(filepath.Join(dir, "logs"), func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			i, e := d.Info()
			if e == nil {
				files++
				size += i.Size()
			}
			return e
		})
		if e != nil || got.Logs.Files != files || got.Logs.Bytes != size {
			t.Fatal(e, got.Logs, files, size)
		}
	}
}

func TestResumeLogsRejectSymlinkAndCollision(t *testing.T) {
	for _, mode := range []string{"symlink", "collision"} {
		t.Run(mode, func(t *testing.T) {
			dir, original := resumeReport(t)
			x, e := report.LoadResume(t.Context(), dir)
			if e != nil {
				t.Fatal(e)
			}
			defer x.Close()
			if e = x.Begin(); e != nil {
				t.Fatal(e)
			}
			next := *original
			next.Cases = append([]result.Case{}, original.Cases...)
			next.Logs = logFixture(t, x.StageDir, "retry")
			if mode == "symlink" {
				e = os.Symlink(filepath.Join(dir, "report.json"), filepath.Join(x.StageDir, "logs/untrusted"))
			} else {
				e = os.MkdirAll(filepath.Join(dir, "logs/resume-1"), 0700)
			}
			if e != nil {
				t.Fatal(e)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "report.json"))
			if e = x.Commit(t.Context(), &next, func(string) bool { return false }, []result.Status{result.Error}); e == nil {
				t.Fatal("unsafe merge accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "report.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("rejected merge changed report")
			}
		})
	}
}
