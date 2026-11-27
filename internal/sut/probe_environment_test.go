package sut

import (
	"encoding/json"
	"io/fs"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOfflineProbeLLMPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, pin, source, want string }{
		{"env", "", "env", "env-model"},
		{"dotenv", "", ".env", "env-model"},
		{"manifest", "pinned-model", "env", "pinned-model"},
		{"flag", "pinned-model", "flag", "env-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := ParseLLMGateway("https://gateway.invalid:9443/v1", "real-fixture-key", "env-model", "low")
			if err != nil {
				t.Fatal(err)
			}
			g.Source["MEMEVAL_SUT_LLM_MODEL"] = tc.source
			m := &Manifest{Gateway: g, LLM: LLM{Required: true, Model: tc.pin, Effort: "medium", Env: map[string]string{
				"OPENAI_BASE_URL": "{llm.base_url}", "OPENAI_API_KEY": "{llm.api_key}", "SDK_MODEL": "{llm.model}", "SDK_EFFORT": "{llm.reasoning_effort}",
			}}}
			m.Sandbox.Network = sandbox.NetworkGateway
			m.Process.EnvPassthrough = []string{"OTHER_API_KEY"}
			before, _ := json.Marshal(m)
			p, err := offlineProbeManifest(m)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			if err := p.applyLLM(env); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"OPENAI_BASE_URL": "https://gateway.invalid:9443/v1", "OPENAI_API_KEY": probeAPIKey, "SDK_MODEL": tc.want, "SDK_EFFORT": "medium"}
			if !reflect.DeepEqual(env, want) || p.Sandbox.Network != sandbox.NetworkNone || p.LLM.Required || len(p.Process.EnvPassthrough) != 0 {
				t.Fatal("incorrect offline probe configuration", env)
			}
			after, _ := json.Marshal(m)
			if string(before) != string(after) || m.Gateway.APIKey != "real-fixture-key" || m.Gateway.Source["MEMEVAL_SUT_LLM_MODEL"] != tc.source {
				t.Fatal("probe mutated original manifest/gateway")
			}
		})
	}
}

func TestOfflineProbeMissingModel(t *testing.T) {
	for _, tc := range []string{"nil-gateway", "parser-default", "cli-default", "empty-dotenv", "empty-flag", "empty", "whitespace"} {
		t.Run(tc, func(t *testing.T) {
			m, root := fixtureManifest(t, "uv-python")
			m.LLM.Required = true
			switch tc {
			case "parser-default":
				m.Gateway, _ = ParseLLMGateway("", "", "", "")
			case "cli-default", "empty-dotenv", "empty-flag":
				// Cobra materializes the default in the environment, but records its
				// source: it is not an explicitly configured model.
				t.Setenv("MEMEVAL_SUT_LLM_MODEL", "gpt-6-luna")
				settings := &dotenv.Settings{Sources: map[string]string{"MEMEVAL_SUT_LLM_MODEL": "default"}}
				if tc != "cli-default" {
					t.Setenv("MEMEVAL_SUT_LLM_MODEL", "")
					settings.Sources["MEMEVAL_SUT_LLM_MODEL"] = ".env"
					if tc == "empty-flag" {
						settings.Sources["MEMEVAL_SUT_LLM_MODEL"] = "flag"
					}
				}
				var err error
				m.Gateway, err = ReadLLMGateway(dotenv.With(t.Context(), settings))
				if err != nil {
					t.Fatal(err)
				}
			case "empty":
				m.Gateway = &LLMGateway{}
			case "whitespace":
				m.LLM.Model = "   "
			}
			_, err := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
			if err == nil || err.Error() != "llm.required but no model configured (MEMEVAL_SUT_LLM_MODEL or manifest llm.model)" {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "cache")); !os.IsNotExist(err) {
				t.Fatal("missing model must fail before build/probe", err)
			}
		})
	}
	// A manifest pin is sufficient even without a gateway configuration object.
	m := &Manifest{LLM: LLM{Required: true, Model: "pinned-model"}}
	if _, err := offlineProbeManifest(m); err != nil {
		t.Fatal(err)
	}
}

