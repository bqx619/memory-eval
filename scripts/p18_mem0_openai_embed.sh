#!/bin/bash
# P18: embedding baseline — Mem0 (raw mode, OpenAI text-embedding-3-large, the same embedder as every CogletNet variant)
# on BEAM 100K, all 400 cases, with the P14/P16/P17 protocol (fixed reader gpt-6-luna, judge jev-1.14.0, same seed).
# See design/p18-embedding-baseline.md. Aborts before touching containers when the Docker daemon (OrbStack) is not
# running — it is never started from here — or when a model service is unavailable: the OpenAI embeddings endpoint
# (the SUT's only external call) and the evaluator's own reader + judge, smoke-tested on one BEAM case with the null SUT.
# Run only on an explicitly configured Docker host; no personal host path is assumed.
# Usage: scripts/p18_mem0_openai_embed.sh [run | resume <run-dir> | cleanup]
#   cleanup — after the results are copied off the host: destroy the memeval images/envs, gc abandoned memeval resources,
#   prune only the build-cache records created since this script's own image build (the daemon is shared with other
#   projects, so no blanket `builder prune`), and remove this script's scratch directories. The host's disk is small.
# Launch detached — scripts/detach.sh reports/p18-logs/p18.out reports/p18-logs/p18.pid bash scripts/p18_mem0_openai_embed.sh run —
# never `nohup … &` from an agent shell (see AGENTS.md).
set -u
cd "$(dirname "$0")/.." || exit 1
MODE=${1:-run}; RESUME_DIR=${2:-}
D() { docker --context orbstack "$@"; }
LOG=reports/p18-logs; mkdir -p "$LOG"; echo $$ > "$LOG/p18.pid"   # own pid (the launcher, scripts/detach.sh, exits at once)
OUT=reports/p18-beam; mkdir -p "$OUT"
cache_ids() { D buildx du 2>/dev/null | awk 'NR>1 && $1 ~ /^[a-z0-9]+\*?$/ {sub(/\*$/,"",$1); print $1}' | sort -u; }   # build-cache record ids
disk() { D system df; df -h /System/Volumes/Data | tail -1; }

if [ "$MODE" = cleanup ]; then   # run only after the run directory has been copied off this host
  echo "##### cleanup $(date +%T)"; disk
  [ "$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ')" = 0 ] || { echo "memeval.run containers still exist — a run may be active; nothing removed"; exit 3; }
  bin/memeval env destroy --all --docker-context orbstack > "$LOG/env-destroy.out" 2>&1; echo "  env destroy exit=$?"
  bin/memeval gc --apply --older-than 1h --docker-context orbstack > "$LOG/gc.out" 2>&1; echo "  gc exit=$?"
  if [ -s "$LOG/buildcache-before.txt" ]; then
    cache_ids | grep -vxF -f "$LOG/buildcache-before.txt" > "$LOG/buildcache-new.txt"
    while read -r id; do D builder prune -f --filter "id=$id" > /dev/null 2>&1 || echo "  prune $id failed"; done < "$LOG/buildcache-new.txt"
    echo "  build-cache records pruned: $(wc -l < "$LOG/buildcache-new.txt" | tr -d ' ') (records that predate this script's build are left alone)"
  else echo "  no build-cache snapshot ($LOG/buildcache-before.txt) — build cache left untouched"; fi
  rm -rf /private/tmp/memeval-p18-*
  echo "  remaining: memeval.env images $(D images -q --filter label=memeval.env | wc -l | tr -d ' '), memeval/* images $(D images --format '{{.Repository}}' | grep -c '^memeval/'), memeval.run containers $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ')"
  disk; echo "##### cleanup done $(date +%T)"; exit 0
fi
S=/private/tmp/memeval-p18-$(date +%Y%m%d%H%M%S); mkdir -p "$S"   # literal scratch root: the per-user TMPDIR folder differs between hosts
BEAM=data/normalized/beam-100k@3205395e897e
SEED=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089   # same seed as every P14/P16/P17 BEAM run
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
COMMON="--suite $BEAM --run-seed $SEED $READER --docker-context orbstack --log-file-cap 134217728 --auto-resume-rate-limited 2"
M=suts/mem0/manifest-openai-embed.json
NAME=mem0-raw-openai-embed-beam100k-400

