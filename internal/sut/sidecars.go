package sut

import (
	"fmt"
	"maps"
	"memeval/internal/sandbox"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Sidecar struct {
	Name      string               `json:"name"`
	Kind      string               `json:"kind" jsonschema:"enum=postgres,enum=qdrant,enum=generic"`
	Preset    string               `json:"preset,omitempty"`
	Image     string               `json:"image,omitempty"`
	Port      uint16               `json:"port"`
	User      string               `json:"user,omitempty"`
	Tmpfs     []string             `json:"tmpfs,omitempty"`
	ShmSize   string               `json:"shm_size,omitempty"`
	Init      [][]string           `json:"init,omitempty"`
	Command   []string             `json:"command,omitempty"`
	Ready     sandbox.ServiceReady `json:"ready"`
	Env       map[string]string    `json:"env,omitempty"`
	SecretEnv []string             `json:"secret_env,omitempty"`
	Exports   map[string]string    `json:"exports,omitempty"`
	Stop      string               `json:"stop,omitempty"`
}

var placeholder = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_.-]*\}`)
var imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
var userPattern = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*$`)
var mountSizePattern = regexp.MustCompile(`^[1-9][0-9]*[kKmMgG]?$`)

func expandService(value string, vars map[string]string) (string, error) {
	var bad string
	value = placeholder.ReplaceAllStringFunc(value, func(key string) string {
		v, ok := vars[key]
		if !ok {
			bad = key
		}
		return v
	})
	if bad != "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("unknown/invalid sidecar placeholder %s", bad)
	}
	return value, nil
}
func (m *Manifest) validateSidecars() error {
	names := map[string]bool{}
	for i := range m.Sidecars {
		s := &m.Sidecars[i]
		if !namePattern.MatchString(s.Name) || names[s.Name] || s.Name == "sut" || s.Name == "llm-gateway" {
			return fmt.Errorf("invalid/duplicate/reserved sidecar name")
		}
		names[s.Name] = true
		if !slices.Contains([]string{"postgres", "qdrant", "generic"}, s.Kind) {
			return fmt.Errorf("unknown sidecar kind")
		}
		if s.Preset != "" {
			p, e := sidecarPreset(s.Preset)
			if e != nil {
				return e
			}
			if s.Image == "" {
				s.Image = p.Image
			}
			if s.Port == 0 {
				s.Port = p.Port
			}
			if s.User == "" {
				s.User = p.User
			}
			if s.Tmpfs == nil {
				s.Tmpfs = p.Tmpfs
			}
			if s.ShmSize == "" {
				s.ShmSize = p.ShmSize
			}
			env := maps.Clone(p.Env)
			if env == nil {
				env = map[string]string{}
			}
			maps.Copy(env, s.Env)
			s.Env = env
			if len(s.Ready.Exec) == 0 && s.Ready.TCP == "" && s.Ready.HTTP == nil {
				timeout := s.Ready.TimeoutS
				s.Ready = p.Ready
				if timeout != 0 {
					s.Ready.TimeoutS = timeout
				}
			}
			if s.Stop == "" {
				s.Stop = p.Stop
			}
		}
		if s.Port == 0 {
			return fmt.Errorf("sidecar %s requires port", s.Name)
		}
		if s.Image != "" && !imagePattern.MatchString(s.Image) {
			return fmt.Errorf("sidecar %s image must include sha256 digest", s.Name)
		}
		if s.Image == "" && (s.Kind != "generic" || len(s.Command) == 0) {
			return fmt.Errorf("sidecar requires image or generic command")
		}
		if s.User != "" && !userPattern.MatchString(s.User) {
			return fmt.Errorf("sidecar user must be non-root uid:gid")
		}
		if s.ShmSize != "" && !mountSizePattern.MatchString(s.ShmSize) {
			return fmt.Errorf("invalid sidecar shm_size")
		}
		seen := map[string]bool{}
		for _, v := range s.Tmpfs {
			target, opts, _ := strings.Cut(v, ":")
			if !strings.HasPrefix(target, "/") || path.Clean(target) != target || target == "/" || seen[target] || strings.ContainsAny(v, "\x00\r\n") || strings.Contains(opts, "bind") {
				return fmt.Errorf("invalid/duplicate sidecar tmpfs")
			}
			seen[target] = true
		}
		if s.Stop == "" {
			s.Stop = "SIGTERM"
		}
		if s.Stop != "SIGTERM" {
			return fmt.Errorf("sidecar stop must be SIGTERM")
		}
		n := 0
		if len(s.Ready.Exec) > 0 {
			n++
		}
		if s.Ready.TCP != "" {
			n++
		}
		if s.Ready.HTTP != nil {
			n++
		}
		if n != 1 || s.Ready.Unix != "" {
			return fmt.Errorf("sidecar requires exactly one exec/tcp/http ready check")
		}
		if s.Ready.TimeoutS == 0 {
			s.Ready.TimeoutS = 30
		}
		if s.Ready.TimeoutS < 0 {
			return fmt.Errorf("negative readiness timeout")
		}
		for _, args := range append([][]string{s.Command, s.Ready.Exec}, s.Init...) {
			for _, a := range args {
				if strings.ContainsRune(a, 0) {
					return fmt.Errorf("NUL sidecar command")
				}
			}
		}
		for _, cmd := range s.Init {
			if len(cmd) == 0 || cmd[0] == "" {
				return fmt.Errorf("empty sidecar init command")
			}
		}
		for _, env := range []map[string]string{s.Env, s.Exports} {
			for k, v := range env {
				if e := m.validKey(k); e != nil {
					return e
				}
				if strings.ContainsRune(v, 0) {
					return fmt.Errorf("NUL sidecar env")
				}
			}
		}
		if e := m.validateSecretEnv(s.SecretEnv, s.Env, nil); e != nil {
			return e
		}
	}
	return nil
}

