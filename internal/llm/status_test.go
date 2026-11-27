package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetiredModelsNeverGoLiveButReplayWorks(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-status-key")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); reply(w) }))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "record"
	cfg.Now = func() time.Time { return time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) }
	r := request()
	r.Model = "gpt-4.1-2025-04-14"
	e := engine(t, cfg)
	if _, err := e.Complete(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	cfg.Now = func() time.Time { return time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC) }
	for _, mode := range []string{"live", "record", "replay", "record-once"} {
		cfg.Mode = mode
		e := engine(t, cfg)
		response, err := e.Complete(context.Background(), r)
		if mode == "replay" || mode == "record-once" {
			if err != nil || !response.CassetteHit {
				t.Fatal(mode, err)
			}
		} else if err == nil || err.Error() != "ModelRetired" {
			t.Fatal(mode, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("retired request sent", calls.Load())
	}
}

func TestExcludedModelsRefuseHTTPAndAllowOldCassette(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); reply(w) }))
	defer server.Close()
	cfg := config(t, server.URL)
	cfg.Now = func() time.Time { return time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) }
	r := request()
	r.Model = "gpt-4o-2024-08-06"
	for _, mode := range []string{"live", "record", "record-once"} {
		cfg.Mode = mode
		e := engine(t, cfg)
		if _, err := e.Complete(context.Background(), r); err == nil || err.Error() != "ModelExcluded" {
			t.Fatal(mode, err)
		}
	}
	// Construct an old-format cassette without calling the excluded provider.
	cfg.Mode = "replay"
	e := engine(t, cfg)
	sha, _, err := e.key(r)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"model":"gpt-4o-2024-08-06","choices":[{"message":{"content":"yes"}}]}`)
	cost := .01
	data, err := json.Marshal(cassette{Version: ClientVersion, RequestSHA: sha, Response: Response{RequestSHA: sha, Text: "yes", ModelReturned: r.Model}, RawResponse: raw, CostOriginal: &cost})
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(cfg.CassetteDir, r.Provider, sha[:2], sha+".json"), data); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"replay", "record-once"} {
		cfg.Mode = mode
		replay := engine(t, cfg)
		resp, err := replay.Complete(context.Background(), r)
		if err != nil || !resp.CassetteHit || resp.ModelReturned != r.Model {
			t.Fatal(mode, resp, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("excluded model reached HTTP")
	}
}
