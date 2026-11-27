package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"memeval/internal/config/dotenv"
	"memeval/internal/episodelog"
	"memeval/internal/report"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var parallelEvidence = flag.String("parallel-evidence", "", "retain offline serial/parallel run evidence in scratch")

func TestParallelCLIParity(t *testing.T) {
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	root := t.TempDir()
	if *parallelEvidence != "" {
		root = *parallelEvidence
		if e := os.MkdirAll(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("MEMEVAL_CACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("MEMEVAL_EP_ROOT", filepath.Join(root, "ep"))
	t.Setenv("MEMEVAL_PARALLEL", "")
	put := func(p string, b []byte) {
		t.Helper()
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	project := filepath.Join(root, "echo")
	if e := os.MkdirAll(project, 0700); e != nil {
		t.Fatal(e)
	}
	echo, e := os.ReadFile("../../suts/echo/echo_bridge.py")
	if e != nil {
		t.Fatal(e)
	}
	put(filepath.Join(project, "echo.py"), echo)
	put(filepath.Join(project, "requirements.in"), nil)
	put(filepath.Join(project, "requirements.lock.txt"), nil)
	manifest := filepath.Join(project, "manifest.json")
	put(manifest, []byte(`{"schema_version":"3","name":"parallel-echo","adapter":{"kind":"generic","transport":"jsonrpc-stdio"},"runtime":{"kind":"uv-python","python":"3.12","requirements":"requirements.in","requirements_lock":"requirements.lock.txt"},"process":{"command":["{venv}/bin/python","{project}/echo.py"]},"sandbox":{"backend":"container","network":"none","limits":{"cpus":0.5}},"ready":{"strategy":"sync"}}`))
	cli := func(args ...string) {
		t.Helper()
		var out, errs bytes.Buffer
		if e := runCommand(t.Context(), args, &out, &errs); e != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, e, &out, &errs)
		}
	}
	cli("env", "install", manifest, "--offline")
	source := filepath.Join(root, "pmb.json")
	cli("import", "pmb", "--input", "../../docs/data/pmb_projection.json", "--format", "single", "--out", source)
	s, e := suite.Load(t.Context(), source)
	if e != nil {
		t.Fatal(e)
	}
	if index := s.Index(); len(index) != 1 || index[0].Cases < 8 {
		t.Fatalf("parallel regression fixture must have one suite episode with multiple cases: %+v", index)
	}
	for _, sut := range []string{"bm25", manifest} {
		name := "bm25"
		if sut == manifest {
			name = "echo"
		}
		var expectedCases, expectedSummary string
		for _, n := range []int{1, 4} {
			dest := filepath.Join(root, name+"-"+strconv.Itoa(n))
			cli("run", "--sut", sut, "--suite", source, "--parallel", strconv.Itoa(n), "--run-seed", strings.Repeat("42", 32), "--limit", "8", "--out", dest)
			r, e := report.Load(t.Context(), filepath.Join(dest, "report.json"))
			if e != nil {
				t.Fatal(e)
			}
			if len(r.Cases) != 8 {
				t.Fatal(len(r.Cases))
			}
			raw, e := os.ReadFile(filepath.Join(dest, "cases.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			var lines bytes.Buffer
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
				var c map[string]any
				if e = json.Unmarshal(line, &c); e != nil {
					t.Fatal(e)
				}
				delete(c, "timings")
				b, _ := json.Marshal(c)
				lines.Write(b)
				lines.WriteByte('\n')
			}
			r.Summary.Timings = nil
			for k, v := range r.ByCategory {
				v.Timings = nil
				r.ByCategory[k] = v
			}
			b, _ := json.Marshal([]any{r.Summary, r.ByCategory, r.Scores, r.Billing, r.Warnings})
			if n == 1 {
				expectedCases, expectedSummary = lines.String(), string(b)
			} else if expectedCases != lines.String() || expectedSummary != string(b) {
				t.Fatalf("%s serial/parallel difference\ncases equal: %t\n%s\n%s", name, expectedCases == lines.String(), expectedSummary, b)
			}
			if sut == manifest {
				if r.Logs == nil || r.Logs.Episodes != 9 {
					t.Fatal(r.Logs)
				}
				index, e := os.ReadFile(filepath.Join(dest, "logs/index.jsonl"))
				if e != nil || len(bytes.Split(bytes.TrimSpace(index), []byte{'\n'})) != 9 {
					t.Fatal(e, "incomplete log index")
				}
				overlap := maxLoggedEpisodeOverlap(t, index)
				if (n == 1 && overlap != 1) || (n == 4 && overlap < 2) || overlap > n {
					t.Fatalf("parallel=%d: max concurrent runtime episodes=%d", n, overlap)
				}
				t.Logf("one source episode: parallel=%d, max concurrent runtime episodes=%d", n, overlap)
				var iso struct {
					Container struct {
						Residue struct {
							Clean bool `json:"run_clean"`
						}
					}
				}
				b, _ = json.Marshal(r.Isolation)
				if e = json.Unmarshal(b, &iso); e != nil || !iso.Container.Residue.Clean {
					t.Fatal(e, "run residue")
				}
			}
			t.Logf("%s: %d cases, parallel=%d; %s", name, len(r.Cases), n, dest)
		}
	}
}

func maxLoggedEpisodeOverlap(t *testing.T, index []byte) int {
	t.Helper()
	type boundary struct {
		at    time.Time
		delta int
	}
	var boundaries []boundary
	for _, line := range bytes.Split(bytes.TrimSpace(index), []byte{'\n'}) {
		var record episodelog.Record
		if e := json.Unmarshal(line, &record); e != nil {
			t.Fatal(e)
		}
		if record.Namespace == "describe" {
			continue
		}
		start, e := time.Parse(time.RFC3339Nano, record.StartedAt)
		if e != nil {
			t.Fatal(e)
		}
		end, e := time.Parse(time.RFC3339Nano, record.ClosedAt)
		if e != nil || !end.After(start) {
			t.Fatal("invalid runtime episode interval", record, e)
		}
		boundaries = append(boundaries, boundary{start, 1}, boundary{end, -1})
	}
	slices.SortFunc(boundaries, func(a, b boundary) int {
		if order := a.at.Compare(b.at); order != 0 {
			return order
		}
		return a.delta - b.delta // A close at the same instant precedes a start.
	})
	active, maximum := 0, 0
	for _, b := range boundaries {
		active += b.delta
		maximum = max(maximum, active)
	}
	return maximum
}

func TestParallelFlagAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		env  string
		args []string
		want string
	}{{"4", nil, "4"}, {"4", []string{"--parallel", "2"}, "2"}, {"", nil, "1"}} {
		t.Setenv("MEMEVAL_PARALLEL", tc.env)
		cmd := newRunCommand(t.Context(), io.Discard)
		if e := dotenv.ApplyFlags(cmd.Flags(), tc.args); e != nil {
			t.Fatal(e)
		}
		if e := cmd.ParseFlags(tc.args); e != nil {
			t.Fatal(e)
		}
		if got := cmd.Flags().Lookup("parallel").Value.String(); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
	// No output directory can be created by rejected values.
	for _, n := range []string{"0", "17"} {
		dest := filepath.Join(t.TempDir(), "must-not-exist")
		e := runCommand(t.Context(), []string{"run", "--parallel", n, "--suite", "missing", "--out", dest}, io.Discard, io.Discard)
		if e == nil || !strings.Contains(e.Error(), "parallel must be between 1 and 16") {
			t.Fatal(e)
		}
		if _, e = os.Stat(dest); !os.IsNotExist(e) {
			t.Fatal(e)
		}
	}
}
