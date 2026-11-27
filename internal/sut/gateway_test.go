package sut

import (
	"encoding/json"
	"memeval/internal/config/dotenv"
	"memeval/internal/config/redact"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLLMConcurrencyValidation(t *testing.T) {
	m, _ := fixtureManifest(t, "uv-python")
	for _, tc := range []struct {
		name string
		n    *int
		bad  bool
	}{
		{"absent", nil, false}, {"minimum", new(1), false},
		{"fanout", new(32), false}, {"zero", new(0), true}, {"negative", new(-1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.LLM.Concurrency = tc.n
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, m.Path, string(raw))
			loaded, err := Load(m.Path)
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "llm.concurrency must be >= 1") {
					t.Fatal("invalid declaration not rejected", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.n == nil {
				if loaded.LLM.Concurrency != nil || strings.Contains(string(raw), `"concurrency"`) {
					t.Fatal("absent concurrency changed manifest serialization")
				}
			} else if loaded.LLM.Concurrency == nil || *loaded.LLM.Concurrency != *tc.n {
				t.Fatal("concurrency lost on load", loaded.LLM)
			}
		})
	}
}

func TestCogneeGatewayFanout(t *testing.T) {
	m, err := Load("../../suts/cognee/manifest-gateway.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.LLM.Concurrency == nil || *m.LLM.Concurrency != 40 {
		t.Fatal("Cognee gateway must declare its per-episode fan-out", m.LLM.Concurrency)
	}
}

func TestLLMGatewayConfiguration(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8317/v1", "http://127.0.0.1/v1", "https://example.invalid/v1", "http://[::1]:8317/v1"} {
		g, e := ParseLLMGateway(u, "fake-gateway-key", "", "")
		if e != nil || g.BaseURL.String() != u || g.Model != "gpt-6-luna" || g.Effort != "low" {
			t.Fatal(g, e)
		}
		b, _ := json.Marshal(g)
		if strings.Contains(string(b), "fake-gateway-key") {
			t.Fatal(string(b))
		}
	}
	for _, u := range []string{"file:///tmp/gw", "ftp://127.0.0.1", "http:///v1", "http://127.0.0.1:99999", "http://key:secret@127.0.0.1", "http://127.0.0.1?api_key=secret"} {
		if _, e := ParseLLMGateway(u, "", "", ""); e == nil {
			t.Fatal("accepted", u)
		}
	}
	t.Setenv("MEMEVAL_SUT_LLM_BASE_URL", "http://127.0.0.1:8317/v1")
	t.Setenv("MEMEVAL_SUT_LLM_API_KEY", "fake-gateway-key")
	t.Setenv("MEMEVAL_SUT_LLM_MODEL", "fixture-model")
	t.Setenv("MEMEVAL_SUT_LLM_REASONING_EFFORT", "medium")
	g, e := ReadLLMGateway(t.Context())
	if e != nil || g.Model != "fixture-model" || g.Effort != "medium" || g.Source["MEMEVAL_SUT_LLM_MODEL"] != "env" {
		t.Fatal(g, e)
	}
	m := &Manifest{DefaultEnvFile: filepath.Join(t.TempDir(), "empty.env"), Gateway: g}
	if !slices.Contains(m.Secrets(), "fake-gateway-key") {
		t.Fatal("gateway key missing from manifest secrets")
	}
	settings := &dotenv.Settings{File: map[string]string{}}
	if dotenv.Public("MEMEVAL_SUT_LLM_API_KEY") || !slices.Contains(settings.Secrets(), "fake-gateway-key") {
		t.Fatal("gateway key is not private")
	}
	if got := redact.New(settings.Secrets()).Text("error: fake-gateway-key"); strings.Contains(got, "fake-gateway-key") {
		t.Fatal(got)
	}
}

