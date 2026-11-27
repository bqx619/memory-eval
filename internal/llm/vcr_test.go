package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/config/redact"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	vcr "gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
)

func TestVCRPrivacyMatcherAndMigration(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "credential-only-marker")
	t.Setenv("OPENAI_ORG_ID", "ambient-org-secret")
	t.Setenv("OPENAI_PROJECT_ID", "ambient-project-secret")
	t.Setenv("OPENAI_BASE_URL", "http://invalid.example/no-network")
	t.Setenv("VCR_DEBUG", "true")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(keyHeader) != "" || r.Header.Get(clientHeader) != "" || r.Header.Get(providerHeader) != "" || r.Header.Get("OpenAI-Organization") != "" || r.Header.Get("OpenAI-Project") != "" {
			t.Error("internal/ambient configuration leaked to provider")
		}
		w.Header().Set("Set-Cookie", "cookie-private-marker")
		w.Header().Set("X-Provider-Secret", "credential-only-marker")
		reply(w)
	}))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "record"
	e := engine(t, cfg)
	r := request()
	r.Messages[0].Content = "private-prompt-marker"
	a, err := e.Complete(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(cfg.CassetteDir, r.Provider, a.RequestSHA[:2], a.RequestSHA)
	b, err := os.ReadFile(base + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-prompt-marker", "credential-only-marker", "cookie-private-marker", "ambient-org-secret", s.URL, "Authorization", "Set-Cookie"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("cassette contains forbidden field/value %s", secret)
		}
	}
	info, err := os.Stat(base + ".yaml")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("permissions", err)
	}
	c, err := vcr.Load(base)
	if err != nil || len(c.Interactions) != 1 {
		t.Fatal("not go-vcr format", err)
	}
	i := c.Interactions[0]
	if i.Request.Body != "" || len(i.Request.Form) != 0 || i.Request.Headers.Get(keyHeader) != a.RequestSHA {
		t.Fatal("request privacy or key")
	}
	query, _ := http.NewRequest("POST", "http://unused", nil)
	query.Header = i.Request.Headers.Clone()
	if !matcher(query, i.Request) {
		t.Fatal("key match failed")
	}
	for _, key := range []string{keyHeader, clientHeader, providerHeader} {
		orig := query.Header.Get(key)
		query.Header.Set(key, "different")
		if matcher(query, i.Request) {
			t.Fatal("matcher ignored", key)
		}
		query.Header.Set(key, orig)
	}
	// Synthesize an actual old response-only snapshot and migrate to a new root.
	legacy := filepath.Join(t.TempDir(), "legacy")
	old := cassette{Version: ClientVersion, RequestSHA: a.RequestSHA, Response: a, RawResponse: json.RawMessage(i.Response.Body), CostOriginal: e.cost(r, a.Usage)}
	data, _ := json.Marshal(old)
	path := filepath.Join(legacy, r.Provider, a.RequestSHA[:2], a.RequestSHA+".json")
	if err = atomicWrite(path, data); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "converted")
	if n, err := MigrateCassettes(legacy, dest, redact.Redactor{}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("migration changed source")
	}
	if _, err = MigrateCassettes(legacy, dest, redact.Redactor{}); err == nil {
		t.Fatal("existing destination overwritten")
	}
	s.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	for _, dir := range []string{legacy, dest} {
		cfg.Mode = "replay"
		cfg.CassetteDir = dir
		replay := engine(t, cfg)
		attempts := 0
		replay.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { attempts++; return nil, fmt.Errorf("network forbidden") })
		b, err := replay.Complete(context.Background(), r)
		if err != nil || !b.CassetteHit {
			t.Fatal(err)
		}
		b.CassetteHit = false
		if !reflect.DeepEqual(a, b) {
			t.Fatal("migration changed response")
		}
		r.SampleID += "-miss"
		if _, err = replay.Complete(context.Background(), r); err == nil {
			t.Fatal("missing replay accepted")
		}
		r.SampleID = request().SampleID
		if attempts != 0 || *replay.Billing().CostNew != 0 {
			t.Fatal("replay attempted HTTP or spent money")
		}
	}
}

func TestBeforeSaveStripsCredentialsIncludingResponseEcho(t *testing.T) {
	i := &vcr.Interaction{Request: vcr.Request{Headers: http.Header{"Authorization": {"Bearer secret-marker"}}, Body: "private-request-marker"}, Response: vcr.Response{Code: 200, Body: `{"message":"secret-marker"}`, Headers: http.Header{"X-Key": {"secret-marker"}}}}
	if err := sanitizeInteraction(i, "sha", "provider", cassette{Response: Response{Text: "secret-marker"}}, redact.New([]string{"secret-marker"})); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(i)
	if bytes.Contains(b, []byte("secret-marker")) || bytes.Contains(b, []byte("Authorization")) || bytes.Contains(b, []byte("private-request-marker")) {
		t.Fatal("credential survived BeforeSave")
	}
}
