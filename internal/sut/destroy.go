package sut

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type DestroyResult struct {
	Steps     []container.CleanupStep `json:"steps"`
	Remaining []string                `json:"remaining"`
	Clean     bool                    `json:"clean"`
}

func DestroyEnvironments(ctx context.Context, b *container.Backend, cache, name string, all bool) (DestroyResult, error) {
	result := DestroyResult{Steps: []container.CleanupStep{}, Remaining: []string{}}
	root, e := CacheRoot(cache)
	if e != nil {
		return result, e
	}
	if !all && (!namePattern.MatchString(strings.Split(name, "@")[0]) || strings.ContainsAny(name, "/\\")) {
		return result, fmt.Errorf("invalid environment name")
	}
	release, e := cacheGate(root, true)
	if e != nil {
		return result, e
	}
	defer release()
	validIdentity := func(n string) bool {
		base, tail, ok := strings.Cut(n, "@")
		return ok && namePattern.MatchString(base) && tail != "" && !strings.ContainsAny(tail, "/\\*?[]@")
	}
	matches := func(n string) bool { return all || n == name || strings.HasPrefix(n, name+"@") }
	dirs, e := os.ReadDir(filepath.Join(root, "envs"))
	if e != nil && !os.IsNotExist(e) {
		return result, e
	}
	names := map[string]bool{}
	identities := map[string]bool{}
	paths := []string{}
	for _, entry := range dirs {
		n := entry.Name()
		if !matches(n) || !strings.Contains(n, "@") {
			continue
		}
		if !validIdentity(n) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return result, fmt.Errorf("unsafe environment directory %s", n)
		}
		paths = append(paths, filepath.Join(root, "envs", n))
		base, sha, _ := strings.Cut(n, "@")
		names[base] = true
		identities[base+"@"+sha[:min(12, len(sha))]] = true
	}
	images, e := b.Resources(ctx, "image", "memeval.env", "")
	if e != nil {
		return result, e
	}
	for _, im := range images {
		identity := im.Labels["memeval.env"]
		if identity == "probe" {
			continue
		}
		if matches(identity) {
			if !validIdentity(identity) {
				return result, fmt.Errorf("invalid environment identity %q", identity)
			}
			identities[identity] = true
			base, _, _ := strings.Cut(identity, "@")
			names[base] = true
		}
	}
	// Failed creates can leave resources even before a lock/image was persisted.
	for _, kind := range []string{"container", "network", "volume"} {
		rs, e := b.Resources(ctx, kind, "memeval.env", "")
		if e != nil {
			return result, e
		}
		for _, r := range rs {
			identity := r.Labels["memeval.env"]
			if identity != "probe" && matches(identity) {
				if !validIdentity(identity) {
					return result, fmt.Errorf("invalid environment identity %q", identity)
				}
				identities[identity] = true
				base, _, _ := strings.Cut(identity, "@")
				names[base] = true
			}
		}
	}
	if !all {
		base, _, _ := strings.Cut(name, "@")
		names[base] = true
	}
	// Under the catalog's exclusive lock no install/episode may acquire a gate.
	for _, p := range paths {
		unlock, e := fileGate(filepath.Join(root, "env-gates", filepath.Base(p)+".lock"), true)
		if e != nil {
			return result, e
		}
		defer unlock()
	}
	var errs []error
	for _, id := range sortedStrings(identities) {
		steps, left, e := b.CleanupScope(ctx, "memeval.env", id)
		result.Steps = append(result.Steps, steps...)
		errs = append(errs, e)
		for _, r := range left {
			result.Remaining = append(result.Remaining, r.Kind+":"+r.Name)
		}
	}
	if e = errors.Join(errs...); e != nil {
		return result, e
	}
	for _, im := range images {
		if !identities[im.Labels["memeval.env"]] {
			continue
		}
		e = b.RemoveResource(ctx, im)
		if e == nil {
			e = b.ResourceGone(ctx, im)
		}
		state := "ok"
		if e != nil {
			state = e.Error()
			errs = append(errs, e)
			result.Remaining = append(result.Remaining, "image:"+im.Name)
		}
		result.Steps = append(result.Steps, container.CleanupStep{Action: "DELETE", Kind: "image", Name: im.Name, Result: state})
	}
	remaining, e := b.Resources(ctx, "image", "memeval.env", "")
	errs = append(errs, e)
	for _, im := range remaining {
		if matches(im.Labels["memeval.env"]) && im.Labels["memeval.env"] != "probe" {
			result.Remaining = append(result.Remaining, "image:"+im.Name)
			errs = append(errs, fmt.Errorf("environment image remains: %s", im.Name))
		}
	}
	if e = errors.Join(errs...); e != nil {
		return result, e
	}
	for _, p := range paths {
		if e = os.RemoveAll(p); e != nil {
			return result, e
		}
		result.Steps = append(result.Steps, container.CleanupStep{Action: "DELETE", Kind: "env-directory", Name: p, Result: "ok"})
	}
	// Use record identity rather than a prefix that can match another env's name.
	records, _ := filepath.Glob(filepath.Join(root, "selftests", "selftest-*.json"))
	for _, p := range records {
		raw, err := os.ReadFile(p)
		if err != nil {
			return result, err
		}
		var r struct {
			SUT string `json:"sut"`
		}
		if json.Unmarshal(raw, &r) != nil || !names[r.SUT] {
			continue
		}
		if e = os.Remove(p); e != nil {
			return result, e
		}
		result.Steps = append(result.Steps, container.CleanupStep{Action: "DELETE", Kind: "selftest", Name: p, Result: "ok"})
	}
	for n := range names {
		gates, _ := filepath.Glob(filepath.Join(root, "env-gates", n+"@*.lock"))
		for _, p := range gates {
			if e = os.Remove(p); e != nil {
				return result, e
			}
			result.Steps = append(result.Steps, container.CleanupStep{Action: "DELETE", Kind: "gate", Name: p, Result: "ok"})
		}
	}
	if all {
		sha, err := b.CurrentProbeSHA()
		if err != nil {
			result.Steps = append(result.Steps, container.CleanupStep{Action: "KEEP", Kind: "probe-image", Name: "current probe unknown", Result: err.Error()})
		} else {
			probes, e := b.Resources(ctx, "image", "memeval.probe", "")
			if e != nil {
				return result, e
			}
			for _, p := range probes {
				if p.Labels["memeval.probe"] == sha {
					continue
				}
				if e = b.RemoveResource(ctx, p); e == nil {
					e = b.ResourceGone(ctx, p)
				}
				if e != nil {
					return result, e
				}
				result.Steps = append(result.Steps, container.CleanupStep{Action: "DELETE", Kind: "probe-image", Name: p.Name, Result: "ok"})
			}
		}
	}
	result.Clean = true
	result.Steps = append(result.Steps, container.CleanupStep{Action: "VERIFY", Kind: "environment", Name: name, Result: "empty; base and sidecar images retained"})
	return result, nil
}
func sortedStrings(m map[string]bool) []string {
	a := []string{}
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}