func TestLLMManifestPrecedenceAndImageIdentity(t *testing.T) {
	m, l, f := installImageFixture(t)
	m.LLM = LLM{Required: true, Model: "manifest-model", Effort: "medium", Env: map[string]string{"OPENAI_BASE_URL": "{llm.base_url}", "OPENAI_API_KEY": "{llm.api_key}", "MODEL": "{llm.model}", "EFFORT": "{llm.reasoning_effort}"}}
	if e := m.validateLLM(); e != nil {
		t.Fatal(e)
	}
	m.Gateway, _ = ParseLLMGateway("http://127.0.0.1:8317/v1", "fake-gateway-key", "env-model", "low")
	m.Gateway.Source = map[string]string{"MEMEVAL_SUT_LLM_MODEL": "env", "MEMEVAL_SUT_LLM_REASONING_EFFORT": "env"}
	if g := m.EffectiveGateway(); g.Model != "manifest-model" || g.Effort != "medium" {
		t.Fatal(g)
	}
	m.Gateway.Source["MEMEVAL_SUT_LLM_MODEL"] = "flag"
	if g := m.EffectiveGateway(); g.Model != "env-model" || g.Effort != "medium" {
		t.Fatal(g)
	}
	if _, e := m.Spec(l, t.TempDir(), "ns", false); e == nil || !strings.Contains(e.Error(), "gateway 或 open") {
		t.Fatal(e)
	}
	m.Sandbox.Network = sandbox.NetworkGateway
	profile := func() string {
		t.Helper()
		b := container.New(container.DefaultConfig())
		h, e := m.Prepare(t.Context(), b, l, "ns", false)
		if e != nil {
			t.Fatal(e)
		}
		defer b.Destroy(t.Context(), h)
		raw, e := os.ReadFile(filepath.Join(h.Dir(), ".memeval", "env.list"))
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(raw), "OPENAI_BASE_URL=http://llm-gateway:8317/v1") || !strings.Contains(string(raw), "OPENAI_API_KEY=fake-gateway-key") || !strings.Contains(string(raw), "MODEL=env-model") {
			t.Fatal("runtime LLM templates not expanded")
		}
		j, _ := json.Marshal([]any{h.EffectiveConfig(), m.LLMDisclosure(nil)})
		if strings.Contains(string(j), "fake-gateway-key") {
			t.Fatal("credential disclosed")
		}
		return h.ProfileSHA256()
	}
	first := profile()
	m.LLM.Effort = "high"
	second := profile()
	if first == second {
		t.Fatal("effort absent from run profile")
	}
	for _, args := range f.Calls(t) {
		if strings.Contains(strings.Join(args, " "), "fake-gateway-key") {
			t.Fatal("credential in CLI argv")
		}
	}
	raw, _ := json.Marshal(m.LLMDisclosure(nil))
	if !strings.Contains(string(raw), `"source":"flag"`) {
		t.Fatal(string(raw))
	}
}

func TestLLMTemplateGatewayComposition(t *testing.T) {
	for _, tc := range []struct {
		url, alias string
	}{
		{"http://127.0.0.1:8317/v1", "http://llm-gateway:8317/v1"},
		{"http://127.0.0.1/api%2Fv1", "http://llm-gateway:80/api%2Fv1"},
	} {
		for _, mode := range []sandbox.NetworkMode{sandbox.NetworkGateway, sandbox.NetworkOpen} {
			t.Run(string(mode)+"/"+tc.url, func(t *testing.T) {
				m := &Manifest{LLM: LLM{Env: map[string]string{
					"ENDPOINT": "{llm.base_url}/chat/completions",
					"CONFIG":   "endpoint={llm.base_url};model={llm.model}",
				}}}
				m.Sandbox.Network = mode
				var err error
				m.Gateway, err = ParseLLMGateway(tc.url, "", "fixture-model", "low")
				if err != nil {
					t.Fatal(err)
				}
				env := map[string]string{"OTHER": tc.url + "/unchanged"}
				if err = m.applyLLM(env); err != nil {
					t.Fatal(err)
				}
				want := tc.url
				if mode == sandbox.NetworkGateway {
					want = tc.alias
				}
				if env["ENDPOINT"] != want+"/chat/completions" || env["CONFIG"] != "endpoint="+want+";model=fixture-model" || env["OTHER"] != tc.url+"/unchanged" {
					t.Fatalf("unexpected template expansion: %#v", env)
				}
			})
		}
	}
}
