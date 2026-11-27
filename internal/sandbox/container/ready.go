package container

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"time"
)

func literalEndpoint(address string, allowZero bool) error {
	a, err := netip.ParseAddrPort(address)
	if err != nil || a.Addr().Zone() != "" || (!allowZero && a.Port() == 0) {
		return fmt.Errorf("endpoint requires a literal IP:port (no DNS)")
	}
	return nil
}

func ReadyCommand(args []string, stderr io.Writer) error {
	f := flag.NewFlagSet("__container-ready", flag.ContinueOnError)
	f.SetOutput(stderr)
	address := f.String("tcp", "", "literal TCP endpoint")
	httpURL := f.String("http", "", "HTTP readiness URL")
	status := f.Int("status", 200, "expected HTTP status")
	f.IntVar(status, "expect", 200, "expected HTTP status")
	service := f.Bool("service", false, "resolve a declared container service alias")
	timeout := f.Duration("timeout", 5*time.Second, "readiness deadline")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *timeout <= 0 {
		return fmt.Errorf("invalid readiness arguments")
	}
	if (*address == "") == (*httpURL == "") {
		return fmt.Errorf("readiness requires exactly one tcp or http endpoint")
	}
	if *httpURL != "" {
		u, e := url.Parse(*httpURL)
		if e != nil || u.Host == "" || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid HTTP readiness URL")
		}
	} else if !*service {
		if err := literalEndpoint(*address, false); err != nil {
			return err
		}
	} else if host, port, e := net.SplitHostPort(*address); e != nil || host == "" || port == "" {
		return fmt.Errorf("invalid service TCP endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	for {
		var err error
		if *httpURL != "" {
			err = requestStatus(*httpURL, nil, *status)
		} else {
			var conn net.Conn
			conn, err = (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", *address)
			if err == nil {
				err = conn.Close()
			}
		}
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("relay TCP readiness failed: %w", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
