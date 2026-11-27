// Package sidecar orchestrates trusted env services inside an episode policy.
package sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"memeval/internal/config/redact"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceBackend performs readiness and ownership checks inside the runtime.
type ServiceBackend interface {
	PrepareServiceCommand(context.Context, sandbox.Handle, sandbox.Service, []string, map[string]string, string) (sandbox.Handle, error)
	WaitService(context.Context, sandbox.Service, sandbox.Process) error
	AuditService(context.Context, int) error
	ExecService(context.Context, sandbox.Process, []string, io.Writer) error
	ServiceFacts(context.Context, sandbox.Process) (sandbox.ServiceEvidence, error)
	ServiceStopped(context.Context, sandbox.Process) error
}

type Evidence struct {
	sandbox.ServiceEvidence
	Managed      bool     `json:"managed"`
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	Port         uint16   `json:"port,omitempty"`
	PID          int      `json:"pid"`
	Ready        bool     `json:"ready"`
	LoopbackOnly bool     `json:"loopback_only"`
	Addresses    []string `json:"addresses,omitempty"`
	Audit        string   `json:"audit,omitempty"`
	Policy       string   `json:"policy"`
	Destroyed    bool     `json:"destroyed"`
	Error        string   `json:"error,omitempty"`
}
type Failure struct{ Reason string }

func (e *Failure) Error() string { return "sidecar isolation: " + e.Reason }

type tail struct {
	mu   sync.Mutex
	data []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	t.data = append(t.data, p...)
	if len(t.data) > 16384 {
		t.data = t.data[len(t.data)-16384:]
	}
	return n, nil
}
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return redact.StripANSI(string(t.data))
}

type Instance struct {
	Spec     sandbox.Service
	Process  sandbox.Process
	Evidence *Evidence
	tail     tail
	drained  chan struct{}
}

// Keep the first trigger even when teardown also fails.
func (i *Instance) failure(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err // Startup was interrupted; the independent cleanup still has to pass.
	}
	text := redact.From(ctx).Text(redact.StripANSI(err.Error()))
	if i.Evidence.Error == "" {
		i.Evidence.Error = text
	}
	return &Failure{Reason: text}
}

type Group struct {
	Items   []*Instance
	Backend sandbox.Sandbox
	Handle  sandbox.Handle
}

// Sinks supplies episode-owned, concurrent-safe writers. A nil sink keeps the
// existing bounded diagnostic rings without creating persistent files.
type Sinks interface {
	Sidecar(name string) io.Writer
	Init(name string, index int) io.Writer
}

func output(tail io.Writer, sink io.Writer) io.Writer {
	if sink == nil {
		return tail
	}
	return io.MultiWriter(tail, sink)
}

