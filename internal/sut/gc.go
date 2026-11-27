package sut

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type GCOptions struct {
	Apply, BaseImages bool
	OlderThan         time.Duration
	Cache, EPRoot     string
	Now               time.Time
}
type GCItem struct {
	Action   string              `json:"action"`
	Kind     string              `json:"kind"`
	Size     int64               `json:"size_bytes"`
	Name     string              `json:"name"`
	Reason   string              `json:"reason"`
	Result   string              `json:"result,omitempty"`
	Resource *container.Resource `json:"-"`
	root     string
	stamp    os.FileInfo
}
type GCResult struct {
	Apply     bool     `json:"apply"`
	Items     []GCItem `json:"items"`
	Remaining []string `json:"remaining"`
}

var episodeName = regexp.MustCompile(`^(?:[0-9a-f]{8}|build-[A-Za-z0-9_-]+)$`)

func GarbageCollect(ctx context.Context, b *container.Backend, o GCOptions) (GCResult, error) {
	result := GCResult{Apply: o.Apply, Items: []GCItem{}, Remaining: []string{}}
	if o.OlderThan < 0 {
		return result, fmt.Errorf("--older-than must be nonnegative")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	root, e := CacheRoot(o.Cache)
	if e != nil {
		return result, e
	}
	o.Cache = root
	if o.Apply {
		release, e := cacheGate(root, true)
		if e != nil {
			return result, e
		}
		defer release()
	}
	items, e := gcCandidates(ctx, b, o)
	result.Items = items
	if e != nil || !o.Apply {
		return result, e
	}
	var errs []error
	for i := range result.Items {
		item := &result.Items[i]
		if item.Action != "DELETE" {
			continue
		}
		var err error
		if item.Resource != nil {
			r := *item.Resource
			if container.OwnerActive(ctx, r.Labels) {
				item.Result = "SKIP: owner became active"
				continue
			}
			if r.Kind == "image" && r.Labels["memeval.env"] != "" {
				live, e := b.OwnedResources(ctx, "container")
				if e != nil {
					err = e
				}
				for _, c := range live {
					if c.Image == r.ID || c.Labels["memeval.env"] == r.Labels["memeval.env"] {
						err = fmt.Errorf("image still referenced by env containers: %s", r.Name)
					}
				}
			}
			if err != nil { /* preserve the dependent image */
			} else if item.Reason == "unused-approved-image" {
				_, err = b.ImageCommand(ctx, "image", "rm", r.ID)
			} else {
				err = b.RemoveResource(ctx, r)
			}
			if err == nil {
				err = b.ResourceGone(ctx, r)
			}
		} else {
			if item.Kind == "episode-directory" || item.Kind == "env-directory" {
				rows, e := b.OwnedResources(ctx, "container")
				if e != nil {
					err = e
				}
				for _, r := range rows {
					if item.Kind == "env-directory" && r.Labels["memeval.env"] == directoryEnvIdentity(item.Name) {
						err = fmt.Errorf("environment still referenced: %s", item.Name)
					}
					for _, m := range r.Mounts {
						if m.Type == "bind" && (sandbox.Within(canonicalHostPath(m.Source), item.Name) || sandbox.Within(item.Name, canonicalHostPath(m.Source))) {
							err = fmt.Errorf("directory still referenced: %s", item.Name)
						}
					}
				}
			}
			if err == nil {
				err = removeGCHost(*item)
			}
		}
		if err != nil {
			item.Result = err.Error()
			result.Remaining = append(result.Remaining, item.Kind+":"+item.Name)
			errs = append(errs, err)
		} else {
			item.Result = "removed; verified absent"
		}
	}
	// Re-enumerate, so daemon-side silent no-ops cannot be reported as success.
	planned := map[string]bool{}
	for _, item := range result.Items {
		if item.Action == "DELETE" {
			planned[item.Kind+":"+item.Name] = true
		}
	}
	next, e := gcCandidates(ctx, b, o)
	errs = append(errs, e)
	for _, item := range next {
		if item.Action == "DELETE" && planned[item.Kind+":"+item.Name] {
			name := item.Kind + ":" + item.Name
			if !stringIn(result.Remaining, name) {
				result.Remaining = append(result.Remaining, name)
				errs = append(errs, fmt.Errorf("gc candidate remains: %s", name))
			}
		}
	}
	return result, errors.Join(errs...)
}
func stringIn(a []string, s string) bool {
	for _, v := range a {
		if s == v {
			return true
		}
	}
	return false
}
func gcCandidates(ctx context.Context, b *container.Backend, o GCOptions) ([]GCItem, error) {
	out := []GCItem{}
	owned := map[string][]container.Resource{}
	refs := []string{}
	for _, kind := range []string{"container", "network", "volume", "image"} {
		rows, e := b.OwnedResources(ctx, kind)
		if e != nil {
			return out, e
		}
		owned[kind] = rows
	}
	locks, e := localLocks(o.Cache)
	if e != nil {
		return out, e
	}
	locked := map[string]bool{}
	lockedEnv := map[string]bool{}
	activeEnv := map[string]bool{}
	activeImages := map[string]bool{}
	for _, r := range owned["container"] {
		if r.Running || container.OwnerActive(ctx, r.Labels) {
			if env := r.Labels["memeval.env"]; env != "" {
				activeEnv[env] = true
			}
			if r.Image != "" {
				activeImages[r.Image] = true
			}
		}
	}
	for _, l := range locks {
		locked[l.Image.ID] = true
		lockedEnv[l.Name+"@"+l.LockSHA256[:min(12, len(l.LockSHA256))]] = true
	}
	add := func(r container.Resource, action, reason string) {
		out = append(out, GCItem{Action: action, Kind: r.Kind, Name: r.Name, Size: r.Size, Reason: reason, Resource: &r})
	}
	anonymous := map[string]container.Resource{}
	anonymousProtected := map[string]bool{}
	for _, r := range owned["container"] {
		active := container.OwnerActive(ctx, r.Labels)
		old := !r.Created.IsZero() && o.Now.Sub(r.Created) >= o.OlderThan
		dead := r.Labels["memeval.owner_pid"] != "" && !active
		for _, m := range r.Mounts {
			if m.Type == "bind" && (active || (!old && !dead)) {
				refs = append(refs, canonicalHostPath(m.Source))
			}
			if m.Type == "volume" && m.Name != "" {
				if active || (!old && !dead) {
					anonymousProtected[m.Name] = true
				} else {
					anonymous[m.Name] = container.Resource{Kind: "volume", ID: m.Name, Name: m.Name, Anonymous: true}
				}
			}
		}
	}
	for _, kind := range []string{"container", "network", "volume"} {
		for _, r := range owned[kind] {
			active := container.OwnerActive(ctx, r.Labels)
			old := !r.Created.IsZero() && o.Now.Sub(r.Created) >= o.OlderThan
			dead := r.Labels["memeval.owner_pid"] != "" && !active
			switch {
			case active:
				add(r, "KEEP", "owner-active")
			case dead:
				add(r, "DELETE", "owner-absent")
			case old:
				add(r, "DELETE", "older-than")
			default:
				add(r, "KEEP", "recent-or-unknown-age")
			}
		}
		rows, e := b.UnlabelledNames(ctx, kind)
		if e != nil {
			return out, e
		}
		for _, r := range rows {
			add(r, "LIST", "unlabelled")
		}
	}
	for name, r := range anonymous {
		if anonymousProtected[name] {
			continue
		}
		out = append(out, GCItem{Action: "DELETE", Kind: "volume", Name: name, Reason: "owned-container-anonymous-volume", Resource: &r})
	}
	probeSHA, probeErr := b.CurrentProbeSHA()
	for _, r := range owned["image"] {
		label := r.Labels["memeval.env"]
		switch {
		case activeEnv[label] || activeImages[r.ID]:
			add(r, "KEEP", "active-container-reference")
		case container.OwnerActive(ctx, r.Labels):
			add(r, "KEEP", "owner-active")
		case r.Labels["memeval.probe"] != "":
			if probeErr != nil || r.Labels["memeval.probe"] == probeSHA {
				add(r, "KEEP", "current-or-unknown-probe")
			} else {
				add(r, "DELETE", "stale-probe")
			}
		case locked[r.ID] || lockedEnv[label]:
			add(r, "KEEP", "installed-lock")
		case label != "":
			add(r, "DELETE", "orphan-env-image")
		case len(r.RepoTags) == 0:
			add(r, "DELETE", "labelled-dangling-image")
		default:
			add(r, "KEEP", "labelled-tagged-image")
		}
	}
	if o.BaseImages {
		approved := map[string]bool{}
		for _, v := range BaseImages() {
			approved[v.Ref+"@"+v.Digest] = true
		}
		var presets map[string]Sidecar
		_ = json.Unmarshal(presetData, &presets)
		for _, p := range presets {
			approved[p.Image] = true
		}
		for _, ref := range sortedStrings(approved) {
			r, e := b.Resource(ctx, "image", ref)
			if e != nil {
				if strings.Contains(strings.ToLower(e.Error()), "no such image") {
					continue
				}
				return out, e
			}
			used := false
			for _, l := range locks {
				for _, base := range l.Image.BaseImages {
					if base == ref {
						used = true
					}
				}
				for _, side := range l.SidecarImages {
					if side.Ref == ref {
						used = true
					}
				}
			}
			for _, im := range owned["image"] {
				if im.Labels["memeval.env"] == "" || im.Labels["memeval.probe"] != "" {
					continue
				}
				if len(r.Layers) == 0 || len(im.Layers) == 0 || layerPrefix(r.Layers, im.Layers) {
					used = true
				}
			}
			if used {
				add(r, "KEEP", "image-parent-or-locked")
			} else {
				add(r, "DELETE", "unused-approved-image")
			}
		}
	}
	epRoot := o.EPRoot
	if epRoot == "" {
		epRoot = dotenv.Value("MEMEVAL_EP_ROOT")
	}
	if epRoot == "" {
		epRoot = "/private/tmp/memeval"
		if runtime.GOOS != "darwin" {
			epRoot = "/tmp/memeval"
		}
	}
	appendHost := func(path, root, kind string, eligible bool, reason string) error {
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		size := int64(0)
		_ = filepath.Walk(path, func(_ string, fi os.FileInfo, e error) error {
			if e == nil && fi.Mode().IsRegular() {
				size += fi.Size()
			}
			return nil
		})
		real, e := filepath.EvalSymlinks(path)
		safe := e == nil && real == filepath.Clean(path) && sandbox.Within(real, root) && real != root
		action := "KEEP"
		if !safe {
			reason = "unsafe-path"
		} else if eligible && o.Now.Sub(st.ModTime()) >= o.OlderThan {
			action = "DELETE"
		}
		out = append(out, GCItem{Action: action, Kind: kind, Size: size, Name: path, Reason: reason, root: root, stamp: st})
		return nil
	}
	epRoot, e = filepath.Abs(epRoot)
	if e != nil {
		return out, e
	}
	if real, err := filepath.EvalSymlinks(epRoot); err == nil {
		epRoot = real
	}
	entries, e := os.ReadDir(epRoot)
	if e != nil && !os.IsNotExist(e) {
		return out, e
	}
	for _, entry := range entries {
		if !episodeName.MatchString(entry.Name()) {
			continue
		}
		p := filepath.Join(epRoot, entry.Name())
		referenced := false
		for _, ref := range refs {
			if sandbox.Within(ref, p) || sandbox.Within(p, ref) {
				referenced = true
			}
		}
		reason := "expired-unreferenced-episode"
		if referenced {
			reason = "container-reference"
		}
		if e = appendHost(p, epRoot, "episode-directory", !referenced, reason); e != nil {
			return out, e
		}
	}
	// Failed builds deliberately retain diagnostics. They are collectible only
	// without a lock, after the age limit, and without a container reference.
	envs := filepath.Join(o.Cache, "envs")
	entries, e = os.ReadDir(envs)
	if e != nil && !os.IsNotExist(e) {
		return out, e
	}
	for _, entry := range entries {
		p := filepath.Join(envs, entry.Name())
		identity := directoryEnvIdentity(p)
		if !entry.IsDir() || identity == "" || !failedInstallDirectory(p) {
			continue
		}
		marker, err := os.Stat(filepath.Join(p, "install-failure.log"))
		if err != nil {
			return out, err
		}
		eligible := o.Now.Sub(marker.ModTime()) >= o.OlderThan
		reason := "failed-install"
		for _, r := range owned["container"] {
			referenced := r.Labels["memeval.env"] == identity
			for _, m := range r.Mounts {
				if m.Type == "bind" && (sandbox.Within(canonicalHostPath(m.Source), p) || sandbox.Within(p, canonicalHostPath(m.Source))) {
					referenced = true
				}
			}
			if referenced {
				eligible, reason = false, "container-reference"
			}
		}
		if e = appendHost(p, envs, "env-directory", eligible, reason); e != nil {
			return out, e
		}
	}
	for _, spec := range []struct{ dir, pattern, kind string }{{"selftests", "selftest-*.json", "selftest"}, {"env-gates", "*@*.lock", "gate"}} {
		dir := filepath.Join(o.Cache, spec.dir)
		paths, _ := filepath.Glob(filepath.Join(dir, spec.pattern))
		for _, p := range paths {
			eligible := true
			reason := "expired-selftest"
			if spec.kind == "gate" {
				env := filepath.Join(o.Cache, "envs", strings.TrimSuffix(filepath.Base(p), ".lock"))
				_, err := os.Lstat(env)
				eligible = os.IsNotExist(err)
				reason = "orphan-gate"
				if !eligible {
					reason = "installed-lock"
				}
			}
			if spec.kind == "selftest" {
				raw, e := os.ReadFile(p)
				if e != nil {
					return out, e
				}
				var r struct {
					CreatedAt time.Time `json:"created_at"`
				}
				if json.Unmarshal(raw, &r) != nil || r.CreatedAt.IsZero() {
					eligible = false
					reason = "invalid-record-retained"
				} else {
					eligible = o.Now.Sub(r.CreatedAt) > 24*time.Hour
				}
			}
			if e = appendHost(p, dir, spec.kind, eligible, reason); e != nil {
				return out, e
			}
		}
	}
	return out, nil
}
func layerPrefix(base, env []string) bool {
	if len(base) > len(env) {
		return false
	}
	for i, v := range base {
		if env[i] != v {
			return false
		}
	}
	return true
}
func removeGCHost(item GCItem) error {
	st, e := os.Lstat(item.Name)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	real, e := filepath.EvalSymlinks(item.Name)
	if e != nil || real != item.Name || !sandbox.Within(real, item.root) || !os.SameFile(st, item.stamp) || !st.ModTime().Equal(item.stamp.ModTime()) {
		return fmt.Errorf("gc path changed: %s", item.Name)
	}
	if item.Kind == "env-directory" && !failedInstallDirectory(item.Name) {
		return fmt.Errorf("failed install state changed: %s", item.Name)
	}
	if item.Kind == "gate" {
		release, e := fileGate(item.Name, true)
		if e != nil {
			return e
		}
		defer release()
	}
	if e = os.RemoveAll(item.Name); e != nil {
		return e
	}
	if _, e = os.Lstat(item.Name); !os.IsNotExist(e) {
		return fmt.Errorf("gc path remains: %s", item.Name)
	}
	return nil
}

func directoryEnvIdentity(path string) string {
	name, sha, ok := strings.Cut(filepath.Base(path), "@")
	if !ok || !namePattern.MatchString(name) || len(sha) < 12 || len(sha) > 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		return ""
	}
	return name + "@" + sha[:12]
}

func failedInstallDirectory(path string) bool {
	if _, e := os.Lstat(filepath.Join(path, "env.lock.json")); !os.IsNotExist(e) {
		return false
	}
	marker, e := os.Lstat(filepath.Join(path, "install-failure.log"))
	return e == nil && marker.Mode().IsRegular()
}

func canonicalHostPath(p string) string {
	if r, e := filepath.EvalSymlinks(p); e == nil {
		return r
	}
	return filepath.Clean(p)
}
