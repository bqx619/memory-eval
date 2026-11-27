package container

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type probeResult struct {
	UID           int      `json:"uid"`
	GID           int      `json:"gid"`
	CapEff        string   `json:"cap_eff_hex"`
	NoNewPrivs    bool     `json:"no_new_privs"`
	RootReadonly  bool     `json:"rootfs_readonly"`
	ProbeWritable bool     `json:"probe_writable"`
	TmpWritable   bool     `json:"tmp_writable"`
	PID           int      `json:"pid"`
	Hostname      string   `json:"hostname"`
	EnvKeys       []string `json:"env_keys"`
}

func (b *Backend) Capabilities(ctx context.Context) (sandbox.Caps, error) {
	b.capsMu.Lock()
	defer b.capsMu.Unlock()
	b.mu.Lock()
	cached := b.caps
	b.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	c := sandbox.Caps{Host: sandbox.Host(ctx), Runtime: map[string]any{"context": b.cfg.Context}}
	fail := func(err error) (sandbox.Caps, error) { c.Enforced = false; c.Reason = err.Error(); return c, nil }
	var version struct {
		Client struct{ Version string }
		Server struct{ Version, Os, Arch string }
	}
	if err := b.decode(ctx, &version, "version", "--format", "json"); err != nil {
		return fail(fmt.Errorf("docker context %q unreachable; start OrbStack and retry: %w", b.cfg.Context, err))
	}
	var info struct {
		OSType, Architecture, KernelVersion, Driver string
		SecurityOptions                             []string
	}
	if err := b.decode(ctx, &info, "info", "--format", "json"); err != nil {
		return fail(err)
	}
	arch := map[string]string{"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}[info.Architecture]
	if version.Server.Os != "linux" || info.OSType != "linux" || "linux/"+arch != b.cfg.Platform {
		return fail(fmt.Errorf("Docker requires Linux server architecture matching %s", b.cfg.Platform))
	}
	c.Runtime = map[string]any{"client": version.Client.Version, "server": version.Server.Version, "os": version.Server.Os, "arch": arch, "kernel": info.KernelVersion, "driver": info.Driver, "security_options": info.SecurityOptions, "context": b.cfg.Context}
	image, err := b.ensureProbeImage(ctx)
	if err != nil {
		return fail(err)
	}
	r, uid, gid, err := b.probe(ctx, image, "65532:65532", 0777)
	if err != nil {
		return fail(err)
	}
	user := "65532:65532"
	mode := "uid-preserved"
	if uid == os.Getuid() && uid != 65532 {
		mode = "uid-remapped"
	} else if uid != 65532 {
		return fail(fmt.Errorf("unexpected bind mount file owner uid %d", uid))
	}
	// A native Linux bind mount preserves uid: EP is host-owned/private, so use
	// the host non-root identity after proving that identity works in the container.
	if mode == "uid-preserved" && os.Getuid() != 65532 {
		if os.Getuid() == 0 {
			return fail(fmt.Errorf("host root cannot be used as a non-root bind-mount fallback"))
		}
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	// Recheck the selected identity against a private directory exactly like EP.
	// The first permissive empty directory serves only to detect UID remapping.
	r, uid, gid, err = b.probe(ctx, image, user, 0700)
	if err != nil {
		return fail(fmt.Errorf("private episode bind probe: %w", err))
	}
	if uid != os.Getuid() {
		return fail(fmt.Errorf("selected identity bind owner mismatch"))
	}
	n, err := strconv.ParseUint(r.CapEff, 16, 64)
	checks := map[string]bool{"uid_nonroot": r.UID > 0 && fmt.Sprintf("%d:%d", r.UID, r.GID) == user, "caps_dropped": err == nil && n == 0, "no_new_privs": r.NoNewPrivs, "rootfs_readonly": r.RootReadonly, "bind_writable": r.ProbeWritable && r.TmpWritable}
	c.Runtime["checks"] = checks
	c.Runtime["user_mode"] = mode
	c.Runtime["user"] = user
	c.Runtime["bind_owner_uid"] = uid
	c.Runtime["bind_owner_gid"] = gid
	c.Runtime["probe"] = map[string]any{"uid": r.UID, "gid": r.GID, "cap_eff_hex": r.CapEff, "no_new_privs": r.NoNewPrivs, "rootfs_readonly": r.RootReadonly, "probe_writable": r.ProbeWritable, "tmp_writable": r.TmpWritable}
	c.UserMode = mode
	for name, ok := range checks {
		if !ok {
			return fail(fmt.Errorf("container capability check failed: %s", name))
		}
	}
	c.Enforced = true
	c.FileReadRestrict = true
	c.FileWriteRestrict = true
	c.DenyRead = true
	c.NetworkNone = true
	c.ProcessTree = true
	c.Mechanism = fmt.Sprintf("docker %s on linux/%s (%s); namespaces+cgroups; read-only rootfs; --network none/internal", version.Server.Version, arch, b.cfg.Context)
	b.mu.Lock()
	b.caps = &c
	b.mu.Unlock()
	return c, nil
}
func (b *Backend) probe(ctx context.Context, image, user string, mode os.FileMode) (result probeResult, uid, gid int, err error) {
	dir, err := sandbox.NewEpisode()
	if err != nil {
		return result, 0, 0, err
	}
	defer os.RemoveAll(dir)
	// The remapping discovery directory is empty; final checks use EP permissions.
	if err = os.Chmod(dir, mode); err != nil {
		return result, 0, 0, err
	}
	id := nonce()
	e := &episode{id: id, name: "memeval-" + id + "-probe", network: "none", volumes: map[string]bool{}}
	e.root = e
	defer func() {
		cleanup := b.removeContainer(context.WithoutCancel(ctx), e)
		if cleanup == nil {
			for name := range e.volumes {
				_, cleanup = b.call(context.WithoutCancel(ctx), "volume", "rm", name)
				if cleanup != nil {
					break
				}
			}
		}
		if cleanup == nil {
			_, cleanup = b.verifyEpisodeEmpty(context.WithoutCancel(ctx), e)
		}
		if cleanup != nil {
			err = fmt.Errorf("caps cleanup failed: %w (probe error: %v)", cleanup, err)
		}
	}()
	r := runConfig{Name: e.name, Network: "none", Image: image, User: user, EP: dir, Cwd: "/tmp", Labels: b.labels(id, "probe", "probe"), Command: []string{"/memeval", "__container-probe", "caps"}, ProbeMount: true}
	args, err := b.runArgs(r)
	if err != nil {
		return result, 0, 0, err
	}
	raw, err := b.call(ctx, args...)
	if err != nil {
		return result, 0, 0, err
	}
	e.containerID = strings.TrimSpace(string(raw))
	v, err := b.inspectContainer(ctx, e.name)
	if err != nil {
		return result, 0, 0, err
	}
	if err = e.recordContainer(v); err != nil {
		return result, 0, 0, err
	}
	raw, err = b.call(ctx, "start", "--attach", e.name)
	if err != nil {
		return result, 0, 0, err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, 0, 0, fmt.Errorf("invalid container caps probe: %w", err)
	}
	uid, gid, err = b.fileOwner(filepath.Join(dir, "memeval-probe-owner"))
	return result, uid, gid, err
}
