#!/bin/bash
# P14: CogletNet comprehensive test + peer controls. See design/p14-cogletnet-comprehensive.md.
# Runs A (six variants, BEAM 100K full), B (MAB conflict_resolution 800: CogletNet + six variants + BM25),
# C (CogletNet PMB-75 repeat), D (CogletNet + BM25 on LongMemEval-S first 20). Continues past a failed run,
# stops on residue or when cumulative gateway prompt tokens exceed 150M.
set -u
cd "$(dirname "$0")/.." || exit 1
T=${TMPDIR:-/tmp}
S=$T/memeval-p14-$(date +%Y%m%d%H%M%S); mkdir -p "$S"
AUTH=${MEMEVAL_AUTH_RUN:?set MEMEVAL_AUTH_RUN to your local credential wrapper}
D() { docker --context orbstack "$@"; }
LOG=reports/p14-logs; mkdir -p "$LOG"
BEAM=data/normalized/beam-100k@3205395e897e; MAB=data/normalized/mab-conflict-resolution@7ea066982b14
PMB=data/normalized/pmb-retrieval.json; LME=data/normalized/longmemeval-s.json
SEED_BEAM=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089
SEED_PMB=$(python3 -c "import json; print(json.load(open('reports/p6-reference/pmb-mem0/report.json'))['run_seed'])")
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
TOTAL=0
go build -o bin/memeval ./cmd/memeval && bash scripts/build_linux_memeval.sh arm64 | tail -1 || exit 1
echo "##### binary $(shasum -a 256 bin/memeval | cut -c1-12) scratch $S $(date +%T)"
[ -x "$AUTH" ] || { echo "authrun missing at $AUTH"; exit 1; }

# CogletNet: fresh staging of the read-only project; install rebuilds only if the input changed.
python3 -B scripts/stage_cogletnet.py --source ../cogletnet --out "$S/staged" > "$LOG/stage.out" 2>&1 || { echo "staging failed"; cat "$LOG/stage.out" | tail -5; exit 1; }
CM=$S/staged/cogletnet/manifest.json; grep -o '"source_sha256[^,]*' "$LOG/stage.out" | head -1
SECONDS=0; "$AUTH" bin/memeval env check "$CM" --docker-context orbstack > "$LOG/cogletnet-check.out" 2>&1 || "$AUTH" bin/memeval env install "$CM" --docker-context orbstack --sut-llm-model gpt-6-luna --sut-llm-effort low > "$LOG/cogletnet-install.out" 2>&1; echo "  cogletnet env exit=$? ${SECONDS}s $(grep -o '"valid": [a-z]*' "$LOG/cogletnet-install.out" "$LOG/cogletnet-check.out" 2>/dev/null | head -1)"

summ() { python3 - "$1" <<'PY'
import json,sys,glob
p=sys.argv[1]
try: r=json.load(open(p+'/report.json'))
except Exception as e: print("  no report:", e); sys.exit()
s=r.get('summary') or {}
if not s: print("  REJECTED", r.get('reason','')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],3),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None}
keys=[k for k in top if k in ('recall','ndcg','mab_exact_match','mab_f1','beam.llm_judge_score','beam.event_ordering.final')]
sut=(r.get('billing') or {}).get('sut') or {}; ev=(r.get('billing') or {}).get('evaluator') or {}
from collections import Counter
errs=Counter((c.get('error_type') or c['status']) for c in r['cases'] if c['status']!='scored')
print("  status", s.get('status_counts'), "|", {k:top[k] for k in keys}, "| sut prompt", sut.get('prompt_tokens'), "| ev prompt", ev.get('prompt_tokens'), f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(3)))
PY
}
account() { python3 - "$1" "$TOTAL" <<'PY'
import json,sys
try: r=json.load(open(sys.argv[1]+'/report.json')); t=((r.get('billing') or {}).get('sut') or {}).get('prompt_tokens') or 0
except Exception: t=0
print(int(sys.argv[2])+int(t))
PY
}
residue() { echo "  residue run $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ') governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; [ "$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ')" = 0 ] || { echo "RESIDUE — stopping"; exit 3; }; }
run() { # run <name> <out-dir> <wrapper or ''> <args...>
  local n=$1 out=$2 wrap=$3; shift 3; rm -rf "${out:?}"; echo "##### $n start $(date +%T)"; SECONDS=0
  if [ -n "$wrap" ]; then "$wrap" bin/memeval run "$@" --out "$out" --docker-context orbstack > "$LOG/$n.out" 2>&1; else bin/memeval run "$@" --out "$out" --docker-context orbstack > "$LOG/$n.out" 2>&1; fi
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$n.out" | cut -c1-160)"; summ "$out"; residue
  TOTAL=$(account "$out"); echo "  cumulative gateway prompt tokens: $TOTAL"; [ "$TOTAL" -lt 150000000 ] || { echo "BUDGET 150M reached — stopping"; exit 4; }
}
ensure() { bin/memeval env check "$1" --docker-context orbstack > /dev/null 2>&1 || { SECONDS=0; bin/memeval env install "$1" --docker-context orbstack > "$LOG/install-$(basename $(dirname $1))-$(basename $1 .json).out" 2>&1; echo "  install $1 exit=$? ${SECONDS}s"; }; }
SIX="suts/mem0/manifest.json suts/mem0/manifest-infer.json suts/cognee/manifest.json suts/cognee/manifest-gateway.json suts/hindsight/manifest.json suts/hindsight/manifest-gateway.json"
name() { echo "$(basename $(dirname $1))-$(python3 -c "import json;print(json.load(open('$1'))['variant'])")"; }

echo "########## A: six variants on BEAM 100K (400)"
for m in $SIX; do ensure $m; run "A-$(name $m)" "reports/p14-beam-full/$(name $m)-beam100k-400" "" --suite $BEAM --sut $m $READER --run-seed $SEED_BEAM --sut-llm-budget-tokens 20000000 --parallel 2; done

echo "########## B: MAB conflict_resolution (800): CogletNet, six variants, BM25"
run "B-cogletnet" "reports/p14-mab-cr/cogletnet-mab-cr-800" "$AUTH" --suite $MAB --sut "$CM" $READER --run-seed $SEED_BEAM --parallel 2
for m in $SIX; do ensure $m; run "B-$(name $m)" "reports/p14-mab-cr/$(name $m)-mab-cr-800" "" --suite $MAB --sut $m $READER --run-seed $SEED_BEAM --sut-llm-budget-tokens 20000000 --parallel 2; done
run "B-bm25" "reports/p14-mab-cr/bm25-mab-cr-800" "" --suite $MAB --sut bm25 $READER --run-seed $SEED_BEAM

echo "########## C: CogletNet PMB-75 repeat (retrieval)"
run "C-cogletnet-pmb75" "reports/p14-cogletnet/cogletnet-pmb75-run2" "$AUTH" --suite $PMB --sut "$CM" --run-seed $SEED_PMB --parallel 2

echo "########## D: LongMemEval-S first 20 (retrieval): CogletNet, BM25"
run "D-cogletnet-lme20" "reports/p14-lme/cogletnet-lme-s-l20" "$AUTH" --suite $LME --sut "$CM" --limit 20 --run-seed $SEED_BEAM --parallel 2
run "D-bm25-lme20" "reports/p14-lme/bm25-lme-s-l20" "" --suite $LME --sut bm25 --limit 20 --run-seed $SEED_BEAM
echo "########## done $(date +%T); cumulative gateway prompt tokens $TOTAL"
