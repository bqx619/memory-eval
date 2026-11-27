package sut

import (
	"context"
	"maps"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSidecarEnvironmentIsolation(t *testing.T) {
	m, root := fixtureManifest(t, "python")
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	m.Sidecars = []Sidecar{
		{Name: "first", Kind: "qdrant", Preset: "qdrant-1.19.2", Exports: map[string]string{"EARLIER_EXPORT": "{sidecar.first.host}:{sidecar.first.port}"}},
		{Name: "postgres", Kind: "postgres", Preset: "postgres-pgvector-18", Env: map[string]string{"POSTGRES_USER": "hindsight", "SERVICE_ONLY": "configured"}},
		{Name: "api", Kind: "generic", Port: 8888, Command: []string{"{python}", "{project}/server.py"}, Ready: sandbox.ServiceReady{TCP: "127.0.0.1:{port}"}},
	}
	if err := m.validateSidecars(); err != nil {
		t.Fatal(err)
	}
	l, err := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := sandbox.NewEpisode()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ep)
	sutEnv := map[string]string{"PATH": ImageRoot + "/.venv/bin:/usr/bin:/bin", "PYTHONPATH": ImageRoot + "/project", "HOME": ep + "/home", "TMPDIR": ep, "XDG_CACHE_HOME": ep + "/cache", "EP": ep, "SUT_PROCESS_VALUE": "private-to-sut", "OPENAI_API_KEY": "fake-llm-credential", "OPENAI_BASE_URL": "http://gateway:8317/v1"}
	// Use the production manifest plan, then replace only the parent's fixture
	// environment so neither image creation nor env.list can mask leakage.
	s, err := m.Spec(l, ep, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Services = nil
	s.Env = maps.Clone(sutEnv)
	if err = m.PlanServices(l, &s); err != nil {
		t.Fatal(err)
	}
	pg := s.Services[1]
	want := map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TZ": "UTC", "HOME": "/tmp", "TMPDIR": "/tmp", "POSTGRES_USER": "hindsight", "POSTGRES_DB": "postgres", "POSTGRES_HOST_AUTH_METHOD": "trust", "SERVICE_ONLY": "configured", "EARLIER_EXPORT": "first:6333"}
	if !maps.Equal(pg.Env, want) {
		t.Fatalf("postgres received SUT environment: %#v", pg.Env)
	}
	for k, v := range sutEnv {
		if s.Services[2].Env[k] != v {
			t.Fatal("generic lost inherited environment", k)
		}
	}
	b := container.New(container.DefaultConfig())
	h, err := b.Prepare(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Destroy(context.Background(), h)
	for _, service := range s.Services {
		if _, err = b.PrepareServiceCommand(t.Context(), h, service, service.Command, service.Env, "sidecar"); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range f.Calls(t) {
		if len(call) < 3 || call[2] != "create" || !slices.Contains(call, "memeval.role=sidecar") {
			continue
		}
		alias := call[slices.Index(call, "--network-alias")+1]
		envfile := call[slices.Index(call, "--env-file")+1]
		raw, err := os.ReadFile(envfile)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			k, v, _ := strings.Cut(line, "=")
			got[k] = v
		}
		if alias == "postgres" && !maps.Equal(got, want) {
			t.Fatal("env.list drift", got)
		}
		if alias == "api" && (got["PATH"] != sutEnv["PATH"] || got["PYTHONPATH"] != sutEnv["PYTHONPATH"] || got["OPENAI_API_KEY"] != sutEnv["OPENAI_API_KEY"] || got["EARLIER_EXPORT"] != "first:6333") {
			t.Fatal("generic env.list lost SUT configuration")
		}
	}
	// Explicit service overrides remain allowed even for independent images.
	m.Sidecars[1].Env["PATH"] = "/explicit/bin"
	m.Sidecars[1].Env["HOME"] = "/explicit/home"
	s.Services = nil
	if err = m.PlanServices(l, &s); err != nil {
		t.Fatal(err)
	}
	if s.Services[1].Env["PATH"] != "/explicit/bin" || s.Services[1].Env["HOME"] != "/explicit/home" {
		t.Fatal("explicit overrides lost")
	}
}
