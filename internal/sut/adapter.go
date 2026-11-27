package sut

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/adapter"
	"memeval/internal/config/redact"
	"memeval/internal/episodelog"
	"memeval/internal/protocol"
	"memeval/internal/sandbox"
	"memeval/internal/sidecar"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"time"
)

// Adapter owns a fresh process per Reset; no private suite data is accepted here.
type Adapter struct {
	Logs            *episodelog.Run
	episodeLog      *episodelog.Episode
	Manifest        *Manifest
	Lock            *Lock
	Backend         sandbox.Sandbox
	Keep            bool
	Client          *jsonrpc.Client
	Handle          sandbox.Handle
	Episodes        []map[string]any
	ProfileSHA256   string
	Unenforceable   bool
	LastStderr      string
	Services        *sidecar.Group
	ServiceEvidence []*sidecar.Evidence
	gateRelease     func()
	CaptureStderr   bool // installer diagnostics; ordinary runs keep the bounded tail
}

func (a *Adapter) Start(ctx context.Context, ns string) (err error) {
	defer a.sanitizeEvidence(ctx)
	if e := a.Close(ctx); e != nil {
		return e
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Lock.EnvDir != "" {
		release, e := environmentGate(a.Lock.EnvDir, false)
		if e != nil {
			return e
		}
		a.gateRelease = release
		if _, e = os.Stat(a.Lock.EnvDir); e != nil {
			a.gateRelease()
			a.gateRelease = nil
			return &NotInstalled{Manifest: a.Manifest.Path, Reason: "retired_host_env", Detail: "missing installed image"}
		}
	}
	log, e := a.Logs.Episode(ns)
	if e != nil {
		if a.gateRelease != nil {
			a.gateRelease()
			a.gateRelease = nil
		}
		return e
	}
	a.episodeLog = log
	defer func() {
		if err != nil {
			var isolation *sandbox.Unenforceable
			if errors.As(err, &isolation) {
				a.Unenforceable = true
			}
			a.episodeLog.RPCError("start", err)
			err = errors.Join(err, a.Close(context.WithoutCancel(ctx)))
		}
	}()
	h, e := a.Manifest.Prepare(ctx, a.Backend, a.Lock, ns, a.Keep)
	if e != nil {
		return e
	}
	a.Handle = h
	if binder, ok := a.Backend.(interface{ BindNamespace(sandbox.Handle, string) }); ok {
		binder.BindNamespace(h, ns)
	}
	a.ProfileSHA256 = h.ProfileSHA256()
	effective := h.EffectiveConfig()
	effective["ns"] = ns
	effective["log_dir"] = log.RelativeDir()
	a.Episodes = append(a.Episodes, effective)
	a.Services, e = sidecar.Start(ctx, a.Backend, h, log)
	for _, ev := range a.Services.Evidence() {
		ev.Error = jsonrpc.Redact(ev.Error, a.Manifest.Secrets())
		a.ServiceEvidence = append(a.ServiceEvidence, ev)
	}
	if e != nil {
		if ctx.Err() == nil || !errors.Is(e, ctx.Err()) {
			a.Unenforceable = true
		}
		return fmt.Errorf("%s", jsonrpc.Redact(e.Error(), a.Manifest.Secrets()))
	}
	p, e := a.Backend.Spawn(ctx, h)
	if e != nil {
		return e
	}

	logDir := h.Dir()
	if a.Manifest.Adapter.Kind == "native" {
		logDir = filepath.Join(logDir, "work")
	}
	c, e := jsonrpc.New(p, logDir, jsonrpc.Options{MaxFrameBytes: a.Manifest.Adapter.MaxFrameBytes, RequestTimeout: time.Duration(a.Manifest.Adapter.RequestTimeoutS * float64(time.Second)), Secrets: a.Manifest.Secrets(), Mirror: log.Writer("sut.stderr.log")})
	if e != nil {
		return e
	}
	a.Client = c
	if e = a.Services.AwaitUnmanaged(ctx); e != nil {
		if ctx.Err() == nil || !errors.Is(e, ctx.Err()) {
			a.Unenforceable = true
		}
		return e
	}
	return nil
}
func (a *Adapter) Call(ctx context.Context, method string, params, out any) (err error) {
	defer a.sanitizeEvidence(ctx)
	defer func() { a.episodeLog.RPCError(method, err) }()
	defer func() {
		if b, ok := a.Backend.(interface{ TimedOut(sandbox.Handle) bool }); ok && a.Handle != nil && b.TimedOut(a.Handle) {
			err = &sandbox.TimeoutError{Cause: err}
		}
	}()
	if a.Client == nil {
		return fmt.Errorf("adapter has no active process")
	}
	if e := a.Services.Audit(ctx); e != nil {
		a.Unenforceable = true
		return e
	}
	e := a.Client.Call(ctx, method, params, out)
	if audit := a.Services.Audit(ctx); audit != nil {
		a.Unenforceable = true
		return errors.Join(e, audit) // retain the RPC error even if the post-call audit also fails
	}
	var rpc *jsonrpc.RPCError
	if errors.As(e, &rpc) && rpc.Code == -32601 {
		return &adapter.UnsupportedError{Note: rpc.Message, Cause: e}
	}
	return e
}
func (a *Adapter) Describe(ctx context.Context) (d protocol.Describe, err error) {
	if err = a.Start(ctx, "describe"); err != nil {
		return
	}
	defer func() { err = errors.Join(err, a.Close(context.WithoutCancel(ctx))) }()
	err = a.Call(ctx, "describe", map[string]any{}, &d)
	return
}

func (a *Adapter) Reset(ctx context.Context, ns string) error {
	if e := a.Start(ctx, ns); e != nil {
		return e
	}
	return a.Call(ctx, "reset", map[string]any{"ns": ns}, nil)
}
func (a *Adapter) Ingest(ctx context.Context, ns string, events []protocol.PublicEvent) (protocol.IngestHandle, error) {
	var h protocol.IngestHandle
	e := a.Call(ctx, "ingest", map[string]any{"ns": ns, "events": events}, &h)
	return h, e
}
func (a *Adapter) Ready(ctx context.Context, ns string, h protocol.IngestHandle, deadline time.Time) (protocol.ReadyResult, error) {
	var r protocol.ReadyResult
	d := ""
	if !deadline.IsZero() {
		d = deadline.UTC().Format(time.RFC3339Nano)
	}
	e := a.Call(ctx, "ready", map[string]any{"ns": ns, "handle": h, "deadline": d}, &r)
	return r, e
}
func (a *Adapter) Retrieve(ctx context.Context, ns string, r protocol.RetrieveRequest) (protocol.RetrieveResponse, error) {
	var out protocol.RetrieveResponse
	e := a.Call(ctx, "retrieve", map[string]any{"ns": ns, "request": r}, &out)
	return out, e
}
func (a *Adapter) Answer(ctx context.Context, ns string, req protocol.RetrieveRequest) (protocol.AnswerResponse, error) {
	var r protocol.AnswerResponse
	e := a.Call(ctx, "answer", map[string]any{"ns": ns, "request": req}, &r)
	return r, e
}
func (a *Adapter) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	defer a.sanitizeEvidence(ctx)
	defer func() {
		if a.gateRelease != nil {
			a.gateRelease()
			a.gateRelease = nil
		}
	}()
	var errs []error
	meta := episodelog.Meta{Keep: a.Keep}
	if a.Handle != nil {
		meta.EpisodeDir = a.Handle.Dir()
	}
	if a.Client != nil {
		errs = append(errs, a.Client.Close())
		a.LastStderr = a.Client.Tail()
		if a.CaptureStderr {
			a.LastStderr = a.Client.DiagnosticLog()
		}
		a.Client = nil
	}
	if a.Services != nil {
		if e := a.Services.Stop(ctx); e != nil {
			a.Unenforceable = true
			errs = append(errs, e)
		}
	}
	if a.Handle != nil {
		if e := a.Backend.Destroy(ctx, a.Handle); e != nil {
			a.Unenforceable = true
			errs = append(errs, e)
		}
		updated := a.Handle.EffectiveConfig()
		for _, cfg := range a.Episodes {
			if cfg["episode_dir"] == a.Handle.Dir() {
				for k, v := range updated {
					cfg[k] = v
				}
			}
		}
		a.Handle = nil
	}
	if e := a.Services.VerifyStopped(ctx); e != nil {
		a.Unenforceable = true
		errs = append(errs, e)
	}
	meta.Sidecars = a.Services.Evidence()
	a.Services = nil
	a.episodeLog.RPCError("close", errors.Join(errs...))
	errs = append(errs, a.episodeLog.Close(meta))
	a.episodeLog = nil
	joined := errors.Join(errs...)
	var isolation *sandbox.Unenforceable
	if errors.As(joined, &isolation) {
		a.Unenforceable = true
	}
	return joined
}

func (a *Adapter) Dump(ctx context.Context, ns string) (protocol.DumpResponse, error) {
	var r protocol.DumpResponse
	e := a.Call(ctx, "dump", map[string]any{"ns": ns}, &r)
	if e == nil {
		e = adapter.ValidateDump(r)
	}
	return r, e
}

// BeginRun binds runtime-owned resource labels to the evaluator invocation.
func (a *Adapter) BeginRun(id string) {
	if b, ok := a.Backend.(interface{ BeginRun(string) }); ok {
		b.BeginRun(id)
	}
}

func (a *Adapter) sanitizeEvidence(ctx context.Context) {
	for _, ev := range a.ServiceEvidence {
		ev.Error = redact.From(ctx).Text(jsonrpc.Redact(redact.StripANSI(ev.Error), a.Manifest.Secrets()))
	}
}
