package sut

import (
	"context"
	"errors"
	"memeval/internal/adapter"
	"memeval/internal/protocol"
	"memeval/internal/sandbox/container"
	"memeval/internal/transport/jsonrpc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeInputExcludesDocumentation(t *testing.T) {
	m, root := fixtureManifest(t, "uv-python")
	doc := filepath.Join(m.Dir, "UPSTREAM_FACTS.md")
	writeFixture(t, doc, "original")
	a, e := inputHash(m)
	if e != nil {
		t.Fatal(e)
	}
	writeFixture(t, doc, "updated facts")
	writeFixture(t, filepath.Join(root, "_bridge", "README.md"), "new docs")
	b, e := inputHash(m)
	if e != nil || a != b {
		t.Fatal("documentation changed installation identity", e)
	}
	writeFixture(t, filepath.Join(m.Dir, "requirements.lock.txt"), "fixture==1")
	c, _ := inputHash(m)
	if c == b {
		t.Fatal("dependency lock not hashed")
	}
	writeFixture(t, filepath.Join(m.Dir, "adapter.py"), "changed")
	d, _ := inputHash(m)
	if d == c {
		t.Fatal("adapter not hashed")
	}
}

func TestInstallProbeFailureDiagnostics(t *testing.T) {
	pythonPath(t)
	m, root := fixtureManifest(t, "uv-python")
	writeFixture(t, filepath.Join(m.Dir, "adapter.py"), `from memeval_bridge import AdapterBase
import sys
class Adapter(AdapterBase):
 def describe(self):
  print('probe stderr api_key=fixture-secret',file=sys.stderr,flush=True)
  print('diagnostic padding\n' * 5000,file=sys.stderr,flush=True)
  raise RuntimeError('SDK unavailable token=fixture-secret')
`)
	_, e := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	var failure *InstallFailure
	if !errors.As(e, &failure) {
		t.Fatal(e)
	}
	b, err := os.ReadFile(failure.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "probe stderr") || !strings.Contains(string(b), "RuntimeError") || strings.Contains(string(b), "fixture-secret") {
		t.Fatal(string(b))
	}
	st, _ := os.Stat(failure.Path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	if _, e = CheckLock(m); e == nil {
		t.Fatal("failed install usable")
	}
	if len(strings.Split(failure.Preview, "\n")) > 20 {
		t.Fatal("preview exceeds 20 lines")
	}
	if e = Destroy(t.Context(), m.CacheDir, m.Name, false); e != nil {
		t.Fatal(e)
	}
}

func TestRPCUnsupportedBoundary(t *testing.T) {
	pythonPath(t)
	m, root := fixtureManifest(t, "uv-python")
	p := filepath.Join(m.Dir, "adapter.py")
	b, _ := os.ReadFile(p)
	s := strings.Replace(string(b), "from memeval_bridge import AdapterBase,", "from memeval_bridge import Unsupported, AdapterBase,", 1)
	s = strings.Replace(s, "def retrieve(self,ns,req): return limited(", "def retrieve(self,ns,req):\n  if req['query']=='unsupported': raise Unsupported('requested scope unavailable')\n  if req['query']=='broken': raise ValueError('SDK failed')\n  return limited(", 1)
	writeFixture(t, p, s)
	l, e := Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	if e != nil {
		t.Fatal(e)
	}
	defer Destroy(context.Background(), m.CacheDir, m.Name, false)
	a := &Adapter{Manifest: m, Lock: l, Backend: container.New(container.DefaultConfig())}
	defer a.Close(context.Background())
	if e = a.Reset(t.Context(), "ns"); e != nil {
		t.Fatal(e)
	}
	_, e = a.Retrieve(t.Context(), "ns", protocol.RetrieveRequest{Query: "unsupported"})
	var rpc *jsonrpc.RPCError
	if !errors.Is(e, adapter.ErrUnsupported) || !errors.As(e, &rpc) || rpc.Code != -32601 || e.Error() != "requested scope unavailable" {
		t.Fatal(e)
	}
	_, e = a.Retrieve(t.Context(), "ns", protocol.RetrieveRequest{Query: "broken"})
	if errors.Is(e, adapter.ErrUnsupported) || !errors.As(e, &rpc) || rpc.Code != -32000 {
		t.Fatal(e)
	}
}
