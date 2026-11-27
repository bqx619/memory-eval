package selftest

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"memeval/internal/config/dotenv"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sidecar"
	"memeval/internal/sut"
	"memeval/internal/transport/jsonrpc"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

func ContainerExpectations(mode sandbox.NetworkMode, services []sandbox.Service) Expectations {
	x := Expectations{}
	for _, k := range []string{"gold_absent", "host_paths_absent", "write_episode", "write_rootfs_fails", "write_tmp_ok", "env_read", "uid_nonroot", "caps_dropped", "no_new_privs", "rootfs_readonly", "pids_limit", "mounts_expected", "environment", "container_destroyed", "network_destroyed", "volumes_none", "ep_removed", "no_published_ports"} {
		x[k] = true
	}
	for _, k := range []string{"tcp4_control", "udp_control", "dns_external", "https_external"} {
		x[k] = mode == sandbox.NetworkOpen
	}
	if mode != sandbox.NetworkOpen {
		x["network_internal"] = true
	}
	if mode == sandbox.NetworkGateway {
		for k, v := range GatewayExpectations() {
			x[k] = v
		}
		x["relay_destroyed"] = true
	}
	for _, s := range services {
		x["sidecar_"+s.Name+"_connect"] = true
		x["sidecar_"+s.Name+"_dns"] = true
	}
	return x
}

type controls struct {
	options container.ProbeOptions
	gold    string
	https   *httptest.Server
	udp     net.PacketConn
	dir     string
}

func (c *controls) close() {
	if c.https != nil {
		c.https.Close()
	}
	if c.udp != nil {
		c.udp.Close()
	}
	os.RemoveAll(c.dir)
}

// Select a local, up, non-point-to-point IPv4 interface without contacting the
// internet. Bind listeners on all IPv4 interfaces so Docker's bridge can route
// to the host. Failure to reach this address makes an open test fail closed.
func controlHost() (string, error) {
	interfaces, e := net.Interfaces()
	if e != nil {
		return "", e
	}
	var candidates []string
	for _, i := range interfaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addresses, _ := i.Addrs()
		for _, a := range addresses {
			ip, _, e := net.ParseCIDR(a.String())
			if e != nil || ip.To4() == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.IsPrivate() {
				return ip.String(), nil
			}
			candidates = append(candidates, ip.String())
		}
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return "", fmt.Errorf("selftest requires a routable local IPv4 control address")
}
func newControls() (*controls, error) {
	c := &controls{}
	var e error
	c.dir, e = os.MkdirTemp("", "memeval-selftest-controls-")
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*controls, error) { c.close(); return nil, e }
	c.gold = filepath.Join(c.dir, "gold-private.json")
	if e = os.WriteFile(c.gold, []byte("synthetic host-only gold control\n"), 0600); e != nil {
		return fail(e)
	}
	host, e := controlHost()
	if e != nil {
		return fail(e)
	}
	ln, e := net.Listen("tcp", ":0")
	if e != nil {
		return fail(e)
	}
	c.https = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected credentials", 400)
			return
		}
		w.Write([]byte("memeval-local-control"))
	}))
	c.https.Listener.Close()
	c.https.Listener = ln
	c.https.Config.ErrorLog = log.New(io.Discard, "", 0)
	c.https.StartTLS()
	port := ln.Addr().(*net.TCPAddr).Port
	c.udp, e = net.ListenPacket("udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if e != nil {
		return fail(e)
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			n, a, e := c.udp.ReadFrom(buf)
			if e != nil {
				return
			}
			c.udp.WriteTo(buf[:n], a)
		}
	}()
	c.options.Control = net.JoinHostPort(host, strconv.Itoa(port))
	if interfaces, err := net.Interfaces(); err == nil {
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
				continue
			}
			addresses, _ := iface.Addrs()
			for _, addr := range addresses {
				ip, _, err := net.ParseCIDR(addr.String())
				if err == nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast() {
					c.options.Control6 = net.JoinHostPort(ip.String(), strconv.Itoa(port))
					break
				}
			}
			if c.options.Control6 != "" {
				break
			}
		}
	}
	c.options.ControlCA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.https.Certificate().Raw}))
	return c, nil
}

func runContainer(ctx context.Context, tester Selftester, m *sut.Manifest, l *sut.Lock, linuxSHA string) (*Record, error) {
	r := &Record{Status: "failed", Checks: map[string]Check{}, Sandboxed: &ProbeResult{Items: map[string]Observation{}}}
	c, e := newControls()
	if e != nil {
		return r, e
	}
	defer c.close()
	r.Control = &ProbeResult{Items: container.ControlObservations(c.options)}
	gold, e := os.ReadFile(c.gold)
	r.Control.Items["gold_exists"] = Observation{Success: e == nil && len(gold) > 0}
	controlsPassed := true
	for _, v := range r.Control.Items {
		controlsPassed = controlsPassed && v.Success
	}
	if !controlsPassed {
		return r, fmt.Errorf("selftest host control failed")
	}
	ep, e := sandbox.NewEpisode()
	if e != nil {
		return r, e
	}
	defer os.RemoveAll(ep)
	s, e := m.Spec(l, ep, "selftest", false)
	if e != nil {
		return r, e
	}
	if e = m.PlanServices(l, &s); e != nil {
		return r, e
	}
	home, _ := os.UserHomeDir()
	repo := dotenv.Root(".")
	o := c.options
	o.StartServices = func(ctx context.Context, b sandbox.Sandbox, h sandbox.Handle) (func(context.Context) error, error) {
		g, e := sidecar.Start(ctx, b, h, nil)
		return func(ctx context.Context) error { return errors.Join(g.Stop(ctx), g.VerifyStopped(ctx)) }, e
	}
	o.LinuxBinarySHA256 = linuxSHA
	o.HostPaths = []string{"/Users", repo, filepath.Join(home, ".config"), filepath.Join(home, ".claude"), filepath.Join(repo, ".env"), c.gold}
	// Allow each declared service its full readiness/init budget before probes.
	timeout := 90 * time.Second
	for _, service := range s.Services {
		timeout += time.Duration(service.TimeoutMS) * time.Millisecond * time.Duration(1+len(service.Init))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	observed, e := tester.Selftest(ctx, s, o)
	if observed != nil {
		r.ProfileSHA256 = observed.ProfileSHA256
		r.EffectiveConfig = observed.EffectiveConfig
		if observed.Probe != nil {
			r.Sandboxed = observed.Probe
		}
	}
	var failed []string
	for name, want := range ContainerExpectations(s.Network, s.Services) {
		v, ok := r.Sandboxed.Items[name]
		check := Check{Expected: want, ControlPassed: controlsPassed, Passed: ok && v.Success == want && controlsPassed}
		if ok {
			check.Observed = &v
		}
		r.Checks[name] = check
		if !check.Passed {
			failed = append(failed, name)
		}
	}
	slices.Sort(failed)
	if len(failed) > 0 {
		e = errors.Join(e, fmt.Errorf("container selftest failed checks: %v", failed))
	}
	if e != nil {
		return r, fmt.Errorf("%s", jsonrpc.Redact(e.Error(), m.Secrets()))
	}
	r.Passed = true
	r.Status = "passed"
	return r, nil
}
