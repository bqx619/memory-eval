package governor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func retryAfter(v string, now time.Time) time.Duration {
	if sec, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && sec >= 0 {
		return time.Duration(sec * float64(time.Second))
	}
	if t, e := http.ParseTime(v); e == nil {
		return max(0, t.Sub(now))
	}
	return 0
}
func retryStatus(n int) bool { return n == 429 || n == 502 || n == 503 || n == 504 }
func hop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, k := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(k))
		}
	}
	for _, k := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}
func copyHeaders(w http.ResponseWriter, h http.Header) {
	h = h.Clone()
	hop(h)
	for k, vs := range h {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}
func (s *Server) stop(w http.ResponseWriter) {
	st := s.Snapshot()
	reason := st.State
	if reason != "budget" {
		reason = "exhausted"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Memeval-Governor", reason)
	w.WriteHeader(503)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "memeval_governor", "reason": reason, "retry_after_s": st.RetryAfter}})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.lifeMu.Lock()
	if s.closing {
		s.lifeMu.Unlock()
		http.Error(w, "governor shutting down", 503)
		return
	}
	s.active.Add(1)
	s.lifeMu.Unlock()
	defer s.active.Done()
	if s.Snapshot().Stopped() {
		s.stop(w)
		return
	}
	if r.Method == "CONNECT" || r.URL.User != nil || r.URL.IsAbs() && (r.URL.Scheme != "http" || !strings.EqualFold(r.URL.Host, s.cfg.Target)) {
		http.Error(w, "single-target gateway only", 403)
		return
	}
	if r.URL.Path == "/memeval/status" {
		http.NotFound(w, r)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, BodyLimit+1))
	if err != nil {
		if r.Context().Err() != nil {
			s.disconnected(r, "", 1, 0)
			return
		}
		http.Error(w, "request body unreadable", 400)
		return
	}
	defer r.Body.Close()
	replayable := len(data) <= BodyLimit
	var fields map[string]json.RawMessage
	model := ""
	stream := false
	if replayable && json.Unmarshal(data, &fields) == nil {
		json.Unmarshal(fields["model"], &model)
		json.Unmarshal(fields["stream"], &stream)
		if stream {
			var options map[string]json.RawMessage
			json.Unmarshal(fields["stream_options"], &options)
			if options == nil {
				options = map[string]json.RawMessage{}
			}
			options["include_usage"] = json.RawMessage("true")
			fields["stream_options"], _ = json.Marshal(options)
			data, _ = json.Marshal(fields)
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	for attempt := 1; attempt <= 7; attempt++ {
		queue, halfProbe, e := s.acquire(r.Context())
		if e != nil {
			if r.Context().Err() != nil {
				s.disconnected(r, model, attempt, queue)
			} else if e == errStopped {
				s.stop(w)
			}
			return
		}
		start := s.hooks.Now()
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.UpstreamTimeout)
		var forwarded atomic.Bool
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaders: func() { forwarded.Store(true) }})
		var body io.Reader = bytes.NewReader(data)
		if !replayable {
			body = io.MultiReader(body, r.Body)
		}
		req := r.Clone(ctx)
		u := *r.URL
		u.Scheme = "http"
		u.Host = s.cfg.Target
		req.URL = &u
		req.RequestURI = ""
		req.Host = s.cfg.Target
		req.Header = r.Header.Clone()
		hop(req.Header)
		req.Header.Del("Accept-Encoding")
		req.Body = io.NopCloser(body)
		req.GetBody = nil
		if replayable {
			req.ContentLength = int64(len(data))
		} else {
			req.ContentLength = -1
		}
		resp, callErr := s.client.Do(req)
		a := Attempt{Time: start.UTC().Format(time.RFC3339Nano), EpisodeIP: ip, Path: r.URL.Path, Model: model, Attempt: attempt, QueueWaitMS: float64(queue) / float64(time.Millisecond), halfProbe: halfProbe}
		ra := time.Duration(0)
		var raw []byte
		var usage *Usage
		sent := false
		downstreamFailed := false
		if resp != nil {
			a.Status = resp.StatusCode
			ra = retryAfter(resp.Header.Get("Retry-After"), s.hooks.Now())
			if !retryStatus(resp.StatusCode) || !replayable {
				if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
					copyHeaders(w, resp.Header)
					w.WriteHeader(resp.StatusCode)
					sent = true
					parser := newSSE()
					buf := make([]byte, 32<<10)
					for {
						n, er := resp.Body.Read(buf)
						if n > 0 {
							parser.feed(buf[:n])
							if _, we := w.Write(buf[:n]); we != nil {
								downstreamFailed = true
								break
							}
							if we := http.NewResponseController(w).Flush(); we != nil {
								downstreamFailed = true
								break
							}
						}
						if er != nil {
							if er != io.EOF {
								callErr = er
							}
							break
						}
					}
					parser.finish()
					usage = parser.usage
				} else {
					raw, callErr = io.ReadAll(resp.Body)
					usage = extractUsage(raw)
				}
			} else {
				raw, callErr = io.ReadAll(io.LimitReader(resp.Body, BodyLimit))
				usage = extractUsage(raw)
			}
			resp.Body.Close()
		}
		cancel()
		a.MS = float64(s.hooks.Now().Sub(start)) / float64(time.Millisecond)
		a.RetryAfter = ra.Seconds()
		a.Usage = usage
		if usage != nil {
			a.Prompt = &usage.Prompt
			a.Cached = &usage.PromptDetails.Cached
			a.Completion = &usage.Completion
			a.Reasoning = &usage.CompletionDetails.Reasoning
		}
		if callErr != nil {
			a.Error = "upstream transport/read error"
		}
		retry := callErr != nil || retryStatus(a.Status)
		if !sent && !retry && !downstreamFailed && r.Context().Err() == nil {
			copyHeaders(w, resp.Header)
			w.WriteHeader(resp.StatusCode)
			_, writeErr := w.Write(raw)
			downstreamFailed = writeErr != nil
			sent = true
		}
		if r.Context().Err() != nil || downstreamFailed {
			a.Error, a.Status, a.RetryAfter = clientDisconnected, 0, 0
			a.NotForwarded = !forwarded.Load()
			retry, ra = false, 0
		}
		s.record(a, retry, ra)
		<-s.sem
		if a.Error == clientDisconnected {
			return
		}
		if sent {
			return
		} // Never replay a stream after any downstream bytes were emitted.
		if r.Context().Err() != nil {
			s.disconnected(r, model, attempt+1, 0)
			return
		}
		if retry && replayable {
			if attempt == 7 {
				s.exhaust()
				s.stop(w)
				return
			}
			if s.Snapshot().Stopped() {
				s.stop(w)
				return
			}
			if s.Snapshot().State != "open" {
				delay := ra
				if delay == 0 {
					delay = s.hooks.Jitter(min(60*time.Second, time.Second*time.Duration(1<<(attempt-1))))
				}
				waiting := s.hooks.Now()
				if s.hooks.Sleep(r.Context(), delay) != nil {
					if r.Context().Err() != nil {
						s.disconnected(r, model, attempt+1, s.hooks.Now().Sub(waiting))
					}
					return
				}
			}
			continue
		}
		if callErr != nil {
			http.Error(w, "upstream connection failed", 502)
			return
		}
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(raw)
		return
	}
}

