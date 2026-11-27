package scoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"memeval/internal/config/dotenv"
	"memeval/internal/procexec"
	"memeval/internal/suite"
	"memeval/internal/testoracle"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLongMemEvalOfficialEdges(t *testing.T) {
	ev := []suite.Event{{SourceID: "a:0", Session: "a"}, {SourceID: "a:1", Session: "a"}, {SourceID: "b:0", Session: "b"}}
	m := LongMemEval(ev, []string{"a:0", "a:0"}, []string{"a:0"}, []string{"a"})
	if *m[LongMemEvalPrefix+"turn.ndcg_any@5"] != 1 {
		t.Fatal(m)
	}
	// One returned turn, two relevant corpus turns => session IDCG is 2.
	if *m[LongMemEvalPrefix+"session.ndcg_any@5"] != .5 {
		t.Fatal(m)
	}
	m = LongMemEval(ev, nil, nil, nil)
	if *m[LongMemEvalPrefix+"turn.recall_all@5"] != 1 || *m[LongMemEvalPrefix+"turn.ndcg_any@5"] != 0 {
		t.Fatal(m)
	}
}
func TestLongMemEvalOfficialPythonOracle(t *testing.T) {
	python := dotenv.Value("MEMEVAL_ORACLE_PYTHON")
	if python == "" {
		home, _ := os.UserHomeDir()
		python = filepath.Join(home, ".cache/memeval/longmemeval-oracle/.venv/bin/python")
	}
	if _, err := os.Stat(python); err != nil {
		t.Skip("official oracle requires offline numpy venv; set MEMEVAL_ORACLE_PYTHON")
	}
	type fixture struct {
		Corpus      []string `json:"corpus"`
		Gold        []string `json:"gold"`
		SessionGold []string `json:"session_gold"`
		Ranking     []int    `json:"ranking"`
	}
	rng := rand.New(rand.NewPCG(7301, 42))
	fixtures := []fixture{}
	got := []map[string]*float64{}
	for iteration := 0; iteration < 256; iteration++ {
		n := 1 + rng.IntN(150)
		f := fixture{Corpus: []string{}, Gold: []string{}, SessionGold: []string{}, Ranking: []int{}}
		events := []suite.Event{}
		sg := map[string]bool{}
		sessions := 1 + rng.IntN(25)
		for i := 0; i < n; i++ {
			sid := fmt.Sprintf("session_%d", rng.IntN(sessions))
			id := fmt.Sprintf("%s_%d", sid, i)
			events = append(events, suite.Event{SourceID: id, Session: sid})
			f.Corpus = append(f.Corpus, id)
			if rng.IntN(8) == 0 {
				f.Gold = append(f.Gold, id)
				sg[sid] = true
			}
		}
		for _, e := range events {
			if sg[e.Session] {
				f.SessionGold = append(f.SessionGold, e.Session)
				delete(sg, e.Session)
			}
		}
		indices := rng.Perm(n)
		length := rng.IntN(n + 1)
		pred := []string{}
		for _, i := range indices[:length] {
			f.Ranking = append(f.Ranking, i)
			pred = append(pred, f.Corpus[i])
		}
		fixtures = append(fixtures, f)
		got = append(got, LongMemEval(events, pred, f.Gold, f.SessionGold))
	}
	// NumPy 2 removed asfarray; binary relevance conversion is identical via asarray(dtype=float).
	script := `import sys,json,importlib.util,numpy as np
sys.dont_write_bytecode=True
if not hasattr(np,'asfarray'): np.asfarray=lambda a: np.asarray(a,dtype=float)
spec=importlib.util.spec_from_file_location('official',sys.argv[1]);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
out=[]
for f in json.load(sys.stdin):
 row={}
 for k in (5,10,50):
  vals=m.evaluate_retrieval(f['ranking'],f['gold'],f['corpus'],k)
  for name,v in zip(('recall_any','recall_all','ndcg_any'),vals):row['longmemeval_official.turn.'+name+'@'+str(k)]=float(v)
 for k in (5,10):
  vals=m.evaluate_retrieval_turn2session(f['ranking'],f['gold'],f['corpus'],k)
  for name,v in zip(('recall_any','recall_all','ndcg_any'),vals):
   if name!='recall_any':row['longmemeval_official.session.'+name+'@'+str(k)]=float(v)
 out.append(row)
json.dump(out,sys.stdout)
`
	b, _ := json.Marshal(fixtures)
	cmd := exec.Command(python, "-c", script, testoracle.Upstream(t, "longmemeval/src/retrieval/eval_utils.py"))
	cmd.Stdin = bytes.NewReader(b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := procexec.Output(cmd)
	if err != nil {
		t.Fatalf("oracle: %v: %s", err, stderr.String())
	}
	var want []map[string]float64
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatal("oracle row count")
	}
	maxDiff := 0.
	for i, row := range want {
		for key, v := range row {
			if got[i][key] == nil {
				t.Fatal(key)
			}
			delta := math.Abs(*got[i][key] - v)
			maxDiff = math.Max(delta, maxDiff)
			if delta > 1e-9 {
				t.Fatalf("fixture %d %s Go %.15g Python %.15g", i, key, *got[i][key], v)
			}
		}
	}
	t.Logf("official oracle: %d fixtures × 13 metrics; max absolute diff %.3g", len(want), maxDiff)
}
