package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/adapter"
	"memeval/internal/adapter/octop"
	"memeval/internal/config/dotenv"
	"memeval/internal/config/redact"
	"memeval/internal/episodelog"
	"memeval/internal/procexec"
	"memeval/internal/report"
	"memeval/internal/result"
	"memeval/internal/runner"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sandbox/container/governor"
	"memeval/internal/sandbox/none"
	"memeval/internal/sandbox/selftest"
	"memeval/internal/sidecar"
	"memeval/internal/suite"
	"memeval/internal/sut"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type isolationExit struct{ reason string }
type governorExit struct{ reason, dest string }

func (e *governorExit) Error() string {
	if e.reason == "interrupted" {
		return fmt.Sprintf("run interrupted; completed results saved; resume with memeval run --resume %s (and the original --sut)", e.dest)
	}
	return fmt.Sprintf("SUT governor %s; completed results saved; resume with memeval run --resume %s (and the original --sut)", e.reason, e.dest)
}

func loadSUT(ctx context.Context, path string) (*sut.Manifest, error) {
	envFile := ""
	if cfg := dotenv.From(ctx); cfg != nil {
		envFile = cfg.Path
	}
	m, err := sut.LoadWithEnv(path, envFile)
	if err != nil {
		return nil, err
	}
	m.Gateway, err = sut.ReadLLMGateway(ctx)
	return m, err
}

func (e *isolationExit) Error() string { return e.reason }
func backend(m *sut.Manifest) (sandbox.Sandbox, error) {
	if m.Sandbox.Backend == "none" {
		return none.Backend{}, nil
	}
	if m.Sandbox.Backend == "container" {
		return container.New(container.DefaultConfig()), nil
	}
	return nil, fmt.Errorf("unknown sandbox backend %q", m.Sandbox.Backend)
}
func newSandboxCommand(ctx context.Context, out io.Writer, group, name string) *cobra.Command {
	cmd := &cobra.Command{Use: name}
	f := cmd.Flags()
	path := f.String("sut", "", "SUT manifest JSON")
	f.StringVar(path, "manifest", "", "alias for --sut")
	home, _ := os.UserHomeDir()
	cache := f.String("cache-dir", filepath.Join(home, ".cache", "memeval"), "container environment cache")
	dest := f.String("out", "reports", "selftest report directory")
	network := f.String("network", "", "override none/gateway/open for an explicit variant")
	force := f.Bool("force", false, "replace installed environment")
	offline := f.Bool("offline", false, "require existing dependency lock and --pull=false; wheel downloads may still use network")
	links := f.String("find-links", "", "local wheel directory (no index)")
	cmd.RunE = func(command *cobra.Command, args []string) (retErr error) {

		if f.NArg() != 0 {
			return fmt.Errorf("unexpected positional arguments")
		}
		if group == "sandbox" && name == "caps" {
			var b sandbox.Sandbox = container.New(container.DefaultConfig())
			if *path != "" {
				m, e := loadSUT(ctx, *path)
				if e != nil {
					return e
				}
				b, e = backend(m)
				if e != nil {
					return e
				}
			}
			caps, e := b.Capabilities(ctx)
			if e != nil {
				return e
			}
			return emit(out, map[string]any{"backend": b.Name(), "caps": caps})
		}
		if *path == "" {
			return fmt.Errorf("--sut manifest is required")
		}
		m, e := loadSUT(ctx, *path)
		if e != nil {
			return e
		}
		if s := dotenv.From(ctx); s != nil {
			m.DefaultEnvFile = s.Path
		}
		if *network != "" {
			if !sandbox.NetworkMode(*network).Valid() {
				return fmt.Errorf("network must be none, gateway or open")
			}
			m.Sandbox.Network = sandbox.NetworkMode(*network)
		}
		m.CacheDir = *cache
		if group == "sut" && name == "setup" {
			l, e := installEnvironment(ctx, m, sut.InstallOptions{Cache: *cache, Force: *force, Offline: *offline, FindLinks: *links, Warn: func(s string) { fmt.Fprintln(command.ErrOrStderr(), "warning: "+s) }})
			if e != nil {
				return e
			}
			return emit(out, map[string]any{"lock": filepath.Join(l.EnvDir, "env.lock.json"), "runtime": l})
		}
		b, e := backend(m)
		if e != nil {
			return e
		}
		caps, e := b.Capabilities(ctx)
		if e != nil {
			return e
		}
		if caps.Reason != "" {
			return &sandbox.Unenforceable{Reason: caps.Reason}
		}
		l, e := sut.CheckLock(m)
		if e != nil {
			return e
		}
		switch group + " " + name {
		case "sut check":
			if cb, ok := b.(*container.Backend); ok {
				defer func() { retErr = errors.Join(retErr, cb.FinishRun(context.WithoutCancel(ctx))) }()
			}
			caps, e := b.Capabilities(ctx)
			if e != nil {
				return e
			}
			h, e := m.Prepare(ctx, b, l, "check", false)
			if e != nil {
				return e
			}
			defer func() { retErr = errors.Join(retErr, b.Destroy(context.Background(), h)) }()
			keys, err := m.PassthroughKeys()
			if err != nil {
				return err
			}
			return emit(out, map[string]any{"valid": true, "backend": b.Name(), "caps": caps, "profile_sha256": h.ProfileSHA256(), "effective_spec": h.EffectiveConfig(), "env_passthrough": keys})
		case "sandbox selftest":
			r, path, e := selftest.Run(ctx, b, m, l, *dest)
			if r != nil {
				if emitErr := emit(out, map[string]any{"report": path, "status": r.Status, "passed": r.Passed}); emitErr != nil {
					return emitErr
				}
			}
			return e
		default:
			return fmt.Errorf("unknown subcommand")
		}

	}
	return cmd
}

