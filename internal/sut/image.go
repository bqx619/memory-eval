package sut

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"memeval/internal/config/redact"
	"memeval/internal/episodelog"
	"memeval/internal/report"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const ImageRoot = "/opt/memeval/env"
const imageFramework = "p8-k3-v3.1"

//go:embed base_images.json
var baseImagesJSON []byte

type BaseImage struct {
	Ref       string   `json:"ref"`
	Digest    string   `json:"digest"`
	Platforms []string `json:"platforms"`
}

func BaseImages() map[string]BaseImage {
	var b map[string]BaseImage
	if e := json.Unmarshal(baseImagesJSON, &b); e != nil {
		panic(e)
	}
	return b
}
func imagePlatform() Platform     { return Platform{"linux", runtime.GOARCH} }
func imagePlatformString() string { return "linux/" + runtime.GOARCH }
func imageLabels(l *Lock) map[string]string {
	return map[string]string{"memeval.env": l.Name + "@" + l.LockSHA256[:min(12, len(l.LockSHA256))], "memeval.lock": l.LockSHA256, "memeval.input": l.InputSHA256, "memeval.framework": imageFramework}
}
func lockHash(l *Lock) string {
	values := []any{l.InputSHA256, l.RuntimeVersion, l.RequirementsLockSHA256, l.PackageLockSHA256, l.Platform, l.LockPlatform, l.Image.BaseImages, l.FrameworkVersion, l.InventoryVersion}
	if len(l.SidecarImages) > 0 {
		values = append(values, l.SidecarImages)
	}
	return sandbox.Hash(values)
}
func shellQuote(s string) string  { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func jsonArgs(v ...string) string { b, _ := json.Marshal(v); return string(b) }
func runtimeIndexes(m *Manifest) string {
	s := ""
	strategy := ""
	for _, i := range m.Runtime.Indexes {
		s += " --index " + shellQuote(i.URL)
		if i.Strategy != "" {
			strategy = i.Strategy
		}
	}
	if strategy != "" {
		s += " --index-strategy " + shellQuote(strategy)
	}
	return s
}

// containerfile is pure: all identity and staging decisions are inputs.
func containerfile(m *Manifest, l *Lock, bases map[string]BaseImage) string {
	base := bases[m.Runtime.Base]
	var out strings.Builder
	fmt.Fprintf(&out, "FROM %s@%s AS base\n", base.Ref, base.Digest)
	out.WriteString("ENV UV_LINK_MODE=copy UV_NO_CACHE=1 PYTHONDONTWRITEBYTECODE=1 UV_PYTHON_INSTALL_DIR=/opt/memeval/env/runtime\n")
	out.WriteString("RUN groupadd -g 65532 memeval && useradd -u 65532 -g 65532 -M memeval && mkdir -p /opt/memeval/env/models-home\n")
	if len(m.Runtime.Apt) > 0 {
		out.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends")
		for _, p := range m.Runtime.Apt {
			out.WriteString(" " + shellQuote(p))
		}
		out.WriteString(" && rm -rf /var/lib/apt/lists/*\n")
	}
	out.WriteString("FROM base AS lockfile\nWORKDIR /lock\n")
	if m.Runtime.Kind == "uv-python" {
		fmt.Fprintf(&out, "COPY %s\n", jsonArgs("project/"+m.Runtime.Requirements, "requirements.in"))
		constraints := ""
		if m.Runtime.Constraints != "" {
			fmt.Fprintf(&out, "COPY %s\n", jsonArgs("project/"+m.Runtime.Constraints, "constraints.txt"))
			constraints = " -c constraints.txt"
		}
		fmt.Fprintf(&out, "RUN uv pip compile requirements.in --python-version %s --generate-hashes%s%s -o requirements.lock.txt\n", shellQuote(m.Runtime.Python), runtimeIndexes(m), constraints)
		out.WriteString("FROM scratch AS lockout\nCOPY --from=lockfile /lock/requirements.lock.txt /requirements.lock.txt\n")
	} else {
		out.WriteString("COPY project/package.json /lock/package.json\nRUN npm install --package-lock-only --ignore-scripts --no-audit --no-fund\nFROM scratch AS lockout\nCOPY --from=lockfile /lock/package-lock.json /package-lock.json\n")
	}
	out.WriteString("FROM base AS env\n")
	labels := imageLabels(l)
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		fmt.Fprintf(&out, "LABEL %s=%s\n", k, jsonArgs(labels[k])[1:len(jsonArgs(labels[k]))-1])
	}
	if m.Runtime.Kind == "uv-python" {
		wheelArgs := ""
		if l.Assets["_wheels"] != "" {
			out.WriteString("COPY assets/_wheels/ /opt/memeval/env/assets/_wheels/\n")
			wheelArgs = " --no-index --find-links /opt/memeval/env/assets/_wheels"
		}
		out.WriteString("COPY requirements.lock.txt /opt/memeval/env/requirements.lock.txt\nRUN uv venv --python " + shellQuote(m.Runtime.Python) + " /opt/memeval/env/.venv && uv pip install --python /opt/memeval/env/.venv/bin/python --require-hashes -r /opt/memeval/env/requirements.lock.txt" + runtimeIndexes(m) + wheelArgs + "\n")
	} else {
		out.WriteString("WORKDIR /opt/memeval/env/project\nCOPY project/package.json package-lock.json ./\nRUN npm ci --no-audit --no-fund\n")
	}
	out.WriteString("COPY --chown=65532:65532 bridge/ /opt/memeval/env/bridge/\nCOPY --chown=65532:65532 project/ /opt/memeval/env/project/\nCOPY --chown=65532:65532 assets/ /opt/memeval/env/assets/\nWORKDIR /opt/memeval/env/project\n")
	if m.Runtime.Kind == "node" {
		out.WriteString("COPY --chown=65532:65532 package-lock.json /opt/memeval/env/project/package-lock.json\n")
	}
	out.WriteString("RUN mkdir -p /opt/memeval/env/models-home /opt/memeval/env/models && chown -R 65532:65532 /opt/memeval/env/models-home /opt/memeval/env/models\nUSER 65532:65532\n")
	expand := strings.NewReplacer("{env}", ImageRoot, "{project}", ImageRoot+"/project", "{python}", l.Executable, "{node}", l.Executable, "{venv}", l.VenvRealPath, "{py_root}", l.PyRoot)
	for _, cmd := range m.Runtime.Prepare {
		out.WriteString("RUN HOME=/opt/memeval/env/models-home")
		for _, arg := range cmd {
			out.WriteString(" " + shellQuote(expand.Replace(arg)))
		}
		out.WriteByte('\n')
	}
	for _, relative := range slices.Sorted(maps.Keys(m.Runtime.CacheLinks)) {
		fmt.Fprintf(&out, "RUN test -e %s\n", shellQuote(expand.Replace(m.Runtime.CacheLinks[relative])))
	}
	// Do not hide find traversal errors (for example an unreadable directory)
	// behind a pipeline's last exit status. Both failures stop the build.
	out.WriteString("RUN find /opt/memeval/env/models-home /opt/memeval/env/models ! -readable -print -quit > /tmp/memeval-unreadable && if test -s /tmp/memeval-unreadable; then cat /tmp/memeval-unreadable; exit 1; fi && rm /tmp/memeval-unreadable\n")
	return out.String()
}
func inputFiles(m *Manifest) (map[string]string, error) {
	entries := map[string]string{}
	err := walkProject(m.Dir, func(p, rel string) error {
		if m.Process.EnvFile != "" && filepath.Clean(p) == filepath.Clean(m.Resolve(m.Process.EnvFile)) {
			return nil
		}
		if strings.HasPrefix(filepath.Base(rel), "manifest") && strings.HasSuffix(rel, ".json") && rel != filepath.Base(m.Path) {
			return nil
		}
		if !runtimeInput(rel, filepath.Base(m.Path)) && !referencedInput(m, rel) {
			return nil
		}
		if rel == filepath.Base(m.Path) {
			clone := *m
			clone.LLM.Model = ""
			clone.LLM.Effort = ""
			clone.LLM.Concurrency = nil // Runtime admission policy does not change the image.
			clone.Notes = ""
			// Network is runtime policy, not an image input. Use the canonical
			// none value to preserve identities of existing default-mode locks.
			clone.Sandbox.Network = sandbox.NetworkNone
			entries["project/"+rel] = sandbox.Hash(clone)
			return nil
		}
		h, e := FileHash(p)
		if e == nil {
			entries["project/"+rel] = h
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	if m.needsBridge() {
		root, e := bridgeRoot(m)
		if e != nil {
			return nil, e
		}
		if e = walkProject(root, func(p, rel string) error {
			if !runtimeInput(rel, "") {
				return nil
			}
			h, e := FileHash(p)
			entries["bridge/"+rel] = h
			return e
		}); e != nil {
			return nil, e
		}
	}
	for _, p := range m.Runtime.Assets {
		if e := validateBuildAsset(m.Resolve(p)); e != nil {
			return nil, e
		}
		if e := hashPath(m.Resolve(p), "assets/"+p, entries); e != nil {
			return nil, e
		}
	}
	return entries, nil
}
func inputHash(m *Manifest) (string, error) {
	entries, e := inputFiles(m)
	return sandbox.Hash([]any{entries, imagePlatform(), BaseImages()[m.Runtime.Base], imageFramework}), e
}
func stageContext(m *Manifest, l *Lock) (string, error) {
	ep, e := sandbox.NewEpisode()
	if e != nil {
		return "", e
	}
	root := filepath.Join(filepath.Dir(ep), "build-"+filepath.Base(ep))
	if e = os.Rename(ep, root); e != nil {
		os.RemoveAll(ep)
		return "", e
	}
	fail := func(e error) (string, error) { os.RemoveAll(root); return "", e }
	for _, d := range []string{"project", "bridge", "assets"} {
		if e = os.MkdirAll(filepath.Join(root, d), 0700); e != nil {
			return fail(e)
		}
	}
	if e = walkProject(m.Dir, func(p, rel string) error {
		if m.Process.EnvFile != "" && filepath.Clean(p) == filepath.Clean(m.Resolve(m.Process.EnvFile)) {
			return nil
		}
		if strings.HasPrefix(filepath.Base(rel), "manifest") && strings.HasSuffix(rel, ".json") && rel != filepath.Base(m.Path) {
			return nil
		}
		if !runtimeInput(rel, filepath.Base(m.Path)) && !referencedInput(m, rel) {
			return nil
		}
		return copyFile(p, filepath.Join(root, "project", rel))
	}); e != nil {
		return fail(e)
	}
	if m.needsBridge() {
		b, e := bridgeRoot(m)
		if e != nil {
			return fail(e)
		}
		if e = walkProject(b, func(p, rel string) error {
			if !runtimeInput(rel, "") {
				return nil
			}
			return copyFile(p, filepath.Join(root, "bridge", rel))
		}); e != nil {
			return fail(e)
		}
	}
	for _, p := range m.Runtime.Assets {
		if e = copyTree(m.Resolve(p), filepath.Join(root, "assets", p)); e != nil {
			return fail(e)
		}
		l.Assets[p] = filepath.ToSlash(filepath.Join("assets", p))
	}
	return root, nil
}
func BuildImage(ctx context.Context, b *container.Backend, m *Manifest, o InstallOptions) (_ *Lock, err error) {
	if m.Runtime.Kind == "binary" {
		return nil, fmt.Errorf("binary runtime not supported in P8 v1")
	}
	probe, e := offlineProbeManifest(m)
	if e != nil {
		return nil, e
	}
	if b.Platform() != imagePlatformString() {
		return nil, fmt.Errorf("build platform %s differs from selected Linux platform %s", b.Platform(), imagePlatformString())
	}
	cache, e := CacheRoot(o.Cache)
	if e != nil {
		return nil, e
	}
	m.CacheDir = cache
	if !o.Force {
		if l, e := CheckLock(m); e == nil {
			return l, nil
		}
	}
	caps, e := b.Capabilities(ctx)
	if e != nil {
		return nil, e
	}
	if !caps.Enforced {
		return nil, &sandbox.Unenforceable{Reason: caps.Reason}
	}
	releaseCatalog, e := cacheGate(cache, false)
	if e != nil {
		return nil, e
	}
	defer releaseCatalog()
	inputs, e := inputFiles(m)
	if e != nil {
		return nil, e
	}
	input, e := inputHash(m)
	if e != nil {
		return nil, e
	}
	l := &Lock{Version: 3, FrameworkVersion: imageFramework, InventoryVersion: 1, LockPlatformSource: "provided; original resolution platform unspecified (installed/checked on target Linux)", Name: m.Name, InputSHA256: input, Inputs: inputs, ImageRoot: ImageRoot, Platform: imagePlatform(), HostPlatform: Platform{runtime.GOOS, runtime.GOARCH}, LockPlatform: imagePlatform(), RuntimeKind: m.Runtime.Kind, RuntimeVersion: m.Runtime.Version, Assets: map[string]string{}}
	base, ok := BaseImages()[m.Runtime.Base]
	if !ok {
		return nil, fmt.Errorf("unknown base image %q", m.Runtime.Base)
	}
	supported := false
	for _, p := range base.Platforms {
		if p == b.Platform() || strings.HasPrefix(p, b.Platform()+"/") {
			supported = true
		}
	}
	if !supported {
		return nil, fmt.Errorf("base %s does not declare platform %s", m.Runtime.Base, b.Platform())
	}
	l.Image.BaseImages = []string{base.Ref + "@" + base.Digest}
	l.Image.Platform = b.Platform()
	if m.Runtime.Kind == "uv-python" {
		l.RuntimeVersion = m.Runtime.Python
		l.PythonVersion = m.Runtime.Python
		l.VenvRealPath = ImageRoot + "/.venv"
		l.PyRoot = "/usr/local"
		l.PythonRealPath = "/usr/local/bin/python"
		l.Executable = l.VenvRealPath + "/bin/python"
	} else {
		l.Executable = "/usr/local/bin/node"
	}
	stage, e := stageContext(m, l)
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(stage)
	if o.FindLinks != "" {
		if e = validateBuildAsset(o.FindLinks); e != nil {
			return nil, e
		}
		if e = copyTree(o.FindLinks, filepath.Join(stage, "assets", "_wheels")); e != nil {
			return nil, e
		}
		l.Assets["_wheels"] = "assets/_wheels"
	}
	log := episodelog.NewFileWriter(filepath.Join(stage, "build.log"), math.MaxInt64, m.Secrets(), redact.From(ctx))
	defer log.Close()
	// A lock-resolution failure still has a stable, inspectable diagnostics folder.
	l.EnvDir = filepath.Join(cache, "envs", m.Name+"@"+input)
	defer func() {
		if err != nil {
			var f *InstallFailure
			if errors.As(err, &f) {
				return
			}
			log.Close()
			os.MkdirAll(l.EnvDir, 0700)
			copyFile(filepath.Join(stage, "build.log"), filepath.Join(l.EnvDir, "build.log"))
			raw, _ := os.ReadFile(filepath.Join(stage, "build.log"))
			err = RecordInstallFailure(m, l, err, string(raw))
		}
	}()
	lockName := "requirements.lock.txt"
	lockPath := m.Runtime.RequirementsLock
	if m.Runtime.Kind == "node" {
		lockName = "package-lock.json"
		lockPath = m.Runtime.PackageLock
		if lockPath == "" {
			if _, e := os.Stat(m.Resolve(lockName)); e == nil {
				lockPath = lockName
			}
		}
	}
	writeRecipe := func() error {
		return os.WriteFile(filepath.Join(stage, "Containerfile"), []byte(containerfile(m, l, BaseImages())), 0600)
	}
	common := []string{"build", "--platform", b.Platform(), "--file", filepath.Join(stage, "Containerfile"), "--progress=plain"}
	if o.Offline {
		common = append(common, "--pull=false")
	}
	if o.Force {
		common = append(common, "--no-cache")
	}
	if lockPath == "" {
		if o.Offline {
			return nil, fmt.Errorf("offline container install requires an existing lockfile; it disables lock resolution and pulls, not wheel downloads")
		}
		if e = writeRecipe(); e != nil {
			return nil, e
		}
		args := append(slices.Clone(common), "--target", "lockout", "--output", "type=local,dest="+filepath.Join(stage, "lock"), stage)
		if e = b.Build(ctx, log, args...); e != nil {
			return nil, e
		}
		lockPath = filepath.Join(stage, "lock", lockName)
	} else {
		lockPath = m.Resolve(lockPath)
	}
	generated := m.Runtime.Kind == "uv-python" && m.Runtime.RequirementsLock == "" || m.Runtime.Kind == "node" && strings.HasPrefix(lockPath, stage+string(filepath.Separator))
	raw, e := os.ReadFile(lockPath)
	if e != nil {
		return nil, e
	}
	if generated {
		l.LockPlatformSource = "container"
	}
	if generated && m.Runtime.Kind == "uv-python" && !strings.Contains(string(raw), "# memeval-lock-platform:") {
		raw = append([]byte("# memeval-lock-platform: "+b.Platform()+"\n"), raw...)
	}
	if m.Runtime.Kind == "uv-python" {
		if !generated && strings.Contains(string(raw), "# memeval-lock-platform:") {
			l.LockPlatformSource = "recorded lock header"
		}
		if e = validateDependencyLock(raw); e != nil {
			return nil, e
		}
		if e = validateLockPlatform(raw, l.Platform); e != nil {
			return nil, e
		}
		l.RequirementsInputSHA256, e = FileHash(m.Resolve(m.Runtime.Requirements))
		if e != nil {
			return nil, e
		}
	}
	if e = os.WriteFile(filepath.Join(stage, lockName), raw, 0600); e != nil {
		return nil, e
	}
	h, e := FileHash(filepath.Join(stage, lockName))
	if e != nil {
		return nil, e
	}
	if m.Runtime.Kind == "uv-python" {
		l.RequirementsLockSHA256 = h
	} else {
		l.PackageLockSHA256 = h
	}
	if e = installSidecarImages(ctx, b, m, l, log); e != nil {
		return nil, e
	}
	l.LockSHA256 = lockHash(l)
	l.EnvDir = filepath.Join(cache, "envs", m.Name+"@"+l.LockSHA256)
	if e = os.MkdirAll(l.EnvDir, 0700); e != nil {
		return nil, e
	}
	if e = writeRecipe(); e != nil {
		return nil, e
	}
	files := map[string]string{}
	for _, d := range []string{"project", "bridge", "assets"} {
		if e = hashPath(filepath.Join(stage, d), d, files); e != nil {
			return nil, e
		}
	}
	for _, n := range []string{lockName, "Containerfile"} {
		files[n], e = FileHash(filepath.Join(stage, n))
		if e != nil {
			return nil, e
		}
	}
	l.Image.ContainerfileSHA256 = files["Containerfile"]
	l.Image.ContextSHA256 = sandbox.Hash(files)
	l.Image.Repository = "memeval/env"
	l.Image.Tag = m.Name + "-" + l.LockSHA256[:12]
	l.Image.Ref = l.Image.Repository + ":" + l.Image.Tag
	args := append(slices.Clone(common), "--target", "env", "--iidfile", filepath.Join(stage, "iid"), "--tag", l.Image.Ref)
	for _, k := range slices.Sorted(maps.Keys(imageLabels(l))) {
		args = append(args, "--label", k+"="+imageLabels(l)[k])
	}
	args = append(args, stage)
	retried, e := buildEnvWithRetry(ctx, b, m, l, stage, log, args)
	if e != nil {
		return nil, e
	}
	id, e := os.ReadFile(filepath.Join(stage, "iid"))
	if e != nil {
		return nil, e
	}
	l.Image.ID = strings.TrimSpace(string(id))
	info, e := inspectImage(ctx, b, l.Image.ID)
	if e != nil {
		return nil, e
	}
	if e = checkImage(l, info); e != nil {
		return nil, e
	}
	l.Image.SizeBytes = info.Size
	l.Image.BuiltAt = time.Now().UTC().Format(time.RFC3339Nano)
	if e = log.Close(); e != nil {
		return nil, e
	}
	if !retried {
		_ = os.Remove(filepath.Join(l.EnvDir, "install-failure.log"))
	}
	for _, n := range []string{lockName, "Containerfile", "build.log"} {
		if e = copyFile(filepath.Join(stage, n), filepath.Join(l.EnvDir, n)); e != nil {
			return nil, e
		}
		if e = os.Chmod(filepath.Join(l.EnvDir, n), 0600); e != nil {
			return nil, e
		}
	}
	if e = report.WriteJSON(ctx, filepath.Join(l.EnvDir, "context.sha256.json"), files); e != nil {
		return nil, e
	}
	l.Caps = caps
	// BuildImage sets CacheDir after constructing the isolated probe config.
	probe.CacheDir = m.CacheDir
	if e = probeRuntime(ctx, probe, l, b); e != nil {
		return nil, e
	}
	if m.Adapter.Kind == "native" {
		l.Probe, e = Probe(ctx, probe, l, b, []string{"{venv}/bin/octopmemory-bridge", "--probe"})
	} else {
		a := &Adapter{Manifest: probe, Lock: l, Backend: b, CaptureStderr: true}
		d, probeErr := a.Describe(ctx)
		e = errors.Join(probeErr, a.Close(context.WithoutCancel(ctx)))
		if e != nil {
			return nil, RecordInstallFailure(m, l, fmt.Errorf("offline describe probe: %w", e), a.LastStderr)
		}
		l.Probe = map[string]any{"ok": true, "network": "none", "describe": d.Name, "selftest": "not applicable until K5"}
	}
	if e != nil {
		return nil, e
	}
	l.CreatedAt = l.Image.BuiltAt
	l.Documentation = map[string]string{}
	e = walkProject(m.Dir, func(p, rel string) error {
		if !runtimeInput(rel, filepath.Base(m.Path)) {
			h, e := FileHash(p)
			l.Documentation[rel] = h
			return e
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	l.Files, e = inventory(l.EnvDir)
	if e != nil {
		return nil, e
	}
	if e = report.WriteJSON(ctx, filepath.Join(l.EnvDir, "env.lock.json"), l); e != nil {
		return nil, e
	}
	return l, nil
}

func validateDependencyLock(raw []byte) error {
	body := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			body += strings.TrimSpace(line)
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "pending") || body != "" && !strings.Contains(body, "--hash=sha256:") {
		return fmt.Errorf("requirements lock is not verified; resolve and hash-pin project dependencies before install")
	}
	return nil
}

func validateBuildAsset(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if strings.HasPrefix(d.Name(), ".env") {
			return fmt.Errorf("private build asset disallowed: %s", p)
		}
		if d.Type()&os.ModeSymlink != 0 {
			resolved, e := filepath.EvalSymlinks(p)
			if e != nil {
				return e
			}
			realRoot, e := filepath.EvalSymlinks(root)
			if e != nil {
				return e
			}
			if !sandbox.Within(resolved, realRoot) {
				return fmt.Errorf("build asset link escapes root: %s", p)
			}
		}
		return nil
	})
}

func referencedInput(m *Manifest, rel string) bool {
	paths := []string{m.Runtime.Constraints, m.Runtime.Requirements, m.Runtime.RequirementsLock, m.Runtime.PackageLock}
	for _, cmd := range m.Runtime.Prepare {
		for _, arg := range cmd {
			if p, ok := strings.CutPrefix(arg, "{project}/"); ok {
				paths = append(paths, p)
			}
		}
	}
	for _, p := range paths {
		if p != "" && (rel == p || strings.HasPrefix(rel, p+"/")) {
			return true
		}
	}
	return false
}
