package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/config/redact"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"os"
	"os/exec"
	"strings"
)

type CommandError struct {
	Operation, Stderr string
	cause             error
}

func (e *CommandError) Unwrap() error { return e.cause }

func (e *CommandError) Error() string {
	return fmt.Sprintf("docker %s: %s", e.Operation, strings.TrimSpace(e.Stderr))
}
func (b *Backend) cliEnv() map[string]string {
	env := map[string]string{"LC_ALL": "C"}
	for _, k := range []string{"PATH", "HOME", "USER", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range b.cfg.CLIEnv {
		env[k] = v
	}
	return env
}
func (b *Backend) command(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, b.cfg.CLI, append([]string{"--context", b.cfg.Context}, args...)...)
	c.Env = sandbox.Environment(b.cliEnv())
	return c
}
func (b *Backend) call(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	c := b.command(ctx, args...)
	c.Stderr = &stderr
	raw, e := procexec.Output(c)
	if e != nil {
		// Retain the original diagnostic (including casing and newlines),
		// subject only to credential redaction. Normalize only its display.
		detail := stderr.String()
		if strings.TrimSpace(detail) == "" {
			detail = e.Error()
		}
		// Full argv may contain project data; errors retain operation only.
		detail = redact.From(ctx).Text(detail)
		cause := e
		if ctx.Err() != nil && (errors.Is(e, ctx.Err()) || c.ProcessState != nil && c.ProcessState.ExitCode() == -1) {
			cause = ctx.Err() // CommandContext terminated this command, not an isolation violation.
		}
		return nil, &CommandError{Operation: args[0], Stderr: detail, cause: cause}
	}
	return raw, nil
}
func (b *Backend) decode(ctx context.Context, out any, args ...string) error {
	raw, e := b.call(ctx, args...)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(raw, out); e != nil {
		return fmt.Errorf("docker %s invalid JSON: %w", args[0], e)
	}
	return nil
}
func (b *Backend) list(ctx context.Context, kind, ep string) ([]string, error) {
	var args []string
	switch kind {
	case "container":
		args = []string{"ps", "-a", "--no-trunc"}
	case "network":
		args = []string{"network", "ls", "--no-trunc"}
	case "volume":
		args = []string{"volume", "ls"}
	default:
		return nil, fmt.Errorf("invalid resource kind")
	}
	args = append(args, "--filter", "label=memeval.episode="+ep, "-q")
	raw, e := b.call(ctx, args...)
	return strings.Fields(string(raw)), e
}

// sameID accepts Docker's full and abbreviated resource IDs. Names (notably
// volume names) must still be compared exactly, never as ID prefixes.
func sameID(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || len(a) >= 12 && len(b) >= 12 && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a))
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if sameID(candidate, id) {
			return true
		}
	}
	return false
}

func uniqueIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !containsID(out, id) {
			out = append(out, id)
		}
	}
	return out
}

type inspection struct {
	ID     string `json:"Id"`
	Name   string
	Config struct {
		Env     []string
		Labels  map[string]string
		Volumes map[string]any
		Image   string
		Cmd     []string
	}
	State struct {
		Running, OOMKilled bool
		ExitCode           int
		StartedAt          string
	}
	HostConfig struct {
		ReadonlyRootfs       bool
		NetworkMode          string
		PortBindings         map[string]any
		CapDrop, SecurityOpt []string
	}
	NetworkSettings struct {
		Networks map[string]any
		Ports    map[string]any
	}
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
}

func (b *Backend) inspectContainer(ctx context.Context, name string) (inspection, error) {
	var a []inspection
	e := b.decode(ctx, &a, "container", "inspect", name)
	if e != nil {
		return inspection{}, e
	}
	if len(a) != 1 {
		return inspection{}, fmt.Errorf("docker container inspect requires one container")
	}
	return a[0], nil
}
