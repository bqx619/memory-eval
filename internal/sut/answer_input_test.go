package sut

import (
	"context"
	"encoding/json"
	"io"
	"memeval/internal/procexec"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPBridgeAnswerInputProvenance(t *testing.T) {
	root, _ := filepath.Abs("../../suts/_bridge")
	for _, runtime := range []string{"python3", "node"} {
		for _, tc := range []struct {
			name             string
			document         any
			missing, invalid bool
		}{
			{name: "known", document: "a"},
			{name: "empty", document: []string{}},
			{name: "missing", missing: true},
			{name: "mixed", document: "unknown", invalid: true},
			{name: "mixed-null", document: "unknown", invalid: true},
			{name: "unknown", document: "unknown", invalid: true},
			{name: "future", document: "future-event", invalid: true},
			{name: "duplicate", document: []string{"a", "a"}, invalid: true},
			{name: "null"},
			{name: "number", document: 7, invalid: true},
			{name: "wrong-array-type", document: []any{"a", 1}, invalid: true},
			{name: "null-array-element", document: []any{nil}, invalid: true},
		} {
			t.Run(runtime+"/"+tc.name, func(t *testing.T) {
				exe, err := exec.LookPath(runtime)
				if err != nil {
					t.Skip(err)
				}
				item := map[string]any{"id": "memory", "text": "rewritten observation"}
				if !tc.missing {
					item["document_id"] = tc.document
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/recall" {
						items := []any{item}
						if tc.name == "mixed" {
							items = append([]any{map[string]any{"id": "unattributed", "text": "observation"}}, items...)
						} else if tc.name == "mixed-null" {
							items = append([]any{map[string]any{"id": "unattributed", "text": "observation", "document_id": nil}}, items...)
						}
						json.NewEncoder(w).Encode(map[string]any{"results": items})
					} else {
						io.WriteString(w, `{}`)
					}
				}))
				defer server.Close()
				route := filepath.Join(t.TempDir(), "routes.json")
				writeFixture(t, route, `{"version":1,"methods":{"reset":{"method":"DELETE","path":"/reset"},"ingest":{"path":"/ingest"},"retrieve":{"path":"/recall","items":"$.results","map":{"id":"$.id","text":"$.text","source_ids":"$.document_id"}}}}`)
				raw, _ := json.Marshal(map[string]any{"routes": route, "base_url": server.URL})
				args := []string{"-B", "-m", "memeval_bridge", "--adapter", "memeval_bridge.http:Adapter", "--config", string(raw)}
				if runtime == "node" {
					args = []string{filepath.Join(root, "node/index.mjs"), "--adapter", filepath.Join(root, "node/http.mjs"), "--config", string(raw)}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, args...)
				cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONPATH="+filepath.Join(root, "python"))
				cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"reset","params":{"ns":"test"}}
{"jsonrpc":"2.0","id":2,"method":"ingest","params":{"ns":"test","events":[{"id":"a","content":"original memory"}]}}
{"jsonrpc":"2.0","id":3,"method":"retrieve","params":{"ns":"test","request":{"query":"q","budget":{"top_k":10,"max_context_chars":1000}}}}
{"jsonrpc":"2.0","id":4,"method":"close"}
`)
				out, err := procexec.Output(cmd)
				if err != nil {
					t.Fatal(err, string(out))
				}
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				if len(lines) != 4 {
					t.Fatal(string(out))
				}
				var reply struct {
					Result struct {
						Items []map[string]any `json:"items"`
					} `json:"result"`
					Error *struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err = json.Unmarshal([]byte(lines[2]), &reply); err != nil {
					t.Fatal(err)
				}
				if tc.invalid {
					if reply.Error == nil || reply.Error.Code != -32000 {
						t.Fatalf("invalid citation washed into accepted response: %s", lines[2])
					}
					return
				}
				if reply.Error != nil || len(reply.Result.Items) != 1 {
					t.Fatalf("valid observation rejected: %s", lines[2])
				}
				got := reply.Result.Items[0]
				if got["text"] != "rewritten observation" || got["status"] != nil {
					t.Fatalf("bridge changed text or decided answer-mode support: %s", lines[2])
				}
				ids, ok := got["source_ids"].([]any)
				want := 0
				if tc.name == "known" {
					want = 1
				}
				if !ok || len(ids) != want || want == 1 && ids[0] != "a" {
					t.Fatal(got)
				}
			})
		}
	}
}
