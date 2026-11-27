package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"math"
	"memeval/internal/config/dotenv"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutoResumeCooldown(t *testing.T) {
	for _, tc := range []struct {
		suggest   float64
		max, want time.Duration
	}{
		{0, time.Minute, 30 * time.Second}, {-5, time.Minute, 30 * time.Second}, {90, time.Minute, time.Minute},
		{45, time.Minute, 45 * time.Second}, {0, 5 * time.Second, 5 * time.Second}, {math.Inf(1), time.Minute, 30 * time.Second}, {math.NaN(), time.Minute, 30 * time.Second},
	} {
		if got := resumeCooldown(tc.suggest, tc.max); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e := resumeSleep(ctx, time.Hour); e != context.Canceled {
		t.Fatal(e)
	}
}
func TestAutoResumeValidation(t *testing.T) {
	for _, n := range []string{"-1", "11"} {
		e := runCommand(t.Context(), []string{"run", "--auto-resume-rate-limited", n, "--suite", "missing"}, io.Discard, io.Discard)
		if e == nil || !strings.Contains(e.Error(), "must be between 0 and 10") {
			t.Fatal(e)
		}
	}
}
func TestAutoResumeFakeGovernor(t *testing.T) {
	for _, tc := range []struct {
		name, stop   string
		rounds       int
		recover      bool
		sleeps, code int
	}{
		{"recover", "exhausted", 1, true, 1, 0}, {"limit", "exhausted", 2, false, 2, 4}, {"budget", "budget", 2, false, 0, 4}, {"disabled", "exhausted", 0, true, 0, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := testoracle.FakeDocker(t)
			f.Configure(t)
			root := t.TempDir()
			project := filepath.Join(root, "project")
			os.MkdirAll(project, 0700)
			put := func(p string, b []byte) {
				t.Helper()
				if e := os.WriteFile(p, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			echo, e := os.ReadFile("../../suts/echo/echo_bridge.py")
			if e != nil {
				t.Fatal(e)
			}
			put(filepath.Join(project, "echo.py"), echo)
			put(filepath.Join(project, "requirements.in"), nil)
			put(filepath.Join(project, "requirements.lock.txt"), nil)
			manifest := filepath.Join(project, "manifest.json")
			put(manifest, []byte(`{"schema_version":"3","name":"auto-fixture","adapter":{"kind":"generic","transport":"jsonrpc-stdio"},"runtime":{"kind":"uv-python","python":"3.12","requirements":"requirements.in","requirements_lock":"requirements.lock.txt"},"process":{"command":["{venv}/bin/python","{project}/echo.py"]},"sandbox":{"backend":"container","network":"gateway"},"ready":{"strategy":"sync"}}`))
			t.Setenv("MEMEVAL_CACHE_DIR", filepath.Join(root, "cache"))
			t.Setenv("MEMEVAL_EP_ROOT", filepath.Join(root, "ep"))
			t.Setenv("MEMEVAL_SUT_LLM_BASE_URL", "http://127.0.0.1:8317/v1")
			for _, k := range []string{"MEMEVAL_SUT_LLM_BUDGET_TOKENS", "MEMEVAL_SUT_LLM_MAX_PAUSE", "MEMEVAL_PARALLEL"} {
				t.Setenv(k, "")
			}
			ctx := dotenv.With(t.Context(), &dotenv.Settings{Sources: map[string]string{}})
			if e = runCommand(ctx, []string{"env", "install", manifest, "--offline"}, io.Discard, io.Discard); e != nil {
				t.Fatal(e)
			}
			var eps []any
			for i := 0; i < 4; i++ {
				eps = append(eps, map[string]any{"id": fmt.Sprint(i), "events": []any{map[string]any{"source_id": "s", "owner": "u", "content": "blue"}}, "cases": []any{map[string]any{"id": fmt.Sprint("q", i), "kind": "qa", "query": "color?", "user": "u", "cutoff": 1, "gold": map[string]any{"answers": []string{"blue"}, "evidence_ids": []string{"s"}}}}})
			}
			source := filepath.Join(root, "suite.json")
			b, _ := json.Marshal(map[string]any{"schema_version": "2.0", "suite_id": "auto-fixture", "provenance": map[string]string{"source_url": "fixture://synthetic"}, "episodes": eps})
			put(source, b)
			control := map[string]string{"FAKE_DOCKER_GOVERNOR_AFTER_SUTS": "2", "FAKE_DOCKER_GOVERNOR_STOP": tc.stop, "FAKE_DOCKER_GOVERNOR_RETRY_AFTER": "60", "FAKE_DOCKER_GOVERNOR_METER": "1"}
			f.Control(t, control)
			sleeps := 0
			var initial *result.Report
			dest := filepath.Join(root, "out")
			ctx = context.WithValue(ctx, autoResumeSleepKey{}, func(_ context.Context, d time.Duration) error {
				sleeps++
				if d != 60*time.Second {
					t.Fatalf("cooldown: %s", d)
				}
				initial, e = report.Load(t.Context(), filepath.Join(dest, "report.json"))
				if e != nil {
					t.Fatal(e)
				}
				if _, e = os.Stat(filepath.Join(dest, ".resume.lock")); !os.IsNotExist(e) {
					t.Fatal("lock held during cooldown", e)
				}
				if tc.recover {
					f.Control(t, map[string]string{"FAKE_DOCKER_GOVERNOR_METER": "1"})
				}
				return nil
			})
			var stderr bytes.Buffer
			e = runCommand(ctx, []string{"run", "--sut", manifest, "--suite", source, "--out", dest, "--auto-resume-rate-limited", fmt.Sprint(tc.rounds)}, io.Discard, &stderr)
			code := 0
			if e != nil {
				code = exitCode(e)
			}
			if code != tc.code || sleeps != tc.sleeps {
				t.Fatalf("code=%d sleeps=%d: %v\n%s", code, sleeps, e, &stderr)
			}
			r, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			if r.ResumeCount != sleeps {
				t.Fatal(r.ResumeCount, sleeps)
			}
			if tc.recover && tc.rounds > 0 {
				if r.Interruption != nil || len(r.Cases) != 4 || r.RunID != initial.RunID || r.RunSeed != initial.RunSeed {
					t.Fatal(r.Interruption, len(r.Cases))
				}
				a, _ := json.Marshal(r.Cases[0])
				b, _ := json.Marshal(initial.Cases[0])
				if !bytes.Equal(a, b) {
					t.Fatal("scored row changed")
				}
				h := r.ResumeHistory[0]
				if h.CasesRetried != 3 || h.CasesRecovered != 3 || h.Reason != "exhausted" {
					t.Fatal(h)
				}
			}
			for i, h := range r.ResumeHistory {
				if h.AutoResume == nil || !h.Auto || h.Round != i+1 || h.WaitedSeconds != 60 {
					t.Fatal(h)
				}
			}
			if sleeps > 0 && !strings.Contains(stderr.String(), "auto-resume round 1/") {
				t.Fatal(stderr.String())
			}
			if _, e = os.Stat(filepath.Join(dest, ".resume.lock")); !os.IsNotExist(e) {
				t.Fatal("stale lock", e)
			}
			t.Logf("%s: exit=%d, auto rounds=%d, cases=%d, interruption=%+v", tc.name, code, r.ResumeCount, len(r.Cases), r.Interruption)
		})
	}
}

// Exercise the real resume commit with a completed report containing timeout,
// gateway and unrelated errors (not only the interrupted, unscheduled suffix).
func TestAutoResumeSelectedErrorsAndHistory(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(fmt.Sprint(unrelated), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "suite.json")
			dest := filepath.Join(root, "run")
			b := []byte(`{"schema_version":"2.0","suite_id":"auto-errors","provenance":{"source_url":"fixture://synthetic"},"episodes":[{"id":"ep","events":[{"source_id":"s","owner":"u","content":"blue"}],"cases":[{"id":"timeout","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"gateway","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}},{"id":"other","kind":"qa","query":"color?","user":"u","cutoff":1,"gold":{"answers":["blue"],"evidence_ids":["s"]}}]}]}`)
			if e := os.WriteFile(source, b, 0600); e != nil {
				t.Fatal(e)
			}
			flags := []string{"--sut", "bm25", "--suite", source, "--out", dest}
			if e := runCommand(t.Context(), append([]string{"run"}, flags...), io.Discard, io.Discard); e != nil {
				t.Fatal(e)
			}
			original, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			for i, typ := range []string{"rpc_timeout", "gateway_exhausted"} {
				original.Cases[i].Status = result.Error
				original.Cases[i].ErrorType = typ
			}
			if unrelated {
				original.Cases[2].Status = result.Error
				original.Cases[2].ErrorType = "*jsonrpc.RPCError"
			}
			unchanged, _ := json.Marshal(original.Cases[2])
			sleepCount := 0
			ctx := context.WithValue(t.Context(), autoResumeSleepKey{}, func(_ context.Context, d time.Duration) error {
				sleepCount++
				if d != 30*time.Second {
					t.Fatal(d)
				}
				return nil
			})
			cmd := newRunCommand(ctx, io.Discard)
			cmd.SetErr(io.Discard)
			if e = cmd.ParseFlags(flags); e != nil {
				t.Fatal(e)
			}
			// The simulated manual pass was limited to one row. Automatic error
			// recovery must drop this list and the already-committed SUT override.
			if e = cmd.Flags().Set("resume-cases", "timeout"); e != nil {
				t.Fatal(e)
			}
			if e = cmd.Flags().Set("resume-sut-change", "true"); e != nil {
				t.Fatal(e)
			}
			// Simulate the first run's transient errors; subsequent invocations use the
			// production command, factory, trusted-input check and atomic Commit.
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				if e := report.Save(ctx, original, dest); e != nil {
					return e
				}
				return fmt.Errorf("fixture errors")
			}
			rounds := 1
			wrapAutoResume(ctx, cmd, io.Discard, &rounds)
			e = cmd.RunE(cmd, nil)
			if unrelated && (e == nil || exitCode(e) != 2) || !unrelated && e != nil {
				t.Fatal(e)
			}
			r, e := report.Load(ctx, filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			if sleepCount != 1 || r.ResumeCount != 1 {
				t.Fatal(sleepCount, r.ResumeCount)
			}
			for _, c := range r.Cases[:2] {
				if c.Status != result.Scored || c.PreviousStatus != result.Error {
					t.Fatal(c)
				}
			}
			got, _ := json.Marshal(r.Cases[2])
			if !bytes.Equal(unchanged, got) {
				t.Fatal("unselected case changed")
			}
			h := r.ResumeHistory[0]
			if !h.Auto || h.CasesRetried != 2 || h.CasesRecovered != 2 || h.WaitedSeconds != 30 || h.Cases != nil || h.SUTChange != nil {
				t.Fatal(h)
			}
		})
	}
}

func TestAutoResumeIgnoresStaleReportAndIsolationFailure(t *testing.T) {
	for _, isolation := range []bool{false, true} {
		dest := t.TempDir()
		r := &result.Report{RunID: "stale", Interruption: &result.Interruption{Reason: "exhausted", Unscheduled: 1}}
		if e := report.Save(t.Context(), r, dest); e != nil {
			t.Fatal(e)
		}
		ctx := context.WithValue(t.Context(), autoResumeSleepKey{}, func(context.Context, time.Duration) error { t.Fatal("must not retry"); return nil })
		cmd := newRunCommand(ctx, io.Discard)
		cmd.Flags().Set("out", dest)
		want := error(fmt.Errorf("fixture preflight failed"))
		if isolation {
			want = &isolationExit{reason: "fixture isolation failed"}
		}
		cmd.RunE = func(*cobra.Command, []string) error {
			if isolation {
				r.RunID = "new"
				report.Save(ctx, r, dest)
			}
			return want
		}
		n := 1
		wrapAutoResume(ctx, cmd, io.Discard, &n)
		if e := cmd.RunE(cmd, nil); e != want {
			t.Fatal(e)
		}
	}
}
