package sut

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/suite"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}
type Bridge struct {
	BaseURL    string         `json:"base_url,omitempty"`
	Routes     string         `json:"routes,omitempty"`
	HeadersEnv []string       `json:"headers_env,omitempty"`
	Module     string         `json:"module,omitempty"`
	Class      string         `json:"class,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
}
type RuntimeIndex struct {
	URL      string `json:"url"`
	Strategy string `json:"strategy,omitempty"`
}
type LLM struct {
	// Concurrency declares runtime fan-out; nil leaves the governor default intact.
	Concurrency *int              `json:"concurrency,omitempty" jsonschema:"minimum=1"`
	Required    bool              `json:"required"`
	Model       string            `json:"model,omitempty"`
	Effort      string            `json:"reasoning_effort,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}
type Runtime struct {
	CacheLinks       map[string]string   `json:"cache_links,omitempty"`
	Base             string              `json:"base,omitempty" jsonschema:"enum=uv-python,enum=uv-python-trixie,enum=node"`
	Apt              []string            `json:"apt,omitempty"`
	Indexes          []RuntimeIndex      `json:"indexes,omitempty"`
	Constraints      string              `json:"constraints,omitempty"`
	Assets           []string            `json:"assets,omitempty"`
	ProbeEnv         map[string]string   `json:"probe_env,omitempty"`
	RequirementsLock string              `json:"requirements_lock,omitempty"`
	Kind             string              `json:"kind" jsonschema:"enum=uv-python,enum=node,enum=binary"`
	Python           string              `json:"python,omitempty"`
	Requirements     string              `json:"requirements,omitempty"`
	Version          string              `json:"version,omitempty"`
	Sources          map[string]Artifact `json:"sources,omitempty"`
	Executable       string              `json:"executable,omitempty"`
	PackageLock      string              `json:"package_lock,omitempty"`
	Prepare          [][]string          `json:"prepare,omitempty"`
}

func Schema() ([]byte, error) { return suite.SchemaJSON(Manifest{}) }

type Manifest struct {
	LLM            LLM       `json:"llm,omitzero"`
	Status         string    `json:"status,omitempty" jsonschema:"enum=active,enum=deferred"`
	DeferredReason string    `json:"deferred_reason,omitempty"`
	Sidecars       []Sidecar `json:"sidecars,omitempty"`
	SchemaVersion  string    `json:"schema_version" jsonschema:"enum=3"`
	Name           string    `json:"name"`
	Variant        string    `json:"variant"`
	Notes          string    `json:"notes"`
	Adapter        struct {
		Kind            string  `json:"kind"`
		Name            string  `json:"name"`
		Transport       string  `json:"transport"`
		MaxFrameBytes   int     `json:"max_frame_bytes"`
		RequestTimeoutS float64 `json:"request_timeout_s"`
	} `json:"adapter"`
	Runtime      Runtime           `json:"runtime"`
	Bridge       *Bridge           `json:"bridge,omitempty"`
	TelemetryEnv map[string]string `json:"telemetry_env,omitempty"`
	CacheDir     string            `json:"-"`

	Process struct {
		Command        []string          `json:"command"`
		Cwd            string            `json:"cwd"`
		Env            map[string]string `json:"env"`
		EnvFile        string            `json:"env_file"`
		EnvPassthrough []string          `json:"env_passthrough"`
		SecretEnv      []string          `json:"secret_env,omitempty"`
	} `json:"process"`
	Config  map[string]any `json:"config"`
	Sandbox struct {
		Backend   string `json:"backend,omitempty" jsonschema:"enum=container,enum=none"`
		Container struct {
			WritableTmpfs []string `json:"writable_tmpfs,omitempty"`
		} `json:"container,omitzero"`
		Network sandbox.NetworkMode `json:"network"`
		Limits  struct {
			CPUs     *float64 `json:"cpus,omitempty" jsonschema:"exclusiveMinimum=0"`
			TimeoutS float64  `json:"timeout_s"`
			MaxRSS   int64    `json:"max_rss"`
			PidsMax  int      `json:"pids_max"`
		} `json:"limits"`
	} `json:"sandbox"`
	Ready struct {
		Strategy string `json:"strategy"`
	} `json:"ready"`
	Declared       map[string]any `json:"declared"`
	Path, Dir      string         `json:"-"`
	DefaultEnvFile string         `json:"-"`
	Gateway        *LLMGateway    `json:"-"`
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var keyPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func Load(path string) (*Manifest, error) {
	return LoadWithEnv(path, "")
}
func LoadWithEnv(path, envFile string) (*Manifest, error) {
	p, e := sandbox.Real(path)
	if e != nil {
		return nil, e
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 1<<20))
	if e != nil {
		return nil, e
	}
	var header struct {
		Version string `json:"schema_version"`
	}
	if e = json.Unmarshal(raw, &header); e != nil {
		return nil, e
	}
	if header.Version != "3" {
		return nil, fmt.Errorf("manifest schema_version %q retired; migrate to schema_version 3 (sandbox.backend, image runtime); see design/p8-container-platform.md", header.Version)
	}
	var m Manifest
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if e = d.Decode(&m); e != nil {
		return nil, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("manifest must contain one JSON object")
	}
	m.Path = p
	m.Dir = filepath.Dir(p)
	if m.Status != "" && m.Status != "active" && m.Status != "deferred" {
		return nil, fmt.Errorf("invalid manifest status")
	}
	if m.Status == "deferred" && strings.TrimSpace(m.DeferredReason) == "" {
		return nil, fmt.Errorf("deferred manifest requires reason")
	}
	cwd, _ := os.Getwd()
	m.DefaultEnvFile = filepath.Join(dotenv.Root(cwd), ".env")
	if envFile != "" {
		m.DefaultEnvFile = envFile
	}
	if m.SchemaVersion != "3" || !namePattern.MatchString(m.Name) {
		return nil, fmt.Errorf("schema_version 3 and safe name required")
	}
	if m.Adapter.Kind != "python-bridge" && m.Adapter.Kind != "http-bridge" && !(m.Adapter.Kind == "generic" && m.Adapter.Transport == "jsonrpc-stdio") && !(m.Adapter.Kind == "native" && m.Adapter.Name == "octop") {
		return nil, fmt.Errorf("supported adapters: generic/jsonrpc-stdio, python-bridge, http-bridge or native/octop")
	}
	if e := m.validateSidecars(); e != nil {
		return nil, e
	}
	if e := m.validateRuntime(); e != nil {
		return nil, e
	}

	if m.Sandbox.Backend == "" {
		m.Sandbox.Backend = "container"
	}
	if m.Sandbox.Backend != "container" && m.Sandbox.Backend != "none" {
		return nil, fmt.Errorf("sandbox backend must be container or none")
	}
	if err := m.validateLLM(); err != nil {
		return nil, err
	}
	if cpus := m.Sandbox.Limits.CPUs; cpus != nil && (*cpus <= 0 || math.IsNaN(*cpus) || math.IsInf(*cpus, 0)) {
		return nil, fmt.Errorf("sandbox.limits.cpus must be positive and finite")
	}
	if !m.Sandbox.Network.Valid() {
		return nil, fmt.Errorf("network must be none, gateway or open")
	}
	if m.Ready.Strategy != "sync" && m.Ready.Strategy != "status" && m.Ready.Strategy != "canary" {
		return nil, fmt.Errorf("ready strategy must be sync, status or canary")
	}
	if len(m.Process.Command) == 0 && m.Adapter.Kind != "python-bridge" && m.Adapter.Kind != "http-bridge" {
		return nil, fmt.Errorf("process command required")
	}
	if m.Adapter.MaxFrameBytes == 0 {
		m.Adapter.MaxFrameBytes = 16 << 20
	}
	if m.Adapter.MaxFrameBytes < 1 || m.Adapter.MaxFrameBytes > 256<<20 {
		return nil, fmt.Errorf("max_frame_bytes outside 1..256MiB")
	}
	if m.Adapter.RequestTimeoutS == 0 {
		m.Adapter.RequestTimeoutS = 30
	}
	if m.Adapter.RequestTimeoutS < 0 {
		return nil, fmt.Errorf("negative request timeout")
	}
	if m.Sandbox.Limits.TimeoutS == 0 {
		m.Sandbox.Limits.TimeoutS = 600
	}
	if m.Sandbox.Limits.TimeoutS < 0 || m.Sandbox.Limits.MaxRSS < 0 || m.Sandbox.Limits.PidsMax < 0 {
		return nil, fmt.Errorf("negative limits")
	}
	for k, v := range m.Runtime.ProbeEnv {
		if e = m.validKey(k); e != nil {
			return nil, e
		}
		if strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("NUL in probe env")
		}
	}
	for k, v := range m.TelemetryEnv {
		if e = m.validKey(k); e != nil {
			return nil, e
		}
		if strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("NUL in telemetry env")
		}
	}
	for k := range m.Process.Env {
		if e = m.validKey(k); e != nil {
			return nil, e
		}
	}
	for _, k := range m.Process.EnvPassthrough {
		if e = m.validKey(k); e != nil {
			return nil, e
		}
	}
	if e = m.validateSecretEnv(m.Process.SecretEnv, m.Process.Env, m.Process.EnvPassthrough); e != nil {
		return nil, e
	}
	if _, e = m.envFile(); e != nil {
		return nil, e
	}
	return &m, nil
}
func Forbidden(k string) bool {
	for _, prefix := range []string{"ANTHROPIC_", "MEMEVAL_", "HERDR_", "CODEX_", "CLAUDE"} {
		if strings.HasPrefix(strings.ToUpper(k), prefix) {
			return true
		}
	}
	return false
}
func (m *Manifest) validKey(k string) error {
	if !keyPattern.MatchString(k) || Forbidden(k) || strings.EqualFold(k, "OPENAI_API_KEY") && !slices.Contains(m.Process.EnvPassthrough, k) {
		return fmt.Errorf("forbidden environment key: %s", k)
	}
	return nil
}
func (m *Manifest) Resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(m.Dir, p)
}
func (m *Manifest) envFile() (map[string]string, error) {
	return dotenv.Read(m.EnvPath(), nil)
}
func (m *Manifest) EnvPath() string {
	if m.Process.EnvFile != "" {
		return m.Resolve(m.Process.EnvFile)
	}
	return m.DefaultEnvFile
}
func (m *Manifest) PassthroughKeys() ([]string, error) {
	values, err := m.envFile()
	if err != nil {
		return nil, err
	}
	keys := slices.Clone(m.Process.EnvPassthrough)
	if keys == nil {
		keys = []string{}
	}
	for _, key := range keys {
		if err := m.validKey(key); err != nil {
			return nil, err
		}
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("env_passthrough key %s missing from env_file", key)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}
func (m *Manifest) Environment(ep, venv string) (map[string]string, error) {
	env := map[string]string{"PATH": venv + "/bin:/usr/bin:/bin", "HOME": ep + "/home", "TMPDIR": ep + "/tmp", "XDG_CONFIG_HOME": ep + "/xdg/config", "XDG_CACHE_HOME": ep + "/xdg/cache", "XDG_DATA_HOME": ep + "/xdg/data", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1", "PYTHONUNBUFFERED": "1", "PYTHONNOUSERSITE": "1", "PYTHONHASHSEED": "0", "TZ": "UTC", "EP": ep}
	apply := func(k, v string) error {
		if e := m.validKey(k); e != nil {
			return e
		}
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("NUL in environment")
		}
		if original, ok := env[k]; ok && original != v && !(k == "HOME" && sandbox.Within(v, ep)) {
			return fmt.Errorf("reserved runtime environment key: %s", k)
		}
		env[k] = v
		return nil
	}
	for k, v := range m.Process.Env {
		if e := apply(k, strings.ReplaceAll(v, "{ep}", ep)); e != nil {
			return nil, e
		}
	}
	values, e := m.envFile()
	if e != nil {
		return nil, e
	}
	for _, k := range m.Process.EnvPassthrough {
		v, ok := values[k]
		if !ok {
			return nil, fmt.Errorf("env_passthrough key %s missing from env_file", k)
		}
		if e = apply(k, v); e != nil {
			return nil, e
		}
	}
	for k, v := range m.TelemetryEnv {
		v = strings.ReplaceAll(v, "{ep}", ep)
		if e := m.validKey(k); e != nil {
			return nil, e
		}
		if original, ok := m.Process.Env[k]; ok && strings.ReplaceAll(original, "{ep}", ep) != v {
			return nil, fmt.Errorf("telemetry env conflicts with process env: %s", k)
		}
		if slices.Contains(m.Process.EnvPassthrough, k) {
			return nil, fmt.Errorf("telemetry env cannot be passed through: %s", k)
		}
		if _, reserved := env[k]; reserved && k != "XDG_CACHE_HOME" && k != "MEM0_DIR" && k != "HOME" {
			if env[k] != v {
				return nil, fmt.Errorf("reserved telemetry environment key: %s", k)
			}
		}
		if (k == "XDG_CACHE_HOME" || k == "HOME") && !sandbox.Within(v, ep) {
			return nil, fmt.Errorf("telemetry cache must be within EP")
		}
		env[k] = v
	}
	if e := m.applyLLM(env); e != nil {
		return nil, e
	}
	return env, nil
}

// Canonical also handles missing deny roots, resolving their existing ancestors.
func Canonical(p string) (string, error) {
	p, e := filepath.Abs(p)
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
func (m *Manifest) Spec(l *Lock, ep, ns string, keep bool) (sandbox.Spec, error) {
	var s sandbox.Spec
	env, e := m.Environment(ep, l.VenvRealPath)
	if e != nil {
		return s, e
	}
	// Render runtime metadata on a copy: the manifest remains the installation
	// identity, and explicit HTTP timeout_s/ready_timeout_s keep their meaning.
	withDeadline := func(config map[string]any) map[string]any {
		cfg := maps.Clone(config)
		if cfg == nil {
			cfg = map[string]any{}
		}
		timeout := m.Adapter.RequestTimeoutS
		if timeout == 0 {
			timeout = 30 // Also the JSON-RPC client's default for unset options.
		}
		cfg["request_timeout_s"] = timeout
		return cfg
	}
	config, e := json.Marshal(withDeadline(m.Config))
	if e != nil {
		return s, e
	}
	expand := func(v string) (string, error) {
		r := strings.NewReplacer("{env}", l.RuntimeRoot(), "{project}", filepath.Join(l.RuntimeRoot(), "project"), "{node}", l.Executable, "{binary}", l.Executable, "{venv}", l.VenvRealPath, "{py_root}", l.PyRoot, "{ep}", ep, "{ns}", ns, "{config_json}", string(config)).Replace(v)
		if strings.ContainsRune(r, 0) {
			return "", fmt.Errorf("NUL in process arguments")
		}
		return r, nil
	}
	for k, v := range env {
		env[k], e = expand(v)
		if e != nil {
			return s, e
		}
	}
	if l.RuntimeRoot() != "" {
		env["PATH"] = filepath.Dir(l.Executable) + ":" + filepath.Join(l.RuntimeRoot(), "runtime/bin") + ":/usr/local/bin:/usr/bin:/bin"
	}
	if m.Adapter.Kind == "python-bridge" || m.Adapter.Kind == "http-bridge" {
		env["PYTHONPATH"] = filepath.Join(l.RuntimeRoot(), "bridge/python") + ":" + filepath.Join(l.RuntimeRoot(), "project")
	}
	links, e := m.cacheLinks()
	if e != nil {
		return s, e
	}
	s = sandbox.Spec{EnvRoot: l.RuntimeRoot(), CacheLinks: links, EpisodeDir: ep, Env: env, Network: m.Sandbox.Network, KeepDir: keep, PythonRoot: l.PyRoot, Venv: l.VenvRealPath, Limits: sandbox.Limits{CPUs: m.Sandbox.Limits.CPUs, Timeout: time.Duration(m.Sandbox.Limits.TimeoutS * float64(time.Second)), MaxRSS: m.Sandbox.Limits.MaxRSS, PidsMax: m.Sandbox.Limits.PidsMax}}
	if m.Sandbox.Backend == "container" {
		s.Container = &sandbox.ContainerSpec{Image: l.Image, EnvID: l.Name + "@" + l.LockSHA256[:min(12, len(l.LockSHA256))], Tmpfs: m.Sandbox.Container.WritableTmpfs}
		for _, v := range l.SidecarImages {
			s.Container.SidecarImageDigests = append(s.Container.SidecarImageDigests, v.Digest)
		}
	}
	if m.Gateway != nil || m.LLM.Required || len(m.LLM.Env) > 0 {
		g := m.EffectiveGateway()
		s.GatewayURL = g.BaseURL
		s.LLMProfile = sandbox.Hash([]any{g.Model, g.Effort})
	}
	s.RequiresLLM = m.LLM.Required
	commands := m.Process.Command
	if m.Adapter.Kind == "python-bridge" && len(commands) == 0 {
		cfg, e := json.Marshal(withDeadline(m.Bridge.Config))
		if e != nil {
			return s, e
		}
		commands = []string{l.Executable, "-m", "memeval_bridge", "--adapter", m.Bridge.Module + ":" + m.Bridge.Class, "--config", string(cfg), "--max-frame-bytes", fmt.Sprint(m.Adapter.MaxFrameBytes)}
	}
	if m.Adapter.Kind == "http-bridge" && len(commands) == 0 {
		cfg := map[string]any{"base_url": m.Bridge.BaseURL, "routes": filepath.Join(l.RuntimeRoot(), "project", m.Bridge.Routes), "headers_env": m.Bridge.HeadersEnv}
		for k, v := range m.Bridge.Config {
			cfg[k] = v
		}
		raw, e := json.Marshal(withDeadline(cfg))
		if e != nil {
			return s, e
		}
		if m.Runtime.Kind == "node" {
			commands = []string{l.Executable, filepath.Join(l.RuntimeRoot(), "bridge/node/index.mjs"), "--adapter", filepath.Join(l.RuntimeRoot(), "bridge/node/http.mjs"), "--config", string(raw)}
		} else {
			entry := "memeval_bridge.http:Adapter"
			if m.Bridge.Module != "" {
				entry = m.Bridge.Module + ":" + m.Bridge.Class
			}
			commands = []string{l.Executable, "-m", "memeval_bridge", "--adapter", entry, "--config", string(raw)}
		}
	}
	if m.Adapter.Kind == "http-bridge" && len(m.Process.Command) == 0 {
		commands = append(commands, "--max-frame-bytes", fmt.Sprint(m.Adapter.MaxFrameBytes))
	}
	for _, arg := range commands {
		v, e := expand(arg)
		if e != nil {
			return s, e
		}
		s.Command = append(s.Command, v)
	}
	if !filepath.IsAbs(s.Command[0]) {
		return s, fmt.Errorf("expanded executable must be absolute")
	}
	s.Cwd = m.Process.Cwd
	if s.Cwd == "" {
		s.Cwd = "{ep}/work"
	}
	s.Cwd, e = expand(s.Cwd)
	if e != nil {
		return s, e
	}
	return s, nil
}
func (m *Manifest) Prepare(ctx context.Context, b sandbox.Sandbox, l *Lock, ns string, keep bool) (sandbox.Handle, error) {
	ep, e := sandbox.NewEpisode()
	if e != nil {
		return nil, e
	}
	s, e := m.Spec(l, ep, ns, keep)
	if e != nil {
		os.RemoveAll(ep)
		return nil, e
	}
	if e = m.PlanServices(l, &s); e != nil {
		os.RemoveAll(ep)
		return nil, e
	}
	h, e := b.Prepare(ctx, s)
	if e != nil {
		os.RemoveAll(ep)
	}
	return h, e
}
func (m *Manifest) Secrets() []string {
	// Explicit static secrets remain available even if env_file is unavailable.
	env, _ := m.envFile()
	values := []string{env["MEMEVAL_SUT_LLM_API_KEY"], os.Getenv("MEMEVAL_SUT_LLM_API_KEY")}
	if m.Gateway != nil {
		values = append(values, m.Gateway.Secrets()...)
	}
	for _, k := range m.Process.EnvPassthrough {
		if credentialEnvKey.MatchString(k) || slices.Contains(m.Process.SecretEnv, k) {
			values = append(values, env[k])
		}
	}
	for _, k := range m.Process.SecretEnv {
		values = append(values, m.Process.Env[k])
	}
	for _, service := range m.Sidecars {
		for _, k := range service.SecretEnv {
			values = append(values, service.Env[k])
		}
	}
	return jsonrpc.SecretValues(values)
}

// Nonstandard credential names can be marked explicitly with secret_env.
// Model names, URLs, provider selections, paths and numeric settings are not
// credentials merely because they are passed through from an env file.
var credentialEnvKey = regexp.MustCompile(`(?i)(^|_)(api_key|key|token|secret|password|passwd|credential|credentials|authorization)$`)

func (m *Manifest) validateSecretEnv(keys []string, env map[string]string, passthrough []string) error {
	for _, key := range keys {
		if err := m.validKey(key); err != nil {
			return err
		}
		if _, ok := env[key]; !ok && !slices.Contains(passthrough, key) {
			return fmt.Errorf("secret_env key %s must name a declared env or env_passthrough entry", key)
		}
	}
	return nil
}
