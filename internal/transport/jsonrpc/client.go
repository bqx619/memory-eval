// Package jsonrpc implements sequential, bounded NDJSON JSON-RPC 2.0.
package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	jsonrpc2 "github.com/sourcegraph/jsonrpc2"
	"io"
	"log"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	s := fmt.Sprintf("rpc_error(%d): %s", e.Code, e.Message)
	var data struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(e.Data, &data) == nil && data.Type != "" {
		s += " [" + data.Type + "]"
	}
	return s
}
func (e *RPCError) SafeDetail() string { return e.Error() }

func (e *RPCError) ErrorType() string {
	var data struct {
		Governor string `json:"governor"`
	}
	if json.Unmarshal(e.Data, &data) == nil {
		switch data.Governor {
		case "exhausted", "budget":
			return "gateway_" + data.Governor
		}
	}
	return "*jsonrpc.RPCError"
}

type ProcessExited struct {
	OOM    bool
	Code   int
	Detail string
}

func (e *ProcessExited) Error() string      { return fmt.Sprintf("process_exited(%d)", e.Code) }
func (e *ProcessExited) ErrorType() string  { return "process_exited" }
func (e *ProcessExited) SafeDetail() string { return e.Detail }

type FrameTooLarge struct{ Limit int }

func (e *FrameTooLarge) Error() string     { return "frame_too_large" }
func (e *FrameTooLarge) ErrorType() string { return e.Error() }

type Timeout struct{}

func (*Timeout) Error() string       { return "rpc_timeout" }
func (e *Timeout) ErrorType() string { return e.Error() }

type ProtocolError struct{ Reason string }

func (e *ProtocolError) Error() string   { return "rpc_protocol_error: " + e.Reason }
func (*ProtocolError) ErrorType() string { return "rpc_protocol_error" }

type Options struct {
	MaxFrameBytes  int
	RequestTimeout time.Duration
	InFlight       int
	Secrets        []string
	Mirror         io.Writer // raw diagnostic bytes; the sink owns redaction and retention
}
type frame struct {
	data []byte
	err  error
}
type Client struct {
	conn         *jsonrpc2.Conn
	transportErr error

	p          sandbox.Process
	options    Options
	slots      chan struct{}
	frames     chan frame
	stop       chan struct{}
	stderrDone chan struct{}
	readDone   chan struct{}
	closed     sync.Once
	closeErr   error
	id         uint64
	log        *os.File
	mu         sync.Mutex
	tail       []byte
}

