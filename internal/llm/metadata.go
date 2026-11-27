package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// ModelPin records requested and returned identifiers without pretending that
// an upstream alias is immutable. Replay includes the original recorded model.
type ModelPin struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ModelReturned string `json:"model_returned,omitempty"`
}

func (e *Engine) observeModel(r Request, response Response) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models[ModelPin{r.Provider, r.Model, response.ModelReturned}] = struct{}{}
}

// ReportMetadata is called after all evaluator calls and cassette writes finish.
// Hash errors must fail reporting rather than silently publish a null/stale hash.
func (e *Engine) ReportMetadata() (map[string]any, error) {
	e.mu.Lock()
	models := make([]ModelPin, 0, len(e.models))
	for model := range e.models {
		models = append(models, model)
	}
	e.mu.Unlock()
	slices.SortFunc(models, func(a, b ModelPin) int {
		for _, pair := range [][2]string{{a.Provider, b.Provider}, {a.Model, b.Model}, {a.ModelReturned, b.ModelReturned}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	digest, count, err := cassetteSet(e.cfg.CassetteDir)
	if err != nil {
		return nil, fmt.Errorf("hash evaluator cassette directory: %w", err)
	}
	metadata := e.Configuration()
	metadata["models_pinned"] = models
	metadata["cassette_set"] = digest
	metadata["cassette_set_algorithm"] = "sha256(sorted JSONL {path,sha256} of all regular files; relative slash paths)"
	metadata["cassette_files"] = count
	return metadata, nil
}

// Paths are sorted globally (not merely WalkDir traversal order). File bytes,
// additions, removals and renames affect the hash; absolute path/mtime/mode do not.
// A nonexistent directory is an empty set; symlinks and special files are errors.
func cassetteSet(dir string) (string, int, error) {
	paths := []string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if path == dir && os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular cassette entry: %s", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	slices.Sort(paths)
	h := sha256.New()
	for _, path := range paths {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return "", 0, err
		}
		content := sha256.New()
		_, readErr := io.Copy(content, f)
		closeErr := f.Close()
		if readErr != nil {
			return "", 0, readErr
		}
		if closeErr != nil {
			return "", 0, closeErr
		}
		if err := json.NewEncoder(h).Encode(struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}{path, hex.EncodeToString(content.Sum(nil))}); err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), len(paths), nil
}
