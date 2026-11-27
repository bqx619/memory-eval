package container

import (
	"context"
	"encoding/json"
	"maps"
	"memeval/internal/sandbox"
	"strings"
	"time"
)

func (b *Backend) disclosure(e *episode, v inspection, args []string) map[string]any {
	b.mu.Lock()
	runtime := maps.Clone(b.caps.Runtime)
	runID := b.runID
	b.mu.Unlock()
	shm := e.config.ShmSize
	if shm == "" {
		shm = b.cfg.DefaultShmSize
	}
	image := e.handle.Spec.Container.Image
	if image.ID != e.config.Image {
		image = sandbox.Image{ID: e.config.Image, Platform: b.cfg.Platform}
	}
	return map[string]any{"run_id": runID, "runtime": runtime, "governor": b.governorDisclosure(), "egress_allowlist": EgressAllowlist(e.handle.Spec.Network, e.handle.Spec.GatewayURL), "image": image, "sidecar_images": append([]string{}, e.handle.Spec.Container.SidecarImageDigests...), "network": map[string]any{"mode": e.handle.Spec.Network, "name": e.network, "internal": e.internal, "dns_policy": map[bool]string{true: "internal aliases only; no external DNS", false: "runtime DNS"}[e.handle.Spec.Network != sandbox.NetworkOpen]}, "flags": publicFlags(args), "user": e.config.User, "user_mode": runtime["user_mode"], "tmpfs": b.tmpfsMounts(e.config.ExtraTmpfs), "shm_size": shm, "limits_enforced": map[string]bool{"timeout": e.handle.Spec.Limits.Timeout > 0, "max_rss": e.handle.Spec.Limits.MaxRSS > 0, "pids_max": true, "cpus": e.handle.Spec.Limits.CPUs != nil}, "mounts": v.Mounts, "host_ports_published": false, "residue": nil, "system_df": map[string]any{"before": nil, "after": nil}}
}
func publicFlags(a []string) []string {
	out := []string{}
	for i := 1; i < len(a); i++ {
		if a[i] == "--label" {
			i++
			continue
		}
		if a[i] == "--env-file" {
			out = append(out, a[i], "<removed after start>")
			i++
			continue
		}
		if len(a[i]) < 1 || a[i][0] != '-' {
			break
		}
		out = append(out, a[i])
		switch a[i] {
		case "--read-only", "--init", "--interactive":
		default:
			if i+1 < len(a) {
				i++
				out = append(out, a[i])
			}
		}
	}
	return out
}

// systemDF reports shared aggregate use only. It never enumerates or cleans
// unrelated resources, and a concurrent project's usage is not attributed to us.
func (b *Backend) systemDF(ctx context.Context) any {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := b.call(ctx, "system", "df", "--format", "{{json .}}")
	if err != nil {
		return map[string]any{"unavailable": err.Error()}
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err = json.Unmarshal([]byte(line), &row); err != nil {
			return map[string]any{"unavailable": "invalid aggregate system df JSON"}
		}
		rows = append(rows, row)
	}
	return rows
}
func (b *Backend) startDisclosure(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.dfStarted {
		b.dfStarted = true
		b.dfBefore = b.systemDF(ctx)
	}
}
func (b *Backend) Report(ctx context.Context) map[string]any {
	gov := b.governorDisclosure()
	after := b.systemDF(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	rows := []map[string]any{}
	var last map[string]any
	counts := map[string]any{"containers_remaining": 0, "networks_remaining": 0, "volumes_remaining": 0, "recorded_volumes_remaining": 0, "ep_removed": true, "kept_eps": []string{}, "run_clean": true}
	for _, e := range b.order {
		if c, ok := e.handle.Extra["container"].(map[string]any); ok {
			last = maps.Clone(c)
			rows = append(rows, c)
			if r, ok := e.handle.Extra["residue"].(map[string]any); ok {
				for _, k := range []string{"containers_remaining", "networks_remaining", "volumes_remaining", "recorded_volumes_remaining"} {
					if n, ok := r[k].(int); ok {
						counts[k] = counts[k].(int) + n
					}
				}
				if r["run_clean"] != true {
					counts["run_clean"] = false
				}
				if r["ep_removed"] != true {
					counts["ep_removed"] = false
				}
				if kept, ok := r["kept_eps"].([]string); ok {
					counts["kept_eps"] = append(counts["kept_eps"].([]string), kept...)
				}
			} else {
				counts["run_clean"] = false
				counts["ep_removed"] = false
			}
		}
	}
	if last == nil {
		last = map[string]any{"run_id": b.runID}
	}
	last["episodes"] = rows
	if gov != nil {
		last["governor"] = gov
	}
	if b.runResidue != nil {
		counts = b.runResidue
	}
	last["residue"] = counts
	last["system_df"] = map[string]any{"before": b.dfBefore, "after": after, "scope": "shared daemon aggregate; changes may include other projects; no cache cleanup"}
	if snapshot, ok := counts["system_df"]; ok {
		last["system_df"] = snapshot
	}
	return last
}