func Start(ctx context.Context, b sandbox.Sandbox, h sandbox.Handle, sinks Sinks) (*Group, error) {
	p, ok := h.(*sandbox.Prepared)
	if !ok {
		return nil, fmt.Errorf("sidecar handle unsupported")
	}
	g := &Group{Backend: b, Handle: h}
	if len(p.Spec.Services) == 0 {
		return g, nil
	}
	commands, ok := b.(sandbox.CommandBackend)
	if !ok {
		return g, fmt.Errorf("backend cannot start sidecars")
	}
	if err := os.MkdirAll(filepath.Join(h.Dir(), "sock"), 0700); err != nil {
		return g, err
	}
	for _, s := range p.Spec.Services {
		if _, ok := b.(ServiceBackend); ok && s.Unmanaged {
			return g, fmt.Errorf("unmanaged host services are unavailable in containers")
		}
		item := &Instance{Spec: s, Evidence: &Evidence{Name: s.Name, Managed: !s.Unmanaged, Transport: s.Transport, Port: s.Port, LoopbackOnly: s.Transport == "unix", Policy: "EP Unix socket"}}
		if s.Transport == "tcp" {
			item.Evidence.Policy = "port enforced by kernel; address isolation checked by listener audit, not kernel"
		}
		g.Items = append(g.Items, item)
		if s.Unmanaged {
			continue // Its owner is a later service or the SUT; never spawn or wait here.
		}
		fail := func(err error) (*Group, error) { return g, item.failure(ctx, err) }
		if err := os.MkdirAll(filepath.Join(h.Dir(), "sidecars", s.Name), 0700); err != nil {
			return fail(err)
		}
		spawn := func(command []string, role string) (sandbox.Process, error) {
			var child sandbox.Handle
			var e error
			if cb, ok := b.(ServiceBackend); ok {
				child, e = cb.PrepareServiceCommand(ctx, h, s, command, s.Env, role)
			} else {
				child, e = commands.PrepareCommand(ctx, h, command, s.Env)
			}
			if e != nil {
				return nil, e
			}
			return b.Spawn(ctx, child)
		}
		if _, containerBackend := b.(ServiceBackend); !containerBackend {
			for i, init := range s.Init {
				var sink io.Writer
				if sinks != nil {
					sink = sinks.Init(s.Name, i+1)
				}
				writer := output(&item.tail, sink)
				child, err := spawn(init, "init")
				if err != nil {
					return fail(err)
				}
				child.Stdin().Close()
				done := make(chan struct{})
				go func() {
					var wg sync.WaitGroup
					wg.Add(2)
					go func() { defer wg.Done(); io.Copy(writer, child.Stdout()) }()
					go func() { defer wg.Done(); io.Copy(writer, child.Stderr()) }()
					wg.Wait()
					close(done)
				}()
				wait, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutMS)*time.Millisecond)
				status, e := child.Wait(wait)
				cancel()
				if e != nil {
					child.Kill(context.Background())
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					sandbox.CloseReaders(child)
					<-done
				}
				sandbox.CloseReaders(child)
				if e != nil || status.Code != 0 {
					return fail(fmt.Errorf("%s init failed: %v exit=%d: %s", s.Name, e, status.Code, item.tail.String()))
				}
			}
		}
		var sink io.Writer
		if sinks != nil {
			sink = sinks.Sidecar(s.Name)
		}
		writer := output(&item.tail, sink)
		child, err := spawn(s.Command, "sidecar")
		if err != nil {
			return fail(err)
		}
		item.Process = child
		item.Evidence.PID = child.PID()
		if _, ok := b.(ServiceBackend); ok {
			item.Evidence.PID = 0
		}
		child.Stdin().Close()
		item.drained = make(chan struct{})
		go func() {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); io.Copy(writer, child.Stdout()) }()
			go func() { defer wg.Done(); io.Copy(writer, child.Stderr()) }()
			wg.Wait()
			close(item.drained)
		}()
		deadline, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutMS)*time.Millisecond)
		if cb, ok := b.(ServiceBackend); ok {
			err = cb.WaitService(deadline, s, child)
			item.Evidence.Policy = "container network; no published ports"
		} else {
			err = waitReady(deadline, s.Ready)
		}
		deadlineErr := deadline.Err()
		cancel()
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				return fail(err)
			}
			if errors.Is(deadlineErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				return fail(fmt.Errorf("%s readiness timeout after %gs: %s", s.Name, float64(s.TimeoutMS)/1000, item.tail.String()))
			}
			return fail(fmt.Errorf("%s readiness: %w: %s", s.Name, err, item.tail.String()))
		}
		if cb, ok := b.(ServiceBackend); ok {
			item.Evidence.PID = 0
			item.Evidence.LoopbackOnly = true
			for i, init := range s.Init {
				var sink io.Writer
				if sinks != nil {
					sink = sinks.Init(s.Name, i+1)
				}
				deadline, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutMS)*time.Millisecond)
				err = cb.ExecService(deadline, child, init, output(&item.tail, sink))
				cancel()
				if err != nil {
					return fail(fmt.Errorf("%s init failed: %w: %s", s.Name, err, item.tail.String()))
				}
			}
		}
		item.Evidence.Ready = true
		if err = g.Audit(ctx); err != nil {
			return fail(err)
		}
	}
	return g, nil
}

// AwaitUnmanaged runs after SUT spawn, before its first RPC. Later audits never
// skip these grants: loss/replacement of a listener fails the run closed.
func (g *Group) AwaitUnmanaged(ctx context.Context) error {
	if g == nil {
		return nil
	}
	for _, item := range g.Items {
		if !item.Spec.Unmanaged {
			continue
		}
		deadline, cancel := context.WithTimeout(ctx, time.Duration(item.Spec.TimeoutMS)*time.Millisecond)
		err := waitReady(deadline, item.Spec.Ready)
		cancel()
		if err != nil {
			return item.failure(ctx, fmt.Errorf("%s unmanaged readiness: %w", item.Spec.Name, err))
		}
		item.Evidence.Ready = true
	}
	return g.Audit(ctx)
}