func New(p sandbox.Process, ep string, o Options) (*Client, error) {
	if o.InFlight > 1 {
		return nil, fmt.Errorf("only in_flight=1 supported")
	}
	if o.MaxFrameBytes == 0 {
		o.MaxFrameBytes = 16 << 20
	}
	if o.MaxFrameBytes < 1 {
		return nil, fmt.Errorf("invalid frame limit")
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = 30 * time.Second
	}
	f, e := os.OpenFile(filepath.Join(ep, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	c := &Client{p: p, options: o, slots: make(chan struct{}, 1), frames: make(chan frame), stop: make(chan struct{}), stderrDone: make(chan struct{}), readDone: make(chan struct{}), log: f}
	c.slots <- struct{}{}
	go func() {
		defer close(c.stderrDone)
		b := make([]byte, 4096)
		for {
			n, e := p.Stderr().Read(b)
			if n > 0 {
				c.logBytes(b[:n])
			}
			if e != nil {
				return
			}
		}
	}()
	go c.read()
	c.conn = jsonrpc2.NewConn(context.Background(), &boundedStream{client: c}, jsonrpc2.HandlerWithError(func(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request) (any, error) { return nil, nil }), jsonrpc2.SetLogger(log.New(io.Discard, "", 0)))
	return c, nil
}
func (c *Client) logBytes(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.log.Write(b)
	if c.options.Mirror != nil {
		_, _ = c.options.Mirror.Write(b)
	}
	c.tail = append(c.tail, b...)
	if len(c.tail) > 64<<10 {
		c.tail = append([]byte(nil), c.tail[len(c.tail)-(64<<10):]...)
	}
}

// Do not consume JSON escape backslashes. The sk- boundary and minimum suffix
// length distinguish credential-shaped tokens from names such as Flask-Login.
var tokenPattern = regexp.MustCompile(`(?i)(bearer\s+[^\s,;"'\\]+|(?:api[_-]?key|token|secret|password)\s*[=:]\s*[^\s,;"'\\]+|\bsk-[A-Za-z0-9_-]{16,})`)

func Redact(s string, secrets []string) string {
	for _, v := range SecretValues(secrets) {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return tokenPattern.ReplaceAllString(s, "[REDACTED]")
}
func (c *Client) Tail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Redact(string(c.tail), c.options.Secrets)
}

// DiagnosticLog is used after Close by the installer, before the EP is removed.
// Ordinary runs retain only the bounded tail in memory.
func (c *Client) DiagnosticLog() string {
	b, err := os.ReadFile(c.log.Name())
	if err != nil {
		return c.Tail()
	}
	return Redact(string(b), c.options.Secrets)
}
func (c *Client) send(f frame) bool {
	select {
	case c.frames <- f:
		return true
	case <-c.stop:
		return false
	}
}
func (c *Client) read() {
	defer close(c.readDone)
	r := bufio.NewReaderSize(c.p.Stdout(), 32<<10)
	for {
		var line []byte
		for {
			part, e := r.ReadSlice('\n')
			if len(line)+len(part) > c.options.MaxFrameBytes {
				c.send(frame{err: &FrameTooLarge{c.options.MaxFrameBytes}})
				return
			}
			line = append(line, part...)
			if errors.Is(e, bufio.ErrBufferFull) {
				continue
			}
			if e != nil {
				if e == io.EOF && len(line) > 0 {
					e = &ProtocolError{"unterminated frame"}
				}
				c.send(frame{err: e})
				return
			}
			break
		}
		if !utf8.Valid(line) {
			c.send(frame{err: &ProtocolError{"invalid UTF-8"}})
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			c.logBytes([]byte("[transport: ignored non-object stdout line]\n"))
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) != nil {
			c.send(frame{err: &ProtocolError{"invalid JSON object"}})
			return
		}
		if _, ok := obj["id"]; !ok {
			c.logBytes([]byte("[transport: ignored notification]\n"))
			continue
		}
		if !c.send(frame{data: line}) {
			return
		}
	}
}
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	select {
	case <-c.stop:
		return &ProcessExited{Code: -1, Detail: c.Tail()}
	case <-ctx.Done():
		return &Timeout{}
	case <-c.slots:
	}
	defer func() { c.slots <- struct{}{} }()
	c.mu.Lock()
	c.id++
	id := c.id
	c.mu.Unlock()
	// Marshal before dispatch so invalid local params don't become process-exit
	// errors. A raw null also preserves the old wire (Conn otherwise omits it).
	payload, err := json.Marshal(params)
	if err != nil {
		return err
	}
	params = json.RawMessage(payload)
	done := make(chan error, 1)
	var result json.RawMessage
	go func() { done <- c.conn.Call(ctx, method, params, &result, jsonrpc2.PickID(jsonrpc2.ID{Num: id})) }()
	select {
	case <-ctx.Done():
		_ = c.p.Kill(context.Background())
		return &Timeout{}
	case err := <-done:
		if ctx.Err() != nil {
			_ = c.p.Kill(context.Background())
			return &Timeout{}
		}
		if err != nil {
			var rpc *jsonrpc2.Error
			if errors.As(err, &rpc) {
				var data json.RawMessage
				if rpc.Data != nil {
					data = *rpc.Data
				}
				return &RPCError{Code: int(rpc.Code), Message: Redact(rpc.Message, c.options.Secrets), Data: redactData(data, c.options.Secrets)}
			}
			c.mu.Lock()
			transportErr := c.transportErr
			c.mu.Unlock()
			if transportErr != nil && !errors.Is(transportErr, io.EOF) {
				_ = c.p.Kill(context.Background())
				return transportErr
			}
			return c.exited()
		}
		if out != nil && json.Unmarshal(result, out) != nil {
			return &ProtocolError{"result does not match protocol"}
		}
		return nil
	}
}

// boundedStream gives Conn only complete, bounded NDJSON responses. Framing,
// stderr, strict envelope validation and process lifetime remain evaluator-owned.
type boundedStream struct{ client *Client }

func (s *boundedStream) remember(err error) error {
	s.client.mu.Lock()
	s.client.transportErr = err
	s.client.mu.Unlock()
	return err
}
func (s *boundedStream) WriteObject(v any) error {
	var b bytes.Buffer
	if err := (jsonrpc2.PlainObjectCodec{}).WriteObject(&b, v); err != nil {
		return s.remember(err)
	}
	if b.Len() > s.client.options.MaxFrameBytes {
		return s.remember(&FrameTooLarge{s.client.options.MaxFrameBytes})
	}
	_, err := s.client.p.Stdin().Write(b.Bytes())
	if err != nil {
		return s.remember(err)
	}
	return nil
}
func (s *boundedStream) ReadObject(v any) error {
	c := s.client
	select {
	case <-c.stop:
		return io.EOF
	case f := <-c.frames:
		if f.err != nil {
			return s.remember(f.err)
		}
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *uint64         `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *RPCError       `json:"error"`
		}
		c.mu.Lock()
		id := c.id
		c.mu.Unlock()
		if json.Unmarshal(f.data, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID == nil || *envelope.ID != id || (envelope.Error == nil) == (envelope.Result == nil) {
			return s.remember(&ProtocolError{"invalid envelope or unexpected id"})
		}
		if err := json.Unmarshal(f.data, v); err != nil {
			return s.remember(&ProtocolError{"invalid JSON-RPC response"})
		}
		return nil
	}
}
func (s *boundedStream) Close() error { return s.client.p.Stdin().Close() }

func (c *Client) exited() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, e := c.p.Wait(ctx)
	if e != nil {
		_ = c.p.Kill(context.Background())
		status.Code = -1
	}
	select {
	case <-c.stderrDone:
	case <-ctx.Done():
	}
	return &ProcessExited{Code: status.Code, Detail: c.Tail(), OOM: status.OOM}
}
func (c *Client) Close() error {
	c.closed.Do(func() {
		c.p.Stdin().Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := c.p.Wait(ctx)
		if e != nil {
			c.closeErr = c.p.Kill(context.Background())
		}
		close(c.stop)
		_ = c.conn.Close()
		sandbox.CloseReaders(c.p)
		<-c.stderrDone
		<-c.readDone
		c.mu.Lock()
		_ = c.log.Close()
		c.mu.Unlock()
	})
	return c.closeErr
}