echo "##### preflight $(date +%T) mode=$MODE"
[ -f "$M" ] || { echo "manifest $M missing (Codex delivery pending)"; exit 1; }
[ -x bin/memeval ] || { echo "bin/memeval missing — build it before launching (not rebuilt here: another run may be using it)"; exit 1; }
NEWER=$(find cmd internal -name '*.go' -newer bin/memeval | wc -l | tr -d ' '); [ "$NEWER" = 0 ] || echo "  note: $NEWER Go source files are newer than bin/memeval; the binary is used as is"
D info > /dev/null 2>&1 || { echo "PREFLIGHT FAILED — Docker daemon (context orbstack) unavailable; start OrbStack first, nothing started"; exit 5; }
preflight() { python3 -B scripts/preflight_services.py --env .env --timeout 90 --embedding https://api.openai.com/v1 OPENAI_API_KEY text-embedding-3-large; }
preflight || { sleep 30; echo "  preflight retry 2 $(date +%T)"; preflight; } || { echo "PREFLIGHT FAILED — OpenAI embeddings endpoint unavailable; nothing started"; exit 5; }
# evaluator smoke: one BEAM case with the null SUT exercises the fixed reader (gpt-6-luna) and the TypeSafe judge (a 402 here = judge billing)
rm -rf "${S:?}/smoke"; bin/memeval run --suite "$BEAM" --sut null $READER --run-seed "$SEED" --limit 1 --out "$S/smoke" > "$LOG/smoke.out" 2>&1
python3 - "$S/smoke/report.json" "$S/smoke/llm_ledger.jsonl" <<'PY' || { echo "PREFLIGHT FAILED — evaluator reader/judge smoke run did not score; see $LOG/smoke.out"; exit 5; }
import json,sys
r=json.load(open(sys.argv[1])); ok=sum(1 for c in r['cases'] if c['status']=='scored')
for l in open(sys.argv[2]):
    e=json.loads(l)
    if e.get('error'): print("  smoke ledger:", e.get('purpose'), e.get('http_status'), e.get('error'))
print("  evaluator smoke: scored", ok, "of", len(r['cases'])); sys.exit(0 if ok else 1)
PY
echo "##### binary $(shasum -a 256 bin/memeval | cut -c1-12) scratch $S $(date +%T)"

summ() { python3 - "$1" <<'PY'
import json,sys,collections
p=sys.argv[1]
try: r=json.load(open(p+'/report.json'))
except Exception as e: print("  no report:", e); sys.exit()
s=r.get('summary') or {}
if not s: print("  REJECTED", (r.get('reason') or '')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],4),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None and k in ('beam.llm_judge_score','beam.event_ordering.final')}
ev=(r.get('billing') or {}).get('evaluator') or {}
errs=collections.Counter((c.get('error_type') or c['status']) for c in r['cases'] if c['status']!='scored')
it=r.get('interruption') or {}
print("  status", s.get('status_counts'), "| n", len(r['cases']), "|", top, "| ev prompt", ev.get('prompt_tokens'), f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(4)), "| resume", r.get('resume_count'), "| interruption", it.get('reason'), it.get('unscheduled_samples'))
PY
}
pending() { python3 - "$1" <<'PY'
import json,sys
try: r=json.load(open(sys.argv[1]+'/report.json')); print(sum(1 for c in r['cases'] if c['status'] in ('error','invalid')))
except Exception: print(0)
PY
}
residue() { local n; n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); echo "  residue run $n governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; [ "$n" = 0 ] || { echo "RESIDUE — stopping"; exit 3; }; }
ensure() { bin/memeval env check "$1" --docker-context orbstack > "$LOG/env-check.out" 2>&1 && return; [ -s "$LOG/buildcache-before.txt" ] || cache_ids > "$LOG/buildcache-before.txt"; SECONDS=0; bin/memeval env install "$1" --docker-context orbstack > "$LOG/env-install.out" 2>&1; echo "  install $1 exit=$? ${SECONDS}s"; grep -q '"valid": true' "$LOG/env-install.out" || { echo "env not valid — see $LOG/env-install.out"; tail -8 "$LOG/env-install.out"; exit 1; }; }
resume_pass() { # resume_pass <run-dir> <pass#>: retry error + invalid cases in place (identities, seed and history preserved)
  echo "##### $NAME resume pass $2 ($(pending "$1") error/invalid) $(date +%T)"; SECONDS=0
  bin/memeval run --resume "$1" --resume-statuses error,invalid --sut "$M" --parallel 2 $COMMON > "$LOG/$NAME-resume-$2.out" 2>&1
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-resume-$2.out" | cut -c1-160)"; summ "$1"; residue
}

case "$MODE" in
  run)
    ensure "$M"
    RUN=$OUT/$NAME; rm -rf "${RUN:?}"; echo "##### $NAME start $(date +%T)"; SECONDS=0
    bin/memeval run --sut "$M" --parallel 2 $COMMON --out "$RUN" > "$LOG/$NAME.out" 2>&1
    echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME.out" | cut -c1-160)"; summ "$RUN"; residue
    ;;
  resume)
    RUN=${RESUME_DIR:?"resume needs the run directory"}; [ -f "$RUN/report.json" ] || { echo "no report at $RUN"; exit 1; }
    ensure "$M"
    ;;
  *) echo "unknown mode: $MODE — use run | resume <run-dir>"; exit 1 ;;
esac
# up to two in-place retries of error/invalid cases; stop early when nothing is pending or a pass does not reduce the count
for pass in 1 2; do
  n=$(pending "$RUN"); [ "$n" -gt 0 ] || break
  resume_pass "$RUN" "$pass"; m=$(pending "$RUN"); [ "$m" -lt "$n" ] || { echo "  pass $pass did not reduce pending ($n → $m); stopping retries"; break; }
done
echo "##### summary $(date +%T)"; summ "$RUN"; disk
echo "##### done $(date +%T) — copy $RUN off this host, then: bash scripts/p18_mem0_openai_embed.sh cleanup"
