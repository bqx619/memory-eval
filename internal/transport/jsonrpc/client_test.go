package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/sandbox"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	inR    *io.PipeReader
	inW    *io.PipeWriter
	outR   *io.PipeReader
	outW   *io.PipeWriter
	errR   *io.PipeReader
	errW   *io.PipeWriter
	done   chan struct{}
	once   sync.Once
	code   int
	killed bool
}

func fake() *fakeProcess {
	p := &fakeProcess{done: make(chan struct{})}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	p.errR, p.errW = io.Pipe()
	return p
}
func (p *fakeProcess) Stdin() io.WriteCloser { return p.inW }
func (p *fakeProcess) Stdout() io.Reader     { return p.outR }
func (p *fakeProcess) Stderr() io.Reader     { return p.errR }
func (p *fakeProcess) PID() int              { return 0 }
func (p *fakeProcess) Wait(ctx context.Context) (sandbox.ExitStatus, error) {
	select {
	case <-p.done:
		return sandbox.ExitStatus{Code: p.code}, nil
	case <-ctx.Done():
		return sandbox.ExitStatus{}, ctx.Err()
	}
}
func (p *fakeProcess) finish(code int) {
	p.once.Do(func() { p.code = code; p.inR.Close(); p.outW.Close(); p.errW.Close(); close(p.done) })
}
func (p *fakeProcess) Kill(context.Context) error { p.killed = true; p.finish(-1); return nil }
func client(t *testing.T, p *fakeProcess, o Options) *Client {
	t.Helper()
	c, e := New(p, t.TempDir(), o)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.finish(0); c.Close() })
	return c
}
func TestFramesAndSequentialIDs(t *testing.T) {
	p := fake()
	c := client(t, p, Options{})
	go func() {
		defer p.finish(0)
		r := bufio.NewReader(p.inR)
		for id := 1; id <= 2; id++ {
			line, _ := r.ReadBytes('\n')
			var request map[string]any
			_ = json.Unmarshal(line, &request)
			if request["id"] != float64(id) {
				return
			}
			fmt.Fprintln(p.outW, "log text\n[1,2]\n{\"jsonrpc\":\"2.0\",\"method\":\"notice\"}")
			fmt.Fprintf(p.outW, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"text\":\"汉字\"}}\n", id)
		}
	}()
	for range 2 {
		var got map[string]string
		if e := c.Call(context.Background(), "echo", map[string]string{}, &got); e != nil || got["text"] != "汉字" {
			t.Fatalf("%v %v", got, e)
		}
	}
}
func TestLargeFrameWithoutNewline(t *testing.T) {
	p := fake()
	c := client(t, p, Options{MaxFrameBytes: 128})
	go func() { r := bufio.NewReader(p.inR); r.ReadBytes('\n'); fmt.Fprint(p.outW, strings.Repeat("x", 40000)) }()
	var large *FrameTooLarge
	if e := c.Call(context.Background(), "big", nil, nil); !errors.As(e, &large) || !p.killed {
		t.Fatalf("%v", e)
	}
}
func TestTimeoutKillsBlockedWriter(t *testing.T) {
	p := fake()
	c := client(t, p, Options{RequestTimeout: 10 * time.Millisecond})
	var timeout *Timeout
	if e := c.Call(context.Background(), "blocked", nil, nil); !errors.As(e, &timeout) || !p.killed {
		t.Fatalf("%v", e)
	}
}
func TestCrashTailAndRPCError(t *testing.T) {
	t.Run("crash", func(t *testing.T) {
		p := fake()
		c := client(t, p, Options{Secrets: []string{"private-value"}})
		go func() {
			bufio.NewReader(p.inR).ReadBytes('\n')
			fmt.Fprintln(p.errW, "private-value token=secret")
			p.finish(17)
		}()
		var exit *ProcessExited
		if e := c.Call(context.Background(), "crash", nil, nil); !errors.As(e, &exit) || exit.Code != 17 || strings.Contains(exit.Detail, "private-value") || strings.Contains(exit.Detail, "token=secret") {
			t.Fatalf("%+v %v", exit, e)
		}
	})
	t.Run("rpc", func(t *testing.T) {
		p := fake()
		c := client(t, p, Options{})
		go func() {
			bufio.NewReader(p.inR).ReadBytes('\n')
			fmt.Fprintln(p.outW, `{"jsonrpc":"2.0","id":1,"error":{"code":-42,"message":"verbatim","data":{"x":1}}}`)
		}()
		var rpc *RPCError
		if e := c.Call(context.Background(), "echo", nil, nil); !errors.As(e, &rpc) || rpc.Message != "verbatim" || string(rpc.Data) != `{"x":1}` {
			t.Fatal(e)
		}
	})
}
func TestInvalidEnvelopes(t *testing.T) {
	for _, line := range []string{`{"jsonrpc":"2.0","id":7,"result":{}}`, `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1}}`, `{"jsonrpc":"1.0","id":1,"result":{}}`, `{"jsonrpc":"2.0","id":null,"result":{}}`, "{broken}"} {
		t.Run(line, func(t *testing.T) {
			p := fake()
			c := client(t, p, Options{})
			go func() { bufio.NewReader(p.inR).ReadBytes('\n'); fmt.Fprintln(p.outW, line) }()
			var protocol *ProtocolError
			if e := c.Call(context.Background(), "echo", nil, nil); !errors.As(e, &protocol) {
				t.Fatal(e)
			}
		})
	}
}
func TestRingBound(t *testing.T) {
	p := fake()
	c := client(t, p, Options{})
	c.logBytes([]byte(strings.Repeat("x", 100000)))
	if len(c.Tail()) != 64<<10 {
		t.Fatal(len(c.Tail()))
	}
}
