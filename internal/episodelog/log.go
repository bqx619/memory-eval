// Package episodelog persists credential-redacted, bounded SUT diagnostics.
package episodelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/config/redact"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

const Redaction = "credentials only; dataset text is not redacted"

type Options struct {
	Disabled bool
	FileCap  int64
	Secrets  []string
}
type Summary struct {
	Dir                     string `json:"dir"`
	Episodes                int    `json:"episodes"`
	Files                   int    `json:"files"`
	Bytes                   int64  `json:"bytes"`
	TruncatedFiles          int    `json:"truncated_files"`
	Redaction               string `json:"redaction"`
	KeptEPSUnredactedStderr int    `json:"kept_eps_unredacted_stderr"`
	Disabled                bool   `json:"disabled"`
}
type Run struct {
	mu       sync.Mutex
	dir      string
	options  Options
	redactor redact.Redactor
	index    *os.File
	episodes []*Episode
	summary  Summary
	closed   bool
	err      error
}
type Meta struct {
	EpisodeDir string `json:"episode_dir,omitempty"`
	Keep       bool   `json:"keep"`
	Sidecars   any    `json:"sidecars,omitempty"`
}
type Record struct {
	Seq       int    `json:"seq"`
	Namespace string `json:"namespace"`
	Dir       string `json:"dir"`
	StartedAt string `json:"started_at"`
	ClosedAt  string `json:"closed_at"`
	Meta
	Files     map[string]FileStats `json:"files"`
	Redaction string               `json:"redaction"`
}
type Episode struct {
	mu      sync.Mutex
	run     *Run
	dir     string
	record  Record
	writers map[string]*Writer
	closed  bool
	err     error
}

var component = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func safeName(s string) bool { return s != "." && s != ".." && component.MatchString(s) }
func New(ctx context.Context, out string, o Options) (*Run, error) {
	if o.Disabled {
		return nil, nil
	}
	if o.FileCap == 0 {
		o.FileCap = DefaultFileCap
	}
	if o.FileCap < MinFileCap {
		return nil, fmt.Errorf("log-file-cap must be at least %d bytes (including omission marker)", MinFileCap)
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		return nil, err
	}
	dir := filepath.Join(out, "logs")
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &Run{dir: dir, options: o, redactor: redact.From(ctx), index: f, summary: Summary{Dir: "logs", Redaction: Redaction, Files: 1}}, nil
}
func (r *Run) Episode(ns string) (*Episode, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, io.ErrClosedPipe
	}
	if !safeName(ns) {
		return nil, fmt.Errorf("invalid log namespace")
	}
	seq := len(r.episodes) + 1
	name := fmt.Sprintf("%04d-%s", seq, ns)
	dir := filepath.Join(r.dir, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	e := &Episode{run: r, dir: dir, writers: map[string]*Writer{}, record: Record{Seq: seq, Namespace: ns, Dir: name, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Redaction: Redaction, Files: map[string]FileStats{}}}
	r.episodes = append(r.episodes, e)
	e.Writer("sut.stderr.log")
	e.Writer("rpc-errors.jsonl")
	return e, nil
}
func (e *Episode) Dir() string {
	if e == nil {
		return ""
	}
	return e.dir
}
func (e *Episode) RelativeDir() string {
	if e == nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join("logs", e.record.Dir))
}
func (e *Episode) Writer(name string) io.Writer {
	if e == nil {
		return io.Discard
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		e.err = errors.Join(e.err, io.ErrClosedPipe)
		return io.Discard
	}
	if !safeName(name) {
		e.err = errors.Join(e.err, fmt.Errorf("invalid log filename"))
		return io.Discard
	}
	if w := e.writers[name]; w != nil {
		return w
	}
	w := newWriter(filepath.Join(e.dir, name), e.run.options.FileCap, e.run.options.Secrets, e.run.redactor)
	e.writers[name] = w
	return w
}
func (e *Episode) Sidecar(name string) io.Writer { return e.Writer("sidecar-" + name + ".log") }
func (e *Episode) Init(name string, i int) io.Writer {
	return e.Writer(fmt.Sprintf("init-%s-%d.log", name, i))
}
func (e *Episode) RPCError(method string, err error) {
	if e == nil || err == nil {
		return
	}
	row := map[string]any{"method": method, "error": err.Error(), "time": time.Now().UTC().Format(time.RFC3339Nano)}
	var rpc *jsonrpc.RPCError
	if errors.As(err, &rpc) {
		row["rpc_error"] = rpc
	}
	var detailed interface{ SafeDetail() string }
	if errors.As(err, &detailed) {
		row["detail"] = detailed.SafeDetail()
	}
	// Redact JSON string values structurally before streaming, preserving valid
	// JSON even when a credential contains quotes or escaped newlines.
	b, encodeErr := e.run.marshal(row)
	if encodeErr != nil {
		e.mu.Lock()
		e.err = errors.Join(e.err, encodeErr)
		e.mu.Unlock()
		return
	}
	_, _ = e.Writer("rpc-errors.jsonl").Write(append(b, '\n'))
}
func (e *Episode) Close(meta Meta) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return e.err
	}
	e.closed = true
	e.record.Meta = meta
	e.record.ClosedAt = time.Now().UTC().Format(time.RFC3339Nano)
	names := make([]string, 0, len(e.writers))
	for name := range e.writers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := e.writers[name]
		e.err = errors.Join(e.err, w.Close())
		e.record.Files[name] = w.Stats()
	}
	b, err := e.run.marshal(e.record)
	if err == nil {
		err = os.WriteFile(filepath.Join(e.dir, "episode.json"), append(b, '\n'), 0600)
	}
	e.err = errors.Join(e.err, err)
	r := e.run
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		_, err = r.index.Write(append(b, '\n'))
		e.err = errors.Join(e.err, err)
	}
	r.summary.Episodes++
	r.summary.Files += len(e.writers) + 1
	r.summary.Bytes += int64(2 * (len(b) + 1))
	for _, st := range e.record.Files {
		r.summary.Bytes += st.BytesWritten
		if st.Omitted > 0 {
			r.summary.TruncatedFiles++
		}
	}
	if meta.Keep && meta.EpisodeDir != "" {
		r.summary.KeptEPSUnredactedStderr++
	}
	r.err = errors.Join(r.err, e.err)
	return e.err
}

// Apply credential masking to decoded values, so credentials containing JSON
// punctuation cannot corrupt episode metadata or RPC error records.
func (r *Run) marshal(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err = d.Decode(&v); err != nil {
		return nil, err
	}
	filter := func(s string) string { return r.redactor.Text(jsonrpc.Redact(s, r.options.Secrets)) }
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return filter(x)
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, value := range x {
				out[filter(k)] = walk(value)
			}
			return out
		}
		return v
	}
	return json.Marshal(walk(v))
}
func (r *Run) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		err := r.err
		r.mu.Unlock()
		return err
	}
	episodes := append([]*Episode{}, r.episodes...)
	r.mu.Unlock()
	for _, e := range episodes {
		_ = e.Close(Meta{})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.err = errors.Join(r.err, r.index.Close())
	}
	return r.err
}
func (r *Run) Summary() Summary {
	if r == nil {
		return Summary{Disabled: true, Redaction: Redaction}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summary
}
