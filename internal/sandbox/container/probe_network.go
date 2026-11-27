package container

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func observe(fn func() error) Observation {
	start := time.Now()
	err := fn()
	o := Observation{Success: err == nil, DurationMS: float64(time.Since(start).Microseconds()) / 1000}
	if err != nil {
		o.Detail = err.Error()
	}
	return o
}
func connect(address, network string) error {
	c, err := net.DialTimeout(network, address, 2*time.Second)
	if err == nil {
		err = c.Close()
	}
	return err
}
func controlUDP(address string) error {
	c, err := net.DialTimeout("udp4", address, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	want := "memeval-control-" + nonce()
	if _, err = c.Write([]byte(want)); err != nil {
		return err
	}
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err == nil && string(buf[:n]) != want {
		err = fmt.Errorf("UDP control response mismatch")
	}
	return err
}
func requestStatus(url string, tlsConfig *tls.Config, accepted ...int) error {
	tr := &http.Transport{TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// No credential/header/environment integration: this probe is deliberately unauthenticated.
	r, err := c.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
	for _, code := range accepted {
		if r.StatusCode == code {
			return nil
		}
	}
	return fmt.Errorf("unexpected HTTP status %d", r.StatusCode)
}

// ControlObservations performs only local-control requests. The host never
// resolves the external DNS test name or sends an internet/gateway request.
func ControlObservations(o ProbeOptions) map[string]Observation {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(o.ControlCA))
	return map[string]Observation{
		"tcp4_control": observe(func() error { return connect(o.Control, "tcp4") }),
		"udp_control":  observe(func() error { return controlUDP(o.Control) }),
		"https_external": observe(func() error {
			return requestStatus("https://"+o.Control, &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}, 200)
		}),
	}
}
func networkObservations(o ProbeOptions) map[string]Observation {
	out := ControlObservations(o)
	out["dns_external"] = observe(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		a, err := (&net.Resolver{PreferGo: true}).LookupHost(ctx, "example.com")
		if err == nil && len(a) == 0 {
			err = fmt.Errorf("DNS returned no addresses")
		}
		return err
	})
	out["tcp6"] = Observation{Detail: "no routable IPv6 host endpoint available (not asserted)"}
	if o.Control6 != "" {
		out["tcp6"] = observe(func() error { return connect(o.Control6, "tcp6") })
	}
	if o.GatewayURL != "" {
		out["gateway_connect"] = observe(func() error { return requestStatus(strings.TrimRight(o.GatewayURL, "/")+"/models", nil, 200, 401, 403) })
	}
	for name, address := range o.Services {
		host, _, _ := net.SplitHostPort(address)
		out["sidecar_"+name+"_dns"] = observe(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			a, err := (&net.Resolver{PreferGo: true}).LookupHost(ctx, host)
			if err == nil && len(a) == 0 {
				err = fmt.Errorf("empty sidecar DNS response")
			}
			return err
		})
		out["sidecar_"+name+"_connect"] = observe(func() error { return connect(address, "tcp") })
	}
	return out
}
