package sut

import (
	"encoding/json"
	"errors"
	"flag"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateImageGoldens = flag.Bool("update-image-goldens", false, "regenerate Containerfile golden fixtures")

func TestBaseImagesAppendixAgreement(t *testing.T) {
	raw, e := os.ReadFile("../../design/p8-container-platform.md")
	if e != nil {
		t.Fatal(e)
	}
	bases := BaseImages()
	if len(bases) != 4 {
		t.Fatal(bases)
	}
	for key, b := range bases {
		for _, p := range b.Platforms {
			if !strings.Contains(string(raw), p) && !strings.Contains(string(raw), strings.TrimPrefix(p, "linux/")) {
				t.Fatal(key, p)
			}
		}
		for _, s := range []string{b.Ref, b.Digest} {
			if !strings.Contains(string(raw), s) {
				t.Fatalf("%s absent from authoritative appendix: %s", key, s)
			}
		}
	}
}
func goldenImage(t *testing.T, name string) (*Manifest, *Lock) {
	t.Helper()
	m := &Manifest{Name: "golden", SchemaVersion: "3", Runtime: Runtime{Kind: "uv-python", Python: "3.12", Base: "uv-python", Requirements: "requirements.in"}}
	m.Sandbox.Backend = "container"
	m.Sandbox.Network = sandbox.NetworkNone
	l := &Lock{Name: m.Name, LockSHA256: strings.Repeat("1", 64), InputSHA256: strings.Repeat("2", 64), Executable: ImageRoot + "/.venv/bin/python", Assets: map[string]string{}}
	switch name {
	case "uv-open":
		m.Sandbox.Network = sandbox.NetworkOpen
	case "trixie":
		m.Runtime.Base = "uv-python-trixie"
		m.Runtime.Apt = []string{"libgomp1"}
	case "node":
		m.Runtime = Runtime{Kind: "node", Version: "24.22.0", Base: "node"}
		l.Executable = "/usr/local/bin/node"
	case "prepare":
		m.Runtime.Prepare = [][]string{{"{python}", "{project}/prepare_models.py", "{env}/models"}}
		m.Runtime.CacheLinks = map[string]string{"home/.cache/models": "{env}/models"}
		m.Runtime.Apt = []string{"libssl3"}
		m.Runtime.Indexes = []RuntimeIndex{{"https://download.pytorch.org/whl/cpu", "unsafe-best-match"}}
		m.Runtime.Constraints = "constraints.txt"
	case "node-prepare":
		m.Runtime = Runtime{Kind: "node", Version: "24.22.0", Base: "node", Prepare: [][]string{{"{node}", "{project}/prepare.mjs"}}, CacheLinks: map[string]string{"home/.cache/mastra": "{env}/models-home/.cache/mastra"}}
		l.Executable = "/usr/local/bin/node"
	case "assets":
		m.Runtime.Assets = []string{"models"}
		l.Assets["models"] = "assets/models"
	}
	return m, l
}
func TestContainerfileGolden(t *testing.T) {
	for _, name := range []string{"uv-none", "uv-open", "trixie", "node", "prepare", "node-prepare", "assets"} {
		t.Run(name, func(t *testing.T) {
			m, l := goldenImage(t, name)
			got := containerfile(m, l, BaseImages())
			p := filepath.Join("testdata/containerfile", name+".golden")
			if *updateImageGoldens {
				if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(p, []byte(got), 0644); e != nil {
					t.Fatal(e)
				}
			}
			want, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			if got != string(want) {
				t.Fatalf("Containerfile drift: %s", name)
			}
			for _, bad := range []string{"# syntax=", "chmod -R a-w", "--network none"} {
				if strings.Contains(got, bad) {
					t.Fatal(bad)
				}
			}
			if !strings.Contains(got, "USER 65532:65532") || !strings.Contains(got, "UV_NO_CACHE=1") {
				t.Fatal(got)
			}
			chown := strings.Index(got, "chown -R 65532:65532 /opt/memeval/env/models-home /opt/memeval/env/models")
			user := strings.Index(got, "USER 65532:65532")
			readable := strings.Index(got, "RUN find /opt/memeval/env/models-home /opt/memeval/env/models ! -readable")
			if chown < 0 || user < chown || readable < user || strings.Count(got, "USER ") != 1 {
				t.Fatal("model ownership/readability sequence", got)
			}
			if prepare := strings.Index(got, "RUN HOME="); prepare >= 0 && (prepare < user || prepare > readable) {
				t.Fatal("prepare must run as uid 65532 before validation", got)
			}
			if strings.Contains(got, "|| true") {
				t.Fatal("readability check must not mask find errors")
			}
		})
	}
}
func installImageFixture(t *testing.T) (*Manifest, *Lock, testoracle.DockerFixture) {
	t.Helper()
	m, root := fixtureManifest(t, "uv-python")
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	l, e := Install(t.Context(), m, InstallOptions{Cache: filepath.Join(root, "cache"), Offline: true})
	if e != nil {
		t.Fatal(e)
	}
	return m, l, f
}
func TestImageTwoStageLockAndOffline(t *testing.T) {
	for _, kind := range []string{"uv-python", "node"} {
		t.Run(kind, func(t *testing.T) {
			m, root := fixtureManifest(t, kind)
			f := testoracle.FakeDocker(t)
			f.Configure(t)
			m.Runtime.RequirementsLock = ""
			if kind == "node" {
				if e := os.Remove(m.Resolve("package-lock.json")); e != nil {
					t.Fatal(e)
				}
			}
			l, e := Install(t.Context(), m, InstallOptions{Cache: filepath.Join(root, "cache")})
			if e != nil {
				t.Fatal(e)
			}
			var builds [][]string
			for _, c := range f.Calls(t) {
				if len(c) > 3 && c[2] == "build" && strings.Contains(strings.Join(c, " "), "--target") {
					builds = append(builds, c)
				}
			}
			if len(builds) != 2 || !strings.Contains(strings.Join(builds[0], " "), "--target lockout --output type=local,dest=") || !strings.Contains(strings.Join(builds[1], " "), "--target env --iidfile") {
				t.Fatal(builds)
			}
			if l.Version != 3 || l.LockSHA256 != lockHash(l) || filepath.Base(l.EnvDir) != m.Name+"@"+l.LockSHA256 || l.ImageRoot != ImageRoot || l.Image.Platform != imagePlatformString() {
				t.Fatal(l)
			}
			raw, e := os.ReadFile(filepath.Join(l.EnvDir, "env.lock.json"))
			if e != nil {
				t.Fatal(e)
			}
			var round Lock
			if e = json.Unmarshal(raw, &round); e != nil || sandbox.Hash(l) != sandbox.Hash(round) {
				t.Fatal("lock round trip", e)
			}
			if kind == "uv-python" {
				b, _ := os.ReadFile(filepath.Join(l.EnvDir, "requirements.lock.txt"))
				if !strings.Contains(string(b), "# This file was autogenerated by uv") || !strings.Contains(string(b), "# memeval-lock-platform: linux/") {
					t.Fatal(string(b))
				}
			}
			for _, c := range builds {
				stage := c[len(c)-1]
				if _, e = os.Stat(stage); !os.IsNotExist(e) {
					t.Fatal("staging remains", e)
				}
				if strings.Contains(strings.Join(c, " "), "--network") {
					t.Fatal(c)
				}
			}
			// Offline cannot silently resolve a missing lock, even with a cached base.
			_, e = Install(t.Context(), m, InstallOptions{Cache: filepath.Join(root, "offline"), Offline: true})
			if e == nil || !strings.Contains(e.Error(), "requires an existing lockfile") {
				t.Fatal(e)
			}
		})
	}
}
func TestImageCheckCategories(t *testing.T) {
	m, l, f := installImageFixture(t)
	assert := func(err error, reason string) {
		t.Helper()
		var n *NotInstalled
		if !errors.As(err, &n) || n.Reason != reason || n.Detail == "" {
			t.Fatalf("want %s with detail: %v", reason, err)
		}
	}
	for _, v := range []struct {
		name   string
		change func(*Lock)
	}{
		{"lock_version", func(l *Lock) { l.Version = 2 }},
		{"retired_host_env", func(l *Lock) { l.ImageRoot = "/host/venv" }},
		{"lock_platform", func(l *Lock) { l.LockPlatform.Arch = "different" }},
		{"files_changed", func(l *Lock) { l.LockSHA256 = "wrong" }},
	} {
		t.Run(v.name, func(t *testing.T) { copy := *l; v.change(&copy); assert(CheckEnv(&copy), v.name) })
	}
	writeFixture(t, filepath.Join(l.EnvDir, "unexpected"), "changed")
	assert(CheckEnv(l), "files_changed")
	os.Remove(filepath.Join(l.EnvDir, "unexpected"))
	writeFixture(t, m.Resolve("adapter.py"), "changed")
	_, e := CheckLock(m)
	assert(e, "input_changed")
	if !strings.Contains(e.Error(), "adapter.py:") {
		t.Fatal(e)
	}
	statePath := filepath.Join(f.State, "state.json")
	raw, e := os.ReadFile(statePath)
	if e != nil {
		t.Fatal(e)
	}
	var state map[string]any
	json.Unmarshal(raw, &state)
	images := state["images"].(map[string]any)
	img := images[l.Image.Ref].(map[string]any)
	labels := img["Config"].(map[string]any)["Labels"].(map[string]any)
	labels["memeval.lock"] = "wrong"
	b, _ := json.Marshal(state)
	os.WriteFile(statePath, b, 0600)
	assert(CheckEnv(l), "label_mismatch")
	delete(images, l.Image.Ref)
	b, _ = json.Marshal(state)
	os.WriteFile(statePath, b, 0600)
	assert(CheckEnv(l), "image_missing")
}
func TestImageListOrphansAndForce(t *testing.T) {
	m, l, f := installImageFixture(t)
	rows, e := List(m.CacheDir)
	if e != nil || len(rows) != 1 || rows[0].Orphan {
		t.Fatal(rows, e)
	}
	_, e = Install(t.Context(), m, InstallOptions{Cache: m.CacheDir, Offline: true, Force: true})
	if e != nil {
		t.Fatal(e)
	}
	matched := false
	for _, c := range f.Calls(t) {
		s := strings.Join(c, " ")
		if strings.Contains(s, "--target env") {
			if strings.Contains(s, "--pull=false --no-cache") {
				matched = true
			}
		}
	}
	if !matched {
		t.Fatal("force/offline flags absent")
	}
	if e = os.RemoveAll(l.EnvDir); e != nil {
		t.Fatal(e)
	}
	rows, e = List(m.CacheDir)
	if e != nil || len(rows) != 1 || !rows[0].Orphan || rows[0].Image.ID != l.Image.ID {
		t.Fatal(rows, e)
	}
}
func TestImageInputAndContext(t *testing.T) {
	m, _ := fixtureManifest(t, "uv-python")
	writeFixture(t, m.Resolve("asset/model.bin"), "fixture model")
	m.Runtime.Assets = []string{"asset"}
	writeFixture(t, m.Resolve("constraints.txt"), "fixture-sdk==1.0.0")
	m.Runtime.Constraints = "constraints.txt"
	before, e := inputHash(m)
	if e != nil {
		t.Fatal(e)
	}
	m.LLM.Model = "pinned"
	m.LLM.Effort = "medium"
	m.LLM.Concurrency = new(32)
	m.Gateway, _ = ParseLLMGateway("http://127.0.0.1:8000/v1", "private-gateway-key", "configured", "low")
	after, _ := inputHash(m)
	if before != after {
		t.Fatal("model/effort/concurrency/key changed image identity")
	}
	m.Runtime.Indexes = []RuntimeIndex{{"https://example.invalid/simple", "unsafe-best-match"}}
	after, _ = inputHash(m)
	if before == after {
		t.Fatal("indexes missing from identity")
	}
	before = after
	writeFixture(t, m.Resolve("constraints.txt"), "fixture-sdk==2.0.0")
	after, _ = inputHash(m)
	if before == after {
		t.Fatal("constraints missing from identity")
	}
	writeFixture(t, m.Resolve(".env"), "PRIVATE=never-stage-this")
	l := &Lock{Assets: map[string]string{}}
	stage, e := stageContext(m, l)
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(stage)
	if _, e = os.Stat(filepath.Join(stage, "project/.env")); !os.IsNotExist(e) {
		t.Fatal("env staged")
	}
	for _, p := range []string{"project/constraints.txt", "bridge/python/memeval_bridge/__init__.py", "assets/asset/model.bin"} {
		if _, e = os.Stat(filepath.Join(stage, p)); e != nil {
			t.Fatal(e)
		}
	}
	writeFixture(t, m.Resolve("asset/.env"), "PRIVATE=never-stage-this")
	if _, e = inputHash(m); e == nil {
		t.Fatal("private asset accepted")
	}
}

func TestImageInputNetworkIsRuntimeOnly(t *testing.T) {
	m, _ := fixtureManifest(t, "uv-python")
	before, err := inputHash(m)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := inputFiles(m)
	if err != nil {
		t.Fatal(err)
	}
	// Canonical none also preserves the pre-fix default-mode manifest hash.
	legacy := *m
	legacy.LLM.Model, legacy.LLM.Effort, legacy.Notes = "", "", ""
	if inputs["project/manifest.json"] != sandbox.Hash(legacy) {
		t.Fatal("existing none image identity changed")
	}
	for _, network := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkOpen, sandbox.NetworkGateway} {
		m.Sandbox.Network = network
		after, err := inputHash(m)
		if err != nil || after != before {
			t.Fatalf("network=%s hash=%s want=%s err=%v", network, after, before, err)
		}
		if m.Sandbox.Network != network {
			t.Fatal("hashing mutated runtime policy")
		}
	}
	// Separately selected manifest files and other build inputs remain distinct.
	m.Path = filepath.Join(m.Dir, "manifest-open.json")
	raw, _ := json.Marshal(m)
	writeFixture(t, m.Path, string(raw))
	variant, err := inputHash(m)
	if err != nil || variant == before {
		t.Fatal("independent manifest variant collapsed", err)
	}
	m.Runtime.Apt = []string{"libgomp1"}
	changed, err := inputHash(m)
	if err != nil || changed == variant {
		t.Fatal("build input ignored", err)
	}
}
func TestImageLockPlatformAndDiagnostics(t *testing.T) {
	m, _ := fixtureManifest(t, "uv-python")
	raw := []byte("# memeval-lock-platform: darwin/arm64\nfixture==1 --hash=sha256:" + strings.Repeat("a", 64))
	if e := validateLockPlatform(raw, imagePlatform()); e == nil {
		t.Fatal("foreign lock accepted")
	}
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	if c, e := container.New(container.DefaultConfig()).Capabilities(t.Context()); e != nil || !c.Enforced {
		t.Fatal(c, e)
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "build"})
	_, e := Install(t.Context(), m, InstallOptions{Cache: t.TempDir(), Offline: true})
	var failure *InstallFailure
	if !errors.As(e, &failure) {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(failure.Path); e != nil || !strings.Contains(string(b), "injected failure") {
		t.Fatal(e, string(b))
	}
}
func TestContainerManifestV3(t *testing.T) {
	for _, path := range []string{"echo/manifest.json", "mem0/manifest.json", "mem0/manifest-infer.json", "mastra/manifest.json", "mastra/manifest-semantic.json", "graphiti/manifest.json", "cognee/manifest.json", "hindsight/manifest.json", "hindsight/manifest-open.json", "octop-memory/manifest.json"} {
		m, e := Load("../../suts/" + path)
		if e != nil {
			t.Fatal(path, e)
		}
		if m.SchemaVersion != "3" || m.Sandbox.Backend != "container" {
			t.Fatal(path)
		}
		for _, k := range m.Process.EnvPassthrough {
			if credentialEnvKey.MatchString(k) {
				t.Fatal("provider credential passthrough", path, k)
			}
		}
	}
}
func TestImageV2LockMigration(t *testing.T) {
	m, root := fixtureManifest(t, "uv-python")
	m.CacheDir = filepath.Join(root, "cache")
	dir := filepath.Join(m.CacheDir, "envs", m.Name+"@old")
	dir, _ = Canonical(dir)
	raw, _ := json.Marshal(map[string]any{"version": 2, "name": m.Name, "env_dir": dir, "image": map[string]any{"base_images": map[string]string{"old": "value"}}})
	writeFixture(t, filepath.Join(dir, "env.lock.json"), string(raw))
	_, e := CheckLock(m)
	var n *NotInstalled
	if !errors.As(e, &n) || n.Reason != "lock_version" {
		t.Fatal(e)
	}
}
func TestImageBuildRejectsUnavailableDaemon(t *testing.T) {
	m, _ := fixtureManifest(t, "uv-python")
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "version"})
	_, e := BuildImage(t.Context(), container.New(container.DefaultConfig()), m, InstallOptions{Cache: t.TempDir(), Offline: true})
	if e == nil || !strings.Contains(e.Error(), `docker context "orbstack" unreachable`) {
		t.Fatal(e)
	}
}
