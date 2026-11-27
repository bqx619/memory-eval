#!/bin/bash
cd "$(dirname "$0")/.." || exit 1
A=reports/p13-beam/logs; mkdir -p "$A"
D() { docker --context orbstack "$@"; }
SUITE=data/normalized/beam-100k@3205395e897e
go build -o bin/memeval ./cmd/memeval && bash scripts/build_linux_memeval.sh arm64 | tail -1 || exit 1
mkdir -p reports/p13-beam
run() { m=$1; input=$2; n=$(basename $(dirname $m))-$(python3 -c "import json;print(json.load(open('$m'))['variant'])")-$input
  bin/memeval env check $m --docker-context orbstack > "$A/check-$n.out" 2>&1 || { SECONDS=0; bin/memeval env install $m --docker-context orbstack > "$A/install-$n.out" 2>&1; echo "  install $m exit=$? ${SECONDS}s"; }
  out=reports/p13-beam/$n-beam100k-l40; rm -rf "${out:?}"; echo "##### $n start $(date +%T)"; SECONDS=0
  bin/memeval run --suite $SUITE --sut $m --mode fixed_reader --reset-policy incremental_per_episode --reader-input $input --reader-max-tokens 2048 --limit 40 --llm record --sut-llm-budget-tokens 20000000 --out "$out" --docker-context orbstack > "$A/$n.out" 2>&1; rc=$?
  echo "  exit=$rc ${SECONDS}s $(tail -1 "$A/$n.out" | cut -c1-200)"
  [ -f "$out/report.json" ] && python3 - "$out/report.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1])); s=r.get('summary') or {}
if not s: print("  REJECTED", r.get('reason')); sys.exit()
def flat(o,p=""):
    for k,v in (o or {}).items():
        if isinstance(v,dict) and 'mean' not in v: yield from flat(v,p+k+".")
        elif isinstance(v,dict) and v.get('mean') is not None: yield p+k, round(v['mean'],3), v.get('n')
        elif isinstance(v,(int,float)): yield p+k, v, None
print("  status", s.get('status_counts'), "| scores", [x for x in flat(r.get('scores'))][:6])
print("  metrics", {k:(round(v['mean'],3),v['n']) for k,v in (s.get('metrics') or {}).items() if isinstance(v,dict) and v.get('mean') is not None})
b=r.get('billing') or {}; sut=b.get('sut') or {}; ev=b.get('evaluator') or {}
print("  sut tokens", sut.get('prompt_tokens'), "calls", sut.get('calls'), "| evaluator", json.dumps(ev)[:200])
PY
  echo "  residue run $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ') governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; }
run suts/mem0/manifest.json items
run suts/cognee/manifest.json items
run suts/hindsight/manifest.json items
run suts/mem0/manifest-infer.json items
run suts/cognee/manifest-gateway.json items
run suts/hindsight/manifest-gateway.json items
echo "##### done $(date +%T)"
