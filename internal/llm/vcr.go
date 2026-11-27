package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memeval/internal/config/redact"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
	vcr "gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
)

const (
	keyHeader      = "X-Memeval-Request-Sha"
	clientHeader   = "X-Memeval-Client-Version"
	providerHeader = "X-Memeval-Provider"
	metadataHeader = "X-Memeval-Response-Metadata"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A distinct discard writer prevents upstream VCR_DEBUG from enabling dumps of
// prompts, private endpoints or credentials. Debug logging is never user data.
type silentVCR struct{}

func (silentVCR) Write(p []byte) (int, error) { return len(p), nil }

// secureCassetteFS preserves 0600 atomic writes and read-only legacy support.
// Old JSON is converted in memory and replayed by go-vcr, never by a second
// response-cache implementation. New recordings always use the go-vcr format.
type secureCassetteFS struct{}

func (secureCassetteFS) WriteFile(path string, b []byte) error { return atomicWrite(path, b) }
func (secureCassetteFS) IsFileExists(path string) bool {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return true
	}
	_, err := os.Stat(strings.TrimSuffix(path, ".yaml") + ".json")
	return !os.IsNotExist(err)
}
func (secureCassetteFS) ReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if !os.IsNotExist(err) {
		return b, err
	}
	legacy := strings.TrimSuffix(path, ".yaml") + ".json"
	b, err = os.ReadFile(legacy)
	if err != nil {
		return nil, err
	}
	c, err := legacyVCR(legacy, b, redact.Redactor{})
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(c)
}

func matcher(r *http.Request, saved vcr.Request) bool {
	return r.Method == http.MethodPost && saved.Method == http.MethodPost &&
		r.Header.Get(keyHeader) != "" && r.Header.Get(keyHeader) == saved.Headers.Get(keyHeader) &&
		r.Header.Get(clientHeader) == ClientVersion && saved.Headers.Get(clientHeader) == ClientVersion &&
		r.Header.Get(providerHeader) == saved.Headers.Get(providerHeader)
}

// Drop every original request field and every unapproved response header. The
// opaque SHA binds provider/endpoint/API/model/request/template/client/sample;
// neither prompts nor private endpoint URLs are needed for matching.
func sanitizeInteraction(i *vcr.Interaction, sha, provider string, saved cassette, redactor redact.Redactor) error {
	if i.Response.Code < 200 || i.Response.Code >= 300 {
		i.DiscardOnSave = true
		return nil
	}
	raw, err := redactCassetteJSON(redactor, []byte(i.Response.Body))
	if err != nil {
		return err
	}
	saved.RawResponse = nil // raw wire response lives only in response.body
	meta, err := json.Marshal(saved)
	if err == nil {
		meta, err = redactCassetteJSON(redactor, meta)
	}
	if err != nil {
		return err
	}
	i.Request = vcr.Request{Method: http.MethodPost, URL: "https://memeval.invalid/response-only", Headers: http.Header{keyHeader: {sha}, clientHeader: {ClientVersion}, providerHeader: {provider}}}
	i.Response = vcr.Response{Code: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Body: string(raw), ContentLength: int64(len(raw)), Headers: http.Header{"Content-Type": {"application/json"}, metadataHeader: {string(meta)}}}
	return nil
}

// Preserve raw wire byte ordering unless a value actually needs redaction.
func redactCassetteJSON(r redact.Redactor, b []byte) ([]byte, error) {
	if !json.Valid(b) {
		return nil, fmt.Errorf("invalid JSON response")
	}
	if bytes.Equal(r.Encoded(b), b) {
		return b, nil
	}
	return r.JSON(b)
}

type recording struct {
	recorder *recorder.Recorder
	client   *http.Client
	replay   bool
	saved    *cassette
	final    cassette
	redactor redact.Redactor
}

