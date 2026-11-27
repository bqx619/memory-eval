package sut

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBridgeConfigCarriesRPCDeadline(t *testing.T) {
	for _, kind := range []string{"python-bridge", "http-python", "http-node", "config-token"} {
		for _, deadline := range []float64{0, 10.5, 300} {
			t.Run(fmt.Sprintf("%s/%g", kind, deadline), func(t *testing.T) {
				m := &Manifest{DefaultEnvFile: filepath.Join(t.TempDir(), "absent.env")}
				m.Adapter.Kind, m.Adapter.RequestTimeoutS, m.Adapter.MaxFrameBytes = kind, deadline, 16<<20
				m.Bridge = &Bridge{Module: "fixture", Class: "Adapter", BaseURL: "http://api:8888", Routes: "routes.json", Config: map[string]any{"request_timeout_s": 999, "timeout_s": 1, "ready_timeout_s": 2}}
				m.Config = m.Bridge.Config
				l := &Lock{ImageRoot: "/opt/memeval/env", Executable: "/opt/memeval/env/.venv/bin/python"}
				if strings.HasPrefix(kind, "http-") {
					m.Adapter.Kind = "http-bridge"
					if kind == "http-node" {
						m.Runtime.Kind = "node"
						l.Executable = "/opt/memeval/env/runtime/bin/node"
					}
				}
				if kind == "config-token" {
					m.Process.Command = []string{l.Executable, "--config", "{config_json}"}
				}
				before, _ := json.Marshal(m)
				s, err := m.Spec(l, t.TempDir(), "fixture", false)
				if err != nil {
					t.Fatal(err)
				}
				var config map[string]any
				for i, arg := range s.Command {
					if arg == "--config" {
						if err := json.Unmarshal([]byte(s.Command[i+1]), &config); err != nil {
							t.Fatal(err)
						}
					}
				}
				want := deadline
				if want == 0 {
					want = 30
				}
				if config["request_timeout_s"] != want || config["timeout_s"] != float64(1) || config["ready_timeout_s"] != float64(2) {
					t.Fatal("incorrect rendered deadline or explicit timeout overwritten", config)
				}
				after, _ := json.Marshal(m)
				if string(before) != string(after) {
					t.Fatal("rendering mutated manifest")
				}
			})
		}
	}
}

func TestHTTPBridgeRequestTimeoutContracts(t *testing.T) {
	root, _ := filepath.Abs("../../suts/_bridge")
	for _, runtime := range []string{"python3", "node"} {
		for _, tc := range []struct {
			name      string
			config    map[string]any
			wantError bool
		}{
			{"rpc-headroom", map[string]any{"request_timeout_s": 10}, false},
			{"explicit", map[string]any{"request_timeout_s": 10, "timeout_s": 1}, true},
			{"minimum", map[string]any{"request_timeout_s": 3}, true},
		} {
			t.Run(runtime+"/"+tc.name, func(t *testing.T) {
				exe, err := exec.LookPath(runtime)
				if err != nil {
					t.Skip(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" {
						select {
						case <-time.After(2 * time.Second):
						case <-r.Context().Done():
							return
						}
					}
					io.WriteString(w, `{}`)
				}))
				defer server.Close()
				path := filepath.Join(t.TempDir(), "routes.json")
				writeFixture(t, path, `{"version":1,"methods":{"reset":{"method":"DELETE","path":"/events"},"ingest":{"method":"POST","path":"/events","body":{"$":"events"}}}}`)
				tc.config["routes"], tc.config["base_url"] = path, server.URL
				raw, _ := json.Marshal(tc.config)
				args := []string{"-B", "-m", "memeval_bridge", "--adapter", "memeval_bridge.http:Adapter", "--config", string(raw)}
				if runtime == "node" {
					args = []string{filepath.Join(root, "node/index.mjs"), "--adapter", filepath.Join(root, "node/http.mjs"), "--config", string(raw)}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, args...)
				cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONPATH="+filepath.Join(root, "python"))
				cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"reset","params":{"ns":"test"}}
{"jsonrpc":"2.0","id":2,"method":"ingest","params":{"ns":"test","events":[{"id":"a","content":"alpha"}]}}
{"jsonrpc":"2.0","id":3,"method":"close"}
`)
				out, err := procexec.Output(cmd)
				if err != nil {
					t.Fatal(err, string(out))
				}
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				if len(lines) != 3 {
					t.Fatal(string(out))
				}
				var reply struct {
					ID     int `json:"id"`
					Result struct {
						Accepted int `json:"accepted"`
					} `json:"result"`
					Error *struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
						Data    struct {
							Type string `json:"type"`
						} `json:"data"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(lines[1]), &reply); err != nil {
					t.Fatal(err)
				}
				if reply.ID != 2 || (reply.Error != nil) != tc.wantError {
					t.Fatal(string(out))
				}
				if tc.wantError {
					message, typ := "HTTP timeout", "Error"
					if runtime == "python3" {
						message, typ = "timed out", "TimeoutError"
					}
					if reply.Error.Code != -32000 || reply.Error.Message != message || reply.Error.Data.Type != typ {
						t.Fatal(lines[1])
					}
				} else if reply.Result.Accepted != 1 {
					t.Fatal(lines[1])
				}
			})
		}
	}
}

