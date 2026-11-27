package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"memeval/internal/config/dotenv"
	"memeval/internal/procexec"
	"memeval/internal/sut"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestGatewayFlagsAndMaskedEnvironment(t *testing.T) {
	for _, binding := range dotenv.Bindings {
		t.Setenv(binding.Env, os.Getenv(binding.Env))
	}
	file := filepath.Join(t.TempDir(), "fixture.env")
	if e := os.WriteFile(file, []byte("MEMEVAL_SUT_LLM_BASE_URL=http://127.0.0.1:8317/v1\nMEMEVAL_SUT_LLM_API_KEY=fake-gateway-key\nMEMEVAL_SUT_LLM_MODEL=from-file\nMEMEVAL_SUT_LLM_REASONING_EFFORT=medium\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"MEMEVAL_SUT_LLM_BASE_URL", "MEMEVAL_SUT_LLM_API_KEY", "MEMEVAL_SUT_LLM_MODEL", "MEMEVAL_SUT_LLM_REASONING_EFFORT"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	settings, undo, err := dotenv.Load(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer undo()
	t.Setenv("MEMEVAL_SUT_LLM_MODEL", "from-process")
	ctx := dotenv.With(t.Context(), settings)
	root := newRootCommand(ctx, io.Discard, io.Discard)
	cmd, _, err := root.Find([]string{"sandbox", "caps"})
	if err != nil {
		t.Fatal(err)
	}
	var got *sut.LLMGateway
	cmd.RunE = func(_ *cobra.Command, _ []string) error { var e error; got, e = sut.ReadLLMGateway(ctx); return e }
	root.SetArgs([]string{"sandbox", "caps", "--sut-llm-model", "from-flag", "--sut-llm-effort", "low"})
	if e := root.ExecuteContext(ctx); e != nil {
		t.Fatal(e)
	}
	if got.Model != "from-flag" || got.Effort != "low" || got.APIKey != "fake-gateway-key" || got.Source["MEMEVAL_SUT_LLM_MODEL"] != "flag" {
		t.Fatal("configuration precedence", got.Source)
	}
	var out, stderr bytes.Buffer
	if e := run(t.Context(), []string{"--env-file", file, "env"}, &out, &stderr); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String()+stderr.String(), "fake-gateway-key") {
		t.Fatal("env leaked gateway key")
	}
	var view struct{ Configuration []map[string]any }
	if e := json.Unmarshal(out.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, r := range view.Configuration {
		if r["key"] == "MEMEVAL_SUT_LLM_API_KEY" {
			found = true
			if r["prefix"] != "****" {
				t.Fatal(r)
			}
		}
	}
	if !found {
		t.Fatal("no masked gateway key")
	}
	root = newRootCommand(ctx, io.Discard, io.Discard)
	root.SetArgs([]string{"env", "--sut-llm-api-key", "forbidden"})
	if e := root.ExecuteContext(ctx); e == nil || !strings.Contains(e.Error(), "unknown flag") {
		t.Fatal("credential flag accepted", e)
	}
}

func TestContainerGovernorCLIEndToEnd(t *testing.T) {
	var calls, otherCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, "only-configured-gateway") }))
	defer server.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherCalls.Add(1) }))
	defer other.Close()
	cmd := exec.Command("go", "run", "./cmd/memeval", "__container-governor", "--listen", "127.0.0.1:0", "--target", strings.TrimPrefix(server.URL, "http://"), "--status-listen", "127.0.0.1:0", "--meter-dir", t.TempDir())
	cmd.Dir = dotenv.Root(".")
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local")
	stderr, e := cmd.StderrPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = procexec.Start(cmd); e != nil {
		t.Fatal(e)
	}
	var process *os.Process
	t.Cleanup(func() {
		if process != nil {
			_ = process.Kill()
		}
		_ = cmd.Process.Kill()
	})
	first := make(chan string, 1)
	scanned := make(chan string, 1)
	go func() {
		var log strings.Builder
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			log.WriteString(line + "\n")
			if strings.HasPrefix(line, "governor listening=") {
				first <- line
			}
		}
		scanned <- log.String()
	}()
	var line string
	select {
	case line = <-first:
	case <-time.After(45 * time.Second):
		t.Fatal("relay did not listen")
	}
	fields := strings.Fields(line)
	listen := strings.TrimPrefix(fields[1], "listening=")
	pid, e := strconv.Atoi(strings.TrimPrefix(fields[len(fields)-1], "pid="))
	if e != nil {
		t.Fatal(line)
	}
	process, _ = os.FindProcess(pid)
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", "http://"+listen+"/v1/chat/completions", strings.NewReader("fake-gateway-key"))
			req.Header.Set("Authorization", "Bearer fake-gateway-key")
			resp, e := client.Do(req)
			if e != nil {
				failures <- e
				return
			}
			defer resp.Body.Close()
			body, e := io.ReadAll(resp.Body)
			if e != nil || string(body) != "only-configured-gateway" {
				failures <- fmt.Errorf("unexpected relay response: %v", e)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	// An absolute-form request or CONNECT cannot choose another destination:
	// the proxy rejects both before any upstream attempt.
	for _, request := range []string{"GET " + other.URL + "/escape HTTP/1.1\r\nHost: elsewhere\r\nConnection: close\r\n\r\n", "CONNECT " + strings.TrimPrefix(other.URL, "http://") + " HTTP/1.1\r\nHost: elsewhere\r\nConnection: close\r\n\r\n"} {
		conn, e := net.DialTimeout("tcp", listen, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, e = io.WriteString(conn, request)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.ReadAll(conn)
		_ = conn.Close()
	}
	if otherCalls.Load() != 0 || calls.Load() != 20 {
		t.Fatal(calls.Load(), otherCalls.Load())
	}
	if e = process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not exit after SIGTERM")
	}
	log := <-scanned
	if strings.Contains(log, "fake-gateway-key") || !strings.Contains(log, "governor listening=") {
		t.Fatal(log)
	}
	t.Log("go run governor: 20 concurrent requests, absolute URL and CONNECT rejected, SIGTERM exit 0; no payload/key in stderr")
}
