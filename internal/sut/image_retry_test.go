package sut

import (
	"errors"
	"memeval/internal/config/redact"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRetryablePrepareLog(t *testing.T) {
	header := "#15 [env 10/11] RUN HOME=/opt/memeval/env/models-home 'node' 'prepare.mjs'\n"
	for _, message := range []string{"fetch failed", "ECONNRESET", "socket disconnected", "TLS", "ETIMEDOUT", "Temporary failure in name resolution", "Connection reset"} {
		if !retryablePrepareLog(strings.NewReader(header + "#15 0.1 " + message + "\n#15 ERROR: process exited\n")) {
			t.Fatal("network failure not recognized", message)
		}
	}
	for name, text := range map[string]string{
		"non_network":         header + "#15 0.1 SyntaxError\n#15 ERROR: process exited\n",
		"successful_step":     header + "#15 0.1 fetch failed\n#15 DONE 1s\n",
		"other_step":          header + "#15 0.1 TLS failed\n#15 DONE 1s\n#16 ERROR: pip failed\n",
		"other_network":       header + "#16 0.1 TLS failed\n#15 ERROR: process exited\n",
		"pip":                 "#14 [env 5/11] RUN uv pip install\n#14 0.1 TLS failed\n#14 ERROR: process exited\n",
		"argv_not_diagnostic": strings.Replace(header, "prepare.mjs", "TLS.mjs", 1) + "#15 ERROR: missing file\n",
		"no_step":             "ERROR: fetch failed\n",
	} {
		t.Run(name, func(t *testing.T) {
			if retryablePrepareLog(strings.NewReader(text)) {
				t.Fatal("must not retry", text)
			}
		})
	}
}

func TestPrepareBuildBoundedRetry(t *testing.T) {
	for _, tc := range []struct {
		name, flaky, message string
		attempts             int
		success              bool
	}{
		{"one_failure", "1", "fetch failed", 2, true},
		{"two_failures", "2", "TLS disconnected fake-build-credential", 3, true},
		{"exhausted", "4", "ETIMEDOUT", 3, false},
		{"non_network", "4", "SyntaxError: invalid syntax", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, root := fixtureManifest(t, "python")
			m.Runtime.Prepare = [][]string{{"{python}", "{project}/prepare.py"}}
			writeFixture(t, m.Resolve("prepare.py"), "print('prepare fixture')\n")
			f := testoracle.FakeDocker(t)
			f.Configure(t)
			f.Control(t, map[string]string{"FAKE_DOCKER_BUILD_FLAKY": tc.flaky, "FAKE_DOCKER_BUILD_ERROR": tc.message})
			ctx := redact.With(t.Context(), redact.New([]string{"fake-build-credential"}))
			l, err := Install(ctx, m, InstallOptions{Cache: filepath.Join(root, "cache"), Offline: true})
			var dir string
			if tc.success {
				if err != nil {
					t.Fatal(err)
				}
				dir = l.EnvDir
				if err = CheckEnv(l); err != nil {
					t.Fatal("retry diagnostics broke inventory", err)
				}
			} else {
				var failure *InstallFailure
				if !errors.As(err, &failure) {
					t.Fatalf("want install failure: %v", err)
				}
				dir = filepath.Dir(failure.Path)
				if _, err := os.Stat(filepath.Join(dir, "env.lock.json")); !os.IsNotExist(err) {
					t.Fatal("failed install published lock", err)
				}
			}
			attempts := 0
			for _, call := range f.Calls(t) {
				if len(call) > 2 && call[2] == "build" && slices.Contains(call, "--target") && slices.Contains(call, "env") {
					attempts++
				}
			}
			if attempts != tc.attempts {
				t.Fatal("unbounded or missing retry", attempts, tc.attempts)
			}
			for _, name := range []string{"build.log", "install-failure.log"} {
				p := filepath.Join(dir, name)
				text, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(text), "prepare network failure: retry") != tc.attempts-1 || strings.Contains(string(text), "fake-build-credential") {
					t.Fatal(name, string(text))
				}
				st, err := os.Stat(p)
				if err != nil || st.Mode().Perm() != 0600 {
					t.Fatal(name, "permissions", err)
				}
			}
			t.Logf("attempts=%d success=%t diagnostics=build.log+install-failure.log", attempts, tc.success)
		})
	}
}
