package container

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container/governor"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type runGovernor struct {
	episode                    *episode
	dir, target, statusAddress string
	temporary                  bool
	connected                  map[string]bool
	status                     governor.Status
	bindings                   []ipBinding
}
type ipBinding = governor.Attribution

// GovernorConfig resolves explicit flag/env/file settings before the manifest's
// per-episode fan-out scaled by parallelism. CLI defaults are exported to the
// process environment, so their presence alone is not an explicit override.
func GovernorConfig(ctx context.Context, declared *int, parallel int) (governor.Config, error) {
	c := governor.Defaults()
	if parallel < 1 || parallel > 16 {
		return c, fmt.Errorf("parallel must be between 1 and 16")
	}
	c.Parallel = parallel
	if declared != nil && *declared < 1 {
		return c, fmt.Errorf("llm.concurrency must be >= 1")
	}
	if declared != nil {
		c.ConcurrencyPerEpisode = *declared
	}
	for key, p := range map[string]*int{"MEMEVAL_SUT_LLM_CONCURRENCY": &c.Concurrency, "MEMEVAL_SUT_LLM_RPM": &c.RPM} {
		if v := dotenv.Value(key); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil {
				return c, fmt.Errorf("invalid %s", key)
			}
			*p = n
			if key == "MEMEVAL_SUT_LLM_CONCURRENCY" {
				c.ConcurrencySource = "env"
			}
		}
	}
	if s := dotenv.From(ctx); s != nil {
		switch s.Sources["MEMEVAL_SUT_LLM_CONCURRENCY"] {
		case "default", "flag":
			c.ConcurrencySource = s.Sources["MEMEVAL_SUT_LLM_CONCURRENCY"]
		case "env", ".env":
			c.ConcurrencySource = "env"
		}
	}
	if c.ConcurrencySource == "default" {
		if c.ConcurrencyPerEpisode > int(^uint(0)>>1)/parallel {
			return c, fmt.Errorf("governor concurrency overflows int: per-episode fan-out times parallel")
		}
		c.Concurrency = c.ConcurrencyPerEpisode * parallel
		if declared != nil {
			c.ConcurrencySource = "manifest"
		}
	}
	if v := dotenv.Value("MEMEVAL_SUT_LLM_BUDGET_TOKENS"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil {
			return c, fmt.Errorf("invalid MEMEVAL_SUT_LLM_BUDGET_TOKENS")
		}
		c.BudgetTokens = n
	}
	if v := dotenv.Value("MEMEVAL_SUT_LLM_MAX_PAUSE"); v != "" {
		d, e := time.ParseDuration(v)
		if e != nil {
			return c, fmt.Errorf("invalid MEMEVAL_SUT_LLM_MAX_PAUSE")
		}
		c.MaxPause = d
	}
	if c.Concurrency < 1 || c.RPM < 0 || c.BudgetTokens < 0 || c.ConsumedTokens < 0 || c.MaxPause <= 0 {
		return c, fmt.Errorf("invalid SUT governor limits")
	}
	return c, nil
}
func (b *Backend) ConfigureGovernor(out string, c governor.Config) error {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov != nil {
		return fmt.Errorf("governor already started")
	}
	if c.Concurrency < 1 || c.RPM < 0 || c.BudgetTokens < 0 || c.ConsumedTokens < 0 || c.MaxPause <= 0 || c.UpstreamTimeout <= 0 {
		return fmt.Errorf("invalid SUT governor limits")
	}
	b.govConfig = c
	b.meterDir = filepath.Join(out, "governor")
	return nil
}
func (b *Backend) prepareGovernor(ctx context.Context, root *episode, target string) error {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if b.gov == nil {
		c := b.govConfig
		if c.Concurrency == 0 {
			var err error
			c, err = GovernorConfig(ctx, nil, 1)
			if err != nil {
				return err
			}
			b.govConfig = c
		}
		image, err := b.ensureProbeImage(ctx)
		if err != nil {
			return err
		}
		dir := b.meterDir
		temporary := dir == ""
		if temporary {
			dir, err = os.MkdirTemp(filepath.Dir(root.handle.Dir()), "governor-")
			if err != nil {
				return err
			}
		} else {
			if err = os.MkdirAll(dir, 0700); err != nil {
				return err
			}
		}
		dir, err = filepath.Abs(dir)
		if err != nil {
			return err
		}
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if err = os.Chmod(dir, 0700); err != nil {
			return err
		}
		_, port, _ := net.SplitHostPort(target)
		statusPort := "8318"
		if port == statusPort {
			statusPort = "8319"
		}
		// The probe image bakes memeval.episode/env labels; containers inherit
		// image labels, so the governor must set its own instead of omitting them.
		id := nonce()
		labels := b.labels("governor-"+id, "", "governor")
		gov := &episode{id: "governor-" + id, name: "memeval-" + id + "-governor", network: "bridge", role: "governor", volumes: map[string]bool{}}
		gov.root = gov
		gov.config = runConfig{Name: gov.name, Network: "bridge", Image: image, User: b.user(), NoBind: true, MeterDir: dir, Cwd: "/tmp", Labels: labels, Limits: sandbox.Limits{PidsMax: 64}, Command: []string{"/memeval", "__container-governor", "--listen", "0.0.0.0:" + port, "--target", target, "--concurrency", strconv.Itoa(c.Concurrency), "--rpm", strconv.Itoa(c.RPM), "--budget-tokens", strconv.FormatInt(c.BudgetTokens, 10), "--consumed-tokens", strconv.FormatInt(c.ConsumedTokens, 10), "--upstream-timeout", c.UpstreamTimeout.String(), "--max-pause", c.MaxPause.String(), "--meter-dir", "/meter", "--status-listen", "127.0.0.1:" + statusPort}}
		b.gov = &runGovernor{episode: gov, dir: dir, temporary: temporary, target: target, statusAddress: "127.0.0.1:" + statusPort, connected: map[string]bool{}}
		if err = b.createAux(ctx, gov); err != nil {
			return err
		}
		if _, err = b.call(ctx, "start", gov.name); err != nil {
			return err
		}
		// Readiness is checked in the governor's namespace, with no published port.
		for {
			if _, err = b.pollGovernorLocked(ctx); err == nil {
				break
			}
			if ctx.Err() != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return err
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	g := b.gov
	if g.target != target {
		return fmt.Errorf("a run governor cannot change upstream target")
	}
	if !g.connected[root.network] {
		if _, err := b.call(ctx, "network", "connect", "--alias", "llm-gateway", root.network, g.episode.name); err != nil {
			return err
		}
		g.connected[root.network] = true
	}
	root.governor = g.episode
	root.config.RelayTarget = target
	root.config.RelayImage = g.episode.config.Image
	root.handle.Extra["relay_name"] = g.episode.name
	root.handle.Extra["governor_name"] = g.episode.name
	return b.auditGovernor(ctx)
}
func (b *Backend) disconnectGovernor(ctx context.Context, ep *episode) error {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov == nil || !b.gov.connected[ep.network] {
		return nil
	}
	if _, e := b.call(ctx, "network", "disconnect", ep.network, b.gov.episode.name); e != nil {
		return e
	}
	delete(b.gov.connected, ep.network)
	now := time.Now()
	for i := range b.gov.bindings {
		v := &b.gov.bindings[i]
		if (v.Container == ep.name || strings.HasPrefix(v.Container, "memeval-"+ep.id+"-")) && v.Until.IsZero() {
			v.Until = now
		}
	}
	return nil
}
func (b *Backend) pollGovernorLocked(ctx context.Context) (governor.Status, error) {
	g := b.gov
	if g == nil {
		return governor.Status{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var s governor.Status
	e := b.decode(ctx, &s, "exec", g.episode.name, "/memeval", "__container-governor-status", "--address", g.statusAddress)
	if e == nil {
		g.status = s
	}
	return s, e
}
func (b *Backend) PollGovernor(ctx context.Context) (governor.Status, error) {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	return b.pollGovernorLocked(ctx)
}
func (b *Backend) GovernorStop() string {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov != nil && b.gov.status.Stopped() {
		return b.gov.status.State
	}
	return ""
}
func (b *Backend) finishGovernor(ctx context.Context) error {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov == nil {
		return nil
	}
	g := b.gov
	if g.episode.removed {
		return nil
	}
	var errs []error
	_, e := b.pollGovernorLocked(ctx)
	errs = append(errs, e)
	// Graceful TERM flushes the last attempt before the ledger is hashed.
	if _, e = b.call(ctx, "kill", "--signal", "TERM", g.episode.name); e != nil && !notFound(e) {
		errs = append(errs, e)
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, e = b.call(wait, "wait", g.episode.name)
	timedOut := wait.Err() != nil
	cancel()
	if e != nil && !timedOut && !notFound(e) {
		errs = append(errs, e)
	}
	// The graceful command publishes final counters after draining requests.
	if raw, err := os.ReadFile(filepath.Join(g.dir, "status.json")); err == nil {
		var final governor.Status
		if err = json.Unmarshal(raw, &final); err != nil {
			errs = append(errs, err)
		} else {
			g.status = final
		}
	}
	errs = append(errs, b.removeContainer(ctx, g.episode))
	for _, v := range sortedVolumes(g.episode.volumes) {
		if _, e = b.call(ctx, "volume", "rm", v); e != nil && !notFound(e) {
			errs = append(errs, e)
		}
		if _, e = b.call(ctx, "volume", "inspect", v); !notFound(e) {
			errs = append(errs, fmt.Errorf("governor volume remains: %s", v))
		}
	}
	if _, e = b.inspectContainer(ctx, g.episode.name); !notFound(e) {
		errs = append(errs, fmt.Errorf("governor container remains: %s", g.episode.name))
	}
	return errors.Join(errs...)
}
func (b *Backend) BindNamespace(h sandbox.Handle, ns string) {
	ep, err := b.episode(h)
	if err == nil {
		ep.namespace = ns
	}
}
func (b *Backend) recordIPs(e *episode, v inspection) {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov == nil || e.root.governor == nil {
		return
	}
	for _, raw := range v.NetworkSettings.Networks {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range []string{"IPAddress", "GlobalIPv6Address"} {
			ip, _ := obj[k].(string)
			if ip != "" {
				b.gov.bindings = append(b.gov.bindings, ipBinding{IP: ip, NS: e.root.namespace, Container: e.name, From: e.createdAt})
			}
		}
	}
}
func (b *Backend) GovernorBilling() (governor.Billing, error) {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov == nil {
		return governor.Billing{}, nil
	}
	f, e := os.Open(filepath.Join(b.gov.dir, governor.MeterFile))
	if e != nil {
		return governor.Billing{}, e
	}
	defer f.Close()
	rows, e := governor.ReadAttempts(f)
	if e != nil {
		return governor.Billing{}, e
	}
	return governor.AggregateAttributed(governor.Attribute(rows, b.gov.bindings), b.gov.status.Billing.CircuitOpens), nil
}
func (b *Backend) governorDisclosure() map[string]any {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov == nil {
		return nil
	}
	g := b.gov
	c := b.govConfig
	raw, e := os.ReadFile(filepath.Join(g.dir, governor.MeterFile))
	sha := ""
	if e == nil {
		sha = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	return map[string]any{"container": g.episode.name, "image": g.episode.config.Image, "listen": g.episode.config.Command[3], "target": g.target, "concurrency": c.Concurrency, "concurrency_source": c.ConcurrencySource, "concurrency_per_episode": c.ConcurrencyPerEpisode, "parallel": c.Parallel, "rpm": c.RPM, "budget_tokens": c.BudgetTokens, "previously_consumed_tokens": c.ConsumedTokens, "upstream_timeout_s": c.UpstreamTimeout.Seconds(), "max_pause_s": c.MaxPause.Seconds(), "circuit_events": g.status.Events, "state": g.status.State, "retry_after_s": g.status.RetryAfter, "meter_file_sha256": sha, "meter_file": "governor/" + governor.MeterFile}
}
func (b *Backend) saveGovernorBindings() error {
	b.govMu.Lock()
	defer b.govMu.Unlock()
	if b.gov == nil {
		return nil
	}
	raw, e := json.MarshalIndent(b.gov.bindings, "", "  ")
	if e != nil {
		return e
	}
	return privateWrite(filepath.Join(b.gov.dir, "episode-ips.json"), raw)
}

func (b *Backend) auditGovernor(ctx context.Context) error {
	g := b.gov
	v, e := b.inspectContainer(ctx, g.episode.name)
	if e != nil {
		return e
	}
	if !sameID(v.ID, g.episode.containerID) || v.Config.Labels["memeval.run"] != g.episode.config.Labels["memeval.run"] || !v.State.Running || len(v.HostConfig.PortBindings) > 0 {
		return fmt.Errorf("governor ownership/running/ports audit failed")
	}
	if len(v.NetworkSettings.Networks) != len(g.connected)+1 {
		return fmt.Errorf("unexpected governor network")
	}
	for n := range v.NetworkSettings.Networks {
		if n != "bridge" && !g.connected[n] {
			return fmt.Errorf("unexpected governor network")
		}
	}
	count := 0
	for i, a := range v.Config.Cmd {
		if a == "--target" {
			count++
			if i+1 >= len(v.Config.Cmd) || v.Config.Cmd[i+1] != g.target {
				return fmt.Errorf("governor target mismatch")
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("governor requires exactly one target")
	}
	return nil
}
