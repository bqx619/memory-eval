package sut

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/procexec"
	"memeval/internal/protocol"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pythonPath(t *testing.T) string {
	t.Helper()
	raw, e := procexec.Output(exec.CommandContext(t.Context(), "uv", "python", "find", "--offline", "3.12"))
	p := strings.TrimSpace(string(raw))
	if e != nil {
		t.Skip("offline Python unavailable:", e)
	}
	return p
}
func writeFixture(t *testing.T, p, body string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixtureManifest(t *testing.T, kind string) (*Manifest, string) {
	t.Helper()
	testoracle.FakeDocker(t).Configure(t)
	root := t.TempDir()
	project := filepath.Join(root, "fixture")
	if e := copyTree("../../suts/_bridge", filepath.Join(root, "_bridge")); e != nil {
		t.Fatal(e)
	}
	writeFixture(t, filepath.Join(project, "requirements.in"), "fixture-sdk==1.0.0\n")
	writeFixture(t, filepath.Join(project, "fixture_sdk.py"), "VERSION='1.0.0'\n")
	writeFixture(t, filepath.Join(project, "requirements.lock.txt"), "fixture-sdk==1.0.0 --hash=sha256:"+strings.Repeat("a", 64)+"\n")
	writeFixture(t, filepath.Join(project, "adapter.py"), `from memeval_bridge import AdapterBase, handle, visible, limited
import fixture_sdk
class Adapter(AdapterBase):
 def describe(self):
  print('SDK log must use stderr')
  return {'name':'fixture','version':fixture_sdk.VERSION,'capabilities':['retrieve','dump','answer'], 'read_only_queries':True,'limitations':[],'config':{}}
 def reset(self, ns): self.events=[];self.seq=0
 def ingest(self, ns, events): self.events.extend(events);self.seq+=1;return handle(self.seq,len(events))
 def ready(self,ns,h,deadline): return visible()
 def retrieve(self,ns,req): return limited([{'id':e['id'],'text':e['content'],'score':None,'source_ids':[e['id']]} for e in self.events],req)
 def answer(self,ns,req): return {'text':'answer'}
 def dump(self,ns): return {'items':[{'id':e['id'],'text':e['content'],'layer':'raw'} for e in self.events]}
`)
	raw := map[string]any{"schema_version": "3", "name": "fixture", "variant": "local", "adapter": map[string]any{"kind": "python-bridge", "transport": "jsonrpc-stdio"}, "runtime": map[string]any{"kind": "uv-python", "python": "3.12", "requirements": "requirements.in", "requirements_lock": "requirements.lock.txt"}, "bridge": map[string]any{"module": "adapter", "class": "Adapter", "config": map[string]any{}}, "process": map[string]any{"command": []string{}, "env": map[string]string{}}, "sandbox": map[string]any{"backend": "container", "network": "none"}, "ready": map[string]any{"strategy": "sync"}, "telemetry_env": map[string]string{"FIXTURE_TELEMETRY": "off"}}
	if kind == "node" {
		node, e := exec.LookPath("node")
		if e != nil {
			t.Skip(e)
		}
		b, e := procexec.Output(exec.Command(node, "--version"))
		if e != nil {
			t.Fatal(e)
		}
		raw["runtime"] = map[string]any{"kind": "node", "version": strings.TrimPrefix(strings.TrimSpace(string(b)), "v")}
		raw["adapter"] = map[string]any{"kind": "generic", "transport": "jsonrpc-stdio"}
		delete(raw, "bridge")
		raw["process"] = map[string]any{"command": []string{"{node}", "{env}/bridge/node/index.mjs", "--adapter", "{project}/adapter.mjs", "--config", "{}"}, "env": map[string]string{}}
		writeFixture(t, filepath.Join(project, "package.json"), `{"name":"fixture","version":"1.0.0","private":true}`)
		writeFixture(t, filepath.Join(project, "package-lock.json"), `{"name":"fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0"}}}`)
		writeFixture(t, filepath.Join(project, "adapter.mjs"), `export class Adapter {describe(){console.log('SDK log');return {name:'fixture',version:'1',capabilities:['retrieve','dump'],read_only_queries:true,limitations:[],config:{}}} reset(){this.events=[];this.seq=0} ingest(ns,events){this.events.push(...events);return {batch_seq:++this.seq,accepted:events.length,filtered:0,failed:0}} ready(){return {visible:true,stages:[],warnings:[]}} retrieve(ns,r){return {items:this.events.slice(0,r.budget.top_k).map(e=>({id:e.id,text:e.content,source_ids:[e.id],score:null}))}} dump(){return {items:this.events.map(e=>({id:e.id,text:e.content}))}} close(){} }`)
	}
	b, _ := json.Marshal(raw)
	writeFixture(t, filepath.Join(project, "manifest.json"), string(b))
	m, e := Load(filepath.Join(project, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	return m, root
}
func wheelFixture(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "wheels")
	os.MkdirAll(dir, 0700)
	f, e := os.Create(filepath.Join(dir, "fixture_sdk-1.0.0-py3-none-any.whl"))
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	files := map[string]string{"fixture_sdk.py": "VERSION='1.0.0'\n", "fixture_sdk-1.0.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: fixture-sdk\nVersion: 1.0.0\n", "fixture_sdk-1.0.0.dist-info/WHEEL": "Wheel-Version: 1.0\nGenerator: fixture\nRoot-Is-Purelib: true\nTag: py3-none-any\n", "fixture_sdk-1.0.0.dist-info/RECORD": ""}
	for n, b := range files {
		w, _ := z.Create(n)
		io.WriteString(w, b)
	}
	z.Close()
	f.Close()
	return dir
}
func nodeArchive(t *testing.T, root string) string {
	t.Helper()
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip(e)
	}
	f, e := os.Create(filepath.Join(root, "node.tar.gz"))
	if e != nil {
		t.Fatal(e)
	}
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	data, e := os.ReadFile(node)
	if e != nil {
		t.Fatal(e)
	}
	for n, b := range map[string][]byte{"node/bin/node": data, "node/lib/node_modules/npm/bin/npm-cli.js": []byte(`const fs=require('node:fs');if(process.argv[2]!=='ci')throw Error('must use npm ci');fs.writeFileSync('npm-ci-proof','local lock installed');`)} {
		if e = tw.WriteHeader(&tar.Header{Name: n, Mode: 0755, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		tw.Write(b)
	}
	tw.Close()
	gz.Close()
	f.Close()
	return f.Name()
}
func TestEnvironmentRoundTrip(t *testing.T)          { testEnvironmentRoundTrip(t, false) }
func TestEnvironmentRoundTripContainer(t *testing.T) { testEnvironmentRoundTrip(t, true) }
func testEnvironmentRoundTrip(t *testing.T, fakeContainer bool) {
	for _, kind := range []string{"uv-python", "node"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "uv-python" {
				pythonPath(t)
			}
			m, root := fixtureManifest(t, kind)
			o := InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")}
			l, e := Install(t.Context(), m, o)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { Destroy(context.Background(), o.Cache, m.Name, false) })
			if e = CheckEnv(l); e != nil {
				t.Fatal(e)
			}
			before, _ := FileHash(filepath.Join(l.EnvDir, "env.lock.json"))
			again, e := Install(t.Context(), m, o)
			if e != nil || again.EnvDir != l.EnvDir {
				t.Fatal(e)
			}
			after, _ := FileHash(filepath.Join(l.EnvDir, "env.lock.json"))
			if before != after {
				t.Fatal("install not idempotent")
			}
			a := &Adapter{Manifest: m, Lock: l, Backend: container.New(container.DefaultConfig())}
			if fakeContainer {
				withFixtureContainer(t, a)
			}
			if e = a.Reset(t.Context(), "ns"); e != nil {
				t.Fatal(e)
			}
			h, e := a.Ingest(t.Context(), "ns", []protocol.PublicEvent{{ID: "source", Content: "text", Role: "user"}})
			if e != nil || h.Accepted != 1 {
				t.Fatal(e, h)
			}
			ready, e := a.Ready(t.Context(), "ns", h, time.Now().Add(time.Second))
			if e != nil || !ready.Visible {
				t.Fatal(e)
			}
			r, e := a.Retrieve(t.Context(), "ns", protocol.RetrieveRequest{Query: "text", Budget: protocol.Budget{TopK: 1, MaxContextChars: 100}})
			if e != nil || len(r.Items) != 1 || r.Items[0].SourceIDs[0] != "source" {
				t.Fatal(e, r)
			}
			d, e := a.Dump(t.Context(), "ns")
			if e != nil || len(d.Items) != 1 {
				t.Fatal(e, d)
			}
			if kind == "uv-python" {
				r, e := a.Answer(t.Context(), "ns", protocol.RetrieveRequest{})
				if e != nil || r.Text != "answer" {
					t.Fatal(e, r)
				}
			}
			if e = a.Close(t.Context()); e != nil {
				t.Fatal(e)
			}
			if e = CheckEnv(l); e != nil {
				t.Fatal("run changed env", e)
			}
			writeFixture(t, filepath.Join(l.EnvDir, "tamper"), "changed")
			if e = CheckEnv(l); e == nil {
				t.Fatal("tampering accepted")
			}
			os.Remove(filepath.Join(l.EnvDir, "tamper"))
			if e = Destroy(t.Context(), o.Cache, m.Name, false); e != nil {
				t.Fatal(e)
			}
			if _, e = os.Stat(l.EnvDir); !os.IsNotExist(e) {
				t.Fatal("environment retained")
			}
			if e = Destroy(t.Context(), o.Cache, m.Name, false); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestPythonProjectContracts(t *testing.T) {
	python := pythonPath(t)
	cmd := exec.Command(python, "-B", "../../suts/_bridge/python/test_contracts.py")
	root, _ := filepath.Abs("../../suts/_bridge/python")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1", "PYTHONPATH=" + root, "EP=" + t.TempDir()}
	out, e := procexec.CombinedOutput(cmd)
	if e != nil {
		t.Fatalf("%v: %s", e, out)
	}
}
func TestArtifactRejectsBadSHA(t *testing.T) {
	m, _ := fixtureManifest(t, "node")
	dir := t.TempDir()
	path := filepath.Join(dir, "local")
	writeFixture(t, path, "fixture")
	_, e := runtimeArchive(t.Context(), m, InstallOptions{Offline: true, RuntimeArchive: path, RuntimeSHA256: strings.Repeat("0", 64)}, t.TempDir(), map[string]string{})
	if e == nil {
		t.Fatal("bad hash accepted")
	}
}
func TestArchiveRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../outside", "runtime/../../outside"} {
		t.Run(fmt.Sprint(name), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "bad.tar.gz")
			f, _ := os.Create(p)
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Typeflag: tar.TypeReg, Size: 1})
			tw.Write([]byte("x"))
			tw.Close()
			gz.Close()
			f.Close()
			if e := unpackRuntime(p, t.TempDir()); e == nil {
				t.Fatal("archive traversal accepted")
			}
		})
	}
}

