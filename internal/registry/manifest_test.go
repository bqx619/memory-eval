package registry

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalLockManifest(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "input.json"), []byte("abc"), 0600)
	line := fmt.Sprintf(`{"path":"input.json","bytes":3,"sha256":"%x"}`, sha256.Sum256([]byte("abc"))) + "\n"
	lock := `{"dataset_id":"fixture","source_url":"offline","revision":"abc","files_manifest":"files.jsonl","files_total":1}`
	os.WriteFile(filepath.Join(dir, "source.lock.json"), []byte(lock), 0600)
	os.WriteFile(filepath.Join(dir, "files.jsonl"), []byte(line), 0600)
	if e := checkLock(t.Context(), dir, "source.lock.json", true); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "input.json"), []byte("abd"), 0600)
	if e := checkLock(t.Context(), dir, "source.lock.json", true); e == nil {
		t.Fatal("tampered data accepted")
	}
	if e := checkLock(t.Context(), dir, "source.lock.json", false); e != nil {
		t.Fatal("metadata-only", e)
	}
	os.WriteFile(filepath.Join(dir, "files.jsonl"), []byte(line+line), 0600)
	if e := checkLock(t.Context(), dir, "source.lock.json", false); e == nil {
		t.Fatal("count mismatch")
	}
	os.WriteFile(filepath.Join(dir, "source.lock.json"), []byte(strings.Replace(lock, `"files_total":1`, `"files_total":2`, 1)), 0600)
	if e := checkLock(t.Context(), dir, "source.lock.json", false); e == nil {
		t.Fatal("duplicate path")
	}
	os.WriteFile(filepath.Join(dir, "source.lock.json"), []byte(strings.Replace(lock, "files.jsonl", "../outside", 1)), 0600)
	if e := checkLock(t.Context(), dir, "source.lock.json", false); e == nil {
		t.Fatal("nonlocal manifest")
	}
}
