package container

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type episode struct {
	mu                                                       sync.Mutex
	timedOut                                                 atomic.Bool
	handle                                                   *sandbox.Prepared
	root                                                     *episode
	id, name, containerID, network, networkID, envFile, role string
	internal                                                 bool
	volumes                                                  map[string]bool
	children                                                 []*episode
	governor                                                 *episode
	readyHelpers                                             []*episode
	removed, destroyed                                       bool
	config                                                   runConfig
	readyMS                                                  float64
	imageDigest                                              string
	createdAt                                                time.Time
	namespace                                                string
}

func privateDir(p string) error             { return os.MkdirAll(p, 0700) }
func privateWrite(p string, v []byte) error { return os.WriteFile(p, v, 0600) }
func (b *Backend) Prepare(ctx context.Context, s sandbox.Spec) (h sandbox.Handle, err error) {
	return b.prepare(ctx, s, "")
}

func (b *Backend) prepare(ctx context.Context, s sandbox.Spec, probeBinary string) (h sandbox.Handle, err error) {
	if s.Network == sandbox.NetworkNone && s.RequiresLLM {
		return nil, fmt.Errorf("this variant requires gateway or open network (该变体需要 gateway 或 open 网络)")
	}
	var target string
	if s.Network == sandbox.NetworkGateway {
		var e error
		target, e = gatewayTarget(s.GatewayURL)
		if e != nil {
			return nil, e
		}
		s.Env = rewriteGatewayURL(s.Env, s.GatewayURL)
	}
	if s.Container == nil || s.Container.Image.ID == "" {
		return nil, fmt.Errorf("environment image missing; run memeval env install")
	}
	caps, e := b.Capabilities(ctx)
	if e != nil {
		return nil, e
	}
	if !caps.Enforced {
		return nil, &sandbox.Unenforceable{Reason: caps.Reason}
	}
	p, e := sandbox.PrepareEpisode(ctx, s)
	if e != nil {
		return nil, e
	}
	b.startDisclosure(ctx)
	p.External = true
	p.Extra = map[string]any{}
	ep := &episode{createdAt: time.Now(), handle: p, id: nonce(), name: "memeval-" + filepath.Base(p.Dir()) + "-sut", network: "none", role: "sut", volumes: map[string]bool{}}
	ep.root = ep
	b.mu.Lock()
	b.episodes[p] = ep
	b.order = append(b.order, ep)
	b.mu.Unlock()
	defer func() {
		if err != nil {
			cleanup := b.Destroy(context.WithoutCancel(ctx), p)
			err = errors.Join(err, cleanup)
		}
	}()
	if len(s.Services) > 0 || s.Network == sandbox.NetworkOpen || s.Network == sandbox.NetworkGateway {
		ep.network = "memeval-" + ep.id
		ep.internal = s.Network != sandbox.NetworkOpen
		args := []string{"network", "create", "--driver", "bridge"}
		if ep.internal {
			args = append(args, "--internal")
		}
		args = append(args, labelArgs(b.labels(ep.id, s.Container.EnvID, "sut"))...)
		args = append(args, ep.network)
		raw, e := b.call(ctx, args...)
		if e != nil {
			return nil, unavailable(e)
		}
		ep.networkID = strings.TrimSpace(string(raw))
	}
	ep.envFile = filepath.Join(p.Dir(), ".memeval", "env.list")
	ep.config = runConfig{LLMProfile: s.LLMProfile, Name: ep.name, Network: ep.network, Image: s.Container.Image.ID, User: b.user(), EnvFile: ep.envFile, EP: p.Dir(), Cwd: p.Spec.Cwd, Labels: b.labels(ep.id, s.Container.EnvID, "sut"), Command: s.Command, Limits: s.Limits, ExtraTmpfs: s.Container.Tmpfs}
	ep.config.ProbeBinary = probeBinary
	if ep.network != "none" {
		ep.config.Alias = "sut"
	}
	if s.Network == sandbox.NetworkGateway {
		if e = b.prepareGovernor(ctx, ep, target); e != nil {
			return nil, unavailable(e)
		}
	}
	if e = b.create(ctx, ep); e != nil {
		return nil, unavailable(e)
	}
	return p, nil
}
func (b *Backend) user() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.caps != nil {
		if u, ok := b.caps.Runtime["user"].(string); ok {
			return u
		}
	}
	return "65532:65532"
}
func (b *Backend) create(ctx context.Context, e *episode) error {
	p := e.handle
	t := time.Now()
	if err := writeEnv(e.envFile, p.Spec.Env); err != nil {
		return err
	}
	// VOLUME paths must be explicitly shadowed by tmpfs, never by anonymous storage.
	var images []struct {
		ID     string `json:"Id"`
		Config struct{ Volumes map[string]any }
	}
	if err := b.decode(ctx, &images, "image", "inspect", e.config.Image); err != nil {
		return err
	}
	if len(images) != 1 {
		return fmt.Errorf("environment image inspect missing")
	}
	for path := range images[0].Config.Volumes {
		covered := path == "/tmp"
		for _, v := range e.config.ExtraTmpfs {
			if strings.SplitN(v, ":", 2)[0] == path {
				covered = true
			}
		}
		if !covered {
			return fmt.Errorf("image VOLUME %s requires an explicit tmpfs mount", path)
		}
	}
	args, err := b.runArgs(e.config)
	if err != nil {
		return err
	}
	raw, err := b.call(ctx, args...)
	if err != nil {
		return err
	}
	e.containerID = strings.TrimSpace(string(raw))
	inspect, err := b.inspectContainer(ctx, e.name)
	if err != nil {
		return err
	}
	if err = e.recordContainer(inspect); err != nil {
		return err
	}
	p.SHA, err = b.profile(e.config, p.Spec.Container.SidecarImageDigests, p.Spec.Network)
	if err != nil {
		return err
	}
	p.Extra["container_name"] = e.name
	p.Extra["network_name"] = e.network
	p.Extra["image_id"] = e.config.Image
	p.Extra["destroyed"] = false
	p.Extra["create_ms"] = float64(time.Since(t).Microseconds()) / 1000
	p.Extra["container"] = b.disclosure(e, inspect, args)
	p.Extra["limits_enforced"] = map[string]bool{"timeout": p.Spec.Limits.Timeout > 0, "max_rss": p.Spec.Limits.MaxRSS > 0, "pids_max": true, "cpus": p.Spec.Limits.CPUs != nil}
	p.Teardown = func(ctx context.Context) error { return b.removeContainer(ctx, e) }
	return nil
}
func (b *Backend) PrepareCommand(ctx context.Context, parent sandbox.Handle, command []string, env map[string]string) (sandbox.Handle, error) {
	return b.prepareCommand(ctx, parent, command, env, "sidecar", nil)
}
func (b *Backend) PrepareServiceCommand(ctx context.Context, parent sandbox.Handle, s sandbox.Service, command []string, env map[string]string, role string) (sandbox.Handle, error) {
	return b.prepareCommand(ctx, parent, command, env, role, &s)
}
func (b *Backend) prepareCommand(ctx context.Context, parent sandbox.Handle, command []string, env map[string]string, role string, service *sandbox.Service) (sandbox.Handle, error) {
	root, err := b.episode(parent)
	if err != nil {
		return nil, err
	}
	root = root.root
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.destroyed {
		return nil, fmt.Errorf("episode destroyed")
	}
	if root.network == "none" {
		return nil, fmt.Errorf("sidecar requires an episode network")
	}
	p, err := sandbox.DeriveCommand(root.handle, command, env)
	if err != nil {
		return nil, err
	}
	p.Extra = map[string]any{}
	n := len(root.children) + 1
	e := &episode{createdAt: time.Now(), root: root, handle: p, id: root.id, name: fmt.Sprintf("memeval-%s-%s-%d", root.id, role, n), network: root.network, internal: root.internal, role: role, envFile: filepath.Join(p.Dir(), ".memeval", fmt.Sprintf("env-%d.list", n))}
	e.config = root.config
	e.config.ProbeBinary = ""
	e.config.Name = e.name
	e.config.Command = command
	e.config.EnvFile = e.envFile
	e.config.Labels = b.labels(e.id, p.Spec.Container.EnvID, role)
	if service != nil {
		e.config.Alias = service.Name
		if service.User != "" {
			e.config.User = service.User
		}
		if service.Tmpfs != nil {
			e.config.ExtraTmpfs = service.Tmpfs
		}
		e.config.ShmSize = service.ShmSize
		e.imageDigest = service.ImageDigest
		if service.Image != "" {
			e.config.Image = service.Image
			e.config.NoBind = true
			e.config.Cwd = ""
		}
		if e.imageDigest == "" {
			e.imageDigest = e.config.Image
		}
	}
	p.Spec.Env = maps.Clone(env)
	if p.Spec.Env == nil {
		p.Spec.Env = map[string]string{}
	}
	if p.Spec.Network == sandbox.NetworkGateway {
		p.Spec.Env = rewriteGatewayURL(p.Spec.Env, p.Spec.GatewayURL)
	}
	root.children = append(root.children, e)
	b.mu.Lock()
	b.episodes[p] = e
	b.mu.Unlock()
	if err = b.create(ctx, e); err != nil {
		return nil, unavailable(err)
	}
	return p, nil
}
func (b *Backend) Destroy(ctx context.Context, h sandbox.Handle) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	e, err := b.episode(h)
	if err != nil {
		return err
	}
	started := time.Now()
	defer func() {
		e.handle.Extra["destroy_ms"] = float64(time.Since(started).Microseconds()) / 1000
		if e.timedOut.Load() {
			e.handle.Extra["timeout"] = true
		}
	}()
	if e != e.root {
		if err = b.removeContainer(ctx, e); err != nil {
			return unavailable(err)
		}
		err = sandbox.Destroy(ctx, e.handle)
		e.handle.Extra["destroyed"] = err == nil
		return unavailable(err)
	}
	b.destroyMu.Lock()
	defer b.destroyMu.Unlock()
	if e.destroyed {
		return nil
	}
	t := time.Now()
	if err = b.removeContainer(ctx, e); err != nil {
		return unavailable(err)
	}
	for i := len(e.children) - 1; i >= 0; i-- {
		if err = b.removeContainer(ctx, e.children[i]); err != nil {
			return unavailable(err)
		}
	}
	for _, aux := range e.readyHelpers {
		if aux != nil {
			if err = b.removeContainer(ctx, aux); err != nil {
				return unavailable(err)
			}
		}
	}
	if err = b.disconnectGovernor(ctx, e); err != nil {
		return unavailable(err)
	}
	if e.network != "none" {
		if err = b.removeListed(ctx, "network", e.id, e.networkID); err != nil {
			return unavailable(err)
		}
	}
	for _, name := range sortedVolumes(e.volumes) {
		if _, err = b.call(ctx, "volume", "rm", name); err != nil && !notFound(err) {
			return unavailable(err)
		}
	}
	residue, err := b.verifyEpisodeEmpty(ctx, e)
	e.handle.Extra["residue"] = residue
	if err != nil {
		return unavailable(err)
	}
	if err = sandbox.Destroy(ctx, e.handle); err != nil {
		return unavailable(err)
	}
	residue["ep_removed"] = !e.handle.Spec.KeepDir
	residue["kept_eps"] = []string{}
	if e.handle.Spec.KeepDir {
		residue["kept_eps"] = []string{e.handle.Dir()}
	}
	e.destroyed = true
	e.handle.Extra["destroyed"] = true
	e.handle.Extra["destroy_ms"] = float64(time.Since(t).Microseconds()) / 1000
	if c, ok := e.handle.Extra["container"].(map[string]any); ok {
		c["residue"] = residue
	}
	return nil
}

// TimedOut is safe to query while a container's deadline is firing. A service
// timeout belongs to the enclosing runtime episode as well as that service.
func (b *Backend) TimedOut(h sandbox.Handle) bool {
	e, err := b.episode(h)
	return err == nil && e.root.timedOut.Load()
}