func TestInstallDescribeProbeLLMEnvironment(t *testing.T) {
	m, root := fixtureManifest(t, "uv-python")
	f := testoracle.FakeDocker(t)
	f.Configure(t)
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "evaluator-fixture-secret")
	m.DefaultEnvFile = filepath.Join(root, "fixture.env")
	writeFixture(t, m.DefaultEnvFile, "OTHER_API_KEY=other-fixture-secret\n")
	m.Process.EnvPassthrough = []string{"OTHER_API_KEY"}
	m.Runtime.ProbeEnv = map[string]string{"PROBE_SETTING": "kept", "SDK_MODEL": "must-not-override-llm"}
	m.Sandbox.Network = sandbox.NetworkGateway
	m.LLM = LLM{Required: true, Model: "manifest-model", Effort: "medium", Env: map[string]string{
		"OPENAI_BASE_URL": "{llm.base_url}", "OPENAI_API_KEY": "{llm.api_key}", "SDK_MODEL": "{llm.model}", "SDK_EFFORT": "{llm.reasoning_effort}",
	}}
	m.Gateway, _ = ParseLLMGateway("http://gateway.invalid:8317/v1", "gateway-fixture-secret", "flag-model", "low")
	m.Gateway.Source["MEMEVAL_SUT_LLM_MODEL"] = "flag"
	body, err := os.ReadFile(m.Resolve("adapter.py"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, m.Resolve("adapter.py"), `import os
assert os.environ['OPENAI_BASE_URL'] == 'http://gateway.invalid:8317/v1'
assert os.environ['OPENAI_API_KEY'] == 'memeval-probe-placeholder'
assert os.environ['SDK_MODEL'] == 'flag-model'
assert os.environ['SDK_EFFORT'] == 'medium'
assert os.environ['PROBE_SETTING'] == 'kept'
assert 'OTHER_API_KEY' not in os.environ
assert not any(k.startswith('MEMEVAL_') for k in os.environ)
`+string(body))
	l, err := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	if l.Probe["ok"] != true || l.Probe["network"] != "none" || l.Probe["describe"] != "fixture" {
		t.Fatal(l.Probe)
	}
	sawSUT := false
	for _, args := range f.Calls(t) {
		line := strings.Join(args, " ")
		for _, secret := range []string{"gateway-fixture-secret", "evaluator-fixture-secret", "other-fixture-secret", probeAPIKey} {
			if strings.Contains(line, secret) {
				t.Fatal("credential/placeholder appeared in argv")
			}
		}
		if strings.Contains(line, "memeval.role=sut") {
			sawSUT = true
			if !strings.Contains(line, "--network none") {
				t.Fatal("install probe enabled egress")
			}
		}
	}
	if !sawSUT {
		t.Fatal("describe container not exercised")
	}
	if err := filepath.WalkDir(l.EnvDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		for _, secret := range []string{"gateway-fixture-secret", "evaluator-fixture-secret", "other-fixture-secret"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("secret in install artifact %s", p)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if m.Gateway.APIKey != "gateway-fixture-secret" || !m.LLM.Required {
		t.Fatal("install changed runtime credentials/config")
	}
	t.Log("fake container describe: expanded model/effort/original URL, placeholder key, no MEMEVAL_* or passthrough credentials, network none")
}

func TestLLMDisclosureNotes(t *testing.T) {
	m := &Manifest{LLM: LLM{Required: true, Model: "fixture-model"}}
	for _, value := range []any{[]string{"SDK fact", "second fact"}, []any{"SDK fact", 42, "second fact"}} {
		got := m.LLMDisclosure(map[string]any{"llm_notes": value})
		if !reflect.DeepEqual(got["notes"], []string{"SDK fact", "second fact"}) {
			t.Fatal(got)
		}
	}
	for _, config := range []map[string]any{nil, {}, {"llm_notes": "not a list"}} {
		got := m.LLMDisclosure(config)
		if notes := got["notes"].([]string); len(notes) != 0 {
			t.Fatal("fabricated SDK facts", notes)
		}
	}
}
