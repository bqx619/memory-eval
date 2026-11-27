package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Resource is obtained only from a label-filtered inventory, or a name-filtered
// list-only inventory. Anonymous volumes are attributed by an owned container.
type Resource struct {
	Kind      string            `json:"kind"`
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	Created   time.Time         `json:"created,omitempty"`
	Size      int64             `json:"size_bytes"`
	Mounts    []ResourceMount   `json:"mounts,omitempty"`
	Layers    []string          `json:"layers,omitempty"`
	RepoTags  []string          `json:"repo_tags,omitempty"`
	Running   bool              `json:"running,omitempty"`
	Image     string            `json:"image,omitempty"`
	Anonymous bool              `json:"-"`
}
type ResourceMount struct{ Type, Name, Source string }
type CleanupStep struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Result string `json:"result"`
}

func HasOwnership(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, "memeval.") {
			return true
		}
	}
	return false
}
func resourceListArgs(kind string) []string {
	switch kind {
	case "container":
		return []string{"ps", "-a", "--no-trunc"}
	case "image":
		return []string{"images", "--no-trunc"}
	case "network":
		return []string{"network", "ls", "--no-trunc"}
	default:
		return []string{"volume", "ls"}
	}
}
func (b *Backend) Resource(ctx context.Context, kind, id string) (Resource, error) {
	var rows []struct {
		ID        string `json:"Id"`
		Name      string
		Created   json.RawMessage
		CreatedAt string
		Size      int64
		Labels    map[string]string
		Config    struct{ Labels map[string]string }
		Mounts    []ResourceMount
		RootFS    struct{ Layers []string }
		RepoTags  []string
		State     struct{ Running bool }
		Image     string
	}
	err := b.decode(ctx, &rows, kind, "inspect", id)
	if err != nil {
		return Resource{}, err
	}
	if len(rows) != 1 {
		return Resource{}, fmt.Errorf("expected one %s", kind)
	}
	v := rows[0]
	labels := v.Labels
	if kind == "container" || kind == "image" {
		labels = v.Config.Labels
	}
	r := Resource{Kind: kind, ID: v.ID, Name: strings.TrimPrefix(v.Name, "/"), Labels: labels, Size: v.Size, Mounts: v.Mounts, Layers: v.RootFS.Layers, RepoTags: v.RepoTags, Running: v.State.Running, Image: v.Image}
	if kind == "volume" {
		r.ID = v.Name
	}
	if r.ID == "" {
		r.ID = id
	}
	if r.Name == "" {
		r.Name = id
	}
	if kind == "image" && len(r.RepoTags) > 0 {
		r.Name = r.RepoTags[0]
	}
	var stamp string
	_ = json.Unmarshal(v.Created, &stamp)
	if stamp == "" {
		stamp = v.CreatedAt
	}
	r.Created, _ = time.Parse(time.RFC3339Nano, stamp)
	return r, nil
}

