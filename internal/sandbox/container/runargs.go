package container

import (
	"fmt"
	"math"
	"memeval/internal/sandbox"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var sizePattern = regexp.MustCompile(`^[1-9][0-9]*[kKmMgG]?$`)

type runConfig struct {
	MeterDir                                            string
	NoBind                                              bool
	ShmSize                                             string
	LLMProfile                                          string
	RelayTarget, RelayImage                             string
	Name, Network, Alias, Image, User, EnvFile, EP, Cwd string
	Labels                                              map[string]string
	Command, ExtraTmpfs                                 []string
	Limits                                              sandbox.Limits
	ProbeMount                                          bool
	ProbeBinary                                         string
}

// A scratch image has no /tmp permissions to inherit. Set the default on
// the mount itself, while preserving explicitly declared permissions/owners.
func withTmpfsMode(spec string) string {
	_, opts, hasOptions := strings.Cut(spec, ":")
	for _, opt := range strings.Split(opts, ",") {
		if strings.HasPrefix(opt, "mode=") {
			return spec
		}
	}
	if !hasOptions {
		return spec + ":mode=1777"
	}
	if opts == "" {
		return spec + "mode=1777"
	}
	return spec + ",mode=1777"
}

func (b *Backend) tmpfsMounts(extra []string) []string {
	out := []string{}
	hasTmp := false
	for _, v := range extra {
		if strings.SplitN(v, ":", 2)[0] == "/tmp" {
			hasTmp = true
		}
	}
	if !hasTmp {
		out = append(out, withTmpfsMode("/tmp:rw,nosuid,nodev,size="+b.cfg.TmpfsSize))
	}
	for _, spec := range extra {
		out = append(out, withTmpfsMode(spec))
	}
	return out
}

func (b *Backend) runArgs(r runConfig) ([]string, error) {
	if !sizePattern.MatchString(b.cfg.TmpfsSize) || !sizePattern.MatchString(b.cfg.DefaultShmSize) {
		return nil, fmt.Errorf("invalid container tmpfs/shm size")
	}
	if !strings.HasPrefix(b.cfg.Platform, "linux/") {
		return nil, fmt.Errorf("container platform must be linux")
	}
	if r.Image == "" {
		return nil, fmt.Errorf("environment image missing; run memeval env install")
	}
	pids := r.Limits.PidsMax
	if pids == 0 {
		pids = b.cfg.DefaultPidsLimit
	}
	if pids < 1 || r.Limits.MaxRSS < 0 {
		return nil, fmt.Errorf("invalid container limits")
	}
	if cpus := r.Limits.CPUs; cpus != nil && (*cpus <= 0 || math.IsNaN(*cpus) || math.IsInf(*cpus, 0)) {
		return nil, fmt.Errorf("container cpus must be positive and finite")
	}
	a := []string{"create", "--name", r.Name, "--platform", b.cfg.Platform, "--network", r.Network}
	if r.Alias != "" {
		a = append(a, "--network-alias", r.Alias)
	}
	a = append(a, "--read-only")
	for _, v := range b.tmpfsMounts(r.ExtraTmpfs) {
		if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("invalid writable tmpfs")
		}
		a = append(a, "--tmpfs", v)
	}
	shm := r.ShmSize
	if shm == "" {
		shm = b.cfg.DefaultShmSize
	}
	if !sizePattern.MatchString(shm) {
		return nil, fmt.Errorf("invalid shm size")
	}
	a = append(a, "--shm-size", shm)
	dest := r.EP
	if r.ProbeMount {
		dest = "/probe"
	}
	if !r.NoBind {
		a = append(a, "-v", r.EP+":"+dest)
	}
	if r.MeterDir != "" {
		a = append(a, "-v", r.MeterDir+":/meter")
	}
	if r.ProbeBinary != "" {
		a = append(a, "-v", r.ProbeBinary+":/memeval:ro")
	}
	if r.Cwd != "" {
		a = append(a, "--workdir", r.Cwd)
	}
	a = append(a, "--user", r.User, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(pids))
	if r.Limits.CPUs != nil {
		a = append(a, "--cpus", strconv.FormatFloat(*r.Limits.CPUs, 'f', -1, 64))
	}
	if r.Limits.MaxRSS > 0 {
		v := strconv.FormatInt(r.Limits.MaxRSS, 10)
		a = append(a, "--memory", v, "--memory-swap", v)
	}
	a = append(a, "--init", "--interactive", "--stop-signal", "SIGTERM", "--stop-timeout", "5")
	if r.EnvFile != "" {
		a = append(a, "--env-file", r.EnvFile)
	}
	a = append(a, labelArgs(r.Labels)...)
	a = append(a, r.Image)
	a = append(a, r.Command...)
	return a, nil
}
func (b *Backend) profile(r runConfig, images []string, mode sandbox.NetworkMode) (string, error) {
	// Replace instance-specific paths/names before hashing. Environment values are
	// never part of argv or this identity. Command slots inside EP are normalized.
	ep := r.EP
	r.Name = "{container}"
	r.EP = "{ep}"
	r.EnvFile = "{ep}/.memeval/env.list"
	r.Labels = nil
	if r.ProbeBinary != "" {
		r.ProbeBinary = "{linux-binary}"
	}
	if r.Network != "none" {
		r.Network = "{network}"
	}
	r.Cwd = strings.ReplaceAll(r.Cwd, ep, "{ep}")
	r.Command = append([]string(nil), r.Command...)
	for i, v := range r.Command {
		r.Command[i] = strings.ReplaceAll(v, ep, "{ep}")
	}
	a, e := b.runArgs(r)
	if e != nil {
		return "", e
	}
	policy := []any{a, r.Image, images, mode, b.cfg.Platform, "container-run-v1"}
	if r.LLMProfile != "" {
		policy = append(policy, r.LLMProfile)
	}
	if mode == sandbox.NetworkGateway {
		b.govMu.Lock()
		defer b.govMu.Unlock()
		policy = append(policy, r.RelayTarget, r.RelayImage, "single-target-governor-v1", b.govConfig)
	}
	return sandbox.Hash(policy), nil
}
func writeEnv(path string, env map[string]string) error {
	keys := []string{}
	for k := range env {
		if !strings.HasPrefix(k, "MEMEVAL_") {
			keys = append(keys, k)
		}
	}
	if !contains(keys, "TMPDIR") {
		keys = append(keys, "TMPDIR")
	}
	sort.Strings(keys)
	var lines strings.Builder
	for _, k := range keys {
		v := env[k]
		if k == "TMPDIR" {
			v = "/tmp"
		}
		if k == "" || strings.ContainsAny(k, "=\x00\r\n") || strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("container env requires single-line keys and values")
		}
		lines.WriteString(k + "=" + v + "\n")
	}
	if e := privateDir(filepath.Dir(path)); e != nil {
		return e
	}
	return privateWrite(path, []byte(lines.String()))
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
