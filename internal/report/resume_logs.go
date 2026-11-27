package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"memeval/internal/episodelog"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strings"
)

// prepareLogs only modifies staging. commitLogs is invoked before report.json
// is published. Existing episode files are never renamed or overwritten.
func (x *ResumeOutput) prepareLogs(r *result.Report) ([]byte, error) {
	stage := filepath.Join(x.StageDir, "logs")
	info, err := os.Lstat(stage)
	if os.IsNotExist(err) {
		if x.Original.Logs != nil && !x.Original.Logs.Disabled {
			summary := *x.Original.Logs
			r.Logs = &summary
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("staged logs must be a directory")
	}
	err = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("episode logs must contain only directories and regular files")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(x.Dir, "logs")
	info, err = os.Lstat(parent)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && !info.IsDir() {
		return nil, fmt.Errorf("existing logs must be a directory")
	}
	name := fmt.Sprintf("resume-%d", x.Original.ResumeCount+1)
	if _, err = os.Lstat(filepath.Join(parent, name)); !os.IsNotExist(err) {
		return nil, fmt.Errorf("resume log destination already exists or unavailable")
	}
	index := filepath.Join(parent, "index.jsonl")
	var previous []byte
	if info, err = os.Lstat(index); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("log index must be a regular file")
		}
		previous, err = os.ReadFile(index)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	fresh, err := os.ReadFile(filepath.Join(stage, "index.jsonl"))
	if err != nil {
		return nil, err
	}
	var merged bytes.Buffer
	merged.Write(previous)
	err = eachLine(fresh, func(line []byte) error {
		var row episodelog.Record
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		if !filepath.IsLocal(row.Dir) {
			return fmt.Errorf("invalid episode log directory")
		}
		row.Dir = filepath.ToSlash(filepath.Join(name, row.Dir))
		return json.NewEncoder(&merged).Encode(row)
	})
	if err != nil {
		return nil, err
	}
	if r.Logs == nil {
		return nil, fmt.Errorf("staged logs missing report summary")
	}
	summary := *r.Logs
	if x.Original.Logs != nil && !x.Original.Logs.Disabled {
		old := x.Original.Logs
		summary.Episodes += old.Episodes
		summary.Files += old.Files
		summary.Bytes += old.Bytes
		summary.TruncatedFiles += old.TruncatedFiles
		summary.KeptEPSUnredactedStderr += old.KeptEPSUnredactedStderr
	} else {
		summary.Files++
	} // new parent index in addition to the attempt index
	summary.Bytes += int64(merged.Len() - len(previous))
	summary.Dir = "logs"
	summary.Disabled = false
	r.Logs = &summary
	// effective_config paths are relative to the report, not the transient stage.
	raw, err := json.Marshal(r.Isolation.EffectiveConfig)
	if err != nil {
		return nil, err
	}
	var entries []map[string]any
	if err = json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if path, ok := entry["log_dir"].(string); ok && strings.HasPrefix(path, "logs/") {
			entry["log_dir"] = filepath.ToSlash(filepath.Join("logs", name, strings.TrimPrefix(path, "logs/")))
		}
	}
	if entries != nil {
		r.Isolation.EffectiveConfig = entries
	}
	return merged.Bytes(), nil
}
func (x *ResumeOutput) commitLogs(index []byte) error {
	if index == nil {
		return nil
	}
	dir := filepath.Join(x.Dir, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	target := filepath.Join(dir, fmt.Sprintf("resume-%d", x.Original.ResumeCount+1))
	if err := os.Rename(filepath.Join(x.StageDir, "logs"), target); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".index-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(index); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(dir, "index.jsonl"))
}
