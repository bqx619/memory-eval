// Package sandbox separates child confinement from the evaluator process.
package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"memeval/internal/config/dotenv"
	"memeval/internal/procexec"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type NetworkMode string

const (
	NetworkNone    NetworkMode = "none"
	NetworkOpen    NetworkMode = "open"
	NetworkGateway NetworkMode = "gateway"
)

func (m NetworkMode) Valid() bool { return m == NetworkNone || m == NetworkGateway || m == NetworkOpen }

type Limits struct {
	CPUs    *float64      `json:"cpus,omitempty"`
	Timeout time.Duration `json:"timeout"`
	MaxRSS  int64         `json:"max_rss"`
	PidsMax int           `json:"pids_max"`
}

type Image struct {
	Repository          string   `json:"repository,omitempty"`
	Tag                 string   `json:"tag,omitempty"`
	SizeBytes           int64    `json:"size_bytes,omitempty"`
	ContainerfileSHA256 string   `json:"containerfile_sha256,omitempty"`
	ContextSHA256       string   `json:"context_sha256,omitempty"`
	BuiltAt             string   `json:"built_at,omitempty"`
	Platform            string   `json:"platform,omitempty"`
	ID                  string   `json:"id,omitempty"`
	Ref                 string   `json:"ref,omitempty"`
	BaseImages          []string `json:"base_images,omitempty"`
}
type ContainerSpec struct {
	Image                      Image
	EnvID                      string
	Tmpfs, SidecarImageDigests []string
}

type Spec struct {
	Container   *ContainerSpec
	GatewayURL  *url.URL
	RequiresLLM bool
	LLMProfile  string

	EpisodeDir string
	Command    []string
	Cwd        string
	Env        map[string]string
	EnvRoot    string
	Network    NetworkMode
	Limits     Limits
	KeepDir    bool
	Services   []Service
	// Transitional runtime paths used by offline none fixtures.
	PythonRoot, Venv string
	CacheLinks       map[string]string
}

type HostInfo struct {
	OS     string         `json:"os"`
	Arch   string         `json:"arch"`
	Kernel string         `json:"kernel"`
	Flags  map[string]any `json:"flags"`
}
type Caps struct {
	UserMode          string         `json:"user_mode,omitempty"`
	Enforced          bool           `json:"enforced"`
	Reason            string         `json:"reason,omitempty"`
	Runtime           map[string]any `json:"runtime,omitempty"`
	ProcessInfo       map[string]any `json:"process_info,omitempty"`
	Signal            string         `json:"signal,omitempty"`
	ProcessIPC        map[string]any `json:"process_ipc,omitempty"`
	FileReadRestrict  bool           `json:"file_read_restrict"`
	FileWriteRestrict bool           `json:"file_write_restrict"`
	DenyRead          bool           `json:"deny_read"`
	NetworkNone       bool           `json:"network_none"`
	ProcessTree       bool           `json:"process_tree"`
	Mechanism         string         `json:"mechanism"`
	Host              HostInfo       `json:"host"`
}
type ExitStatus struct {
	Code   int    `json:"code"`
	Signal string `json:"signal"`
	OOM    bool   `json:"oom"`
}
type Handle interface {
	Dir() string
	ProfileSHA256() string
	EffectiveConfig() map[string]any
}
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	PID() int
	Wait(context.Context) (ExitStatus, error)
	Kill(context.Context) error
}
type Sandbox interface {
	Name() string
	Capabilities(context.Context) (Caps, error)
	Prepare(context.Context, Spec) (Handle, error)
	Spawn(context.Context, Handle) (Process, error)
	Destroy(context.Context, Handle) error
}
type Unenforceable struct{ Reason string }

func (e *Unenforceable) Error() string { return "isolation unavailable: " + e.Reason }
func Host(ctx context.Context) HostInfo {
	h := HostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, Flags: map[string]any{"fd_hygiene": procexec.Policy}}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if b, e := procexec.Output(exec.CommandContext(c, "/usr/bin/uname", "-r")); e == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	return h
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func Real(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(p)
}

// CanonicalPath resolves symlinks even when a denied leaf does not yet exist.
func CanonicalPath(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	tail := []string{}
	for {
		r, e := filepath.EvalSymlinks(p)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", e
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
}
func Within(path, root string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}
func NewEpisode() (string, error) {
	root := dotenv.Value("MEMEVAL_EP_ROOT")
	if root == "" {
		root = "/private/tmp/memeval"
		if runtime.GOOS != "darwin" {
			root = "/tmp/memeval"
		}
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("episode root must be absolute")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return "", e
	}
	root, e := Real(root)
	if e != nil {
		return "", e
	}
	st, e := os.Stat(root)
	if e != nil {
		return "", e
	}
	if st.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("episode root must have private permissions (0700)")
	}
	for i := 0; i < 32; i++ {
		var id [4]byte
		if _, e := rand.Read(id[:]); e != nil {
			return "", e
		}
		p := filepath.Join(root, hex.EncodeToString(id[:]))
		if e := os.Mkdir(p, 0700); os.IsExist(e) {
			continue
		} else if e != nil {
			return "", e
		}
		return p, nil
	}
	return "", fmt.Errorf("cannot allocate unique episode directory")
}

// Prepared owns exactly one fresh directory and at most one process.
type Prepared struct {
	External  bool
	Teardown  func(context.Context) error
	Extra     map[string]any
	Spec      Spec
	SHA       string
	mu        sync.Mutex
	Child     Process
	destroyed bool
	Derived   bool
	Commands  []*Prepared
}

