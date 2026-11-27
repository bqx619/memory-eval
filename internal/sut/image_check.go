package sut

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type imageInfo struct {
	ID               string `json:"Id"`
	Os, Architecture string
	Size             int64
	RepoDigests      []string
	Config           struct {
		Labels  map[string]string
		Volumes map[string]any
	}
}

func inspectImage(ctx context.Context, b *container.Backend, id string) (imageInfo, error) {
	raw, e := b.ImageCommand(ctx, "image", "inspect", id)
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "no such image") {
			return imageInfo{}, &NotInstalled{Reason: "image_missing", Detail: id}
		}
		return imageInfo{}, fmt.Errorf("docker context %q unreachable; start OrbStack and retry: %w", container.DefaultConfig().Context, e)
	}
	var rows []imageInfo
	if e = json.Unmarshal(raw, &rows); e != nil {
		return imageInfo{}, e
	}
	if len(rows) != 1 {
		return imageInfo{}, fmt.Errorf("expected one image")
	}
	return rows[0], nil
}
func checkImage(l *Lock, v imageInfo) error {
	bad := func(reason, detail string) error {
		return &NotInstalled{Manifest: l.Name, Reason: reason, Detail: detail}
	}
	if v.ID != l.Image.ID {
		return bad("image_missing", "image ID does not match lock")
	}
	if v.Os != l.Platform.OS || v.Architecture != l.Platform.Arch {
		return bad("label_mismatch", "image platform does not match lock")
	}
	for k, w := range imageLabels(l) {
		if v.Config.Labels[k] != w {
			return bad("label_mismatch", "image label "+k+" does not match lock")
		}
	}
	return nil
}
func validateLockPlatform(raw []byte, p Platform) error {
	want := p.OS + "/" + p.Arch
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "# memeval-lock-platform: "); ok && strings.TrimSpace(value) != want {
			return &NotInstalled{Reason: "lock_platform", Detail: "dependency lock targets " + strings.TrimSpace(value) + ", expected " + want}
		}
	}
	return nil
}
func localLocks(cache string) ([]Lock, error) {
	root, e := CacheRoot(cache)
	if e != nil {
		return nil, e
	}
	paths, e := filepath.Glob(filepath.Join(root, "envs", "*", "env.lock.json"))
	if e != nil {
		return nil, e
	}
	out := []Lock{}
	for _, p := range paths {
		raw, e := os.ReadFile(p)
		if e != nil {
			return nil, e
		}
		var l Lock
		var header struct {
			Version int `json:"version"`
		}
		if e = json.Unmarshal(raw, &header); e != nil {
			return nil, e
		}
		if header.Version != 3 {
			var fields map[string]json.RawMessage
			if e = json.Unmarshal(raw, &fields); e != nil {
				return nil, e
			}
			delete(fields, "image")
			raw, _ = json.Marshal(fields)
		}
		if e = json.Unmarshal(raw, &l); e != nil {
			return nil, e
		}
		if l.EnvDir != filepath.Dir(p) {
			return nil, &NotInstalled{Manifest: l.Name, Reason: "retired_host_env", Detail: "lock directory does not match env_dir"}
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}
func List(cache string) ([]Lock, error) {
	out, e := localLocks(cache)
	if e != nil {
		return nil, e
	}
	b := container.New(container.DefaultConfig())
	raw, e := b.ImageCommand(context.Background(), "images", "--filter", "label=memeval.env", "--no-trunc", "--format", "json")
	if e != nil {
		return nil, fmt.Errorf("docker context %q unreachable; start OrbStack and retry: %w", container.DefaultConfig().Context, e)
	}
	seen := map[string]bool{}
	for _, l := range out {
		seen[l.Image.ID] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row struct{ ID, Repository, Tag string }
		if e = json.Unmarshal([]byte(line), &row); e != nil {
			return nil, e
		}
		if seen[row.ID] {
			continue
		}
		v, e := inspectImage(context.Background(), b, row.ID)
		if e != nil {
			return nil, e
		}
		if v.Config.Labels["memeval.probe"] != "" {
			continue
		}
		label := v.Config.Labels["memeval.env"]
		if label == "" {
			continue
		}
		name, _, _ := strings.Cut(label, "@")
		l := Lock{Version: 3, Name: name, Orphan: true, LockSHA256: v.Config.Labels["memeval.lock"], Image: sandbox.Image{ID: v.ID, Repository: row.Repository, Tag: row.Tag, Ref: row.Repository + ":" + row.Tag, SizeBytes: v.Size}, Platform: Platform{v.Os, v.Architecture}}
		out = append(out, l)
		seen[row.ID] = true
	}
	return out, nil
}
func inputDiff(old, next map[string]string) string {
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range next {
		keys[k] = true
	}
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if old[k] == next[k] {
			continue
		}
		a, b := old[k], next[k]
		if a == "" {
			a = "missing"
		}
		if b == "" {
			b = "missing"
		}
		lines = append(lines, fmt.Sprintf("%s: %s -> %s", k, a[:min(12, len(a))], b[:min(12, len(b))]))
		if len(lines) == 10 {
			break
		}
	}
	if len(lines) == 0 {
		return "runtime configuration or target platform changed"
	}
	return strings.Join(lines, "; ")
}
func CheckLock(m *Manifest) (*Lock, error) {
	inp, e := inputHash(m)
	if e != nil {
		return nil, &NotInstalled{Manifest: m.Path, Reason: "input_changed", Detail: e.Error()}
	}
	ls, e := localLocks(m.CacheDir)
	if e != nil {
		return nil, e
	}
	var closest *Lock
	for _, l := range ls {
		if l.Name != m.Name {
			continue
		}
		copy := l
		if closest == nil {
			closest = &copy
		}
		if l.InputSHA256 == inp {
			if e = CheckEnv(&l); e != nil {
				return nil, e
			}
			return &l, nil
		}
	}
	if closest != nil {
		if closest.Version != 3 {
			return nil, &NotInstalled{Manifest: m.Path, Reason: "lock_version", Detail: "version 3 image lock required"}
		}
		next, _ := inputFiles(m)
		return nil, &NotInstalled{Manifest: m.Path, Reason: "input_changed", Detail: inputDiff(closest.Inputs, next)}
	}
	return nil, &NotInstalled{Manifest: m.Path, Reason: "image_missing", Detail: "no installed image lock for manifest"}
}
func CheckEnv(l *Lock) error {
	bad := func(reason, detail string) error {
		return &NotInstalled{Manifest: l.Name, Reason: reason, Detail: detail}
	}
	if l.Version != 3 {
		return bad("lock_version", fmt.Sprintf("lock version %d; version 3 required", l.Version))
	}
	if l.FrameworkVersion != imageFramework || l.InventoryVersion != 1 {
		return bad("lock_version", "framework or inventory rule version changed; reinstall required")
	}
	if l.ImageRoot != ImageRoot || l.Platform.OS != "linux" {
		return bad("retired_host_env", "environment is not an image at "+ImageRoot)
	}
	if l.LockPlatform != l.Platform || l.Platform != imagePlatform() {
		return bad("lock_platform", "dependency lock/image target platform differs from selected Linux platform")
	}
	real, e := sandbox.Real(l.EnvDir)
	if e != nil || real != l.EnvDir || filepath.Base(l.EnvDir) != l.Name+"@"+l.LockSHA256 {
		return bad("files_changed", "environment directory identity mismatch")
	}
	if l.LockSHA256 != lockHash(l) {
		return bad("files_changed", "lock hash mismatch")
	}
	files, e := inventory(l.EnvDir)
	if e != nil {
		return bad("files_changed", e.Error())
	}
	if sandbox.Hash(files) != sandbox.Hash(l.Files) {
		return bad("files_changed", inputDiff(l.Files, files))
	}
	if h, e := FileHash(filepath.Join(l.EnvDir, "Containerfile")); e != nil || h != l.Image.ContainerfileSHA256 {
		return bad("files_changed", "Containerfile digest differs from image lock")
	}
	contextRaw, e := os.ReadFile(filepath.Join(l.EnvDir, "context.sha256.json"))
	if e != nil {
		return bad("files_changed", e.Error())
	}
	var contextFiles map[string]string
	if json.Unmarshal(contextRaw, &contextFiles) != nil || sandbox.Hash(contextFiles) != l.Image.ContextSHA256 || contextFiles["Containerfile"] != l.Image.ContainerfileSHA256 {
		return bad("files_changed", "build context digest differs from image lock")
	}
	lockName, want := "requirements.lock.txt", l.RequirementsLockSHA256
	if l.RuntimeKind == "node" {
		lockName, want = "package-lock.json", l.PackageLockSHA256
	}
	if h, e := FileHash(filepath.Join(l.EnvDir, lockName)); e != nil || h != want {
		return bad("files_changed", "dependency lock digest differs from image identity")
	}
	if l.RuntimeKind == "uv-python" {
		raw, e := os.ReadFile(filepath.Join(l.EnvDir, "requirements.lock.txt"))
		if e != nil {
			return bad("files_changed", e.Error())
		}
		if e = validateLockPlatform(raw, l.Platform); e != nil {
			return e
		}
	}
	v, e := inspectImage(context.Background(), container.New(container.DefaultConfig()), l.Image.ID)
	if e != nil {
		return e
	}
	if e = checkImage(l, v); e != nil {
		return e
	}
	return checkSidecarImages(context.Background(), container.New(container.DefaultConfig()), l)
}
