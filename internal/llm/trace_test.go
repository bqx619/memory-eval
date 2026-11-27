package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func readTrace(t *testing.T, path string) []PromptTrace {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("trace permission", info, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("fixture-private-api-key")) {
		t.Fatal("API key in trace")
	}
	rows := []PromptTrace{}
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var row PromptTrace
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestTraceOptInRetriesReplayAndCassettePrivacy(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-private-api-key")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			w.WriteHeader(429)
			fmt.Fprint(w, "retry fixture")
			return
		}
		reply(w)
	}))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "record"
	e := engine(t, cfg)
	req := request()
	req.Messages = []Message{{"system", "full system\n你好"}, {"user", "private-prompt-only-marker\n</script>"}}
	without, err := e.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(cfg.LedgerPath), "prompts.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("default trace created", err)
	}
	// A previous broad mode must be tightened even when reusing an out directory.
	if err := os.WriteFile(path, []byte("stale trace"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	cfg.TracePrompts = true
	record := engine(t, cfg)
	with, err := record.Complete(context.Background(), req)
	if err != nil || with.RequestSHA != without.RequestSHA {
		t.Fatal("trace changed key", err)
	}
	rows := readTrace(t, path)
	if len(rows) != 2 || rows[0].Error != "LLMHTTPError (HTTP 429)" || rows[0].RawResponse != "retry fixture" || rows[1].ResponseText != "yes" || !reflect.DeepEqual(rows[1].Messages, req.Messages) {
		t.Fatal(rows)
	}
	files, _ := filepath.Glob(filepath.Join(cfg.CassetteDir, "*/*/*.*"))
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil || bytes.Contains(b, []byte("private-prompt-only-marker")) {
			t.Fatal("prompt leaked into cassette", err)
		}
	}
	hash, _, _ := cassetteSet(cfg.CassetteDir)
	s.Close()
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "")
	cfg.Mode = "replay"
	cfg.LedgerPath = filepath.Join(t.TempDir(), "ledger.jsonl")
	replay := engine(t, cfg)
	if _, err := replay.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rows = readTrace(t, filepath.Join(filepath.Dir(cfg.LedgerPath), "prompts.jsonl"))
	if len(rows) != 1 || !rows[0].CassetteHit || !reflect.DeepEqual(rows[0].Messages, req.Messages) || rows[0].ResponseText != "yes" || calls.Load() != 3 {
		t.Fatal(rows, calls.Load())
	}
	next, _, _ := cassetteSet(cfg.CassetteDir)
	if hash != next {
		t.Fatal("traced replay changed cassette contents")
	}
}

func TestTraceParallelRowsAndWriteFailure(t *testing.T) {
	t.Setenv("MEMEVAL_OPENAI_API_KEY", "fixture-private-api-key")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reply(w) }))
	defer s.Close()
	cfg := config(t, s.URL)
	cfg.Mode = "live"
	cfg.TracePrompts = true
	e := engine(t, cfg)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := request()
			req.SampleID = fmt.Sprint(i)
			if _, err := e.Complete(context.Background(), req); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rows := readTrace(t, filepath.Join(filepath.Dir(cfg.LedgerPath), "prompts.jsonl"))
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.SampleID] = true
	}
	if len(rows) != 12 || len(ids) != 12 {
		t.Fatal("trace rows interleaved/lost", len(rows), len(ids))
	}
	if err := e.prompts.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Complete(context.Background(), request()); err == nil {
		t.Fatal("trace write failure ignored")
	}
}
