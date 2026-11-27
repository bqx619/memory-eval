package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/sandbox"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TemplateIdentity binds default flags not otherwise present in a manifest Spec.
func TemplateIdentity(c Config) any {
	return []any{"container-selftest-v3-governor-v1", c.Platform, c.TmpfsSize, c.DefaultShmSize, c.DefaultPidsLimit}
}

// Selftest uses the ordinary SUT create/start/destroy path. Only the command
// and read-only probe binary mount differ; credentials stay in the env file.
func (b *Backend) Selftest(ctx context.Context, s sandbox.Spec, o ProbeOptions) (r *SelftestResult, err error) {
	// A selftest owns its short-lived governor and ledger. It must neither reuse
	// a live run's governor nor leave its destroyed handle on that run's backend.
	probeBackend := New(b.cfg)
	probeBackend.runID = b.runID
	probeBackend.fileOwner = b.fileOwner
	b = probeBackend
	defer func() {
		if s.Network == sandbox.NetworkGateway {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			err = errors.Join(err, b.finishGovernor(cleanup))
			if b.gov != nil && b.gov.temporary {
				os.RemoveAll(b.gov.dir)
			}
		}
	}()
	r = &SelftestResult{Probe: &ProbeResult{Items: map[string]Observation{}, Facts: map[string]any{}}}
	path, sha, e := ProbeBinary(b.cfg)
	if e != nil {
		return r, e
	}
	if sha != o.LinuxBinarySHA256 {
		return r, fmt.Errorf("Linux probe binary changed since selftest Signature")
	}
	if s.KeepDir {
		return r, fmt.Errorf("selftest requires episode cleanup")
	}
	options := filepath.Join(s.EpisodeDir, ".memeval", "probe-options.json")
	output := filepath.Join(s.EpisodeDir, "probe.json")
	s.Command = []string{"/memeval", "__container-probe", "--out", output, "--options", options, "--control", o.Control}
	h, e := b.prepare(ctx, s, path)
	if e != nil {
		return r, e
	}
	ep, _ := b.episode(h)
	r.ProfileSHA256 = h.ProfileSHA256()
	var stopServices func(context.Context) error
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		destroyErr := b.Destroy(cleanup, h)
		if s.Network == sandbox.NetworkGateway {
			destroyErr = errors.Join(destroyErr, b.finishGovernor(cleanup))
			if b.gov != nil && b.gov.temporary {
				defer os.RemoveAll(b.gov.dir)
			}
		}
		err = errors.Join(err, destroyErr)
		if stopServices != nil {
			err = errors.Join(err, stopServices(cleanup))
		}
		r.EffectiveConfig = h.EffectiveConfig()
		residue, residueErr := b.verifyEpisodeEmpty(cleanup, ep)
		err = errors.Join(err, residueErr)
		check := func(k string, ok bool) { r.Probe.Items[k] = Observation{Success: ok} }
		check("container_destroyed", residue["containers_remaining"] == 0)
		check("network_destroyed", residue["networks_remaining"] == 0)
		check("volumes_none", residue["volumes_remaining"] == 0 && residue["recorded_volumes_remaining"] == 0)
		_, stat := os.Stat(h.Dir())
		check("ep_removed", os.IsNotExist(stat))
		if s.Network == sandbox.NetworkGateway {
			gone := false
			if ep.governor != nil {
				_, e := b.inspectContainer(cleanup, ep.governor.name)
				gone = containerNotFound(e)
			}
			check("relay_destroyed", gone)
		}
		r.Probe.Facts["residue"] = residue
		_, after, e := ProbeBinary(b.cfg)
		if e != nil || after != sha {
			err = errors.Join(err, fmt.Errorf("Linux probe binary changed during selftest"))
		}
	}()
	o.EpisodeDir, o.EnvRoot, o.ExtraTmpfs = h.Dir(), s.EnvRoot, s.Container.Tmpfs
	args, e := b.runArgs(ep.config)
	if e != nil {
		return r, e
	}
	// Inspect only create flags, not arguments of the replacement command.
	o.Init = slices.Contains(args[:len(args)-len(ep.config.Command)-1], "--init")
	o.PidsLimit = s.Limits.PidsMax
	if o.PidsLimit == 0 {
		o.PidsLimit = b.cfg.DefaultPidsLimit
	}
	o.Services = map[string]string{}
	for _, svc := range s.Services {
		if svc.Transport != "tcp" || svc.Port == 0 {
			return r, fmt.Errorf("container selftest sidecar %s requires container TCP declaration (P8 K4)", svc.Name)
		}
		o.Services[svc.Name] = net.JoinHostPort(svc.Name, strconv.Itoa(int(svc.Port)))
	}
	if s.Network == sandbox.NetworkGateway {
		o.GatewayURL = GatewayBaseURL(s.GatewayURL)
	}
	for k := range s.Env {
		if !strings.HasPrefix(k, "MEMEVAL_") {
			o.AllowedEnv = append(o.AllowedEnv, k)
		}
	}
	var images []struct{ Config struct{ Env []string } }
	if e = b.decode(ctx, &images, "image", "inspect", s.Container.Image.ID); e != nil {
		return r, e
	}
	for _, image := range images {
		for _, v := range image.Config.Env {
			k, _, _ := strings.Cut(v, "=")
			if !strings.HasPrefix(k, "MEMEVAL_") && !strings.HasPrefix(k, "ANTHROPIC_") && !strings.HasPrefix(k, "OPENAI_") {
				o.AllowedEnv = append(o.AllowedEnv, k)
			}
		}
	}
	// Standard image entrypoint shells may export these process bookkeeping keys.
	o.AllowedEnv = append(o.AllowedEnv, "TMPDIR", "HOSTNAME", "PWD", "SHLVL", "_")
	slices.Sort(o.AllowedEnv)
	o.AllowedEnv = slices.Compact(o.AllowedEnv)
	raw, _ := json.Marshal(o)
	if e = privateWrite(options, raw); e != nil {
		return r, e
	}
	if len(s.Services) > 0 {
		if o.StartServices == nil {
			return r, fmt.Errorf("selftest service orchestration missing")
		}
		stopServices, e = o.StartServices(ctx, b, h)
		if e != nil {
			return r, e
		}
	}
	p, e := b.Spawn(ctx, h)
	if e != nil {
		return r, e
	}
	defer sandbox.CloseReaders(p)
	p.Stdin().Close()
	drain := make(chan struct{}, 2)
	go func() { io.Copy(io.Discard, p.Stdout()); drain <- struct{}{} }()
	go func() { io.Copy(io.Discard, p.Stderr()); drain <- struct{}{} }()
	status, e := p.Wait(ctx)
	if e != nil {
		return r, e
	}
	<-drain
	<-drain
	if status.Code != 0 || status.OOM {
		return r, fmt.Errorf("container selftest probe exited %d (oom=%v)", status.Code, status.OOM)
	}
	raw, e = os.ReadFile(output)
	if e != nil {
		return r, fmt.Errorf("container selftest probe result: %w", e)
	}
	if len(raw) > 1<<20 {
		return r, fmt.Errorf("container selftest result exceeds 1 MiB")
	}
	if e = json.Unmarshal(raw, r.Probe); e != nil {
		return r, e
	}
	if r.Probe.Items == nil {
		r.Probe.Items = map[string]Observation{}
	}
	if r.Probe.Facts == nil {
		r.Probe.Facts = map[string]any{}
	}
	v, e := b.inspectContainer(ctx, ep.name)
	if e != nil {
		return r, e
	}
	noPorts := len(v.HostConfig.PortBindings) == 0
	for _, ports := range v.NetworkSettings.Ports {
		noPorts = noPorts && ports == nil
	}
	bindsOK := expectedProbeBinds(v, h.Dir(), path, o.Init)
	mo := r.Probe.Items["mounts_expected"]
	mo.Success = mo.Success && bindsOK
	if !bindsOK {
		mo.Detail += "; unexpected bind mount in docker inspect"
	}
	r.Probe.Items["mounts_expected"] = mo
	for _, child := range ep.children {
		if child.removed {
			continue
		}
		cv, e := b.inspectContainer(ctx, child.name)
		if e != nil {
			return r, e
		}
		noPorts = noPorts && len(cv.HostConfig.PortBindings) == 0
	}
	r.Probe.Items["no_published_ports"] = Observation{Success: noPorts}
	internal := ep.network == "none"
	if ep.network != "none" {
		var ns []struct{ Internal bool }
		if e = b.decode(ctx, &ns, "network", "inspect", ep.network); e != nil {
			return r, e
		}
		internal = len(ns) == 1 && ns[0].Internal
	}
	r.Probe.Items["network_internal"] = Observation{Success: internal}
	if s.Network == sandbox.NetworkGateway {
		target, _ := gatewayTarget(s.GatewayURL)
		rv, e := b.inspectContainer(ctx, ep.governor.name)
		if e != nil {
			return r, e
		}
		count, match := 0, false
		for i, arg := range rv.Config.Cmd {
			if arg == "--target" {
				count++
				match = i+1 < len(rv.Config.Cmd) && rv.Config.Cmd[i+1] == target
			}
		}
		r.Probe.Items["relay_single_target"] = Observation{Success: count == 1 && match && len(rv.HostConfig.PortBindings) == 0}
		r.Probe.Facts["egress_allowlist"] = EgressAllowlist(s.Network, s.GatewayURL)
	}
	return r, nil
}