func TestNodeProjectContracts(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("Node unavailable")
	}
	cmd := exec.Command(node, "../../suts/_bridge/node/test_contracts.mjs", t.TempDir())
	cmd.Env = append(installEnv(""), "EP="+t.TempDir())
	out, e := procexec.CombinedOutput(cmd)
	if e != nil {
		t.Fatalf("%v: %s", e, out)
	}
}
func TestEnvironmentDestroyKillsTree(t *testing.T) {
	m, root := fixtureManifest(t, "node")
	l, e := Install(t.Context(), m, InstallOptions{Cache: filepath.Join(root, "cache"), Offline: true})
	if e != nil {
		t.Fatal(e)
	}
	b := container.New(container.DefaultConfig())
	ep, e := sandbox.NewEpisode()
	if e != nil {
		t.Fatal(e)
	}
	s, e := m.Spec(l, ep, "tree", false)
	if e != nil {
		t.Fatal(e)
	}
	s.Command = []string{l.Executable, "-e", `process.on('SIGTERM',()=>{});const p=require('node:child_process').spawn(process.execPath,['-e',"process.on('SIGTERM',()=>{});setTimeout(()=>{},60000)"],{stdio:'ignore'});setTimeout(()=>console.log(p.pid),100);setTimeout(()=>{},60000)`}
	h, e := b.Prepare(t.Context(), s)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Destroy(context.Background(), h)
	p, e := b.Spawn(t.Context(), h)
	if e != nil {
		t.Fatal(e)
	}
	defer sandbox.CloseReaders(p)
	var child int
	if _, e = fmt.Fscan(p.Stdout(), &child); e != nil {
		t.Fatal(e)
	}
	wait := make(chan struct{})
	go func() { p.Wait(context.Background()); close(wait) }()
	if e = b.Destroy(t.Context(), h); e != nil {
		t.Fatal(e)
	}
	if e = Destroy(t.Context(), m.CacheDir, m.Name, false); e != nil {
		t.Fatal(e)
	}
	select {
	case <-wait:
	case <-time.After(3 * time.Second):
		t.Fatal("leader survived destroy")
	}
	if sandbox.Alive(child) {
		t.Fatal("child survived destroy", child)
	}
	if _, e = os.Stat(l.EnvDir); !os.IsNotExist(e) {
		t.Fatal("env directory survived")
	}
}