func (h *Prepared) Dir() string           { return h.Spec.EpisodeDir }
func (h *Prepared) ProfileSHA256() string { return h.SHA }
func (h *Prepared) EffectiveConfig() map[string]any {
	keys := make([]string, 0, len(h.Spec.Env))
	for k := range h.Spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	config := map[string]any{"episode_dir": h.Dir(), "cwd": h.Spec.Cwd, "env_keys": keys, "env_root": h.Spec.EnvRoot, "network": h.Spec.Network, "limits": h.Spec.Limits, "limits_enforced": map[string]bool{"timeout": !h.External && h.Spec.Limits.Timeout > 0, "max_rss": false, "pids_max": false}, "keep": h.Spec.KeepDir, "external": h.External}
	if len(h.Spec.Command) > 0 {
		config["executable"] = h.Spec.Command[0]
		config["argument_count"] = len(h.Spec.Command) - 1
	}
	if len(h.Spec.CacheLinks) > 0 {
		config["cache_links"] = maps.Clone(h.Spec.CacheLinks)
	}
	for k, v := range h.Extra {
		config[k] = v
	}
	return config
}

// PrepareEpisode creates the private workspace without interpreting host runtime paths.
func PrepareEpisode(ctx context.Context, s Spec) (_ *Prepared, err error) {
	created := false
	defer func() {
		if err != nil && created {
			_ = os.RemoveAll(s.EpisodeDir)
		}
	}()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !s.Network.Valid() {
		return nil, fmt.Errorf("unknown network mode")
	}
	if err := ValidateCacheLinks(s.EnvRoot, s.CacheLinks); err != nil {
		return nil, err
	}
	if len(s.Command) == 0 || !filepath.IsAbs(s.Command[0]) {
		return nil, fmt.Errorf("absolute executable required")
	}
	if s.EpisodeDir == "" {
		p, e := NewEpisode()
		if e != nil {
			return nil, e
		}
		s.EpisodeDir = p
		created = true
	}
	ep, e := Real(s.EpisodeDir)
	if e != nil {
		return nil, e
	}
	s.EpisodeDir = ep
	entries, e := os.ReadDir(ep)
	if e != nil {
		return nil, e
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf("episode directory must be empty")
	}
	for _, d := range []string{"home", "tmp", "xdg/config", "xdg/cache", "xdg/data", "work"} {
		if e := os.MkdirAll(filepath.Join(ep, d), 0700); e != nil {
			return nil, e
		}
	}
	for relative, target := range s.CacheLinks {
		dest := filepath.Join(ep, relative)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, dest); err != nil {
			return nil, err
		}
	}
	if s.Cwd == "" {
		s.Cwd = filepath.Join(ep, "work")
	}
	if !Within(s.Cwd, ep) {
		return nil, fmt.Errorf("cwd must be inside episode")
	}
	return &Prepared{Spec: s}, nil
}

// ValidateCacheLinks is lexical: targets may exist only inside the container.
// Reject overlapping destinations before creating anything; otherwise the
// parent of a later link could already be a link out of the episode.
func ValidateCacheLinks(root string, links map[string]string) error {
	for relative, target := range links {
		if relative == "" || relative == "." || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") || strings.ContainsAny(relative, "\\\x00\r\n") || !filepath.IsAbs(root) || !filepath.IsAbs(target) || filepath.Clean(target) != target || target == root || !Within(target, root) || strings.ContainsAny(target, "\\\x00\r\n") {
			return fmt.Errorf("invalid runtime cache link %q -> %q (target must be below %s)", relative, target, root)
		}
		for other := range links {
			if other != relative && strings.HasPrefix(other, relative+"/") {
				return fmt.Errorf("overlapping runtime cache links: %q and %q", relative, other)
			}
		}
	}
	return nil
}
func Destroy(ctx context.Context, h Handle) error {
	p, ok := h.(*Prepared)
	if !ok {
		return fmt.Errorf("foreign handle")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.destroyed {
		return nil
	}
	if p.Teardown != nil {
		if e := p.Teardown(ctx); e != nil {
			return e
		}
	}
	if p.Child != nil {
		if e := p.Child.Kill(ctx); e != nil {
			return e
		}
	}
	for i := len(p.Commands) - 1; i >= 0; i-- {
		if e := Destroy(ctx, p.Commands[i]); e != nil {
			return e
		}
	}
	if !p.Derived && !p.External {
		if e := CleanupPaths(ctx, p.Dir()); e != nil {
			return e
		}
	}
	if !p.Derived && !p.Spec.KeepDir {
		if e := os.RemoveAll(p.Dir()); e != nil {
			return e
		}
	}
	p.destroyed = true
	return nil
}
func Environment(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	a := make([]string, 0, len(keys))
	for _, k := range keys {
		a = append(a, k+"="+env[k])
	}
	return a
}
func Spawn(ctx context.Context, h *Prepared, executable string, args []string) (Process, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.destroyed || h.Child != nil {
		return nil, fmt.Errorf("handle already used")
	}
	p, e := start(ctx, h.Spec, executable, args)
	if e == nil {
		h.Child = p
	}
	return p, e
}

// SpawnWithEnv attaches a host CLI with evaluator-owned environment and lifetime.
// The container process wrapper owns its timeout and teardown, not this CLI.
func SpawnWithEnv(ctx context.Context, h *Prepared, executable string, args []string, env map[string]string) (Process, error) {
	s := h.Spec
	s.Env = env
	s.Limits.Timeout = 0
	return start(ctx, s, executable, args)
}
