package sut

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/protocol"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSandboxedHTTPEnvironment(t *testing.T)          { testSandboxedHTTPEnvironment(t, false) }
func TestSandboxedHTTPEnvironmentContainer(t *testing.T) { testSandboxedHTTPEnvironment(t, true) }
func testSandboxedHTTPEnvironment(t *testing.T, fakeContainer bool) {
	pythonPath(t)
	m, root := fixtureManifest(t, "uv-python")
	m.Adapter.Kind = "http-bridge"
	lsn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lsn.Addr().(*net.TCPAddr).Port
	lsn.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	m.Bridge = &Bridge{BaseURL: base, Routes: "routes.json"}
	m.Sidecars = []Sidecar{{Name: "http", Kind: "generic", Port: uint16(port), Command: []string{"{python}", "{project}/server.py", "{port}"}, Ready: sandbox.ServiceReady{HTTP: &sandbox.HTTPReady{URL: base + "/health", Status: 200}, TimeoutS: 5}, Stop: "SIGTERM"}}

	writeFixture(t, filepath.Join(m.Dir, "server.py"), `import http.server,socketserver,sys,json
events=[]
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def do_GET(self):
  result={'visible':True,'stages':[],'warnings':[]} if self.path=='/ready' else {'items':[{'id':e['id'],'text':e['content'],'source_ids':[e['id']]} for e in events]}
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(result).encode())
 def do_DELETE(self):
  events.clear();self.send_response(200);self.end_headers();self.wfile.write(b'{}')
 def do_POST(self):
  events.extend(json.loads(self.rfile.read(int(self.headers['Content-Length']))));self.send_response(200);self.end_headers();self.wfile.write(b'{}')
http.server.HTTPServer(("127.0.0.1",int(sys.argv[1])),Handler).serve_forever()
`)
	writeFixture(t, filepath.Join(m.Dir, "routes.json"), `{"version":1,"describe":{"name":"HTTP fixture","version":"1","capabilities":["retrieve","dump","read_only_queries"],"read_only_queries":true,"limitations":["synthetic HTTP service, not a project benchmark"]},"methods":{"reset":{"method":"DELETE","path":"/events"},"ingest":{"method":"POST","path":"/events","body":{"$":"events"}},"ready":{"method":"GET","path":"/ready"},"retrieve":{"method":"GET","path":"/events","items":"$.items","map":{"id":"$.id","text":"$.text","source_ids":"$.source_ids"}},"dump":{"method":"GET","path":"/events","items":"$.items","map":{"id":"$.id","text":"$.text","source_ids":"$.source_ids"}}}}`)
	raw, _ := json.Marshal(m)
	os.WriteFile(m.Path, raw, 0600)
	m, e := Load(m.Path)
	if e != nil {
		t.Fatal(e)
	}
	l, e := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	if e != nil {
		t.Fatal(e)
	}
	defer Destroy(context.Background(), m.CacheDir, m.Name, false)
	a := &Adapter{Manifest: m, Lock: l, Backend: container.New(container.DefaultConfig())}
	withFixtureContainer(t, a)
	defer a.Close(context.Background())
	if e = a.Reset(t.Context(), "ns"); e != nil {
		t.Fatal(e)
	}
	h, e := a.Ingest(t.Context(), "ns", []protocol.PublicEvent{{ID: "source", Content: "hello", Role: "user"}})
	if e != nil || h.Accepted != 1 {
		t.Fatal(h, e)
	}
	r, e := a.Ready(t.Context(), "ns", h, time.Now().Add(time.Second))
	if e != nil || !r.Visible {
		t.Fatal(r, e)
	}
	items, e := a.Retrieve(t.Context(), "ns", protocol.RetrieveRequest{Query: "hello", Budget: protocol.Budget{TopK: 1, MaxContextChars: 100}})
	if e != nil || len(items.Items) != 1 {
		t.Fatal(items, e)
	}
	dump, e := a.Dump(t.Context(), "ns")
	if e != nil || len(dump.Items) != 1 {
		t.Fatal(dump, e)
	}
	if e = a.Close(t.Context()); e != nil {
		t.Fatal(e)
	}
	if len(a.ServiceEvidence) != 1 || !a.ServiceEvidence[0].Destroyed {
		t.Fatal(a.ServiceEvidence)
	}
	if e = CheckEnv(l); e != nil {
		t.Fatal("runtime mutated read-only env", e)
	}
}
