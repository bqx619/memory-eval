package report

import (
	"encoding/json"
	"fmt"
	"memeval/internal/result"
	"os"
	"path/filepath"
)

func (x *ResumeOutput) checkStagedArtifacts() error {
	files, err := os.ReadDir(x.StageDir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			switch file.Name() {
			case "governor", "logs", "selftest":
				continue
			}
		} else if file.Type().IsRegular() {
			continue
		}
		return fmt.Errorf("unexpected staged artifact: %s", file.Name())
	}
	_, err = x.selftestFiles()
	return err
}

// Records are timestamped flat files. Do not follow symlinks or overwrite an
// existing record, even when its contents happen to match.
func (x *ResumeOutput) selftestFiles() ([]os.DirEntry, error) {
	src := filepath.Join(x.StageDir, "selftest")
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("unexpected staged artifact: selftest (must be a directory)")
	}
	dst := filepath.Join(x.Dir, "selftest")
	if info, err = os.Lstat(dst); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("selftest destination must be a directory")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	files, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []os.DirEntry{} // distinguish an empty directory from absence
	}
	for _, file := range files {
		if !file.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected staged artifact: selftest/%s", file.Name())
		}
		if _, err = os.Lstat(filepath.Join(dst, file.Name())); err == nil {
			return nil, fmt.Errorf("selftest record collision: %s", file.Name())
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return files, nil
}

// Only relocate evidence produced in this generation; cached host evidence and
// unknown metadata fields remain unchanged.
func (x *ResumeOutput) prepareSelftest(r *result.Report) error {
	raw, err := json.Marshal(r.Isolation.Selftest)
	if err != nil {
		return err
	}
	var evidence map[string]json.RawMessage
	if err = json.Unmarshal(raw, &evidence); err != nil {
		return err
	}
	var path string
	if json.Unmarshal(evidence["path"], &path) != nil {
		return nil
	}
	if filepath.Clean(filepath.Dir(path)) == filepath.Join(x.StageDir, "selftest") {
		evidence["path"], err = json.Marshal(filepath.Join(x.Dir, "selftest", filepath.Base(path)))
		if err != nil {
			return err
		}
		r.Isolation.Selftest = evidence
	}
	return nil
}

func (x *ResumeOutput) commitSelftest() error {
	files, err := x.selftestFiles()
	if err != nil || files == nil {
		return err
	}
	src, dst := filepath.Join(x.StageDir, "selftest"), filepath.Join(x.Dir, "selftest")
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	// Staging is a sibling of the output on the same filesystem. Link then
	// unlink gives a no-replace move; Rename would silently replace a collision.
	linked := []string{}
	for _, file := range files {
		if err := os.Link(filepath.Join(src, file.Name()), filepath.Join(dst, file.Name())); err != nil {
			for _, name := range linked {
				_ = os.Remove(filepath.Join(dst, name))
			}
			return fmt.Errorf("commit selftest %s (destination must not exist): %w", file.Name(), err)
		}
		linked = append(linked, file.Name())
	}
	for _, name := range linked {
		if err := os.Remove(filepath.Join(src, name)); err != nil {
			return err
		}
	}
	return os.Remove(src)
}
