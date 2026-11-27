package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func notFound(e error) bool {
	if e == nil {
		return false
	}
	s := strings.ToLower(e.Error())
	for _, kind := range []string{"object", "container", "network", "volume", "image"} {
		if strings.Contains(s, "no such "+kind) {
			return true
		}
	}
	return false
}
func containerNotFound(e error) bool {
	return notFound(e)
}
func sortedVolumes(m map[string]bool) []string {
	a := []string{}
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}
func (b *Backend) removeListed(ctx context.Context, kind, ep, id string) error {
	ids, err := b.list(ctx, kind, ep)
	if err != nil {
		return err
	}
	match := containsID(ids, id)
	if kind == "volume" {
		match = contains(ids, id)
	}
	if id == "" || !match {
		return nil
	}
	var args []string
	if kind == "container" {
		args = []string{"rm", "-f", id}
	} else {
		args = []string{kind, "rm", id}
	}
	_, err = b.call(ctx, args...)
	if notFound(err) {
		return nil
	}
	return err
}

// Attribute anonymous volumes only to a confirmed owned container. Inspect
// supplies the canonical full ID, including when a list returned a short ID.
func (e *episode) recordContainer(v inspection) error {
	if v.ID == "" || v.Config.Labels["memeval.episode"] != e.id ||
		(e.containerID != "" && !sameID(e.containerID, v.ID)) ||
		strings.TrimPrefix(v.Name, "/") != e.name {
		return fmt.Errorf("container ownership changed")
	}
	e.containerID = v.ID
	for _, m := range v.Mounts {
		if m.Type == "volume" && m.Name != "" {
			e.root.volumes[m.Name] = true
		}
	}
	return nil
}

func (b *Backend) removeContainer(ctx context.Context, e *episode) error {
	e.mu.Lock()
	var p *containerProcess
	if e.handle != nil {
		p, _ = e.handle.Child.(*containerProcess)
	}
	e.mu.Unlock()
	if p != nil {
		p.killMu.Lock()
		defer p.killMu.Unlock()
	}
	return b.removeContainerLocked(ctx, e)
}

// The process kill lock (if spawned) also serializes explicit Destroy with the
// deadline goroutine: neither may remove the object under the other's inspect.
func (b *Backend) removeContainerLocked(ctx context.Context, e *episode) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.envFile != "" {
		if err := os.Remove(e.envFile); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if e.removed {
		return nil
	}
	// Recover a create that succeeded remotely even if its response was lost.
	if e.containerID == "" {
		ids, err := b.list(ctx, "container", e.id)
		if err != nil {
			return err
		}
		for _, id := range ids {
			v, err := b.inspectContainer(ctx, id)
			if err != nil {
				return err
			}
			if strings.TrimPrefix(v.Name, "/") == e.name {
				if err := e.recordContainer(v); err != nil {
					return err
				}
				break
			}
		}
	}
	if e.containerID == "" {
		// No successful create or matching owned resource to remove. Do not
		// claim removal of a container that was never identified.
		return nil
	}
	ids, err := b.list(ctx, "container", e.id)
	if err != nil {
		return err
	}
	if containsID(ids, e.containerID) {
		// Inspect again before deletion: a failed create-inspect must not lose
		// anonymous volumes that the daemon has already allocated.
		v, err := b.inspectContainer(ctx, e.containerID)
		if err != nil && !containerNotFound(err) {
			return err
		}
		if err == nil {
			if err = e.recordContainer(v); err != nil {
				return err
			}
		}
	}
	// Even if the scoped list omitted it, the ID returned by our create is
	// authoritative. Only rm success (or already absent) completes cleanup.
	if _, err = b.call(ctx, "rm", "-f", e.containerID); err != nil && !containerNotFound(err) {
		return err
	}
	e.removed = true
	return nil
}
func (b *Backend) verifyEpisodeEmpty(ctx context.Context, e *episode) (map[string]any, error) {
	r := map[string]any{}
	total := 0
	volumeNames := map[string]bool{}
	for _, kind := range []string{"container", "network", "volume"} {
		ids, err := b.list(ctx, kind, e.id)
		if err != nil {
			return r, err
		}
		if kind == "volume" {
			for _, id := range ids {
				volumeNames[id] = true
			}
		} else {
			ids = uniqueIDs(ids)
		}
		r[kind+"s_remaining"] = len(ids)
		total += len(ids)
	}
	anonymous := 0
	for _, name := range sortedVolumes(e.volumes) {
		_, err := b.call(ctx, "volume", "inspect", name)
		if err == nil {
			anonymous++
			if !volumeNames[name] {
				total++
				volumeNames[name] = true
			}
		} else if !notFound(err) {
			return r, err
		}
	}
	r["volumes_remaining"] = len(volumeNames)
	r["recorded_volumes_remaining"] = anonymous
	r["run_clean"] = total == 0
	if total > 0 {
		return r, fmt.Errorf("episode resources remain after destroy: %v", r)
	}
	return r, nil
}
func (b *Backend) AuditService(ctx context.Context, pid int) error {
	b.mu.Lock()
	episodes := make([]*episode, 0, len(b.episodes))
	for _, e := range b.episodes {
		episodes = append(episodes, e)
	}
	b.mu.Unlock()
	var ep *episode
	for _, e := range episodes {
		e.mu.Lock()
		matches := e.handle.Child != nil && e.handle.Child.PID() == pid
		e.mu.Unlock()
		if matches {
			ep = e
			break
		}
	}
	if ep == nil {
		return fmt.Errorf("unknown service process")
	}
	_, _, err := b.auditService(ctx, ep)
	return err
}

