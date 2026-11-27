package report

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"memeval/internal/identity"
	"memeval/internal/llm"
	"memeval/internal/result"
	"memeval/internal/scoring/judge/inputlog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

// ResumeOutput stages a complete replacement, retaining an immutable prior
// generation. The old report is not touched on preflight/execution failures.
type ResumeOutput struct {
	Auto           *result.AutoResume
	Cases          *int
	AllowSUTChange bool
	Original       *result.Report
	Dir, StageDir  string
	files          map[string][]byte
	caseLines      map[string][]byte
	judgeRows      []inputlog.Record
	lock           *os.File
	started        time.Time
	committed      bool
}

func LoadResume(ctx context.Context, dir string) (*ResumeOutput, error) {
	r, err := Load(ctx, filepath.Join(dir, "report.json"))
	if err != nil {
		return nil, err
	}
	if j := r.Protocol.Judge; j != nil && j.Kind != "decision" {
		return nil, fmt.Errorf("resume rejects legacy judge kind %q; Jev-only requires protocol.judge.kind=decision", j.Kind)
	}
	x := &ResumeOutput{Original: r, Dir: dir, files: map[string][]byte{}, caseLines: map[string][]byte{}}
	// Only run-owned artifacts; never traverse cassettes, SUT data or history.
	for _, name := range []string{"report.json", "cases.jsonl", "cases.csv", "predictions.jsonl", "report.html", "llm_ledger.jsonl", "judge_inputs.jsonl", "prompts.jsonl"} {
		path := filepath.Join(dir, name)
		info, e := os.Lstat(path)
		if os.IsNotExist(e) && name != "report.json" && name != "cases.jsonl" {
			continue
		}
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("resume artifact must be a regular file: %s", name)
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		x.files[name] = b
	}
	if err = json.Unmarshal(x.files["report.json"], r); err != nil {
		return nil, err
	}
	var reportWire struct {
		Cases []json.RawMessage `json:"cases"`
	}
	if err = json.Unmarshal(x.files["report.json"], &reportWire); err != nil {
		return nil, err
	}
	i := 0
	err = eachLine(x.files["cases.jsonl"], func(line []byte) error {
		var row result.Case
		if e := json.Unmarshal(line, &row); e != nil {
			return e
		}
		if i >= len(r.Cases) || row.SampleID != r.Cases[i].SampleID || !sameJSON(line, reportWire.Cases[i]) {
			return fmt.Errorf("resume report/cases.jsonl mismatch at row %d", i+1)
		}
		if _, exists := x.caseLines[row.SampleID]; exists {
			return fmt.Errorf("duplicate resume sample")
		}
		x.caseLines[row.SampleID] = bytes.Clone(line)
		i++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if i != len(r.Cases) {
		return nil, fmt.Errorf("resume report/cases.jsonl count mismatch")
	}
	if r.Billing.Evaluator != nil {
		var billing llm.Billing
		b, e := json.Marshal(r.Billing.Evaluator)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &billing); e != nil {
			return nil, e
		}
		calls := 0
		if e = eachLine(x.files["llm_ledger.jsonl"], func(line []byte) error {
			var entry llm.LedgerEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				return err
			}
			if entry.RunID != r.RunID {
				return fmt.Errorf("resume ledger run identity mismatch")
			}
			calls++
			return nil
		}); e != nil {
			return nil, e
		}
		if calls != billing.Calls {
			return nil, fmt.Errorf("resume evaluator ledger/report call count mismatch")
		}
	}
	if r.JudgeInputs != nil {
		x.judgeRows, err = inputlog.Read(dir, *r.JudgeInputs)
		if err != nil {
			return nil, err
		}
	}
	return x, nil
}

