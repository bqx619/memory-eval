package governor

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

func sortEpisodes(a []EpisodeBilling) {
	slices.SortFunc(a, func(a, b EpisodeBilling) int {
		if a.NS < b.NS {
			return -1
		}
		if a.NS > b.NS {
			return 1
		}
		return 0
	})
}
func Command(ctx context.Context, args []string, stderr io.Writer) (retErr error) {
	c := Defaults()
	f := flag.NewFlagSet("__container-governor", flag.ContinueOnError)
	f.SetOutput(stderr)
	listen := f.String("listen", "", "HTTP listen IP:port")
	status := f.String("status-listen", "", "loopback status IP:port")
	f.StringVar(&c.Target, "target", "", "single upstream IP:port")
	f.IntVar(&c.Concurrency, "concurrency", c.Concurrency, "")
	f.IntVar(&c.RPM, "rpm", 0, "")
	f.Int64Var(&c.ConsumedTokens, "consumed-tokens", 0, "observed tokens retained by resume")
	f.Int64Var(&c.BudgetTokens, "budget-tokens", 0, "")
	f.DurationVar(&c.UpstreamTimeout, "upstream-timeout", c.UpstreamTimeout, "")
	f.DurationVar(&c.MaxPause, "max-pause", c.MaxPause, "")
	f.StringVar(&c.MeterDir, "meter-dir", "", "")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected governor arguments")
	}
	a, e := netip.ParseAddrPort(*status)
	if e != nil || !a.Addr().IsLoopback() {
		return fmt.Errorf("status listener must be loopback IP:port")
	}
	if _, e = netip.ParseAddrPort(*listen); e != nil {
		return fmt.Errorf("HTTP listener must be IP:port")
	}
	s, e := New(c, Hooks{})
	if e != nil {
		return e
	}
	defer func() {
		retErr = errors.Join(retErr, s.Close())
		raw, err := json.Marshal(s.Snapshot())
		if err == nil {
			err = os.WriteFile(filepath.Join(c.MeterDir, "status.json"), raw, 0600)
		}
		retErr = errors.Join(retErr, err)
	}()
	l, e := net.Listen("tcp", *listen)
	if e != nil {
		return e
	}
	defer l.Close()
	sl, e := net.Listen("tcp", *status)
	if e != nil {
		return e
	}
	defer sl.Close()
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	ss := &http.Server{Handler: http.HandlerFunc(s.StatusHandler), ReadHeaderTimeout: 5 * time.Second}
	failures := make(chan error, 2)
	go func() { failures <- srv.Serve(l) }()
	go func() { failures <- ss.Serve(sl) }()
	fmt.Fprintf(stderr, "governor listening=%s status=%s pid=%d\n", l.Addr(), sl.Addr(), os.Getpid())
	select {
	case <-ctx.Done():
	case e = <-failures:
	}
	shut, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	srv.Shutdown(shut)
	ss.Shutdown(shut)
	srv.Close()
	ss.Close()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

// StatusCommand is exec'd inside the governor container. There is no published
// status port, no SUT credential, and no arbitrary destination argument.
func StatusCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("__container-governor-status", flag.ContinueOnError)
	p := f.String("address", "", "loopback status IP:port")
	if e := f.Parse(args); e != nil {
		return e
	}
	a, e := netip.ParseAddrPort(*p)
	if e != nil || !a.Addr().IsLoopback() {
		return fmt.Errorf("status address must be loopback")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+a.String()+"/memeval/status", nil)
	c := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, e := c.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("governor status HTTP %d", resp.StatusCode)
	}
	var s Status
	if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(s)
}
