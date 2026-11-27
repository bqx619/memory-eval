package report

import (
	"encoding/json"
	"fmt"
	"memeval/internal/result"
	"memeval/internal/sandbox/container/governor"
	"os"
	"path/filepath"
)

// Keep immutable per-generation meter files. Totals include all committed
// attempts, including failed/replaced cases, just like evaluator billing.
func (x *ResumeOutput) prepareGovernor(r *result.Report) error {
	fresh := filepath.Join(x.StageDir, "governor")
	if _, err := os.Stat(fresh); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	rows := []governor.Attempt{}
	for _, root := range []string{filepath.Join(x.Dir, "governor"), fresh} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() != governor.MeterFile {
				return nil
			}
			raw, e := os.ReadFile(filepath.Join(filepath.Dir(path), "episode-ips.json"))
			if e != nil {
				return e
			}
			var bindings []governor.Attribution
			if e = json.Unmarshal(raw, &bindings); e != nil {
				return e
			}
			f, e := os.Open(path)
			if e != nil {
				return e
			}
			part, e := governor.ReadAttempts(f)
			f.Close()
			if e != nil {
				return e
			}
			rows = append(rows, governor.Attribute(part, bindings)...)
			return nil
		})
		if err != nil {
			return err
		}
	}
	var prior, current governor.Billing
	raw, _ := json.Marshal(x.Original.Billing.SUT)
	json.Unmarshal(raw, &prior)
	raw, _ = json.Marshal(r.Billing.SUT)
	json.Unmarshal(raw, &current)
	r.Billing.SUT = governor.AggregateAttributed(rows, prior.CircuitOpens+current.CircuitOpens)
	r.Summary.Billing = r.Billing
	raw, err := json.Marshal(r.Isolation.Container)
	if err != nil {
		return err
	}
	var container map[string]any
	if err = json.Unmarshal(raw, &container); err != nil {
		return err
	}
	if gov, ok := container["governor"].(map[string]any); ok {
		gov["meter_file"] = fmt.Sprintf("governor/resume-%d/%s", x.Original.ResumeCount+1, governor.MeterFile)
	}
	r.Isolation.Container = container
	return nil
}
func (x *ResumeOutput) commitGovernor() error {
	src := filepath.Join(x.StageDir, "governor")
	if _, e := os.Stat(src); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	root := filepath.Join(x.Dir, "governor")
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	return os.Rename(src, filepath.Join(root, fmt.Sprintf("resume-%d", x.Original.ResumeCount+1)))
}