func eachLine(b []byte, fn func([]byte) error) error {
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 4096), 64<<20)
	for s.Scan() {
		if err := fn(s.Bytes()); err != nil {
			return err
		}
	}
	return s.Err()
}
func sameJSON(a, b []byte) bool {
	decode := func(raw []byte) any {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return nil
		}
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

func (x *ResumeOutput) unchanged() error {
	for name, old := range x.files {
		current, err := os.ReadFile(filepath.Join(x.Dir, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(current, old) {
			return fmt.Errorf("resume source changed concurrently: %s", name)
		}
	}
	return nil
}

// Begin is intentionally called only after all public-input hashes validate.
func (x *ResumeOutput) Begin() error {
	f, err := os.OpenFile(filepath.Join(x.Dir, ".resume.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("resume lock (another resume or interrupted attempt): %w", err)
	}
	x.lock = f
	x.started = time.Now()
	if _, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		return err
	}
	if err = x.unchanged(); err != nil {
		return err
	}
	history := filepath.Join(x.Dir, "resume-history", fmt.Sprintf("%04d", x.Original.ResumeCount+1))
	if _, err = os.Lstat(history); !os.IsNotExist(err) {
		return fmt.Errorf("resume history destination already exists or is unreadable; inspect the prior generation before retrying")
	}
	x.StageDir, err = os.MkdirTemp(filepath.Dir(x.Dir), ".memeval-resume-*")
	return err
}
func (x *ResumeOutput) Close() {
	if x.StageDir != "" {
		// An interrupted resume may already have incurred cost. Never discard
		// its attempt ledger/prompts just because no merged report was committed.
		files, _ := os.ReadDir(x.StageDir)
		keep := false
		if !x.committed {
			for _, file := range files {
				if info, err := file.Info(); err == nil && info.Size() > 0 {
					keep = true
				}
			}
		}
		if keep {
			attempts := filepath.Join(x.Dir, "resume-attempts")
			if os.MkdirAll(attempts, 0700) == nil {
				_ = os.Rename(x.StageDir, filepath.Join(attempts, filepath.Base(x.StageDir)))
			}
			// If rename fails, retain the sibling staging directory for recovery.
		} else {
			os.RemoveAll(x.StageDir)
		}
	}
	if x.lock != nil {
		x.lock.Close()
		os.Remove(filepath.Join(x.Dir, ".resume.lock"))
		x.lock = nil
	}
}

func (x *ResumeOutput) Commit(ctx context.Context, r *result.Report, selected func(string) bool, statuses []result.Status) error {
	if x.StageDir == "" || x.lock == nil {
		return fmt.Errorf("resume output not staged")
	}
	if err := x.unchanged(); err != nil {
		return err
	}
	if r.RunID != x.Original.RunID || r.RunSeed != x.Original.RunSeed || r.SuiteSHA256 != x.Original.SuiteSHA256 || x.Original.Interruption == nil && len(r.Cases) != len(x.Original.Cases) {
		return fmt.Errorf("resume output identity mismatch")
	}
	// Reject unsupported entries and selftest collisions before creating history
	// or moving any artifacts, so a rejected generation remains retryable.
	if err := x.checkStagedArtifacts(); err != nil {
		return err
	}
	redone := 0
	executed := map[string]bool{}
	oldCases := map[string]result.Case{}
	for _, c := range x.Original.Cases {
		oldCases[c.SampleID] = c
	}
	seen := map[string]bool{}
	for i, c := range r.Cases {
		old, exists := oldCases[c.SampleID]
		if !exists {
			if x.Original.Interruption == nil {
				return fmt.Errorf("resume unexpected new sample")
			}
			redone++
			executed[c.SampleID] = true
			continue
		}
		seen[c.SampleID] = true
		if c.SampleID != old.SampleID || c.PublicInputSHA256 != old.PublicInputSHA256 {
			return fmt.Errorf("resume output public input mismatch")
		}
		if selected(c.SampleID) && !reflect.DeepEqual(c, old) {
			redone++
			executed[c.SampleID] = true
		} else {
			b, err := json.Marshal(c)
			oldB, _ := json.Marshal(old)
			if err != nil || !bytes.Equal(b, oldB) {
				return fmt.Errorf("resume changed a reused case")
			}
			// Keep even older/unknown JSON fields on the reused case.
			r.Cases[i].PreservedJSON = x.caseLines[c.SampleID]
		}
	}
	if len(seen) != len(oldCases) {
		return fmt.Errorf("resume dropped previous samples")
	}
	if err := x.prepareGovernor(r); err != nil {
		return err
	}
	path := filepath.Join("resume-history", fmt.Sprintf("%04d", x.Original.ResumeCount+1))
	var attemptEvaluator any = r.EvaluatorLLM
	if metadata, ok := r.EvaluatorLLM.(map[string]any); ok {
		attemptEvaluator = maps.Clone(metadata)
	}
	attempt := result.ResumeAttempt{ResumedAtUTC: x.started.UTC().Format(time.RFC3339Nano), RedoneCases: redone, Statuses: statuses, HistoryPath: filepath.ToSlash(path), Billing: r.Billing.Evaluator, Reader: r.Reader, EvaluatorLLM: attemptEvaluator}
	attempt.Cases = x.Cases
	if x.AllowSUTChange {
		previous, current := resumeSUTIdentity(x.Original.SUT, true), resumeSUTIdentity(r.SUT, false)
		if !reflect.DeepEqual(previous, current) {
			attempt.SUTChange = &result.SUTChange{Previous: previous, Current: current}
		}
	}
	if x.Auto != nil {
		metadata := *x.Auto
		metadata.CasesRetried = redone
		for _, c := range r.Cases {
			if executed[c.SampleID] && c.Status == result.Scored {
				metadata.CasesRecovered++
			}
		}
		attempt.AutoResume = &metadata
	}
	r.ResumeCount = x.Original.ResumeCount + 1
	r.ResumeHistory = append(slices.Clone(x.Original.ResumeHistory), attempt)
	// Later automatic/manual retries still contain preserved rows from earlier
	// identities. Keep each committed change visible in the current report.
	for i, previousAttempt := range r.ResumeHistory {
		if change := previousAttempt.SUTChange; change != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Resume %d replaced the SUT identity for %d cases: %.12s → %.12s; preserved rows keep the previous identity (%s)", i+1, previousAttempt.RedoneCases, change.Previous.ManifestSHA256, change.Current.ManifestSHA256, previousAttempt.HistoryPath))
		}
	}
	r.WallSeconds += x.Original.WallSeconds
	r.Warnings = append(r.Warnings, "Resumed results mix preserved case rows and selected retries; cumulative evaluator billing includes committed run/resume ledgers; interrupted attempt logs remain separate; prior configurations and artifacts are in resume-history")
	// Attempt-level logs are append-only. A later retry never erases prior spend.
	entries := []llm.LedgerEntry{}
	for _, name := range []string{"llm_ledger.jsonl", "prompts.jsonl"} {
		fresh, err := os.ReadFile(filepath.Join(x.StageDir, name))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		combined := append(bytes.Clone(x.files[name]), fresh...)
		if name == "llm_ledger.jsonl" {
			if err := eachLine(combined, func(line []byte) error {
				var e llm.LedgerEntry
				if err := json.Unmarshal(line, &e); err != nil {
					return err
				}
				entries = append(entries, e)
				return nil
			}); err != nil {
				return err
			}
		}
		if len(combined) > 0 {
			if err := os.WriteFile(filepath.Join(x.StageDir, name), combined, 0600); err != nil {
				return err
			}
		}
	}
	if len(entries) > 0 {
		r.Billing.Evaluator = llm.BillingEntries(entries, "")
		purposes := map[string]bool{}
		for p := range x.Original.Billing.EvaluatorByPurpose {
			purposes[p] = true
		}
		for p := range r.Billing.EvaluatorByPurpose {
			purposes[p] = true
		}
		for _, e := range entries {
			purposes[e.Purpose] = true
		}
		r.Billing.EvaluatorByPurpose = map[string]any{}
		for p := range purposes {
			r.Billing.EvaluatorByPurpose[p] = llm.BillingEntries(entries, p)
		}
		r.Summary.Billing = r.Billing
	}
	if m, ok := r.EvaluatorLLM.(map[string]any); ok {
		pins := []llm.ModelPin{}
		for _, meta := range []any{x.Original.EvaluatorLLM, r.EvaluatorLLM} {
			var models struct {
				Pins []llm.ModelPin `json:"models_pinned"`
			}
			b, err := json.Marshal(meta)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &models); err != nil {
				return err
			}
			for _, pin := range models.Pins {
				if !slices.Contains(pins, pin) {
					pins = append(pins, pin)
				}
			}
		}
		slices.SortFunc(pins, func(a, b llm.ModelPin) int {
			ab, _ := json.Marshal(a)
			bb, _ := json.Marshal(b)
			return bytes.Compare(ab, bb)
		})
		m["models_pinned"] = pins
	}
	if r.JudgeInputs != nil || x.Original.JudgeInputs != nil {
		rows := []inputlog.Record{}
		for _, row := range x.judgeRows {
			if !executed[identity.SampleID(row.CaseID, row.RepeatIndex)] {
				rows = append(rows, row)
			}
		}
		if r.JudgeInputs != nil {
			fresh, err := inputlog.Read(x.StageDir, *r.JudgeInputs)
			if err != nil {
				return err
			}
			rows = append(rows, fresh...)
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(x.StageDir, inputlog.Filename), b.Bytes(), 0600); err != nil {
			return err
		}
		if r.Protocol.Judge != nil {
			templates := map[string]inputlog.Template{}
			for _, row := range rows {
				name, sha, _ := strings.Cut(row.Recipe.TemplateID, "@")
				templates[name] = inputlog.Template{ID: name, SHA256: sha}
			}
			r.Protocol.Judge.Templates = templates
			// The official block is serialized through an interface; keep its copy aligned.
			raw, _ := json.Marshal(r.Scores.Official)
			var off result.Official
			if json.Unmarshal(raw, &off) == nil {
				off.Judge = r.Protocol.Judge
				r.Scores.Official = off
			}
		}
		r.JudgeInputs = &inputlog.Artifact{Path: inputlog.Filename, SHA256: fmt.Sprintf("%x", sha256.Sum256(b.Bytes())), Rows: len(rows), SchemaVersion: inputlog.Version}
	}
	logIndex, err := x.prepareLogs(r)
	if err != nil {
		return err
	}
	if err := x.prepareSelftest(r); err != nil {
		return err
	}
	if err := Save(ctx, r, x.StageDir); err != nil {
		return err
	}
	// Preserve the exact old JSONL bytes for reused rows (including formatting).
	fresh, err := os.ReadFile(filepath.Join(x.StageDir, "cases.jsonl"))
	if err != nil {
		return err
	}
	var lines bytes.Buffer
	err = eachLine(fresh, func(line []byte) error {
		var c result.Case
		if err := json.Unmarshal(line, &c); err != nil {
			return err
		}
		if !executed[c.SampleID] {
			line = x.caseLines[c.SampleID]
		}
		lines.Write(line)
		lines.WriteByte('\n')
		return nil
	})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(x.StageDir, "cases.jsonl"), lines.Bytes(), 0600); err != nil {
		return err
	}
	if err = x.unchanged(); err != nil {
		return err
	}
	if err = x.checkStagedArtifacts(); err != nil {
		return err
	}
	history := filepath.Join(x.Dir, path)
	if err = os.MkdirAll(filepath.Dir(history), 0700); err != nil {
		return err
	}
	if err = os.Mkdir(history, 0700); err != nil {
		return fmt.Errorf("resume history already exists or unavailable: %w", err)
	}
	for name, data := range x.files {
		if err = os.WriteFile(filepath.Join(history, name), data, 0600); err != nil {
			return err
		}
	}
	if err := x.commitGovernor(); err != nil {
		return err
	}
	if err := x.commitLogs(logIndex); err != nil {
		return err
	}
	if err := x.commitSelftest(); err != nil {
		return err
	}
	files, err := os.ReadDir(x.StageDir)
	if err != nil {
		return err
	}
	// Publish report.json last as the generation commit marker. If interrupted
	// between renames, LoadResume rejects mismatched cases; history can restore it.
	slices.SortFunc(files, func(a, b os.DirEntry) int {
		if a.Name() == "report.json" {
			return 1
		}
		if b.Name() == "report.json" {
			return -1
		}
		return bytes.Compare([]byte(a.Name()), []byte(b.Name()))
	})
	for _, file := range files {
		if !file.Type().IsRegular() {
			return fmt.Errorf("unexpected staged artifact: %s", file.Name())
		}
		if err = os.Rename(filepath.Join(x.StageDir, file.Name()), filepath.Join(x.Dir, file.Name())); err != nil {
			return err
		}
	}
	x.committed = true
	return nil
}

func resumeSUTIdentity(s result.SUT, legacy bool) result.SUTIdentity {
	snapshot := s.SourceSnapshotSHA256
	// Older reports recorded the manifest's bridge config only in describe.
	// New reports take this value directly from the manifest, not the adapter.
	if snapshot == nil && legacy {
		if value, ok := s.Describe.Config["source_snapshot_sha256"].(string); ok && value != "" {
			snapshot = new(value)
		}
	}
	return result.SUTIdentity{ManifestSHA256: s.ManifestSHA256, EnvLockSHA256: s.EnvLockSHA256, Variant: s.Variant, SourceSnapshotSHA256: snapshot}
}