func runExternal(ctx context.Context, s *suite.Suite, path, dest, network, cache string, o runner.Options, keep, allow bool, logOptions episodelog.Options, out, errOut io.Writer) (err error) {
	m, e := loadSUT(ctx, path)
	if e != nil {
		return e
	}
	if cfg := dotenv.From(ctx); cfg != nil {
		m.DefaultEnvFile = cfg.Path
	}
	if network != "" {
		if !sandbox.NetworkMode(network).Valid() {
			return fmt.Errorf("invalid network mode")
		}
		m.Sandbox.Network = sandbox.NetworkMode(network)
	}
	if o.Resume != nil && !o.Resume.AllowSUTChange && sandbox.Hash(m) != o.Resume.Original.SUT.ManifestSHA256 {
		return fmt.Errorf("resume SUT manifest mismatch")
	}
	m.CacheDir = cache
	b, e := backend(m)
	if e != nil {
		return e
	}
	caps, e := b.Capabilities(ctx)
	if e != nil {
		return e
	}
	if caps.Reason != "" {
		return &sandbox.Unenforceable{Reason: caps.Reason}
	}
	l, e := sut.CheckLock(m)
	if e != nil {
		return e
	}
	iso := result.Isolation{EgressAllowlist: container.EgressAllowlist(m.Sandbox.Network, m.Gateway.BaseURL), FDHygiene: procexec.Policy, Tier: "C", Backend: b.Name(), NetworkMode: string(m.Sandbox.Network), Host: result.Host{OS: caps.Host.OS, Arch: caps.Host.Arch, Kernel: caps.Host.Kernel}, Caps: caps}
	if b.Name() == "container/docker" {
		iso.Tier = "D"
		iso.HostExecutables = []string{}
		iso.Signal = "container-pid-namespace"
		iso.ProcessIPC = map[string]any{"scope": "container-private-ipc-namespace", "shm_size": container.DefaultConfig().DefaultShmSize}
	}
	signature, e := selftest.Signature(m, l, caps)
	if e != nil {
		return e
	}
	evidence, selfErr := selftest.Recent(selftestDir(l), signature, time.Now())
	restricted := m.Sandbox.Network == sandbox.NetworkNone || m.Sandbox.Network == sandbox.NetworkGateway
	if selfErr != nil && caps.FileReadRestrict && caps.FileWriteRestrict && (!restricted || caps.NetworkNone) {
		evidence, selfErr = ensureSelftest(ctx, m, l, filepath.Join(dest, "selftest"))
	}
	enforced := caps.FileReadRestrict && caps.FileWriteRestrict && (!restricted || caps.NetworkNone) && selfErr == nil
	reason := ""
	if !caps.FileReadRestrict || !caps.FileWriteRestrict || restricted && !caps.NetworkNone {
		reason = "required isolation capability is unavailable"
	}
	if selfErr != nil {
		if reason != "" {
			reason += "; "
		}
		reason += selfErr.Error()
	}
	var governorWarnings []string
	var sutBilling *governor.Billing
	reject := func(reason string) error {
		iso.Reason = reason
		payload := map[string]any{"isolation": iso, "reason": reason}
		if len(governorWarnings) > 0 {
			payload["warnings"] = governorWarnings
			payload["billing"] = map[string]any{"sut": sutBilling}
		}
		if e := report.WriteJSON(context.WithoutCancel(ctx), filepath.Join(dest, "report.json"), payload); e != nil {
			return e
		}
		return &isolationExit{reason}
	}
	if !enforced && (!allow || b.Name() != "none") {
		return reject(reason)
	}
	iso.Reason = reason
	iso.Selftest = evidence
	iso.FileReadEnforced = enforced && caps.FileReadRestrict
	iso.FileWriteEnforced = enforced && caps.FileWriteRestrict
	iso.DenyReadEnforced = enforced && caps.DenyRead
	iso.ProcessTreeEnforced = enforced && caps.ProcessTree
	iso.NetworkEnforced = enforced && restricted
	logOptions.Secrets = m.Secrets()
	logs, e := episodelog.New(ctx, dest, logOptions)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, logs.Close()) }()
	a := &sut.Adapter{Manifest: m, Lock: l, Backend: b, Keep: keep, Logs: logs}
	workers := []*sut.Adapter{a}
	// The scheduler creates workers before dispatch; each owns its RPC state,
	// while the backend and log index are explicitly shared and synchronized.
	o.NewAdapter = func(context.Context) (adapter.Adapter, error) {
		worker := &sut.Adapter{Manifest: m, Lock: l, Backend: b, Keep: keep, Logs: logs}
		workers = append(workers, worker)
		if m.Adapter.Kind == "native" {
			return octop.New(worker), nil
		}
		return worker, nil
	}
	o.RecordNamespace = true
	var pollStop func()
	var pollError func() error
	var governorConcurrency int
	if cb, ok := b.(*container.Backend); ok && m.Sandbox.Network == sandbox.NetworkGateway {
		config, err := container.GovernorConfig(ctx, m.LLM.Concurrency, o.Parallel)
		if err != nil {
			return err
		}
		if o.Resume != nil {
			var previous governor.Billing
			raw, _ := json.Marshal(o.Resume.Original.Billing.SUT)
			if err = json.Unmarshal(raw, &previous); err != nil {
				return err
			}
			config.ConsumedTokens = previous.Prompt + previous.Completion
		}
		if err = cb.ConfigureGovernor(dest, config); err != nil {
			return err
		}
		governorConcurrency = config.Concurrency
		pollStop, pollError, o.StopScheduling = watchGovernor(ctx, cb)
		defer pollStop()
	}
	var tested adapter.Adapter = a
	if m.Adapter.Kind == "native" {
		tested = octop.New(a)
	}
	r, e := runner.Run(ctx, s, tested, o)
	if pollStop != nil {
		pollStop()
	}
	e = errors.Join(e, logs.Close())
	unenforceable := false
	var serviceEvidence []*sidecar.Evidence
	var episodeConfigs []map[string]any
	for _, worker := range workers {
		serviceEvidence = append(serviceEvidence, worker.ServiceEvidence...)
		episodeConfigs = append(episodeConfigs, worker.Episodes...)
		unenforceable = unenforceable || worker.Unenforceable
	}
	iso.Sidecars, iso.EffectiveConfig = serviceEvidence, episodeConfigs
	var cleanupErr error
	if cb, ok := b.(*container.Backend); ok {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		cleanupErr = cb.FinishRun(cleanup)
		if pollError != nil {
			cleanupErr = errors.Join(cleanupErr, pollError())
		}
		iso.Container = cb.Report(cleanup)
	}
	if cb, ok := b.(*container.Backend); ok && m.Sandbox.Network == sandbox.NetworkGateway {
		billing, err := cb.GovernorBilling()
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else {
			sutBilling = &billing
			governorWarnings = governorDisconnectWarnings(billing, governorConcurrency, errOut)
			if r != nil {
				r.Warnings = append(r.Warnings, governorWarnings...)
				r.Billing.SUT = billing
				r.Summary.Billing = r.Billing
				if reason := cb.GovernorStop(); reason != "" && r.Interruption == nil {
					r.Interruption = &result.Interruption{Reason: reason}
				}
			}
		}
	}
	if unenforceable || cleanupErr != nil {
		iso.NetworkEnforced = false
		return reject(isolationFailureReason(ctx, serviceEvidence, cleanupErr, m.Secrets()))
	}
	if e != nil {
		return e
	}
	hash := a.ProfileSHA256
	iso.SandboxProfileSHA256 = &hash
	r.Isolation = iso
	r.Warnings = slices.DeleteFunc(r.Warnings, func(w string) bool { return w == runner.InProcessWarning })
	logSummary := logs.Summary()
	r.Logs = &logSummary
	if o.Mode != runner.FixedReader && o.Repeats <= 1 {
		r.FrameworkVersion = "0.4.0-m2a"
	}
	if m.Adapter.Kind == "native" {
		r.Protocol.Changes = append(r.Protocol.Changes, octop.ProtocolChanges()...)
		r.Protocol.ChangesFromV1 = append(r.Protocol.ChangesFromV1, r.Protocol.Changes...)
	}
	r.Summary.UnenforcedIsolation = !enforced
	for category, summary := range r.ByCategory {
		summary.UnenforcedIsolation = !enforced
		r.ByCategory[category] = summary
	}
	r.SUT.Name = m.Name
	r.SUT.Variant = m.Variant
	r.SUT.ManifestSHA256 = sandbox.Hash(m)
	if m.Bridge != nil {
		if snapshot, ok := m.Bridge.Config["source_snapshot_sha256"].(string); ok && snapshot != "" {
			r.SUT.SourceSnapshotSHA256 = new(jsonrpc.Redact(snapshot, m.Secrets()))
		}
	}
	r.SUT.Lock = l
	r.SUT.EnvLockSHA256, e = sut.FileHash(filepath.Join(l.EnvDir, "env.lock.json"))
	if e != nil {
		return e
	}
	if m.Declared != nil {
		r.SUT.Declared = safeValue(m.Declared, m.Secrets()).(map[string]any)
	}
	for k, v := range m.Declared {
		r.SUT.Fingerprint[k] = result.FingerprintValue{Status: "declared", Value: safeValue(v, m.Secrets())}
	}
	if r.SUT.Describe.Config != nil {
		r.SUT.Describe.Config = safeValue(r.SUT.Describe.Config, m.Secrets()).(map[string]any)
	}
	// Copy adapter-supplied SDK facts only after credential redaction.
	r.SUT.LLM = m.LLMDisclosure(r.SUT.Describe.Config)
	r.SUT.Describe.Name = jsonrpc.Redact(r.SUT.Describe.Name, m.Secrets())
	r.SUT.Describe.Version = jsonrpc.Redact(r.SUT.Describe.Version, m.Secrets())
	for i, v := range r.SUT.Describe.Capabilities {
		r.SUT.Describe.Capabilities[i] = jsonrpc.Redact(v, m.Secrets())
	}
	for i, v := range r.SUT.Describe.Limitations {
		r.SUT.Describe.Limitations[i] = jsonrpc.Redact(v, m.Secrets())
	}
	switch m.Sandbox.Network {
	case sandbox.NetworkGateway:
		r.Warnings = append(r.Warnings, "SUT gateway usage is metered per upstream attempt; missing usage is unmetered; token budget uses observed prompt+completion and may be exceeded by in-flight calls; evaluator traffic is separate")
	case sandbox.NetworkOpen:
		r.Warnings = append(r.Warnings, "SUT provider calls are direct and unmetered; declared fingerprints are not independently verified")
	case sandbox.NetworkNone:
		r.Warnings = append(r.Warnings, "SUT network mode is none; evaluator traffic is separate")
	}
	if b.Name() == "none" {
		r.Warnings = append(r.Warnings, "The none backend is an unenforced local fixture; environment files are checked before run")
	}
	if !enforced {
		r.Warnings = append(r.Warnings, "UNENFORCED ISOLATION: explicit --allow-unenforced; results must not be described as isolated")
	}
	if strings.Contains(m.Name, "echo") {
		r.Warnings = append(r.Warnings, "Echo is a transport fixture; its scores are not memory-quality evidence")
	}
	r.DataFlow = buildDataFlow(ctx, m, l, r)
	publication, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if e = report.Save(publication, r, dest); e != nil {
		return e
	}
	if e = emit(out, map[string]any{"report": filepath.Join(dest, "report.json"), "summary": r.Summary}); e != nil {
		return e
	}
	if r.Interruption != nil {
		return &governorExit{reason: r.Interruption.Reason, dest: dest}
	}
	if r.Summary.ExecutionErrors > 0 {
		return fmt.Errorf("run completed with %d execution errors", r.Summary.ExecutionErrors)
	}
	return nil
}
func safeValue(v any, secrets []string) any {
	switch x := v.(type) {
	case string:
		return jsonrpc.Redact(x, secrets)
	case map[string]any:
		r := map[string]any{}
		for k, v := range x {
			lower := strings.ToLower(k)
			if !strings.HasSuffix(lower, "_env") && (strings.Contains(lower, "api_key") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || lower == "token") {
				r[k] = "[REDACTED]"
				continue
			}
			r[jsonrpc.Redact(k, secrets)] = safeValue(v, secrets)
		}
		return r
	case []any:
		r := make([]any, len(x))
		for i, v := range x {
			r[i] = safeValue(v, secrets)
		}
		return r
	case nil:
		return nil
	}
	return v
}

