package sut

import (
	"fmt"
	"memeval/internal/sandbox"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m *Manifest) validateRuntime() error {
	if _, err := m.cacheLinks(); err != nil {
		return err
	}
	if m.Runtime.Base == "" && m.Runtime.Kind != "binary" {
		m.Runtime.Base = m.Runtime.Kind
	}
	if m.Runtime.Kind != "binary" && m.Runtime.Base != "uv-python" && m.Runtime.Base != "uv-python-trixie" && m.Runtime.Base != "node" {
		return fmt.Errorf("unknown runtime.base")
	}
	if m.Runtime.Kind == "node" && m.Runtime.Base != "node" || m.Runtime.Kind == "uv-python" && m.Runtime.Base == "node" {
		return fmt.Errorf("runtime.base does not match kind")
	}
	for _, pkg := range m.Runtime.Apt {
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(=[a-zA-Z0-9:+.~_-]+)?$`).MatchString(pkg) {
			return fmt.Errorf("invalid apt package")
		}
	}
	for _, idx := range m.Runtime.Indexes {
		u, e := url.Parse(idx.URL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("index requires credential-free http(s) URL")
		}
		if idx.Strategy != "" && idx.Strategy != "first-index" && idx.Strategy != "unsafe-first-match" && idx.Strategy != "unsafe-best-match" {
			return fmt.Errorf("invalid index strategy")
		}
	}
	strategy := ""
	for _, idx := range m.Runtime.Indexes {
		if strategy != "" && idx.Strategy != "" && idx.Strategy != strategy {
			return fmt.Errorf("indexes must agree on strategy")
		}
		if idx.Strategy != "" {
			strategy = idx.Strategy
		}
	}
	for _, p := range append([]string{m.Runtime.Requirements, m.Runtime.RequirementsLock, m.Runtime.PackageLock, m.Runtime.Constraints}, m.Runtime.Assets...) {
		if p != "" && (filepath.IsAbs(p) || filepath.Clean(p) == ".." || strings.HasPrefix(filepath.Clean(p), "../")) {
			return fmt.Errorf("runtime input must be project relative")
		}
	}

	for _, command := range m.Runtime.Prepare {
		if len(command) == 0 {
			return fmt.Errorf("prepare requires a command")
		}
		for _, arg := range command {
			if strings.ContainsAny(arg, "\x00\r\n") {
				return fmt.Errorf("invalid prepare argument")
			}
			if p, ok := strings.CutPrefix(arg, "{project}/"); ok && (filepath.IsAbs(p) || filepath.Clean(p) == ".." || strings.HasPrefix(filepath.Clean(p), "../")) {
				return fmt.Errorf("prepare input escapes project")
			}
		}
	}

	if m.Adapter.Kind == "http-bridge" {
		if m.Bridge == nil || m.Bridge.BaseURL == "" || m.Bridge.Routes == "" || (m.Runtime.Kind != "uv-python" && m.Runtime.Kind != "node") {
			return fmt.Errorf("http-bridge requires Python/Node and bridge.base_url/routes")
		}
		if filepath.IsAbs(m.Bridge.Routes) || strings.Contains(m.Bridge.Routes, "..") {
			return fmt.Errorf("HTTP routes must be project relative")
		}
		for _, key := range m.Bridge.HeadersEnv {
			if !slices.Contains(m.Process.EnvPassthrough, key) && m.LLM.Env[key] == "" {
				return fmt.Errorf("HTTP header env must be explicitly declared in env_passthrough")
			}
			if e := m.validKey(key); e != nil {
				return e
			}
		}
	}

	if m.Adapter.Kind == "python-bridge" {
		if m.Runtime.Kind != "uv-python" || m.Bridge == nil || m.Bridge.Module == "" || m.Bridge.Class == "" {
			return fmt.Errorf("python-bridge requires Python runtime and bridge.module/class")
		}
	}
	switch m.Runtime.Kind {
	case "uv-python":
		if m.Runtime.Python == "" || m.Runtime.Requirements == "" {
			return fmt.Errorf("uv-python requires python and requirements")
		}
	case "node":
		if !versionPattern.MatchString(m.Runtime.Version) {
			return fmt.Errorf("node requires pinned x.y.z version")
		}
	case "binary":
		if m.Runtime.Version == "" || m.Runtime.Executable == "" || len(m.Runtime.Sources) == 0 {
			return fmt.Errorf("binary requires version, executable and platform sources")
		}
	default:
		return fmt.Errorf("unsupported runtime kind")
	}
	if m.Runtime.Executable != "" && (filepath.IsAbs(m.Runtime.Executable) || strings.Contains(m.Runtime.Executable, "..")) {
		return fmt.Errorf("runtime executable must be relative")
	}
	for _, a := range m.Runtime.Sources {
		if a.URL == "" || !shaPattern.MatchString(a.SHA256) {
			return fmt.Errorf("runtime source requires URL and SHA256")
		}
	}
	return nil
}

// Cache link targets live in the image, and need not exist on the host.
func (m *Manifest) cacheLinks() (map[string]string, error) {
	links := map[string]string{}
	for relative, target := range m.Runtime.CacheLinks {
		target = strings.ReplaceAll(target, "{env}", ImageRoot)
		if strings.ContainsAny(target, "{}") {
			return nil, fmt.Errorf("cache_links only supports {env} in target")
		}
		links[relative] = target
	}
	if err := sandbox.ValidateCacheLinks(ImageRoot, links); err != nil {
		return nil, err
	}
	return links, nil
}
func platform() string { return runtime.GOOS + "-" + runtime.GOARCH }
func (l *Lock) AssetPath(m *Manifest, p string) string {
	if l.EnvDir != "" {
		if v, ok := l.Assets[p]; ok {
			return filepath.Join(l.RuntimeRoot(), v)
		}
	}
	return m.Resolve(p)
}

func (m *Manifest) needsBridge() bool {
	if m.Adapter.Kind == "python-bridge" || m.Adapter.Kind == "http-bridge" || m.Runtime.Kind == "node" {
		return true
	}
	for _, a := range m.Process.Command {
		if strings.Contains(a, "{env}/bridge/") {
			return true
		}
	}
	return false
}
