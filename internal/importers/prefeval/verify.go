package prefeval

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Verify checks the entire pinned extracted tree; no network or archive extraction occurs.
func Verify(ctx context.Context, input string) (string, error) {
	root := filepath.Dir(filepath.Clean(input))
	var lock struct {
		Revision string
		Manifest string `json:"files_manifest"`
		Total    int    `json:"files_total"`
	}
	if e := readJSON(filepath.Join(root, "source.lock.json"), &lock); e != nil {
		return "", e
	}
	if lock.Revision != Revision || lock.Manifest != "files.sha256.jsonl" {
		return "", fmt.Errorf("PrefEval lock differs from pinned revision/manifest")
	}
	b, e := os.ReadFile(filepath.Join(root, lock.Manifest))
	if e != nil {
		return "", e
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	seen := map[string]bool{}
	for sc.Scan() {
		if e = ctx.Err(); e != nil {
			return "", e
		}
		var row struct {
			Path, SHA256 string
			Bytes        int64
		}
		if e = json.Unmarshal(sc.Bytes(), &row); e != nil {
			return "", e
		}
		if !filepath.IsLocal(row.Path) || seen[row.Path] {
			return "", fmt.Errorf("invalid/duplicate manifest path %q", row.Path)
		}
		seen[row.Path] = true
		f, e := os.Open(filepath.Join(root, row.Path))
		if e != nil {
			return "", e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		f.Close()
		if e != nil {
			return "", e
		}
		if n != row.Bytes || fmt.Sprintf("%x", h.Sum(nil)) != row.SHA256 {
			return "", fmt.Errorf("PrefEval manifest mismatch: %s", row.Path)
		}
	}
	if e = sc.Err(); e != nil {
		return "", e
	}
	if len(seen) != lock.Total {
		return "", fmt.Errorf("manifest count differs")
	}
	// Refuse extra importable inputs not covered by the manifest.
	e = filepath.WalkDir(input, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if !seen[filepath.ToSlash(rel)] {
			return fmt.Errorf("unlocked input %s", rel)
		}
		return nil
	})
	return hash(b), e
}