// PlanServices expands container aliases and ports without touching host sockets.
func (m *Manifest) PlanServices(l *Lock, s *sandbox.Spec) error {
	if len(m.Sidecars) == 0 {
		return nil
	}
	vars := map[string]string{"{ep}": s.EpisodeDir, "{env}": l.RuntimeRoot(), "{project}": filepath.Join(l.RuntimeRoot(), "project"), "{venv}": l.VenvRealPath, "{python}": l.Executable, "{node}": l.Executable}
	for _, v := range m.Sidecars {
		vars["{sidecar."+v.Name+".host}"] = v.Name
		vars["{sidecar."+v.Name+".port}"] = strconv.Itoa(int(v.Port))
	}
	exports := map[string]string{}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	for _, v := range m.Sidecars {
		local := maps.Clone(vars)
		local["{port}"] = strconv.Itoa(int(v.Port))
		x := sandbox.Service{Name: v.Name, Kind: v.Kind, Transport: "tcp", Port: v.Port, Env: maps.Clone(s.Env), Exports: map[string]string{}, Ready: v.Ready, TimeoutMS: int64(v.Ready.TimeoutS * 1000), User: v.User, Tmpfs: slices.Clone(v.Tmpfs), ShmSize: v.ShmSize}
		if v.Image != "" {
			// Independent images own their runtime environment (especially PATH).
			// Only explicit service configuration and earlier exports cross over.
			x.Env = map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TZ": "UTC", "HOME": "/tmp", "TMPDIR": "/tmp"}
			maps.Copy(x.Env, exports)
			for _, locked := range l.SidecarImages {
				if locked.Name == v.Name && locked.Ref == v.Image {
					x.Image = locked.ID
					x.ImageDigest = locked.Digest
				}
			}
			if x.Image == "" {
				return fmt.Errorf("sidecar %s image missing from environment lock; run memeval env install", v.Name)
			}
		}
		var err error
		for k, value := range v.Env {
			x.Env[k], err = expandService(value, local)
			if err != nil {
				return err
			}
		}
		for k, value := range x.Env {
			x.Env[k], err = expandService(value, local)
			if err != nil {
				return err
			}
			local["{env."+k+"}"] = x.Env[k]
		}
		expandArgs := func(args []string) ([]string, error) {
			out := make([]string, len(args))
			for i, a := range args {
				var e error
				out[i], e = expandService(a, local)
				if e != nil {
					return nil, e
				}
			}
			return out, nil
		}
		if x.Command, err = expandArgs(v.Command); err != nil {
			return err
		}
		for _, a := range v.Init {
			cmd, e := expandArgs(a)
			if e != nil {
				return e
			}
			x.Init = append(x.Init, cmd)
		}
		if x.Ready.Exec, err = expandArgs(v.Ready.Exec); err != nil {
			return err
		}
		if x.Ready.TCP, err = expandService(v.Ready.TCP, local); err != nil {
			return err
		}
		if v.Ready.HTTP != nil {
			h := *v.Ready.HTTP
			h.URL, err = expandService(h.URL, local)
			if err != nil {
				return err
			}
			if h.Status == 0 {
				h.Status = 200
			}
			x.Ready.HTTP = &h
		}
		if err = validateContainerReady(x); err != nil {
			return err
		}
		for k, value := range v.Exports {
			for _, slot := range placeholder.FindAllString(value, -1) {
				if slot != "{ep}" && slot != "{port}" && !strings.HasPrefix(slot, "{sidecar.") {
					return fmt.Errorf("unsupported export placeholder %s", slot)
				}
			}
			x.Exports[k], err = expandService(value, local)
			if err != nil {
				return err
			}
			s.Env[k] = x.Exports[k]
			exports[k] = x.Exports[k]
			vars["{sidecar."+v.Name+".export."+k+"}"] = x.Exports[k]
		}
		s.Services = append(s.Services, x)
	}
	for i, v := range s.Command {
		var e error
		s.Command[i], e = expandService(v, vars)
		if e != nil {
			return e
		}
	}
	for k, v := range s.Env {
		var e error
		s.Env[k], e = expandService(v, vars)
		if e != nil {
			return e
		}
	}
	return nil
}
func validateContainerReady(s sandbox.Service) error {
	if len(s.Ready.Exec) > 0 {
		return nil
	}
	address := s.Ready.TCP
	if s.Ready.HTTP != nil {
		u, e := url.Parse(s.Ready.HTTP.URL)
		if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid container HTTP readiness")
		}
		address = u.Host
		if s.Ready.HTTP.Status < 100 || s.Ready.HTTP.Status > 599 {
			return fmt.Errorf("invalid readiness HTTP status")
		}
	}
	host, port, e := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if e != nil || port != strconv.Itoa(int(s.Port)) || !(host == s.Name || host == "localhost" || ip != nil && ip.IsLoopback()) {
		return fmt.Errorf("readiness must target sidecar alias/loopback and declared port")
	}
	return nil
}
