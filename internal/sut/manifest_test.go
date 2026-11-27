package sut

import (
	"context"
	"encoding/json"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/none"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) *Manifest {
	t.Helper()
	m, e := Load("../../suts/echo/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	m.Dir = t.TempDir()
	return m
}
func TestEnvironmentDoesNotInherit(t *testing.T) {
	m := fixture(t)
	t.Setenv("CODEX_SECRET", "evaluator-private")
	t.Setenv("OPENAI_API_KEY", "evaluator-key")
	m.Process.EnvFile = ".env"
	m.Process.EnvPassthrough = []string{"OPENAI_API_KEY"}
	if e := os.WriteFile(filepath.Join(m.Dir, ".env"), []byte("OPENAI_API_KEY=sut-key\n"), 0600); e != nil {
		t.Fatal(e)
	}
	env, e := m.Environment("/episode", "/venv")
	if e != nil {
		t.Fatal(e)
	}
	if env["OPENAI_API_KEY"] != "sut-key" || env["CODEX_SECRET"] != "" || env["HOME"] != "/episode/home" {
		t.Fatal(env)
	}
	m.Process.Env["HOME"] = "/real/home"
	if _, e = m.Environment("/episode", "/venv"); e == nil {
		t.Fatal("HOME override accepted")
	}
}
func TestSharedEnvFileOnlyPassesAllowlistedKeys(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY", "MEMEVAL_SECRET", "HERDR_ENV", "CODEX_HOME", "CLAUDE_CONFIG", "OPENAI_API_KEY"} {
		t.Run(key, func(t *testing.T) {
			m := fixture(t)
			m.Process.EnvFile = ".env"
			os.WriteFile(filepath.Join(m.Dir, ".env"), []byte(key+"=secret\n"), 0600)
			if _, e := m.envFile(); e != nil {
				t.Fatal("unselected shared-file key rejected", e)
			}
			env, e := m.Environment("/episode", "/venv")
			if e != nil || env[key] != "" {
				t.Fatal("unselected shared key leaked", e)
			}
			m.Process.EnvPassthrough = []string{key}
			_, e = m.Environment("/episode", "/venv")
			if Forbidden(key) && e == nil {
				t.Fatal("forbidden key passed")
			}
			if key == "OPENAI_API_KEY" && e != nil {
				t.Fatal("explicit allowed SUT credential rejected", e)
			}
		})
	}
}
func TestPassThroughNeverFallsBack(t *testing.T) {
	m := fixture(t)
	m.Process.EnvPassthrough = []string{"SUT_KEY"}
	t.Setenv("SUT_KEY", "parent")
	if _, e := m.Environment("/episode", "/venv"); e == nil {
		t.Fatal("read from parent")
	}
}
func TestRedactedEffectiveConfigAndFreshEpisode(t *testing.T) {
	s := sandbox.Spec{Command: []string{"/usr/bin/true", "secret-argument"}, Env: map[string]string{"SUT_KEY": "private-value"}, Network: sandbox.NetworkNone}
	b := none.Backend{}
	h, e := b.Prepare(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Destroy(context.Background(), h)
	j, _ := json.Marshal(h.EffectiveConfig())
	if strings.Contains(string(j), "private-value") || strings.Contains(string(j), "secret-argument") {
		t.Fatal(string(j))
	}
	for _, d := range []string{"home", "tmp", "xdg/config", "xdg/cache", "xdg/data", "work"} {
		if _, e := os.Stat(filepath.Join(h.Dir(), d)); e != nil {
			t.Fatal(e)
		}
	}
	if e = b.Destroy(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(h.Dir()); !os.IsNotExist(e) {
		t.Fatal("episode retained")
	}
}
func TestLockMismatch(t *testing.T) {
	m := fixture(t)
	if _, e := CheckLock(m); e == nil || !strings.Contains(e.Error(), "env install") {
		t.Fatal(e)
	}
}

func TestEvaluatorKeysNeverEnterSUT(t *testing.T) {
	for _, key := range []string{"MEMEVAL_OPENAI_API_KEY", "MEMEVAL_ANTHROPIC_API_KEY", "MEMEVAL_OPENAI_BASE_URL"} {
		t.Setenv(key, "evaluator-only-sentinel")
		m := fixture(t)
		env, err := m.Environment("/episode", "/venv")
		if err != nil || env[key] != "" {
			t.Fatal("evaluator environment leaked", key, err)
		}
		m.Process.Env[key] = "forbidden"
		if _, err = m.Environment("/episode", "/venv"); err == nil {
			t.Fatal("explicit evaluator env accepted", key)
		}
		delete(m.Process.Env, key)
		m.Process.EnvPassthrough = []string{key}
		if _, err = m.Environment("/episode", "/venv"); err == nil {
			t.Fatal("evaluator passthrough accepted", key)
		}
	}
}
