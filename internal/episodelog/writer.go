package episodelog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/config/redact"
	"memeval/internal/transport/jsonrpc"
	"os"
	"strings"
	"sync"
)

const DefaultFileCap int64 = 32 << 20

// The complete omission marker must fit alongside the streamed head.
const MinFileCap int64 = 256
const lineBuffer = 32 << 10

type FileStats struct {
	BytesTotal   int64 `json:"bytes_total"`
	BytesWritten int64 `json:"bytes_written"`
	Omitted      int64 `json:"omitted"`
}

// Writer buffers lines before redacting and bounds both the retained log and
// unterminated lines. Write errors are sticky, surfaced by Close, so a failed
// log disk cannot stop draining a child pipe and deadlock the child.
type Writer struct {
	mu              sync.Mutex
	file            *os.File
	cap             int64
	secrets, values []string
	redactor        redact.Redactor
	carry           int
	raw             []byte
	pending         []byte
	continuation    string
	head            int64
	tail            []byte
	stats           FileStats
	closed          bool
	err             error
}

func newWriter(path string, cap int64, secrets []string, r redact.Redactor) *Writer {
	r = redact.New(append(append([]string{}, r.Values...), jsonrpc.SecretValues(secrets)...))
	w := &Writer{cap: cap, secrets: jsonrpc.SecretValues(secrets), redactor: r}
	w.values = append(w.values, w.secrets...)
	for _, v := range r.Values {
		w.values = append(w.values, v)
		b, _ := json.Marshal(v)
		w.values = append(w.values, string(b[1:len(b)-1]))
	}
	for _, v := range w.values {
		w.carry = max(w.carry, len(v)-1)
	}
	w.file, w.err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	return w
}
func (w *Writer) Write(p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	n := len(p)
	for len(p) > 0 {
		take := min(len(p), lineBuffer)
		w.raw = append(w.raw, p[:take]...)
		p = p[take:]
		w.flushSecrets(false)
	}
	return n, nil
}
func (w *Writer) filter(s string) string { return w.redactor.Text(jsonrpc.Redact(s, w.secrets)) }

// Literal values are removed before heuristic credential matching. Otherwise a
// heuristic such as sk-* starting inside a known secret could split that secret,
// or hold an entire unbounded line while waiting for the token terminator.
func (w *Writer) flushSecrets(final bool) {
	cut := len(w.raw)
	if !final {
		cut -= w.carry
	}
	if cut <= 0 {
		return
	}
	raw := string(w.raw)
	previous := -1
	for previous != cut {
		previous = cut
		for _, v := range w.values {
			if v == "" {
				continue
			}
			for off := 0; off < cut; {
				at := strings.Index(raw[off:], v)
				if at < 0 {
					break
				}
				at += off
				if at < cut && at+len(v) > cut {
					cut = at
				}
				off = at + 1
			}
		}
	}
	if cut == 0 {
		return
	}
	p := []byte(w.redactor.Text(raw[:cut]))
	w.raw = w.raw[cut:]
	for len(p) > 0 {
		n := min(len(p), lineBuffer)
		w.pending = append(w.pending, p[:n]...)
		p = p[n:]
		w.flush(false)
	}
}
func (w *Writer) flush(final bool) {
	for len(w.pending) > 0 {
		if w.continuation != "" {
			i := 0
			for i < len(w.pending) && jsonrpc.CredentialContinues(w.continuation, w.pending[i]) {
				i++
			}
			w.pending = w.pending[i:]
			if len(w.pending) == 0 {
				return
			}
			w.continuation = ""
		}
		cut := len(w.pending)
		if !final {
			// Retain partial credential labels, e.g. "Authorization: Bear".
			limit := len(w.pending) - 32
			if limit <= 0 {
				return
			}
			if i := bytes.LastIndexByte(w.pending[:limit], '\n'); i >= 0 {
				cut = i + 1
			} else if len(w.pending) > lineBuffer+32 {
				cut = limit
			} else {
				return
			}
		}
		raw := string(w.pending)
		for _, m := range jsonrpc.CredentialMatches(raw) {
			if m.Start < cut && m.End > cut {
				if m.Start == 0 && !final {
					// An unknown token can be longer than our line limit. Emit one marker,
					// then discard only its lexical continuation, never unrelated text.
					w.retain([]byte("[REDACTED]"))
					w.pending = w.pending[m.End:]
					if m.End == len(raw) {
						w.continuation = m.Kind
					}
					cut = -1
					break
				}
				cut = m.Start
			}
		}
		if cut < 0 {
			continue
		}
		if cut == 0 {
			return
		}
		w.retain([]byte(w.filter(raw[:cut])))
		w.pending = w.pending[cut:]
	}
}
func (w *Writer) retain(p []byte) {
	w.stats.BytesTotal += int64(len(p))
	headCap := w.cap / 2
	if n := min(int64(len(p)), headCap-w.head); n > 0 {
		w.write(p[:n])
		w.head += n
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	tailCap := int(w.cap - headCap)
	if len(p) >= tailCap {
		w.tail = append(w.tail[:0], p[len(p)-tailCap:]...)
	} else {
		if excess := len(w.tail) + len(p) - tailCap; excess > 0 {
			copy(w.tail, w.tail[excess:])
			w.tail = w.tail[:len(w.tail)-excess]
		}
		w.tail = append(w.tail, p...)
	}
}
func (w *Writer) write(p []byte) {
	if w.err != nil {
		return
	}
	n, err := w.file.Write(p)
	w.stats.BytesWritten += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = errors.Join(w.err, err)
}
func omissionMarker(omitted, head, tail int64) string {
	return fmt.Sprintf("\n[memeval: %d bytes omitted; head %d + tail %d retained]\n", omitted, head, tail)
}
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.flushSecrets(true)
	w.flush(true)
	w.closed = true
	if w.stats.BytesTotal > w.cap {
		tail := int64(len(w.tail))
		for {
			omitted := w.stats.BytesTotal - w.head - tail
			marker := omissionMarker(omitted, w.head, tail)
			available := w.cap - w.head - int64(len(marker))
			if tail <= available {
				w.stats.Omitted = omitted
				w.write([]byte(marker))
				break
			}
			tail--
		}
		w.tail = w.tail[int64(len(w.tail))-tail:]
	}
	w.write(w.tail)
	w.tail = nil
	w.pending = nil
	w.raw = nil
	if w.file != nil {
		w.err = errors.Join(w.err, w.file.Close())
	}
	return w.err
}
func (w *Writer) Stats() FileStats {
	if w == nil {
		return FileStats{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// NewFileWriter reuses the credential-safe streaming writer for install logs.
func NewFileWriter(path string, cap int64, secrets []string, r redact.Redactor) *Writer {
 return newWriter(path,cap,secrets,r)
}