func TestHTTPBridgeContracts(t *testing.T) {
	root, _ := filepath.Abs("../../suts/_bridge")
	for _, runtime := range []string{"python3", "node"} {
		for _, transport := range []string{"unix", "tcp"} {
			t.Run(runtime+"/"+transport, func(t *testing.T) {
				exe, err := exec.LookPath(runtime)
				if err != nil {
					t.Skip(err)
				}
				dir, err := sandbox.NewEpisode()
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(dir)
				var events []map[string]any
				var mu sync.Mutex
				requests := 0
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					requests++
					w.Header().Set("Content-Type", "application/json")
					switch r.Method + " " + r.URL.Path {
					case "DELETE /ns/test":
						events = nil
						io.WriteString(w, `{}`)
					case "POST /ns/test":
						json.NewDecoder(r.Body).Decode(&events)
						io.WriteString(w, `{}`)
					case "GET /ready/test":
						io.WriteString(w, `{"visible":true,"stages":[],"warnings":[]}`)
					case "GET /ns/test":
						offset := 0
						fmt.Sscan(r.URL.Query().Get("offset"), &offset)
						out := []map[string]any{}
						for i := offset; i < len(events) && i < offset+1; i++ {
							out = append(out, map[string]any{"id": events[i]["id"], "text": events[i]["content"], "sources": []any{events[i]["id"]}})
						}
						json.NewEncoder(w).Encode(map[string]any{"results": out})
					default:
						w.WriteHeader(404)
					}
				})
				network, address := "tcp4", "127.0.0.1:0"
				if transport == "unix" {
					network, address = "unix", filepath.Join(dir, "app.sock")
				}
				l, err := net.Listen(network, address)
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{Handler: handler}
				go server.Serve(l)
				defer server.Close()
				base := "http://" + l.Addr().String()
				if transport == "unix" {
					base = "http://unix:" + address
				}
				route := map[string]any{"method": "GET", "path": "/ns/{ns}", "query": map[string]any{"offset": map[string]string{"$": "offset"}}, "items": "$.results", "map": map[string]string{"id": "$.id", "text": "$.text", "source_ids": "$.sources"}, "pagination": map[string]any{"limit": 1, "max_pages": 4}}
				routes := map[string]any{"version": 1, "describe": map[string]any{"name": "http-fixture", "version": "1", "capabilities": []string{"retrieve", "dump"}, "read_only_queries": true, "limitations": []string{}}, "methods": map[string]any{"reset": map[string]string{"method": "DELETE", "path": "/ns/{ns}"}, "ingest": map[string]any{"path": "/ns/{ns}", "body": map[string]string{"$": "events"}}, "ready": map[string]string{"method": "GET", "path": "/ready/{ns}"}, "retrieve": route, "dump": route}}
				raw, _ := json.Marshal(routes)
				path := filepath.Join(dir, "routes.json")
				os.WriteFile(path, raw, 0600)
				cfg, _ := json.Marshal(map[string]any{"routes": path, "base_url": base})
				commands := []string{"-m", "memeval_bridge", "--adapter", "memeval_bridge.http:Adapter", "--config", string(cfg)}
				if runtime == "node" {
					commands = []string{filepath.Join(root, "node/index.mjs"), "--adapter", filepath.Join(root, "node/http.mjs"), "--config", string(cfg)}
				}
				messages := []any{map[string]any{"method": "describe"}, map[string]any{"method": "reset", "params": map[string]any{"ns": "test"}}, map[string]any{"method": "ingest", "params": map[string]any{"ns": "test", "events": []map[string]string{{"id": "a", "content": "alpha"}, {"id": "b", "content": "βeta"}}}}, map[string]any{"method": "ready", "params": map[string]any{"ns": "test", "handle": map[string]int{"batch_seq": 1, "accepted": 2, "filtered": 0, "failed": 0}}}, map[string]any{"method": "retrieve", "params": map[string]any{"ns": "test", "request": map[string]any{"query": "q", "budget": map[string]int{"top_k": 2, "max_context_chars": 100}}}}, map[string]any{"method": "dump", "params": map[string]string{"ns": "test"}}, map[string]any{"method": "reset", "params": map[string]string{"ns": "test"}}, map[string]any{"method": "dump", "params": map[string]string{"ns": "test"}}, map[string]any{"method": "close"}}
				var input strings.Builder
				for i, v := range messages {
					m := v.(map[string]any)
					m["id"] = i
					m["jsonrpc"] = "2.0"
					b, _ := json.Marshal(m)
					input.Write(b)
					input.WriteByte('\n')
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, commands...)
				cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONPATH="+filepath.Join(root, "python"))
				cmd.Stdin = strings.NewReader(input.String())
				out, err := procexec.Output(cmd)
				if err != nil {
					t.Fatal(err, string(out))
				}
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				if len(lines) != len(messages) {
					t.Fatal(string(out))
				}
				var responses []map[string]json.RawMessage
				for _, line := range lines {
					var r map[string]json.RawMessage
					if err = json.Unmarshal([]byte(line), &r); err != nil {
						t.Fatal(err)
					}
					if r["error"] != nil {
						t.Fatal(line)
					}
					responses = append(responses, r)
				}
				for _, idx := range []int{4, 5} {
					var r struct {
						Items []struct {
							ID      string   `json:"id"`
							Sources []string `json:"source_ids"`
						}
					}
					json.Unmarshal(responses[idx]["result"], &r)
					if len(r.Items) != 2 || r.Items[0].ID != "a" || r.Items[1].Sources[0] != "b" {
						t.Fatal(string(responses[idx]["result"]))
					}
				}
				if !strings.Contains(string(responses[7]["result"]), `"items": []`) && !strings.Contains(string(responses[7]["result"]), `"items":[]`) {
					t.Fatal("reset didn't empty service")
				}
				mu.Lock()
				defer mu.Unlock()
				if requests < 10 {
					t.Fatal("pagination bypassed", requests)
				}
			})
		}
	}
}
