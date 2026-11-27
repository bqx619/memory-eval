package sut

import (
	"errors"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSecretsOnlyCredentialsAndExplicitMarkers(t *testing.T) {
	m := fixture(t)
	m.Process.EnvFile = ".env"
	m.Process.EnvPassthrough = []string{"SUT_API_KEY", "SUT_MODEL", "SUT_BASE_URL", "SUT_PROVIDER", "SUT_TOKEN", "CUSTOM_VALUE"}
	writeFixture(t, m.EnvPath(), "SUT_API_KEY=credential-value-long\nSUT_MODEL=local-model-name\nSUT_BASE_URL=https://local.invalid/v1\nSUT_PROVIDER=local\nSUT_TOKEN=1234567\nCUSTOM_VALUE=custom-credential\nUNSELECTED_API_KEY=unselected-credential\n")
	m.Process.Env = map[string]string{"FLAG": "1", "PROVIDER": "none", "ENGINE": "local", "SETTING": "long-static-config", "STATIC_API_KEY": "unmarked-static-value", "EXPLICIT": "marked-static-value", "SHORT": "1234567"}
	m.Process.SecretEnv = []string{"EXPLICIT", "SHORT", "CUSTOM_VALUE"}
	m.Sidecars = []Sidecar{{Env: map[string]string{"FLAG": "1", "PROVIDER": "none", "SETTING": "sidecar-static-value", "CREDENTIAL": "marked-sidecar-value", "SHORT": "凭据值"}, SecretEnv: []string{"CREDENTIAL", "SHORT"}}}
	got := m.Secrets()
	want := []string{"credential-value-long", "custom-credential", "marked-static-value", "marked-sidecar-value"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("secret selection = %q, want %q", got, want)
	}
	// An unrelated environment construction failure must not discard known secrets.
	m.Process.Env["HOME"] = "/outside"
	if !slices.Equal(m.Secrets(), jsonrpc.SecretValues(want)) {
		t.Fatal("runtime validation suppressed credential redaction")
	}
	os.Remove(m.EnvPath())
	if !slices.Contains(m.Secrets(), "marked-static-value") || !slices.Contains(m.Secrets(), "marked-sidecar-value") {
		t.Fatal("explicit markers depend on env_file")
	}
}

func TestInstallFailurePreservesStaticConfiguration(t *testing.T) {
	m := fixture(t)
	m.Process.EnvFile = ".env"
	m.Process.EnvPassthrough = []string{"SUT_API_KEY", "SUT_MODEL"}
	writeFixture(t, m.EnvPath(), "SUT_API_KEY=private-credential\nSUT_MODEL=local-model-name\n")
	m.Process.Env = map[string]string{"FLAG": "1", "PROVIDER": "none", "ENGINE": "local", "SETTING": "long-static-config", "EXPLICIT": "private-static-value"}
	m.Process.SecretEnv = []string{"EXPLICIT"}
	m.Sidecars = []Sidecar{{Env: map[string]string{"FLAG": "1", "ENGINE": "local", "AUTH": "sidecar-credential"}, SecretEnv: []string{"AUTH"}}}
	line := "/home/example/env/1.0.1/site-packages none local local-model-name long-static-config"
	cause := &jsonrpc.RPCError{Code: -32000, Message: line + " private-credential", Data: []byte(`{"type":"ValueError","detail":"private-static-value sidecar-credential"}`)}
	l := &Lock{EnvDir: t.TempDir()}
	err := RecordInstallFailure(m, l, cause, line+" private-credential private-static-value sidecar-credential")
	var failure *InstallFailure
	if !errors.As(err, &failure) {
		t.Fatal(err)
	}
	raw, e := os.ReadFile(failure.Path)
	if e != nil {
		t.Fatal(e)
	}
	for _, out := range []string{string(raw), failure.Error(), jsonrpc.Redact(cause.Message, m.Secrets())} {
		if !strings.Contains(out, line) {
			t.Fatalf("diagnostic corrupted: %s", out)
		}
		for _, secret := range m.Secrets() {
			if strings.Contains(out, secret) {
				t.Fatal("credential leaked")
			}
		}
	}
	info, _ := os.Stat(failure.Path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestSecretEnvManifestValidation(t *testing.T) {
	for _, target := range []string{"process", "sidecar"} {
		t.Run(target, func(t *testing.T) {
			m := fixture(t)
			m.Path = filepath.Join(m.Dir, "manifest.json")
			m.DefaultEnvFile = filepath.Join(m.Dir, "absent.env")
			if target == "process" {
				m.Process.Env = map[string]string{"PRIVATE_VALUE": "credential-value"}
				m.Process.SecretEnv = []string{"PRIVATE_VALUE"}
				if err := m.validateSecretEnv(m.Process.SecretEnv, m.Process.Env, m.Process.EnvPassthrough); err != nil {
					t.Fatal(err)
				}
				m.Process.SecretEnv = []string{"TYPO"}
				if err := m.validateSecretEnv(m.Process.SecretEnv, m.Process.Env, nil); err == nil {
					t.Fatal("unknown marker accepted")
				}
			} else {
				m.Sidecars = []Sidecar{{Name: "fixture", Kind: "generic", Port: 9999, Command: []string{"/fixture"}, Stop: "SIGTERM", Env: map[string]string{"PRIVATE_VALUE": "credential-value"}, SecretEnv: []string{"PRIVATE_VALUE"}}}
				m.Sidecars[0].Ready.TCP = "127.0.0.1:{port}"
				if err := m.validateSidecars(); err != nil {
					t.Fatal(err)
				}
				m.Sidecars[0].SecretEnv = []string{"TYPO"}
				if err := m.validateSidecars(); err == nil {
					t.Fatal("unknown marker accepted")
				}
			}
		})
	}
}
