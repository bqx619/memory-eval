package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"memeval/internal/procexec"
	"memeval/internal/result"
	"memeval/internal/suite"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

func host(ctx context.Context) result.Host {
	h := result.Host{OS: runtime.GOOS, Arch: runtime.GOARCH, Kernel: "unavailable"}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if b, err := procexec.Output(exec.CommandContext(ctx, "uname", "-r")); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	return h
}
func environment() result.Environment {
	e := result.Environment{GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, CodeSHA256: "unavailable"}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return e
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	paths := []string{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && (dir == "cmd" || strings.HasSuffix(path, ".go")) {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return e
		}
	}
	sort.Strings(paths)
	entries := [][2]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		h := sha256.Sum256(b)
		entries = append(entries, [2]string{filepath.ToSlash(rel), hex.EncodeToString(h[:])})
	}
	if h, err := suite.Digest(entries); err == nil {
		e.CodeSHA256 = h
	}
	return e
}