// Internal child dispatch precedes signal handlers and all evaluator setup.
func internalCommand(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "__container-governor":
		if e := governor.Command(context.Background(), args[1:], os.Stderr); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return true, 2
		}
		return true, 0
	case "__container-governor-status":
		if e := governor.StatusCommand(context.Background(), args[1:], os.Stdout); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return true, 2
		}
		return true, 0
	case "__container-ready":
		if e := container.ReadyCommand(args[1:], os.Stderr); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return true, 2
		}
		return true, 0
	case "__container-probe":
		if e := container.Probe(args[1:], os.Stdout); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return true, 2
		}
		return true, 0
	case "__probe":
		if e := selftest.Probe(args[1:], os.Stdout); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return true, 2
		}
		return true, 0
	}
	return false, 0
}
func exitCode(e error) int {
	var paused *governorExit
	if errors.As(e, &paused) {
		return 4
	}
	var missing *sut.NotInstalled
	if errors.As(e, &missing) {
		return 3
	}
	var denied *isolationExit
	if errors.As(e, &denied) {
		return 3
	}
	return 2
}

func governorDisconnectWarnings(b governor.Billing, concurrency int, stderr io.Writer) []string {
	if b.ClientDisconnects == 0 {
		return nil
	}
	warning := fmt.Sprintf("governor: %d SUT connections closed while queued/retrying; raise --sut-llm-concurrency (now %d) or the SUT's HTTP timeout", b.ClientDisconnects, concurrency)
	fmt.Fprintln(stderr, warning)
	return []string{warning}
}

func isolationFailureReason(ctx context.Context, evidence []*sidecar.Evidence, cleanup error, secrets []string) string {
	reason := "backend or sidecar isolation audit failed; no scored results published"
	firstLine := func(text string) string {
		text = redact.From(ctx).Text(jsonrpc.Redact(redact.StripANSI(text), secrets))
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return line
			}
		}
		return ""
	}
	detail := ""
	for _, ev := range evidence {
		if ev != nil {
			if line := firstLine(ev.Error); line != "" {
				detail = firstLine(ev.Name) + ": " + line
				break
			}
		}
	}
	if detail == "" && cleanup != nil {
		detail = firstLine(cleanup.Error())
	}
	if detail != "" {
		r := []rune(detail)
		if len(r) > 200 {
			detail = string(r[:197]) + "..."
		}
		reason += ": " + detail
	}
	return reason
}