func TestBinaryEnvironment(t *testing.T) {
	m, root := fixtureManifest(t, "node")
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip(e)
	}
	sha, _ := FileHash(node)
	m.Runtime.Kind = "binary"
	m.Runtime.Executable = "bin/runtime"
	m.Runtime.Sources = map[string]Artifact{platform(): {URL: node, SHA256: sha}}
	m.Process.Command[0] = "{binary}"
	raw, _ := json.Marshal(m)
	writeFixture(t, m.Path, string(raw))
	_, e = Install(t.Context(), m, InstallOptions{Offline: true, Cache: filepath.Join(root, "cache")})
	if e == nil || !strings.Contains(e.Error(), "not supported in P8 v1") {
		t.Fatal(e)
	}
}

func TestBridgeProtocolFailures(t *testing.T) {
	python := pythonPath(t)
	root := t.TempDir()
	module := `from memeval_bridge import AdapterBase
class Adapter(AdapterBase):
 def describe(self):
  print('sdk stdout noise')
  return {'name':'fixture'}
 def reset(self,ns): pass
 def ingest(self,ns,events): return {'batch_seq':1,'accepted':999,'filtered':0,'failed':0}
`
	writeFixture(t, filepath.Join(root, "fixture.py"), module)
	bridge, _ := filepath.Abs("../../suts/_bridge/python")
	call := func(payload string, limit string) string {
		cmd := exec.Command(python, "-B", "-m", "memeval_bridge", "--adapter", "fixture:Adapter", "--max-frame-bytes", limit)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "PYTHONPATH=" + bridge + ":" + root}
		cmd.Stdin = strings.NewReader(payload)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		b, e := procexec.Output(cmd)
		if e != nil {
			t.Fatal(e, stderr.String())
		}
		return string(b)
	}
	payload := `{"jsonrpc":"2.0","id":1,"method":"describe"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"reset","params":{"ns":"ns"}}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"ingest","params":{"ns":"ns","events":[]}}` + "\n" + `{"jsonrpc":"2.0","id":4,"method":"answer","params":{"ns":"ns","request":{}}}` + "\n" + `{"jsonrpc":"2.0","id":5,"method":"close"}` + "\n"
	out := call(payload, "2048")
	if strings.Contains(out, "sdk stdout noise") || !strings.Contains(out, "ingest accounting mismatch") || !strings.Contains(out, "-32601") {
		t.Fatal(out)
	}
	out = call(strings.Repeat("x", 129)+"\n", "128")
	if !strings.Contains(out, "frame too large") {
		t.Fatal(out)
	}
}
func TestManifestV3AndTelemetry(t *testing.T) {
	for _, name := range []string{"mem0/manifest.json", "mem0/manifest-infer.json", "mastra/manifest.json", "mastra/manifest-semantic.json", "graphiti/manifest.json"} {
		m, e := Load("../../suts/" + name)
		if e != nil {
			t.Fatal(name, e)
		}
		if m.SchemaVersion != "3" {
			t.Fatal(name)
		}
		m.Process.EnvPassthrough = nil
		m.LLM = LLM{}
		env, e := m.Environment("/episode", "/venv")
		if e != nil {
			t.Fatal(name, e)
		}
		for k, v := range m.TelemetryEnv {
			if env[k] != strings.ReplaceAll(v, "{ep}", "/episode") {
				t.Fatal(name, k)
			}
		}
	}
}
