package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/runner"
)

type autoResumeRoundKey struct{}
type autoResumeSleepKey struct{} // context-local test clock; never a global hook

func resumeSleep(ctx context.Context, d time.Duration) error {
	if sleep, ok := ctx.Value(autoResumeSleepKey{}).(func(context.Context, time.Duration) error); ok {
		return sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// A configured ceiling smaller than 30s wins: auto-resume never exceeds the
// user's maximum pause. Invalid/nonfinite suggestions use the 30s floor.
func resumeCooldown(suggested float64, ceiling time.Duration) time.Duration {
	if math.IsNaN(suggested) || math.IsInf(suggested, 0) || suggested < 30 {
		suggested = 30
	}
	if suggested >= ceiling.Seconds() {
		return ceiling
	}
	return time.Duration(suggested * float64(time.Second))
}

func autoResumeCandidate(r *result.Report) (reason string, count int, cooldown float64) {
	var c struct {
		Governor struct {
			State      string  `json:"state"`
			RetryAfter float64 `json:"retry_after_s"`
		} `json:"governor"`
	}
	b, _ := json.Marshal(r.Isolation.Container)
	_ = json.Unmarshal(b, &c)
	g := c.Governor
	if g.State == "budget" || r.Interruption != nil && r.Interruption.Reason == "budget" {
		return "budget", 0, 0
	}
	for _, c := range r.Cases {
		if c.ErrorType == "gateway_budget" {
			return "budget", 0, 0
		}
		if runner.RateLimitedCase(c) {
			count++
			if c.ErrorType == "gateway_exhausted" {
				reason = "exhausted"
			}
		}
	}
	if r.Interruption != nil {
		count += r.Interruption.Unscheduled
		if r.Interruption.Reason == "exhausted" {
			reason = "exhausted"
		}
	}
	if g.State == "exhausted" {
		reason = "exhausted"
	}
	if reason == "" && count > 0 {
		reason = "rpc_timeout"
	}
	return reason, count, g.RetryAfter
}

func wrapAutoResume(ctx context.Context, cmd *cobra.Command, out io.Writer, rounds *int) {
	single := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if *rounds < 0 || *rounds > 10 {
			return fmt.Errorf("auto-resume-rate-limited must be between 0 and 10")
		}
		if *rounds == 0 {
			return single(c, args)
		}
		// Capture before one execution changes --out to its staging directory or
		// resolves a suite alias. No re-parsing dotenv or consulting private files.
		f := cmd.Flags()
		dir, _ := f.GetString("resume")
		if dir == "" {
			dir, _ = f.GetString("out")
		}
		type savedFlag struct {
			value   string
			changed bool
		}
		flags := map[string]savedFlag{}
		f.VisitAll(func(v *pflag.Flag) { flags[v.Name] = savedFlag{v.Value.String(), v.Changed} })
		maxPause := 30 * time.Minute
		if v := f.Lookup("sut-llm-max-pause"); v != nil {
			var err error
			maxPause, err = time.ParseDuration(v.Value.String())
			if err != nil || maxPause <= 0 {
				return fmt.Errorf("invalid sut-llm-max-pause")
			}
		}
		path := filepath.Join(dir, "report.json")
		before, _ := os.ReadFile(path)
		err := single(c, args) // all locks/engines close before another round
		for round := 1; *rounds > 0; round++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil && exitCode(err) == 3 {
				return err
			} // never retry failed isolation
			after, readErr := os.ReadFile(path)
			if readErr != nil || sha256.Sum256(before) == sha256.Sum256(after) {
				return err
			}
			r, loadErr := report.Load(ctx, path)
			if loadErr != nil {
				return loadErr
			}
			reason, n, suggested := autoResumeCandidate(r)
			if reason == "budget" {
				return &governorExit{reason: "budget", dest: dir}
			}
			if n == 0 || round > *rounds {
				if reason == "exhausted" {
					return &governorExit{reason: reason, dest: dir}
				}
				return err
			}
			wait := resumeCooldown(suggested, maxPause)
			fmt.Fprintf(cmd.ErrOrStderr(), "auto-resume round %d/%d: waiting %g s for gateway cooldown; retrying %d cases\n", round, *rounds, wait.Seconds(), n)
			if e := resumeSleep(ctx, wait); e != nil {
				return e
			}
			metadata := &result.AutoResume{Auto: true, Round: round, Reason: reason, WaitedSeconds: wait.Seconds()}
			next := newRunCommand(context.WithValue(ctx, autoResumeRoundKey{}, metadata), out)
			next.SetErr(cmd.ErrOrStderr())
			for name, saved := range flags {
				// Automatic rounds select only retryable error/ingest_failed rows;
				// rows outside the manual list remain scored. The committed report
				// already carries the new SUT identity, so no override is needed.
				if name == "resume-cases" || name == "resume-sut-change" {
					continue
				}
				// Persistent backend flags are already applied to the process and
				// retain their dotenv Sources; local flags keep Changed semantics.
				if flag := next.Flags().Lookup(name); flag != nil {
					var e error
					if saved.changed {
						e = next.Flags().Set(name, saved.value)
					} else {
						e = flag.Value.Set(saved.value)
					}
					if e != nil {
						return e
					}
				}
			}
			for name, value := range map[string]string{"resume": dir, "out": dir, "resume-statuses": "error,ingest_failed", "auto-resume-rate-limited": "0"} {
				if e := next.Flags().Set(name, value); e != nil {
					return e
				}
			}
			before = after
			err = next.RunE(next, nil)
		}
		return err
	}
}
