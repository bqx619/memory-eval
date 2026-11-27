package sut

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"strings"
)

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}
type SidecarImage struct {
	Name     string `json:"name"`
	Ref      string `json:"ref"`
	ID       string `json:"id"`
	Digest   string `json:"digest"`
	Platform string `json:"platform"`
}
type Lock struct {
	SidecarImages           []SidecarImage    `json:"sidecar_images,omitempty"`
	RuntimeActualVersion    string            `json:"runtime_actual_version"`
	FrameworkVersion        string            `json:"framework_version"`
	InventoryVersion        int               `json:"inventory_version"`
	LockPlatformSource      string            `json:"lock_platform_source"`
	ImageRoot               string            `json:"image_root,omitempty"`
	Inputs                  map[string]string `json:"inputs,omitempty"`
	HostPlatform            Platform          `json:"host_platform"`
	LockPlatform            Platform          `json:"lock_platform"`
	Orphan                  bool              `json:"orphan,omitempty"`
	Image                   sandbox.Image     `json:"image,omitzero"`
	PackageLockSHA256       string            `json:"package_lock_sha256,omitempty"`
	Version                 int               `json:"version"`
	Name                    string            `json:"name"`
	InputSHA256             string            `json:"input_sha256"`
	LockSHA256              string            `json:"lock_sha256"`
	EnvDir                  string            `json:"env_dir"`
	RuntimeKind             string            `json:"runtime_kind"`
	RuntimeVersion          string            `json:"runtime_version"`
	Executable              string            `json:"executable"`
	PythonVersion           string            `json:"python_version,omitempty"`
	PythonRealPath          string            `json:"python_real_path,omitempty"`
	PyRoot                  string            `json:"py_root,omitempty"`
	VenvRealPath            string            `json:"venv_real_path,omitempty"`
	RequirementsLockSHA256  string            `json:"requirements_lock_sha256,omitempty"`
	RequirementsInputSHA256 string            `json:"requirements_input_sha256,omitempty"`
	Platform                Platform          `json:"platform"`
	Files                   map[string]string `json:"files"`
	Assets                  map[string]string `json:"assets,omitempty"`
	Downloads               map[string]string `json:"downloads"`
	Caps                    sandbox.Caps      `json:"sandbox_caps"`
	Probe                   map[string]any    `json:"probe"`
	CreatedAt               string            `json:"created_at"`
	Documentation           map[string]string `json:"documentation,omitempty"`
}
type InstallOptions struct {
	Warn           func(string)
	Cache          string
	Force, Offline bool
	FindLinks      string
	// Local tarball fixtures are always checked against an explicit SHA256.
	RuntimeArchive, RuntimeSHA256 string
}
type NotInstalled struct {
	Manifest string `json:"manifest"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail"`
}

func (e *NotInstalled) Error() string {
	return fmt.Sprintf("environment not installed (%s): %s; run memeval env install %s", e.Reason, e.Detail, e.Manifest)
}
func (l *Lock) RuntimeRoot() string {
	if l.ImageRoot != "" {
		return l.ImageRoot
	}
	return l.EnvDir
}
func FileHash(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = ioCopy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func CacheRoot(cache string) (string, error) {
	if cache == "" {
		cache = dotenv.Value("MEMEVAL_CACHE_DIR")
	}
	if cache == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		cache = filepath.Join(home, ".cache/memeval")
	}
	return Canonical(cache)
}
func bridgeRoot(m *Manifest) (string, error) {
	for p := m.Dir; ; p = filepath.Dir(p) {
		candidate := filepath.Join(p, "_bridge")
		if i, e := os.Stat(candidate); e == nil && i.IsDir() {
			return candidate, nil
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return "", fmt.Errorf("shared _bridge directory not found above manifest")
}
func walkProject(root string, fn func(string, string) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "__pycache__" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".env") || d.Name() == "lock.json" || d.Name() == ".DS_Store" {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("project source symlink disallowed: %s", rel)
		}
		return fn(p, rel)
	})
}
func hashPath(root, prefix string, out map[string]string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		r, _ := filepath.Rel(root, p)
		h, e := FileHash(p)
		if e == nil {
			out[prefix+"/"+r] = h
		}
		return e
	})
}
func installEnv(extraPath string) []string {
	// Install tools do not receive evaluator or SUT provider credentials.
	out := []string{"PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1", "UV_NO_PROGRESS=1", "npm_config_audit=false", "npm_config_fund=false"}
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "SYSTEMROOT", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			if k == "PATH" && extraPath != "" {
				v = extraPath + ":" + v
			}
			out = append(out, k+"="+v)
		}
	}
	return out
}
func Setup(ctx context.Context, m *Manifest, cache string) (*Lock, error) {
	return Install(ctx, m, InstallOptions{Cache: cache})
}
func Install(ctx context.Context, m *Manifest, o InstallOptions) (*Lock, error) {
	if _, err := LoadUpstream(filepath.Join(m.Dir, "UPSTREAM.json")); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		warning := "UPSTREAM.json missing for " + m.Name + "; result cards will disclose upstream: null"
		if o.Warn != nil {
			o.Warn(warning)
		} else {
			fmt.Fprintln(os.Stderr, "warning: "+warning)
		}
	}
	if m.Status == "deferred" {
		return nil, fmt.Errorf("env install deferred: %s", m.DeferredReason)
	}
	if m.Runtime.Kind == "binary" {
		return nil, fmt.Errorf("binary runtime not supported in P8 v1")
	}
	if m.Runtime.RequirementsLock != "" {
		raw, e := os.ReadFile(m.Resolve(m.Runtime.RequirementsLock))
		if e != nil {
			return nil, e
		}
		if e = validateDependencyLock(raw); e != nil {
			return nil, e
		}
	}
	return BuildImage(ctx, container.New(container.DefaultConfig()), m, o)
}
func Destroy(ctx context.Context, cache, name string, all bool) error {
	_, err := DestroyEnvironments(ctx, container.New(container.DefaultConfig()), cache, name, all)
	return err
}
