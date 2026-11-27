package governor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"memeval/internal/mathutil"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type Server struct {
	lifeMu        sync.Mutex
	active        sync.WaitGroup
	closing       bool
	cfg           Config
	hooks         Hooks
	client        *http.Client
	transport     *http.Transport
	sem           chan struct{}
	mu            sync.Mutex
	state, reason string
	changed       chan struct{}
	failures      int
	cooldown      time.Duration
	opened, until time.Time
	paused        time.Duration
	halfBusy      bool
	tokens        float64
	refilled      time.Time
	events        []CircuitEvent
	billing       Billing
	waits         []float64
	meter         *os.File
	meterErr      error
}

func New(c Config, h Hooks) (*Server, error) {
	a, e := netip.ParseAddrPort(c.Target)
	if e != nil || a.Port() == 0 || a.Addr().Zone() != "" {
		return nil, fmt.Errorf("governor target must be a literal IP:port")
	}
	if c.Concurrency < 1 || c.RPM < 0 || c.BudgetTokens < 0 || c.ConsumedTokens < 0 || c.UpstreamTimeout <= 0 || c.MaxPause <= 0 {
		return nil, fmt.Errorf("invalid governor limits")
	}
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.Sleep == nil {
		h.Sleep = sleep
	}
	if h.Jitter == nil {
		h.Jitter = func(d time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(d) + 1)) }
	}
	if err := os.MkdirAll(c.MeterDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(c.MeterDir, 0700); err != nil {
		return nil, err
	}
	// A run generation always gets a fresh ledger; never overwrite prior spend.
	f, e := os.OpenFile(filepath.Join(c.MeterDir, MeterFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: false, MaxIdleConns: c.Concurrency, MaxIdleConnsPerHost: c.Concurrency, DisableCompression: true}
	s := &Server{cfg: c, hooks: h, transport: tr, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, sem: make(chan struct{}, c.Concurrency), state: "closed", changed: make(chan struct{}), refilled: h.Now(), tokens: float64(c.RPM), meter: f, events: []CircuitEvent{}}
	return s, nil
}
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (s *Server) Close() error {
	s.lifeMu.Lock()
	s.closing = true
	s.lifeMu.Unlock()
	s.active.Wait()
	s.transport.CloseIdleConnections()
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.meterErr, s.meter.Close())
}
func (s *Server) change(state, reason string, cool time.Duration) {
	s.state = state
	s.reason = reason
	s.events = append(s.events, CircuitEvent{s.hooks.Now().UTC().Format(time.RFC3339Nano), state, reason, cool.Seconds()})
	close(s.changed)
	s.changed = make(chan struct{})
}
func (s *Server) refresh() {
	now := s.hooks.Now()
	if s.state == "open" && s.paused+now.Sub(s.opened) >= s.cfg.MaxPause {
		s.paused += now.Sub(s.opened)
		s.change("exhausted", "maximum cumulative circuit pause reached", 0)
	}
	if s.cfg.BudgetTokens > 0 && s.cfg.ConsumedTokens+s.billing.Prompt+s.billing.Completion >= s.cfg.BudgetTokens && s.state != "budget" {
		s.change("budget", "SUT token budget reached", 0)
	}
}
func (s *Server) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	b := s.billing
	b.QueueP95 = quantile95(s.waits)
	b.ByEpisode = []EpisodeBilling{}
	wait := max(time.Duration(0), s.until.Sub(s.hooks.Now()))
	if s.state != "open" {
		wait = 0
	}
	return Status{s.state, s.reason, wait.Seconds(), b, slices.Clone(s.events)}
}
func quantile95(v []float64) float64 {
	if p := mathutil.Percentile(v, .95); p != nil {
		return *p
	}
	return 0
}
func (s *Server) StatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.URL.Path != "/memeval/status" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.Snapshot())
}
func (s *Server) acquire(ctx context.Context) (queue time.Duration, halfProbe bool, err error) {
	start := s.hooks.Now()
	defer func() { queue = s.hooks.Now().Sub(start) }()
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, false, ctx.Err()
	}
	release := true
	defer func() {
		if release {
			<-s.sem
		}
	}()
	for {
		s.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return 0, false, err
		}
		s.refresh()
		now := s.hooks.Now()
		if s.state == "exhausted" || s.state == "budget" {
			s.mu.Unlock()
			return 0, false, errStopped
		}
		var wait time.Duration
		if s.state == "open" {
			wait = s.until.Sub(now)
			if wait <= 0 {
				s.paused += now.Sub(s.opened)
				s.halfBusy = false
				s.change("half_open", "cooldown elapsed", 0)
			} else {
				wait = min(wait, s.cfg.MaxPause-s.paused-now.Sub(s.opened))
			}
		}
		if s.state == "half_open" && s.halfBusy {
			ch := s.changed
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return 0, false, ctx.Err()
			case <-ch:
			}
			continue
		}
		if wait <= 0 && s.cfg.RPM > 0 {
			rate := float64(s.cfg.RPM) / 60
			s.tokens = min(float64(s.cfg.RPM), s.tokens+now.Sub(s.refilled).Seconds()*rate)
			s.refilled = now
			if s.tokens < 1 {
				wait = time.Duration((1 - s.tokens) / rate * float64(time.Second))
			} else {
				s.tokens--
			}
		}
		if wait > 0 {
			changed := s.changed
			s.mu.Unlock()
			if err := s.waitChange(ctx, wait, changed); err != nil {
				return 0, false, err
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return 0, false, err
		}
		if s.state == "half_open" {
			halfProbe = true
			s.halfBusy = true
		}
		s.mu.Unlock()
		release = false
		return 0, halfProbe, nil
	}
}