// Queue/body/backoff cancellations are observable but did not call upstream.
func (s *Server) disconnected(r *http.Request, model string, attempt int, queue time.Duration) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.record(Attempt{Time: s.hooks.Now().UTC().Format(time.RFC3339Nano), EpisodeIP: ip,
		Path: r.URL.Path, Model: model, Attempt: attempt, QueueWaitMS: float64(queue) / float64(time.Millisecond),
		Error: clientDisconnected, NotForwarded: true}, false, 0)
}
func extractUsage(b []byte) *Usage {
	var v struct {
		Usage *Usage `json:"usage"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return v.Usage
}

// Bounded SSE inspection is independent from the exact bytes flushed downstream.
type sseParser struct {
	line    []byte
	data    []byte
	discard bool
	usage   *Usage
}

func newSSE() *sseParser { return &sseParser{} }
func (p *sseParser) feed(b []byte) {
	for _, c := range b {
		if c == '\n' {
			p.lineDone()
			continue
		}
		if len(p.line) < BodyLimit {
			p.line = append(p.line, c)
		} else {
			p.discard = true
		}
	}
}
func (p *sseParser) lineDone() {
	line := bytes.TrimSuffix(p.line, []byte{'\r'})
	if len(line) == 0 {
		if !p.discard {
			if u := extractUsage(bytes.TrimSpace(p.data)); u != nil {
				p.usage = u
			}
		}
		p.data = nil
		p.discard = false
	} else if bytes.HasPrefix(line, []byte("data:")) {
		if len(p.data)+len(line) > BodyLimit {
			p.discard = true
		} else {
			p.data = append(p.data, bytes.TrimPrefix(line, []byte("data:"))...)
			p.data = append(p.data, '\n')
		}
	}
	p.line = nil
}
func (p *sseParser) finish() {
	if len(p.line) > 0 {
		p.lineDone()
	}
	p.line = nil
	p.lineDone()
}

// ReadAttempts never follows other files or interprets provider content.
func ReadAttempts(r io.Reader) ([]Attempt, error) {
	rows := []Attempt{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		var a Attempt
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			return nil, err
		}
		rows = append(rows, a)
	}
	return rows, sc.Err()
}
func Aggregate(rows []Attempt, ips map[string]string, opens int64) Billing {
	b := Billing{ByEpisode: []EpisodeBilling{}, CircuitOpens: opens}
	by := map[string]*EpisodeBilling{}
	waits := []float64{}
	for _, a := range rows {
		b.add(a)
		waits = append(waits, a.QueueWaitMS)
		ns := ips[a.EpisodeIP]
		if ns == "" {
			ns = "unattributed"
		}
		e := by[ns]
		if e == nil {
			e = &EpisodeBilling{NS: ns}
			by[ns] = e
		}
		if !a.NotForwarded {
			e.Calls++
		}
		if a.Usage == nil {
			continue
		}
		u := a.Usage
		e.Prompt += u.Prompt
		e.Completion += u.Completion
	}
	for _, e := range by {
		b.ByEpisode = append(b.ByEpisode, *e)
	}
	sortEpisodes(b.ByEpisode)
	b.QueueP95 = quantile95(waits)
	return b
}

func Attribute(rows []Attempt, bindings []Attribution) []Attempt {
	out := append([]Attempt(nil), rows...)
	for i := range out {
		a := &out[i]
		at, err := time.Parse(time.RFC3339Nano, a.Time)
		ns := "unattributed"
		if err == nil {
			for _, v := range bindings {
				if a.EpisodeIP == v.IP && !at.Before(v.From) && (v.Until.IsZero() || !at.After(v.Until)) {
					ns = v.NS
				}
			}
		}
		a.EpisodeIP = ns
	}
	return out
}
func AggregateAttributed(rows []Attempt, opens int64) Billing {
	ips := map[string]string{}
	for _, r := range rows {
		ips[r.EpisodeIP] = r.EpisodeIP
	}
	return Aggregate(rows, ips, opens)
}
