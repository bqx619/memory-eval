package container

import (
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/procexec"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFakeDockerConcurrentState(t *testing.T) {
	b, f := fixture(t)
	const ep = "concurrent-state"
	create := func(name string) {
		t.Helper()
		if _, err := b.call(t.Context(), "create", "--name", name, "--label", "memeval.episode="+ep, "fixture:image"); err != nil {
			t.Fatal(err)
		}
	}
	create("stable")
	for i := 0; i < 20; i++ {
		create(fmt.Sprintf("victim-%d", i))
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errs := make(chan error, 21)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for ctx.Err() == nil {
			raw, err := os.ReadFile(filepath.Join(f.State, "state.json"))
			var value map[string]any
			if err == nil {
				err = json.Unmarshal(raw, &value)
			}
			if err != nil {
				errs <- fmt.Errorf("unlocked snapshot reader: %w", err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			<-start
			for round := 0; round < 3; round++ {
				for _, args := range [][]string{
					{"ps", "-a", "--filter", "label=memeval.episode=" + ep, "-q"},
					{"container", "inspect", "stable"},
					{"rm", "-f", fmt.Sprintf("victim-%d", i)},
				} {
					if _, err := b.call(ctx, args...); err != nil {
						errs <- err
						return
					}
				}
			}
		})
	}
	close(start)
	wg.Wait()
	cancel()
	<-readerDone
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	ids, err := b.list(t.Context(), "container", ep)
	if err != nil || len(ids) != 1 {
		t.Fatal("lost updates/resurrected containers", ids, err)
	}
	if _, err = b.inspectContainer(t.Context(), "stable"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.call(t.Context(), "rm", "-f", "stable"); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(f.State, "state-*.json")); len(files) != 0 {
		t.Fatal("temporary state files leaked", files)
	}
	if !t.Failed() {
		t.Log("20 concurrent clients × 3 rounds × ps/inspect/rm = 180 operations; unlocked JSON reader saw only complete snapshots")
	}
}

func TestFakeDockerEmptyStateReadRetry(t *testing.T) {
	_, f := fixture(t)
	// Load only the state() AST: no CLI, real daemon or resource mutation code.
	script := `import ast, contextlib, fcntl, json, os, sys, tempfile, time
from pathlib import Path
from unittest.mock import patch
ROOT=Path(sys.argv[2])
tree=ast.parse(Path(sys.argv[1]).read_text())
node=next(n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name=='state')
exec(compile(ast.Module(body=[node],type_ignores=[]),'<state fixture>','exec'),globals())
file=ROOT/'state.json'
file.write_text('{"counter":0}')
original=Path.read_text
reads=[]
def transient(self,*a,**kw):
 if self==file:
  reads.append(1)
  if len(reads)==1:return ''
 return original(self,*a,**kw)
with patch.object(Path,'read_text',transient):
 with state() as value:value['counter']+=1
assert len(reads)==2 and json.loads(file.read_text())['counter']==1
file.write_text('')
try:
 with state():pass
except json.JSONDecodeError:pass
else:raise AssertionError('permanent corruption must not reset state')
print('empty read retried once; permanent corruption rejected')
`
	fixturePath, err := filepath.Abs("testdata/fake_docker.py")
	if err != nil {
		t.Fatal(err)
	}
	out, err := procexec.CombinedOutput(exec.CommandContext(t.Context(), f.Python, "-c", script, fixturePath, f.State))
	if err != nil || !strings.Contains(string(out), "empty read retried once") {
		t.Fatal(string(out), err)
	}
}
