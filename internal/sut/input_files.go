package sut

import (
	"path/filepath"
	"strings"
)

// Runtime sources/configuration and dependency locks affect installation identity.
// Documentation and developer tests are retained in the lock inventory only.
func runtimeInput(rel, manifest string) bool {
	base := filepath.Base(rel)
	if base == "UPSTREAM.json" {
		return false
	}
	if rel == manifest {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "tests" || part == "testdata" || strings.HasPrefix(part, "test_") || strings.HasSuffix(part, "_test.go") {
			return false
		}
	}
	if strings.HasPrefix(base, "requirements") || base == "package.json" || base == "package-lock.json" || base == "npm-shrinkwrap.json" {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".sh", ".json", ".toml", ".yaml", ".yml", ".ini", ".cfg", ".lock", ".so", ".dylib", ".wasm", ".node":
		return true
	}
	return false
}
