package container

import (
	"context"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
)

type Observation struct {
	Errno      int     `json:"errno,omitempty"`
	Success    bool    `json:"success"`
	DurationMS float64 `json:"duration_ms"`
	Detail     string  `json:"detail,omitempty"`
}
type ProbeResult struct {
	Items       map[string]Observation `json:"items"`
	ExtraFDs    []int                  `json:"extra_fds,omitempty"`
	ObservedFDs []procexec.Descriptor  `json:"observed_fds,omitempty"`
	Facts       map[string]any         `json:"facts,omitempty"`
}

// ProbeOptions contains paths, public control certificates and key names only.
// Environment values (including provider credentials) never enter this file.
type ProbeOptions struct {
	// Orchestration stays above the backend to avoid depending on adapter/SDK code.
	StartServices     func(context.Context, sandbox.Sandbox, sandbox.Handle) (func(context.Context) error, error) `json:"-"`
	EpisodeDir        string                                                                                      `json:"episode_dir"`
	EnvRoot           string                                                                                      `json:"env_root"`
	HostPaths         []string                                                                                    `json:"host_paths"`
	AllowedEnv        []string                                                                                    `json:"allowed_env"`
	ExtraTmpfs        []string                                                                                    `json:"extra_tmpfs"`
	PidsLimit         int                                                                                         `json:"pids_limit"`
	Init              bool                                                                                        `json:"init"`
	Control           string                                                                                      `json:"control"`
	Control6          string                                                                                      `json:"control6,omitempty"`
	ControlCA         string                                                                                      `json:"control_ca"`
	GatewayURL        string                                                                                      `json:"gateway_url,omitempty"`
	Services          map[string]string                                                                           `json:"services,omitempty"`
	LinuxBinarySHA256 string                                                                                      `json:"linux_binary_sha256"`
}

type SelftestResult struct {
	Probe           *ProbeResult
	ProfileSHA256   string
	EffectiveConfig map[string]any
}
