package octop

import (
	"context"
	"errors"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"strings"
	"testing"
)

func TestContainerContractGate(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, dockerErr, envErr, want string
		versionCalls, installCalls             int
	}{
		{"disabled", "", "", "", "MEMEVAL_OCTOP_IMAGE_TEST=1", 0, 0},
		{"not-one", "true", "", "", "MEMEVAL_OCTOP_IMAGE_TEST=1", 0, 0},
		{"daemon-missing", "1", "context unreachable", "", "Docker context unavailable", 1, 0},
		{"env-missing", "1", "", "image_missing", "CheckEnv failed", 1, 1},
		{"ready", "1", "", "", "", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, i := 0, 0
			err := imagePrerequisites(t.Context(), tc.enabled, func(context.Context) error {
				v++
				if tc.dockerErr != "" {
					return errors.New(tc.dockerErr)
				}
				return nil
			}, func() error {
				i++
				if tc.envErr != "" {
					return errors.New(tc.envErr)
				}
				return nil
			})
			if (tc.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.want) || v != tc.versionCalls || i != tc.installCalls {
				t.Fatal(err, v, i)
			}
		})
	}
}

func TestContainerContractGateUsesExplicitContextWithoutBuild(t *testing.T) {
	f := testoracle.FakeDocker(t)
	b := container.New(container.Config{CLI: f.CLI, Context: "fixture-context", LinuxBinary: f.LinuxBinary})
	probe := func(ctx context.Context) error {
		_, err := b.ImageCommand(ctx, "version", "--format", "json")
		return err
	}
	if err := imagePrerequisites(t.Context(), "1", probe, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "version"})
	if err := imagePrerequisites(t.Context(), "1", probe, func() error { t.Fatal("checked env with unreachable daemon"); return nil }); err == nil || !strings.Contains(err.Error(), "Docker context unavailable") {
		t.Fatal(err)
	}
	for _, args := range f.Calls(t) {
		if strings.Join(args, " ") != "--context fixture-context version --format json" {
			t.Fatal("unexpected build/start or context", args)
		}
	}
}