// Serialize inspection with timeout Kill and removal. Only an owned, completed
// removal permits not-found; an unexplained disappearance remains an audit error.
func (b *Backend) auditService(ctx context.Context, ep *episode) (inspection, bool, error) {
	if p, ok := ep.handle.Child.(*containerProcess); ok {
		p.killMu.Lock()
		defer p.killMu.Unlock()
		if p.killErr != nil {
			return inspection{}, false, p.killErr
		}
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	v, err := b.inspectContainer(ctx, ep.name)
	if err != nil {
		if ep.removed && containerNotFound(err) {
			return inspection{}, true, nil
		}
		return inspection{}, false, err
	}
	if ep.removed {
		return inspection{}, false, fmt.Errorf("sidecar %s survived cleanup", ep.name)
	}
	if !sameID(v.ID, ep.containerID) || !v.State.Running || v.Config.Labels["memeval.episode"] != ep.id || len(v.HostConfig.PortBindings) > 0 || len(v.NetworkSettings.Networks) != 1 {
		return inspection{}, false, fmt.Errorf("container service isolation audit failed")
	}
	if _, ok := v.NetworkSettings.Networks[ep.network]; !ok {
		return inspection{}, false, fmt.Errorf("unexpected service network")
	}
	for _, ports := range v.NetworkSettings.Ports {
		if ports != nil {
			return inspection{}, false, fmt.Errorf("host ports published")
		}
	}
	if ep.internal {
		var ns []struct{ Internal bool }
		if err = b.decode(ctx, &ns, "network", "inspect", ep.network); err != nil {
			return inspection{}, false, err
		}
		if len(ns) != 1 || !ns[0].Internal {
			return inspection{}, false, fmt.Errorf("episode network is not internal")
		}
	}
	return v, false, nil
}

func (b *Backend) WaitService(ctx context.Context, s sandbox.Service, p sandbox.Process) error {
	e, err := b.serviceEpisode(p)
	if err != nil {
		return err
	}
	started := time.Now()
	defer func() { e.readyMS = float64(time.Since(started).Microseconds()) / 1000 }()
	if len(s.Ready.Exec) == 0 {
		if s.Transport != "tcp" || s.Port == 0 {
			return fmt.Errorf("container readiness requires TCP or exec (P8 K4)")
		}
		image, err := b.ensureProbeImage(ctx)
		if err != nil {
			return err
		}
		command := []string{"/memeval", "__container-ready", "--service", "--timeout", fmt.Sprint(time.Duration(s.TimeoutMS) * time.Millisecond)}
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(s.Port)))
		if s.Ready.HTTP != nil {
			u, err := url.Parse(s.Ready.HTTP.URL)
			if err != nil {
				return err
			}
			u.Host = address
			status := s.Ready.HTTP.Status
			if status == 0 {
				status = 200
			}
			command = append(command, "--http", u.String(), "--expect", strconv.Itoa(status))
		} else {
			command = append(command, "--tcp", address)
		}
		root := e.root
		helper := &episode{root: root, id: root.id, name: fmt.Sprintf("memeval-%s-ready-%s", root.id, s.Name), role: "ready"}
		helper.config = runConfig{Name: helper.name, Network: "container:" + e.name, Image: image, User: b.user(), Cwd: "/tmp", NoBind: true, Labels: b.labels(root.id, root.handle.Spec.Container.EnvID, "ready"), Command: command}
		root.readyHelpers = append(root.readyHelpers, helper)
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			b.removeContainer(cleanup, helper)
		}()
		if err = b.createAux(ctx, helper); err != nil {
			return err
		}
		if _, err = b.call(ctx, "start", "--attach", helper.name); err != nil {
			return err
		}
		raw, err := b.call(ctx, "wait", helper.name)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) != "0" {
			return fmt.Errorf("sidecar readiness failed: exit %s", strings.TrimSpace(string(raw)))
		}
		if err = b.removeContainer(ctx, helper); err != nil {
			return err
		}
		return b.AuditService(ctx, p.PID())
	}
	for {
		if _, err := b.call(ctx, append([]string{"exec", e.name}, s.Ready.Exec...)...); err == nil {
			return b.AuditService(ctx, p.PID())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
}

func (b *Backend) serviceEpisode(p sandbox.Process) (*episode, error) {
	cp, ok := p.(*containerProcess)
	if !ok || cp.backend != b {
		return nil, fmt.Errorf("foreign service process")
	}
	return cp.episode, nil
}
func (b *Backend) ExecService(ctx context.Context, p sandbox.Process, args []string, out io.Writer) error {
	e, err := b.serviceEpisode(p)
	if err != nil {
		return err
	}
	if err = b.AuditService(ctx, p.PID()); err != nil {
		return err
	}
	cmd := b.command(ctx, append([]string{"exec", e.name}, args...)...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err = procexec.Run(cmd); err != nil {
		if ctx.Err() != nil && (errors.Is(err, ctx.Err()) || cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == -1) {
			return ctx.Err()
		}
		return fmt.Errorf("sidecar init exec failed: %w", err)
	}
	return b.AuditService(ctx, p.PID())
}
func (b *Backend) ServiceFacts(ctx context.Context, p sandbox.Process) (sandbox.ServiceEvidence, error) {
	e, err := b.serviceEpisode(p)
	if err != nil {
		return sandbox.ServiceEvidence{}, err
	}
	info, stopped, err := b.auditService(ctx, e)
	if err != nil {
		return sandbox.ServiceEvidence{}, err
	}
	keys := []string{}
	for _, entry := range info.Config.Env {
		key, _, _ := strings.Cut(entry, "=")
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return sandbox.ServiceEvidence{EnvKeys: keys, Container: e.name, Image: e.config.Image, ImageDigest: e.imageDigest, Network: e.network, Internal: e.internal, HostPortsPublished: false, ExecReadyMS: e.readyMS, Stopped: stopped, Timeout: e.timedOut.Load()}, nil
}
func (b *Backend) ServiceStopped(ctx context.Context, p sandbox.Process) error {
	e, err := b.serviceEpisode(p)
	if err != nil {
		return err
	}
	_, err = b.inspectContainer(ctx, e.name)
	if containerNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("sidecar %s survived cleanup", e.name)
}