// Resources always applies a positive ownership filter before inspecting.
func (b *Backend) Resources(ctx context.Context, kind, key, value string) ([]Resource, error) {
	if !strings.HasPrefix(key, "memeval.") {
		return nil, fmt.Errorf("ownership label required")
	}
	filter := "label=" + key
	if value != "" {
		filter += "=" + value
	}
	args := append(resourceListArgs(kind), "--filter", filter, "-q")
	raw, e := b.call(ctx, args...)
	if e != nil {
		return nil, e
	}
	out := []Resource{}
	ids := strings.Fields(string(raw))
	if kind != "volume" {
		ids = uniqueIDs(ids)
	}
	for _, id := range ids {
		r, e := b.Resource(ctx, kind, id)
		if notFound(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if _, ok := r.Labels[key]; !ok || value != "" && r.Labels[key] != value {
			return nil, fmt.Errorf("%s ownership changed: %s", kind, id)
		}
		out = append(out, r)
	}
	return out, nil
}
func (b *Backend) OwnedResources(ctx context.Context, kind string) ([]Resource, error) {
	out := []Resource{}
	seen := map[string]bool{}
	// Docker label filters are conjunctive, so enumerate keys separately.
	for _, key := range []string{"memeval.run", "memeval.episode", "memeval.env", "memeval.role", "memeval.owner_pid", "memeval.owner_start", "memeval.probe", "memeval.lock", "memeval.input", "memeval.framework"} {
		rs, e := b.Resources(ctx, kind, key, "")
		if e != nil {
			return nil, e
		}
		for _, r := range rs {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}
func (b *Backend) UnlabelledNames(ctx context.Context, kind string) ([]Resource, error) {
	// Never inspect a name match: a similar name does not establish ownership.
	args := append(resourceListArgs(kind), "--filter", "name=memeval-", "--format", "json")
	raw, e := b.call(ctx, args...)
	if e != nil {
		return nil, e
	}
	out := []Resource{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row struct{ ID, Name, Names, Labels string }
		if e = json.Unmarshal([]byte(line), &row); e != nil {
			return nil, e
		}
		if strings.Contains(row.Labels, "memeval.") {
			continue
		}
		name := row.Name
		if name == "" {
			name = row.Names
		}
		if !strings.HasPrefix(name, "memeval-") {
			continue
		}
		id := row.ID
		if id == "" {
			id = name
		}
		out = append(out, Resource{Kind: kind, ID: id, Name: name})
	}
	return out, nil
}
func (b *Backend) RemoveResource(ctx context.Context, r Resource) error {
	if !HasOwnership(r.Labels) && !r.Anonymous {
		return fmt.Errorf("refusing unowned %s %s", r.Kind, r.Name)
	}
	// Recheck labels just before mutation. Anonymous volumes have explicit
	// provenance from Mounts of an already authenticated owned container.
	current, e := b.Resource(ctx, r.Kind, r.ID)
	if notFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !r.Anonymous {
		for k, v := range current.Labels {
			if strings.HasPrefix(k, "memeval.") && r.Labels[k] != v {
				return fmt.Errorf("ownership changed: %s", r.ID)
			}
		}
		for k, v := range r.Labels {
			if strings.HasPrefix(k, "memeval.") && current.Labels[k] != v {
				return fmt.Errorf("ownership changed: %s", r.ID)
			}
		}
	}
	args := []string{r.Kind, "rm", r.ID}
	if r.Kind == "container" {
		args = []string{"rm", "-f", r.ID}
	}
	if r.Kind == "image" {
		args = []string{"rmi", "-f", r.ID}
	}
	_, e = b.call(ctx, args...)
	if notFound(e) {
		return nil
	}
	return e
}
func (b *Backend) ResourceGone(ctx context.Context, r Resource) error {
	_, e := b.Resource(ctx, r.Kind, r.ID)
	if notFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	return fmt.Errorf("%s remains: %s", r.Kind, r.Name)
}
func (b *Backend) CleanupScope(ctx context.Context, key, value string) ([]CleanupStep, []Resource, error) {
	steps := []CleanupStep{}
	var errs []error
	anonymous := map[string]bool{}
	for _, kind := range []string{"container", "network", "volume"} {
		rows, e := b.Resources(ctx, kind, key, value)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		for _, r := range rows {
			if kind == "container" {
				for _, m := range r.Mounts {
					if m.Type == "volume" && m.Name != "" {
						anonymous[m.Name] = true
					}
				}
			}
			e = b.RemoveResource(ctx, r)
			result := "ok"
			if e != nil {
				result = e.Error()
				errs = append(errs, e)
			}
			steps = append(steps, CleanupStep{"DELETE", kind, r.Name, result})
		}
	}
	for name := range anonymous {
		r := Resource{Kind: "volume", ID: name, Name: name, Anonymous: true}
		e := b.RemoveResource(ctx, r)
		if e != nil {
			errs = append(errs, e)
		}
		result := "ok"
		if e != nil {
			result = e.Error()
		}
		steps = append(steps, CleanupStep{"DELETE", "volume", name, result})
	}
	remaining := []Resource{}
	for _, kind := range []string{"container", "network", "volume"} {
		rs, e := b.Resources(ctx, kind, key, value)
		errs = append(errs, e)
		remaining = append(remaining, rs...)
	}
	for name := range anonymous {
		r := Resource{Kind: "volume", ID: name, Name: name, Anonymous: true}
		if e := b.ResourceGone(ctx, r); e != nil {
			remaining = append(remaining, r)
			errs = append(errs, e)
		}
	}
	if len(remaining) > 0 {
		names := []string{}
		for _, r := range remaining {
			names = append(names, r.Kind+":"+r.Name)
		}
		errs = append(errs, fmt.Errorf("resources remain: %s", strings.Join(names, ", ")))
	}
	return steps, remaining, errors.Join(errs...)
}

// OwnerStart identifies the actual process, rather than the Backend's creation.
func OwnerStart(ctx context.Context, pid int) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	raw, e := procexec.Output(cmd)
	if e != nil {
		return "", e
	}
	s := strings.Join(strings.Fields(string(raw)), " ")
	if s == "" {
		return "", fmt.Errorf("owner absent")
	}
	return "ps:" + s, nil
}
func OwnerActive(ctx context.Context, labels map[string]string) bool {
	pid, e := strconv.Atoi(labels["memeval.owner_pid"])
	if e != nil || pid <= 0 {
		return false
	}
	if !sandbox.Alive(pid) {
		return false
	}
	start, e := OwnerStart(ctx, pid)
	// Unknown/legacy start formats fail closed while the PID is alive.
	expected := labels["memeval.owner_start"]
	return e != nil || !strings.HasPrefix(expected, "ps:") || start == expected
}
func currentOwnerStart() string {
	s, e := OwnerStart(context.Background(), os.Getpid())
	if e != nil {
		return "unknown"
	}
	return s
}
func (b *Backend) CurrentProbeSHA() (string, error) { _, sha, e := ProbeBinary(b.cfg); return sha, e }
