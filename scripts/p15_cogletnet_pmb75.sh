#!/bin/bash
# P15: CogletNet (2026-10-25 upstream maintenance) PMB-75 retrieval rerun with per-case trace, plus a BM25
# trace control. See design/p15-case-review.md. Aborts before touching containers when a model service the
# run depends on is unavailable (OpenAI embedding credits, summary/validation gateway).
set -u
cd "$(dirname "$0")/.." || exit 1
T=${TMPDIR:-/tmp}
S=$T/memeval-p15-$(date +%Y%m%d%H%M%S); mkdir -p "$S"
AUTH=${MEMEVAL_AUTH_RUN:?set MEMEVAL_AUTH_RUN to your local credential wrapper}
D() { docker --context orbstack "$@"; }
LOG=reports/p15-logs; mkdir -p "$LOG"
PMB=data/normalized/pmb-retrieval.json
SEED_PMB=$(python3 -c "import json; print(json.load(open('reports/p6-reference/pmb-mem0/report.json'))['run_seed'])")
OUT=reports/p15-cogletnet
NAME=${1:-cogletnet-v3-pmb75}   # run name, e.g. cogletnet-v4-pmb75 after an upstream change
COMMON="--suite $PMB --run-seed $SEED_PMB --llm replay --top-k 10 --max-context-chars 100000 --trace-text --log-file-cap 134217728 --docker-context orbstack"

echo "##### preflight $(date +%T)"
python3 -B scripts/preflight_services.py --env ../cogletnet/.env \
  --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_VALIDATION_MODEL \
  --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL || { echo "PREFLIGHT FAILED — a model service is unavailable; nothing started"; exit 5; }
[ -x "$AUTH" ] || { echo "authrun missing at $AUTH"; exit 1; }

go build -o bin/memeval ./cmd/memeval && bash scripts/build_linux_memeval.sh arm64 | tail -1 || exit 1
echo "##### binary $(shasum -a 256 bin/memeval | cut -c1-12) scratch $S $(date +%T)"
python3 -B scripts/stage_cogletnet.py --source ../cogletnet --out "$S/staged" > "$LOG/stage.out" 2>&1 || { echo "staging failed"; tail -5 "$LOG/stage.out"; exit 1; }
CM=$S/staged/cogletnet/manifest.json; grep -o '"source_snapshot_sha256[^,]*' "$LOG/stage.out" | head -1
SECONDS=0; "$AUTH" bin/memeval env check "$CM" --docker-context orbstack > "$LOG/cogletnet-check.out" 2>&1 || "$AUTH" bin/memeval env install "$CM" --docker-context orbstack --sut-llm-model gpt-6-luna --sut-llm-effort low > "$LOG/cogletnet-install.out" 2>&1
echo "  cogletnet env exit=$? ${SECONDS}s $(grep -o '"valid": [a-z]*' "$LOG/cogletnet-install.out" "$LOG/cogletnet-check.out" 2>/dev/null | head -1)"
grep -q '"valid": true' "$LOG/cogletnet-install.out" "$LOG/cogletnet-check.out" 2>/dev/null || { echo "env not valid — see $LOG/cogletnet-install.out"; tail -8 "$LOG/cogletnet-install.out"; exit 1; }

summ() { python3 - "$1" <<'PY'
import json,sys,collections
p=sys.argv[1]
try: r=json.load(open(p+'/report.json'))
except Exception as e: print("  no report:", e); sys.exit()
s=r.get('summary') or {}
if not s: print("  REJECTED", (r.get('reason') or '')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],4),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None and k in ('recall','mrr','ndcg','empty_result_correct')}
errs=collections.Counter((c.get('error_type') or c['status']) for c in r['cases'] if c['status']!='scored')
print("  status", s.get('status_counts'), "|", top, f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(4)), "| trace", (r.get('protocol') or {}).get('trace_text'))
PY
}
residue() { echo "  residue run $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ') governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; }
run() { # run <name> <out-dir> <wrapper or ''> <args...>
  local n=$1 out=$2 wrap=$3; shift 3; rm -rf "${out:?}"; echo "##### $n start $(date +%T)"; SECONDS=0
  if [ -n "$wrap" ]; then "$wrap" bin/memeval run "$@" --out "$out" > "$LOG/$n.out" 2>&1; else bin/memeval run "$@" --out "$out" > "$LOG/$n.out" 2>&1; fi
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$n.out" | cut -c1-160)"; summ "$out"; residue
}

run "$NAME" "$OUT/$NAME" "$AUTH" --sut "$CM" --parallel 4 --sut-llm-model gpt-6-luna --sut-llm-effort low $COMMON
if [ -f "$OUT/bm25-pmb75-trace/report.json" ]; then echo "##### bm25-pmb75-trace control already present, kept"; else run "bm25-pmb75-trace" "$OUT/bm25-pmb75-trace" "" --sut bm25 $COMMON; fi

echo "##### attribution and case review $(date +%T)"
python3 -B scripts/cogletnet_failures.py "$OUT/$NAME" --json "$LOG/$NAME-failures.json" | tail -6
if [ -f scripts/build_case_review.py ]; then
  python3 -B scripts/build_case_review.py "$OUT/$NAME" --suite "$PMB" --name "$NAME" --control "$OUT/bm25-pmb75-trace" --out docs-site/cases/ && echo "  case review written to docs-site/cases/$NAME.js"
else
  echo "  scripts/build_case_review.py not present yet; case review skipped"
fi
echo "##### done $(date +%T)"