var errStopped = errors.New("governor stopped")

func (s *Server) record(a Attempt, retryable bool, ra time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meterErr == nil {
		s.meterErr = json.NewEncoder(s.meter).Encode(a)
		if s.meterErr != nil {
			s.change("exhausted", "meter write failed", 0)
		}
	}
	s.billing.add(a)
	s.waits = append(s.waits, a.QueueWaitMS)
	if a.Error == clientDisconnected {
		// A cancelled waiter must not release another request's live probe.
		if a.halfProbe && s.state == "half_open" {
			s.halfBusy = false
			close(s.changed)
			s.changed = make(chan struct{})
		}
		s.refresh()
		return // Neither increment nor reset consecutive upstream failures.
	}
	if s.state != "exhausted" && s.state != "budget" {
		if retryable {
			s.failures++
			if s.state == "half_open" || s.failures >= 5 || ra > 60*time.Second {
				if s.state != "open" {
					if s.state == "half_open" {
						s.cooldown = min(600*time.Second, s.cooldown*2)
					} else {
						s.cooldown = 60 * time.Second
					}
					s.opened = s.hooks.Now()
					s.until = s.opened.Add(max(s.cooldown, ra))
					s.billing.CircuitOpens++
					s.change("open", "consecutive retryable failures or long Retry-After", max(s.cooldown, ra))
				} else if deadline := s.hooks.Now().Add(ra); deadline.After(s.until) {
					s.until = deadline
				}
			}
		} else {
			s.failures = 0
			if s.state == "half_open" {
				s.cooldown = 0
				s.halfBusy = false
				s.change("closed", "half-open attempt succeeded", 0)
			}
		}
	}
	s.refresh()
}
func (s *Server) exhaust() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "budget" {
		s.change("exhausted", "upstream retry limit reached", 0)
	}
}

// Wake paused/rate-limited admissions immediately on a state change (especially
// budget/exhaustion), rather than retaining them through the full cooldown.
func (s *Server) waitChange(ctx context.Context, d time.Duration, ch <-chan struct{}) error {
	sleepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.hooks.Sleep(sleepCtx, d) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	case err := <-done:
		return err
	}
}
