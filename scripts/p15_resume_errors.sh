#!/bin/bash
# P15: re-run only the errored cases of an existing run (e.g. after transient JEV connect failures), up to N rounds.
# Usage: scripts/p15_resume_errors.sh <run-name> <staged-manifest.json> [rounds=2]
# `memeval run --resume` restores mode/seed/protocol from the run directory and requires the original external
# SUT manifest (memeval compares its canonical hash with report.sut.manifest_sha256); other flags are passed identical to the original run.
set -u
cd "$(dirname "$0")/.." || exit 1
NAME=${1:?run name}; CM=${2:?staged manifest path}; ROUNDS=${3:-2}
T=${TMPDIR:-/tmp}
AUTH=${MEMEVAL_AUTH_RUN:?set MEMEVAL_AUTH_RUN to your local credential wrapper}
LOG=reports/p15-logs; OUT=reports/p15-cogletnet; PMB=data/normalized/pmb-retrieval.json; RUN=$OUT/$NAME
D() { docker --context orbstack "$@"; }
[ -f "$RUN/report.json" ] || { echo "no report at $RUN"; exit 1; }
[ -x "$AUTH" ] || { echo "authrun missing at $AUTH"; exit 1; }
[ -f "$CM" ] || { echo "staged manifest missing at $CM"; exit 1; }   # memeval itself checks sandbox.Hash(manifest) against report.sut.manifest_sha256 on --resume

echo "##### preflight $(date +%T)"
python3 -B scripts/preflight_services.py --env ../cogletnet/.env \
  --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_VALIDATION_MODEL \
  --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL || { echo "PREFLIGHT FAILED — a model service is unavailable; nothing started"; exit 5; }

errors() { python3 -c "import json;r=json.load(open('$RUN/report.json'));print(sum(1 for c in r['cases'] if c['status']!='scored'))"; }
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
print("  status", s.get('status_counts'), "|", top, f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(4)), "| resume_count", r.get('resume_count'))
PY
}
residue() { echo "  residue run $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ') governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; }

for i in $(seq 1 "$ROUNDS"); do
  n=$(errors); echo "##### round $i: $n errored cases to redo $(date +%T)"; [ "$n" = 0 ] && break
  SECONDS=0
  "$AUTH" bin/memeval run --resume "$RUN" --resume-statuses error --sut "$CM" --suite "$PMB" --llm replay --top-k 10 \
    --max-context-chars 100000 --trace-text --log-file-cap 134217728 --docker-context orbstack --parallel 2 \
    --sut-llm-model gpt-6-luna --sut-llm-effort low > "$LOG/$NAME-resume-$i.out" 2>&1
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-resume-$i.out" | cut -c1-200)"; summ "$RUN"; residue
done
echo "##### attribution and case review $(date +%T)"
python3 -B scripts/cogletnet_failures.py "$RUN" --json "$LOG/$NAME-failures.json" | tail -6
python3 -B scripts/build_case_review.py "$RUN" --suite "$PMB" --name "$NAME" --control "$OUT/bm25-pmb75-trace" --out docs-site/cases/ && echo "  case review written to docs-site/cases/$NAME.js"
echo "##### done $(date +%T)"
