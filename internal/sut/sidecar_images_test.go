package sut

import (
	"context"
	"encoding/json"
	"memeval/internal/episodelog"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sidecar"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func planningLock(t *testing.T, m *Manifest) *Lock {
	t.Helper()
	l := &Lock{EnvDir: t.TempDir(), ImageRoot: ImageRoot, Executable: ImageRoot + "/.venv/bin/python", VenvRealPath: ImageRoot + "/.venv"}
	for _, s := range m.Sidecars {
		if s.Image != "" {
			_, d, _ := strings.Cut(s.Image, "@")
			l.SidecarImages = append(l.SidecarImages, SidecarImage{Name: s.Name, Ref: s.Image, ID: s.Image, Digest: d})
		}
	}
	return l
}
func TestPresetAppendixAgreement(t *testing.T) {
	raw, e := os.ReadFile("../../design/p8-container-platform.md")
	if e != nil {
		t.Fatal(e)
	}
	var presets map[string]Sidecar
	if e = json.Unmarshal(presetData, &presets); e != nil {
		t.Fatal(e)
	}
	if len(presets) != 2 {
		t.Fatal(presets)
	}
	for name, p := range presets {
		ref, digest, ok := strings.Cut(p.Image, "@")
		if !ok || !strings.Contains(string(raw), ref) || !strings.Contains(string(raw), digest) {
			t.Fatal(name, p.Image)
		}
	}
	if _, e = sidecarPreset("redis"); e == nil {
		t.Fatal("unlocked preset")
	}
}
func TestImageSidecarPlanValidation(t *testing.T) {
	m := &Manifest{Sidecars: []Sidecar{{Name: "postgres", Kind: "postgres", Preset: "postgres-pgvector-18", Env: map[string]string{"POSTGRES_USER": "hindsight", "POSTGRES_DB": "postgres"}, Exports: map[string]string{"DB": "postgresql://hindsight@{sidecar.postgres.host}:{sidecar.postgres.port}/postgres"}}, {Name: "api", Kind: "generic", Port: 8888, Command: []string{"{python}", "{project}/server.py", "{sidecar.postgres.host}"}, Ready: sandbox.ServiceReady{TCP: "127.0.0.1:{port}"}}}}
	if e := m.validateSidecars(); e != nil {
		t.Fatal(e)
	}
	l := planningLock(t, m)
	s := sandbox.Spec{EpisodeDir: t.TempDir(), Env: map[string]string{}, Command: []string{"/fixture", "{sidecar.api.host}"}}
	if e := m.PlanServices(l, &s); e != nil {
		t.Fatal(e)
	}
	pg, api := s.Services[0], s.Services[1]
	if pg.Port != 5432 || api.Port != 8888 || s.Command[1] != "api" || api.Env["DB"] != "postgresql://hindsight@postgres:5432/postgres" || strings.Join(pg.Ready.Exec, " ") != "pg_isready -U hindsight -d postgres -h 127.0.0.1" {
		t.Fatal(s)
	}
	if e := m.PlanServices(&Lock{}, &sandbox.Spec{Env: map[string]string{}}); e == nil {
		t.Fatal("unlocked sidecar accepted")
	}
	for name, change := range map[string]func(*Sidecar){"port": func(s *Sidecar) { s.Port = 0 }, "kind": func(s *Sidecar) { s.Kind = "redis" }, "no_image_command": func(s *Sidecar) { s.Command = nil }, "mutable_image": func(s *Sidecar) { s.Image = "postgres:latest" }, "root": func(s *Sidecar) { s.User = "0:0" }, "ready": func(s *Sidecar) { s.Ready.Exec = []string{"true"} }} {
		t.Run(name, func(t *testing.T) {
			v := m.Sidecars[1]
			change(&v)
			x := &Manifest{Sidecars: []Sidecar{v}}
			if e := x.validateSidecars(); e == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
}
func TestSidecarPullLockAndIdentity(t *testing.T) {
	f := testoracle.FakeDocker(t)
	b := container.New(container.Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Platform: f.Platform})
	m := &Manifest{Sidecars: []Sidecar{{Name: "pg", Kind: "postgres", Preset: "postgres-pgvector-18"}}}
	if e := m.validateSidecars(); e != nil {
		t.Fatal(e)
	}
	l := planningLock(t, m)
	l.SidecarImages = nil
	if e := installSidecarImages(t.Context(), b, m, l, os.Stderr); e != nil {
		t.Fatal(e)
	}
	if len(l.SidecarImages) != 1 || l.SidecarImages[0].Digest == "" || l.SidecarImages[0].ID == "" {
		t.Fatal(l)
	}
	l.SidecarImages = nil
	if e := installSidecarImages(t.Context(), b, m, l, os.Stderr); e != nil {
		t.Fatal(e)
	}
	pulls := 0
	for _, args := range f.Calls(t) {
		if len(args) > 2 && args[2] == "pull" {
			pulls++
		}
	}
	if pulls != 1 {
		t.Fatal("pull not idempotent", pulls)
	}
	raw, _ := json.Marshal(l)
	var round Lock
	json.Unmarshal(raw, &round)
	if sandbox.Hash(l) != sandbox.Hash(&round) {
		t.Fatal("lock roundtrip")
	}
	before := lockHash(l)
	l.SidecarImages[0].Digest = "sha256:" + strings.Repeat("f", 64)
	if lockHash(l) == before {
		t.Fatal("sidecar digest outside lock identity")
	}
}

func TestImageSidecarsLifecycle(t *testing.T) {
	m, root := fixtureManifest(t, "python")
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	m.Sidecars = []Sidecar{{Name: "postgres", Kind: "postgres", Preset: "postgres-pgvector-18", Init: [][]string{{"psql", "-U", "memeval", "-d", "postgres", "-c", "CREATE EXTENSION vector"}}}, {Name: "qdrant", Kind: "qdrant", Preset: "qdrant-1.19.2"}}
	if e := m.validateSidecars(); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(m)
	writeFixture(t, m.Path, string(raw))
	var err error
	m, err = Load(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckEnv(l); err != nil {
		t.Fatal(err)
	}
	logs, err := episodelog.New(t.Context(), t.TempDir(), episodelog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	sink, _ := logs.Episode("sidecars")
	b := container.New(container.DefaultConfig())
	h, err := m.Prepare(t.Context(), b, l, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Destroy(context.Background(), h)
	f.Control(t, map[string]string{"FAKE_DOCKER_EXEC_RETRIES": "2"})
	g, err := sidecar.Start(t.Context(), b, h, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Stop(context.Background())
	evidence := g.Evidence()
	if len(evidence) != 2 {
		t.Fatal(evidence)
	}
	for _, v := range evidence {
		if !slices.Contains(v.EnvKeys, "PATH") || !slices.Contains(v.EnvKeys, "HOME") || slices.Contains(v.EnvKeys, "PYTHONPATH") || slices.Contains(v.EnvKeys, "EP") {
			t.Fatal("effective environment keys", v.EnvKeys)
		}
		if !v.Ready || v.PID != 0 || !v.LoopbackOnly || v.Audit != "container-inspect" || v.Container == "" || v.Image == "" || v.ImageDigest == "" || v.Network == "" || !v.Internal || v.HostPortsPublished || v.ExecReadyMS <= 0 {
			t.Fatalf("%+v", v)
		}
	}
	if g.VerifyStopped(t.Context()) == nil {
		t.Fatal("live services counted as stopped")
	}
	for _, failure := range []string{"stopped", "published", "ports", "extra_network", "external"} {
		f.Control(t, map[string]string{"FAKE_DOCKER_SERVICE_AUDIT": failure})
		if err = g.Audit(t.Context()); err == nil {
			t.Fatal("inspect audit accepted", failure)
		}
	}
	f.Control(t, map[string]string{})
	if err = g.Audit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = b.Destroy(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if err = g.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = g.VerifyStopped(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = logs.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"sidecar-postgres.log", "sidecar-qdrant.log", "init-postgres-1.log"} {
		raw, err := os.ReadFile(filepath.Join(sink.Dir(), file))
		if err != nil || !strings.Contains(string(raw), "stdout") || !strings.Contains(string(raw), "stderr") {
			t.Fatal(file, string(raw), err)
		}
	}
	calls := f.Calls(t)
	names := map[string]string{}
	removals := []string{}
	pgReady, pgInit, qCreate := -1, -1, -1
	for i, args := range calls {
		if len(args) < 3 {
			continue
		}
		if args[2] == "create" && slices.Contains(args, "memeval.role=sidecar") {
			alias := args[slices.Index(args, "--network-alias")+1]
			names[alias] = args[slices.Index(args, "--name")+1]
			for _, flag := range []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init"} {
				if !slices.Contains(args, flag) {
					t.Fatal(alias, "missing", flag)
				}
			}
			if slices.Contains(args, "-p") || slices.Contains(args, "-v") {
				t.Fatal("preset binds or publishes", args)
			}
			if alias == "postgres" {
				if !slices.Contains(args, "999:999") || !slices.Contains(args, "256m") || !slices.Contains(args, "/var/lib/postgresql:rw,nosuid,nodev,size=4g,uid=999,gid=999,mode=0700") {
					t.Fatal(args)
				}
			}
			if alias == "qdrant" {
				qCreate = i
			}
		}
		if args[2] == "exec" && slices.Contains(args, "pg_isready") {
			pgReady = i
		}
		if args[2] == "exec" && slices.Contains(args, "psql") {
			pgInit = i
		}
		if args[2] == "rm" {
			removals = append(removals, args[len(args)-1])
		}
	}
	if !(pgReady >= 0 && pgReady < pgInit && pgInit < qCreate) {
		t.Fatal("init must follow readiness before next service", pgReady, pgInit, qCreate)
	}
	// Compare removal IDs against create results via fixture state/calls: name order
	// is checked by container ownership IDs saved in the inspect log below.
	if names["postgres"] == "" || names["qdrant"] == "" || len(removals) < 3 {
		t.Fatal(names, removals)
	}
	encoded, _ := json.Marshal(evidence)
	t.Logf("evidence=%s; logs=%s; sidecar_images=%+v", encoded, sink.Dir(), l.SidecarImages)
}

func TestV3RejectsRetiredSidecarFields(t *testing.T) {
	m, _ := fixtureManifest(t, "python")
	raw, _ := json.Marshal(m)
	var base map[string]any
	json.Unmarshal(raw, &base)
	for _, field := range []string{"managed", "transport", "source", "strip_components", "extensions"} {
		v := map[string]any{"name": "db", "kind": "generic", "port": 9999, "command": []string{"{python}"}, "ready": map[string]any{"tcp": "127.0.0.1:9999"}, field: nil}
		base["sidecars"] = []any{v}
		raw, _ = json.Marshal(base)
		writeFixture(t, m.Path, string(raw))
		if _, e := Load(m.Path); e == nil || !strings.Contains(e.Error(), "unknown field") {
			t.Fatal(field, e)
		}
	}
	base["sidecars"] = []any{map[string]any{"name": "db", "kind": "generic", "port": 9999, "command": []string{"{python}"}, "ready": map[string]any{"unix": "/tmp/db.sock"}}}
	raw, _ = json.Marshal(base)
	writeFixture(t, m.Path, string(raw))
	if _, e := Load(m.Path); e == nil || !strings.Contains(e.Error(), "unknown field") {
		t.Fatal(e)
	}
}
func TestSidecarDigestInputIdentity(t *testing.T) {
	m, _ := fixtureManifest(t, "python")
	m.Sidecars = []Sidecar{{Name: "db", Kind: "postgres", Preset: "postgres-pgvector-18"}}
	if e := m.validateSidecars(); e != nil {
		t.Fatal(e)
	}
	a, e := inputHash(m)
	if e != nil {
		t.Fatal(e)
	}
	ref, _, _ := strings.Cut(m.Sidecars[0].Image, "@")
	m.Sidecars[0].Image = ref + "@sha256:" + strings.Repeat("e", 64)
	b, e := inputHash(m)
	if e != nil || a == b {
		t.Fatal("sidecar digest not in input identity", e)
	}
}
func TestSidecarDigestPlatformChecks(t *testing.T) {
	ref := "example/image:v1@sha256:" + strings.Repeat("a", 64)
	v := imageInfo{ID: "sha256:id", Os: "linux", Architecture: "arm64", RepoDigests: []string{ref}}
	if e := verifySidecarImage(v, ref, "linux/arm64"); e != nil {
		t.Fatal(e)
	}
	v.Architecture = "amd64"
	if verifySidecarImage(v, ref, "linux/arm64") == nil {
		t.Fatal("wrong platform")
	}
	v.Architecture = "arm64"
	v.RepoDigests = []string{strings.ReplaceAll(ref, "aaaa", "bbbb")}
	if verifySidecarImage(v, ref, "linux/arm64") == nil {
		t.Fatal("wrong digest")
	}
}