// VerifyStopped is called AFTER the enclosing episode's path cleanup, which
// also owns detached listeners that were never spawned by this Group.
func (g *Group) VerifyStopped(ctx context.Context) error {
	if g == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	for _, item := range g.Items {
		if cb, ok := g.Backend.(ServiceBackend); ok {
			if item.Process == nil {
				continue
			}
			err := cb.ServiceStopped(ctx, item.Process)
			item.Evidence.Destroyed = err == nil
			if err != nil {
				return item.failure(ctx, err)
			}
			continue
		}
		if !item.Spec.Unmanaged {
			continue
		}
		dead := true
		for _, line := range strings.Split(item.Evidence.Audit, "\n") {
			if strings.HasPrefix(line, "p") {
				pid, _ := strconv.Atoi(line[1:])
				dead = dead && !sandbox.Alive(pid)
			}
		}
		item.Evidence.Destroyed = dead && Ready(ctx, item.Spec.Ready) != nil
		if !item.Evidence.Destroyed {
			return item.failure(ctx, fmt.Errorf("%s unmanaged listener survived episode cleanup", item.Spec.Name))
		}
	}
	return nil
}
func (g *Group) Audit(ctx context.Context) error {
	if g == nil {
		return nil
	}
	// Cancellation stops work, not proof of isolation. Detach before inspection
	// (including cancellation racing an in-flight RPC), but bound the audit.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	for _, item := range g.Items {
		if !item.Evidence.Ready {
			continue
		}
		if cb, ok := g.Backend.(ServiceBackend); ok {
			facts, err := cb.ServiceFacts(ctx, item.Process)
			if err != nil {
				return item.failure(ctx, err)
			}
			if facts.Stopped {
				// Preserve the live inspection's environment keys after teardown.
				facts.EnvKeys = item.Evidence.EnvKeys
				item.Evidence.Destroyed = true
			}
			item.Evidence.ServiceEvidence = facts
			item.Evidence.Audit = "container-inspect"
			continue
		}
		if item.Process != nil && !sandbox.Alive(item.Process.PID()) {
			return item.failure(ctx, fmt.Errorf("%s service exited", item.Spec.Name))
		}
		if item.Spec.Transport != "tcp" {
			continue
		}
		addresses, summary, err := AuditPort(ctx, item.Spec.Port)
		if err == nil {
			for _, line := range strings.Split(summary, "\n") {
				if strings.HasPrefix(line, "p") {
					pid, e := strconv.Atoi(line[1:])
					if e != nil {
						err = e
						break
					}
					if item.Process == nil || pid != item.Process.PID() {
						owned, e := sandbox.OwnsProcess(ctx, pid, g.Handle.Dir())
						if e != nil || !owned {
							err = fmt.Errorf("listener PID is not owned by episode")
							break
						}
					}
					if item.Spec.Unmanaged {
						item.Evidence.PID = pid
					}
				}
			}
		}
		item.Evidence.Addresses = addresses
		item.Evidence.Audit = summary
		item.Evidence.LoopbackOnly = err == nil
		if err != nil {
			return item.failure(ctx, err)
		}
	}
	return nil
}
func (g *Group) Stop(ctx context.Context) error {
	if g == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	var errs []error
	for i := len(g.Items) - 1; i >= 0; i-- {
		item := g.Items[i]
		if item.Process == nil {
			continue
		}
		if e := item.Process.Kill(ctx); e != nil {
			errs = append(errs, item.failure(ctx, e))
		}
		if p, ok := item.Process.(interface{ TimedOut() bool }); ok {
			item.Evidence.Timeout = p.TimedOut()
		}
		if item.drained != nil {
			select {
			case <-item.drained:
			case <-time.After(time.Second):
				sandbox.CloseReaders(item.Process)
			case <-ctx.Done():
				errs = append(errs, item.failure(ctx, ctx.Err()))
			}
		}
		sandbox.CloseReaders(item.Process)
		if item.drained != nil {
			<-item.drained
		}
		if cb, ok := g.Backend.(ServiceBackend); ok {
			err := cb.ServiceStopped(ctx, item.Process)
			item.Evidence.Destroyed = err == nil
			if err != nil {
				errs = append(errs, item.failure(ctx, err))
			}
		} else {
			item.Evidence.Destroyed = !sandbox.Alive(item.Process.PID())
		}
	}
	return errors.Join(errs...)
}
func (g *Group) Evidence() []*Evidence {
	if g == nil {
		return nil
	}
	out := []*Evidence{}
	for _, item := range g.Items {
		out = append(out, item.Evidence)
	}
	return out
}
func Ready(ctx context.Context, r sandbox.ServiceReady) error {
	if r.HTTP != nil {
		u, err := url.Parse(r.HTTP.URL)
		if err != nil {
			return err
		}
		if u.Scheme != "http" || u.User != nil {
			return fmt.Errorf("readiness HTTP must be local plaintext without credentials")
		}
		tr := &http.Transport{Proxy: nil}
		defer tr.CloseIdleConnections()
		if strings.TrimSuffix(u.Host, ":") == "unix" {
			socket, path, ok := strings.Cut(u.Path, ".sock")
			if !ok {
				return fmt.Errorf("Unix HTTP URL requires .sock")
			}
			socket += ".sock"
			tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}
			u.Host = "localhost"
			u.Path = path
			if u.Path == "" {
				u.Path = "/"
			}
		} else {
			host := u.Hostname()
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return fmt.Errorf("HTTP readiness must use loopback")
			}
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			return err
		}
		client := &http.Client{Transport: tr, Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("readiness redirect refused") }}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		want := r.HTTP.Status
		if want == 0 {
			want = 200
		}
		if resp.StatusCode != want {
			return fmt.Errorf("HTTP status %d", resp.StatusCode)
		}
		return nil
	}
	network, address := "unix", r.Unix
	if r.TCP != "" {
		network, address = "tcp", r.TCP
		host, _, e := net.SplitHostPort(address)
		if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("readiness requires loopback TCP")
		}
	}
	conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, network, address)
	if err == nil {
		conn.Close()
	}
	return err
}