func (e *Engine) recording(r Request, sha string) (*recording, error) {
	session := &recording{client: e.http, redactor: e.cfg.Redactor}
	if e.cfg.Mode == "live" {
		return session, nil
	}
	path := filepath.Join(e.cfg.CassetteDir, r.Provider, sha[:2], sha)
	mode := recorder.ModeRecordOnly
	switch e.cfg.Mode {
	case "replay":
		mode = recorder.ModeReplayOnly
	case "record-once":
		mode = recorder.ModeRecordOnce
	}
	base := e.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clean := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Match headers are evaluator-local and must never reach any provider.
		wire := req.Clone(req.Context())
		wire.Header = req.Header.Clone()
		wire.Header.Del(keyHeader)
		wire.Header.Del(clientHeader)
		wire.Header.Del(providerHeader)
		return base.RoundTrip(wire)
	})
	rec, err := recorder.New(path, recorder.WithMode(mode), recorder.WithFS(secureCassetteFS{}),
		recorder.WithRealTransport(clean), recorder.WithSkipRequestLatency(true), recorder.WithMatcher(matcher),
		recorder.WithDebugWriter(silentVCR{}),
		recorder.WithHook(func(i *vcr.Interaction) error {
			return sanitizeInteraction(i, sha, r.Provider, session.final, session.redactor)
		}, recorder.BeforeSaveHook),
		recorder.WithHook(func(i *vcr.Interaction) error {
			if !i.WasReplayed() {
				return nil
			}
			var c cassette
			if json.Unmarshal([]byte(i.Response.Headers.Get(metadataHeader)), &c) != nil || c.Version != ClientVersion || c.RequestSHA != sha || c.Response.RequestSHA != sha || !json.Valid([]byte(i.Response.Body)) {
				return &Error{Kind: "CassetteInvalid"}
			}
			c.RawResponse = json.RawMessage(i.Response.Body)
			session.saved = &c
			return nil
		}, recorder.BeforeResponseReplayHook))
	if err != nil {
		if errors.Is(err, vcr.ErrCassetteNotFound) {
			return nil, &Error{Kind: "CassetteMiss"}
		}
		return nil, &Error{Kind: "CassetteInvalid"}
	}
	session.recorder = rec
	session.replay = !rec.IsRecording()
	copyClient := *e.http
	copyClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.Header = req.Header.Clone()
		req.Header.Set(keyHeader, sha)
		req.Header.Set(clientHeader, ClientVersion)
		req.Header.Set(providerHeader, r.Provider)
		return rec.RoundTrip(req)
	})
	session.client = &copyClient
	return session, nil
}

func (s *recording) save(sha string, resp Response, cost *float64) error {
	if s.recorder == nil || s.replay {
		return nil
	}
	s.final = cassette{Version: ClientVersion, RequestSHA: sha, Response: resp, CostOriginal: cost}
	if err := s.recorder.Stop(); err != nil {
		return &Error{Kind: "CassetteWriteError"}
	}
	return nil
}

func (e *Engine) replay(ctx context.Context, r Request, sha, endpoint string, s *recording) (Response, error) {
	// Even old response snapshots now pass through the provider's HTTP client and
	// go-vcr RoundTripper. No API key/status checks or capability probes are needed.
	_, _, _, callErr := e.call(ctx, endpoint, "", r, s.client)
	if callErr != nil && s.saved == nil {
		return Response{RequestSHA: sha}, &Error{Kind: "CassetteInvalid"}
	}
	if s.saved == nil {
		return Response{RequestSHA: sha}, &Error{Kind: "CassetteMiss"}
	}
	resp, err := e.sanitizeResponse(s.saved.Response)
	if err != nil {
		return Response{RequestSHA: sha}, &Error{Kind: "CassetteInvalid"}
	}
	resp.CassetteHit = true
	e.observeModel(r, resp)
	err = e.log(r, resp, s.saved.RawResponse, s.saved.CostOriginal, nil)
	return resp, err
}

func legacyVCR(path string, b []byte, redactor redact.Redactor) (*vcr.Cassette, error) {
	var old cassette
	sha := strings.TrimSuffix(filepath.Base(path), ".json")
	if json.Unmarshal(b, &old) != nil || len(sha) != 64 || old.Version != ClientVersion || old.RequestSHA != sha || old.Response.RequestSHA != sha || !json.Valid(old.RawResponse) {
		return nil, fmt.Errorf("invalid legacy cassette")
	}
	provider := filepath.Base(filepath.Dir(filepath.Dir(path)))
	i := &vcr.Interaction{Response: vcr.Response{Code: 200, Body: string(old.RawResponse)}}
	if err := sanitizeInteraction(i, sha, provider, old, redactor); err != nil {
		return nil, err
	}
	c := vcr.New(strings.TrimSuffix(path, ".json"))
	c.MarshalFunc = yaml.Marshal
	c.AddInteraction(i)
	return c, nil
}

// MigrateCassettes is an offline, one-time copy. Sources are never changed;
// destination must be new and outside the source tree. It is safe to inspect a
// partially completed destination if a malformed source stops migration.
func MigrateCassettes(source, dest string, redactor redact.Redactor) (int, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return 0, err
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return 0, err
	}
	rel, err := filepath.Rel(source, dest)
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("destination must be outside source")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		return 0, fmt.Errorf("destination must not exist")
	}
	count := 0
	err = filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed")
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return fmt.Errorf("unexpected legacy cassette file")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c, err := legacyVCR(path, b, redactor)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) != 3 || len(parts[1]) != 2 || parts[1] != parts[2][:2] {
			return fmt.Errorf("invalid legacy cassette path")
		}
		c.File = filepath.Join(dest, strings.TrimSuffix(relative, ".json")+".yaml")
		if err = c.SaveWithFS(secureCassetteFS{}); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

var _ vcr.FS = secureCassetteFS{}
var _ io.Writer = silentVCR{}
