package sandbox

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
)

// CommandBackend starts additional commands under the SAME episode policy.
// Children are owned by the original handle and destroyed in reverse order.
type CommandBackend interface {
	PrepareCommand(context.Context, Handle, []string, map[string]string) (Handle, error)
}

func DeriveCommand(parent Handle, command []string, env map[string]string) (*Prepared, error) {
	p, ok := parent.(*Prepared)
	if !ok {
		return nil, fmt.Errorf("foreign command handle")
	}
	if !p.External && (len(command) == 0 || !filepath.IsAbs(command[0])) {
		return nil, fmt.Errorf("absolute service executable required")
	}
	exe := ""
	if len(command) > 0 {
		exe = filepath.Clean(command[0])
	}
	if !p.External {
		var err error
		exe, err = Real(exe)
		if err != nil {
			return nil, err
		}
	}
	if !p.External && (p.Spec.EnvRoot == "" || !Within(exe, p.Spec.EnvRoot)) {
		return nil, fmt.Errorf("service executable must be inside env")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.destroyed {
		return nil, fmt.Errorf("episode already destroyed")
	}
	s := p.Spec
	s.Command = append([]string(nil), command...)
	s.Env = maps.Clone(env)
	h := &Prepared{Spec: s, Derived: true, External: p.External}
	p.Commands = append(p.Commands, h)
	return h, nil
}

// Service is an expanded, runtime-only declaration. Container ports are internal
// to the episode network and never published to the host.
type Service struct {
	Image, ImageDigest, User, ShmSize string
	Tmpfs                             []string
	Unmanaged                         bool
	Name, Kind, Transport             string
	Init                              [][]string
	Command                           []string
	Env                               map[string]string
	Exports                           map[string]string
	Ready                             ServiceReady
	Port                              uint16
	TimeoutMS                         int64
}
type ServiceReady struct {
	Unix     string     `json:"-"` // none-backend fixtures only, not part of manifest v3
	Exec     []string   `json:"exec,omitempty"`
	TCP      string     `json:"tcp,omitempty"`
	HTTP     *HTTPReady `json:"http,omitempty"`
	TimeoutS float64    `json:"timeout_s,omitempty"`
}
type HTTPReady struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// ServiceEvidence records runtime ownership, independent of the host CLI PID.
type ServiceEvidence struct {
	Stopped            bool     `json:"stopped,omitempty"`
	Timeout            bool     `json:"timeout,omitempty"`
	EnvKeys            []string `json:"env_keys"`
	Container          string   `json:"container,omitempty"`
	Image              string   `json:"image,omitempty"`
	ImageDigest        string   `json:"image_digest,omitempty"`
	Network            string   `json:"network,omitempty"`
	Internal           bool     `json:"internal"`
	HostPortsPublished bool     `json:"host_ports_published"`
	ExecReadyMS        float64  `json:"exec_ready_ms"`
}