// ValidateReady runs before any host-side dial. A manifest cannot use readiness
// as a way to make the evaluator contact an unrelated host service.
func ValidateReady(s sandbox.Service, ep string) error {
	address := s.Ready.TCP
	if s.Ready.HTTP != nil {
		u, e := url.Parse(s.Ready.HTTP.URL)
		if e != nil {
			return e
		}
		if u.Scheme != "http" || u.User != nil {
			return fmt.Errorf("readiness requires local HTTP")
		}
		if strings.TrimSuffix(u.Host, ":") == "unix" {
			p, _, ok := strings.Cut(u.Path, ".sock")
			if !ok {
				return fmt.Errorf("Unix HTTP readiness requires .sock")
			}
			if s.Transport != "unix" || !sandbox.Within(p+".sock", ep) {
				return fmt.Errorf("Unix HTTP readiness escapes EP")
			}
			return nil
		}
		address = u.Host
	}
	if s.Transport == "unix" {
		if address != "" || !sandbox.Within(s.Ready.Unix, ep) {
			return fmt.Errorf("Unix readiness must stay in EP")
		}
		return nil
	}
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return e
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port != strconv.Itoa(int(s.Port)) {
		return fmt.Errorf("TCP readiness must use allocated loopback port")
	}
	return nil
}
func waitReady(ctx context.Context, r sandbox.ServiceReady) error {
	for {
		if err := Ready(ctx, r); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
}

func AuditPort(ctx context.Context, port uint16) ([]string, string, error) {
	exe, err := exec.LookPath("lsof")
	if err != nil {
		exe = "/usr/sbin/lsof"
	}
	cmd := exec.CommandContext(ctx, exe, "-nP", "-a", "-iTCP:"+strconv.Itoa(int(port)), "-sTCP:LISTEN", "-Fpn")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin", "LC_ALL=C"}
	raw, err := procexec.Output(cmd)
	if err != nil {
		return nil, "", fmt.Errorf("listener audit unavailable: %w", err)
	}
	return ParseListeners(raw, port)
}
func ParseListeners(raw []byte, port uint16) ([]string, string, error) {
	addresses := []string{}
	summary := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if len(line) < 2 {
			continue
		}
		if line[0] == 'p' {
			if _, err := strconv.Atoi(line[1:]); err == nil {
				summary = append(summary, line)
			}
			continue
		}
		if line[0] != 'n' {
			continue
		}
		address := strings.TrimSuffix(line[1:], " (LISTEN)")
		host, p, err := net.SplitHostPort(address)
		if err != nil {
			return addresses, strings.Join(summary, "\n"), fmt.Errorf("unparseable listener")
		}
		ip := net.ParseIP(host)
		if p != strconv.Itoa(int(port)) || ip == nil || !ip.IsLoopback() {
			return append(addresses, address), strings.Join(append(summary, "n"+address), "\n"), fmt.Errorf("non-loopback or unexpected listener: %s", address)
		}
		addresses = append(addresses, address)
		summary = append(summary, "n"+address)
	}
	if len(addresses) == 0 {
		return nil, strings.Join(summary, "\n"), fmt.Errorf("no auditable listener")
	}
	return addresses, strings.Join(summary, "\n"), nil
}
