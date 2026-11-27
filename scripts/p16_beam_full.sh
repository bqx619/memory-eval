#!/bin/bash
# P16: BEAM 100K, all 400 cases, on the current CogletNet source, then completion of every P14 BEAM control that
# did not finish: the three gateway variants that stopped on their 20 M token cap are resumed with a larger cap,
# the single invalid (empty reader answer) case of mem0 raw / hindsight none is retried, and cognee none — whose
# 400 cases all timed out at request_timeout_s=300 under --parallel 2 — is rerun from scratch at --parallel 1
# after the manifest timeout was raised to 1800 s (P14 §2.1 decision). See design/p16-beam-full.md.
# Aborts before touching containers when a model service is unavailable (CogletNet's embedding / summary /
# validation / JEV endpoints, and the evaluator's own reader + judge, smoke-tested on one BEAM case with the null SUT).
# Usage: scripts/p16_beam_full.sh [all|cogletnet|controls]
# Launch detached — scripts/detach.sh reports/p16-logs/p16.out reports/p16-logs/p16.pid bash scripts/p16_beam_full.sh all —
# never `nohup … &` from an agent shell: the first launch (2026-10-26 09:34) was killed with the Codex turn at 09:45.
set -u
cd "$(dirname "$0")/.." || exit 1
T=${TMPDIR:-/tmp}
S=$T/memeval-p16-$(date +%Y%m%d%H%M%S); mkdir -p "$S"
AUTH=${MEMEVAL_AUTH_RUN:?set MEMEVAL_AUTH_RUN to your local credential wrapper}
D() { docker --context orbstack "$@"; }
LOG=reports/p16-logs; mkdir -p "$LOG"; echo $$ > "$LOG/p16.pid"   # own pid (the launcher, scripts/detach.sh, exits at once)
OUT=reports/p16-beam; mkdir -p "$OUT"
P14=reports/p14-beam-full
BEAM=data/normalized/beam-100k@3205395e897e
SEED=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089   # same seed as every P14 BEAM run
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
COMMON="--suite $BEAM --run-seed $SEED $READER --docker-context orbstack --log-file-cap 134217728 --auto-resume-rate-limited 2"
V5=c2d0770c3503e470be3b57418e4774a80c28a5c1a1b57bbc4ff9e55891c72e89   # source snapshot of the P15 v5 PMB run
ONLY=${1:-all}
TOTAL=0; STOP_AT=250000000   # cumulative gateway prompt tokens across this script's runs; a runaway guard, not a plan budget

echo "##### preflight $(date +%T) mode=$ONLY"
preflight() { python3 -B scripts/preflight_services.py --env ../cogletnet/.env --timeout 90 \
  --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_VALIDATION_MODEL \
  --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL; }
# three attempts 30 s apart: the gateway's first call after idling can exceed the probe timeout (seen 2026-10-26 09:23)
preflight || { sleep 30; echo "  preflight retry 2 $(date +%T)"; preflight; } || { sleep 30; echo "  preflight retry 3 $(date +%T)"; preflight; } || { echo "PREFLIGHT FAILED — a CogletNet model service is unavailable after 3 attempts; nothing started"; exit 5; }
[ -x "$AUTH" ] || { echo "authrun missing at $AUTH"; exit 1; }
go build -o bin/memeval ./cmd/memeval && bash scripts/build_linux_memeval.sh arm64 | tail -1 || exit 1
echo "##### binary $(shasum -a 256 bin/memeval | cut -c1-12) scratch $S $(date +%T)"
# evaluator smoke: one BEAM case with the null SUT exercises the fixed reader (OpenAI gpt-6-luna) and the TypeSafe judge
rm -rf "$S/smoke"; bin/memeval run --suite "$BEAM" --sut null $READER --run-seed "$SEED" --limit 1 --out "$S/smoke" > "$LOG/smoke.out" 2>&1
python3 - "$S/smoke/report.json" <<'PY' || { echo "PREFLIGHT FAILED — evaluator reader/judge smoke run did not score; see $LOG/smoke.out"; exit 5; }
import json,sys
r=json.load(open(sys.argv[1])); ok=sum(1 for c in r['cases'] if c['status']=='scored')
print("  evaluator smoke: scored", ok, "of", len(r['cases'])); sys.exit(0 if ok else 1)
PY

summ() { python3 - "$1" <<'PY'
import json,sys,collections
p=sys.argv[1]
try: r=json.load(open(p+'/report.json'))
except Exception as e: print("  no report:", e); sys.exit()
s=r.get('summary') or {}
if not s: print("  REJECTED", (r.get('reason') or '')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],4),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None and k in ('beam.llm_judge_score','beam.event_ordering.final')}
sut=(r.get('billing') or {}).get('sut') or {}; ev=(r.get('billing') or {}).get('evaluator') or {}
errs=collections.Counter((c.get('error_type') or c['status']) for c in r['cases'] if c['status']!='scored')
it=r.get('interruption') or {}
print("  status", s.get('status_counts'), "| n", len(r['cases']), "|", top, "| sut prompt", sut.get('prompt_tokens'), "| ev prompt", ev.get('prompt_tokens'), f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(4)), "| resume", r.get('resume_count'), "| interruption", it.get('reason'), it.get('unscheduled_samples'))
PY
}
account() { python3 - "$1" "$TOTAL" <<'PY'
import json,sys
try: r=json.load(open(sys.argv[1]+'/report.json')); t=((r.get('billing') or {}).get('sut') or {}).get('prompt_tokens') or 0
except Exception: t=0
print(int(sys.argv[2])+int(t))
PY
}
residue() { local n; n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); echo "  residue run $n governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; [ "$n" = 0 ] || { echo "RESIDUE — stopping"; exit 3; }; }
after() { summ "$1"; residue; TOTAL=$(account "$1"); echo "  cumulative gateway prompt tokens: $TOTAL"; [ "$TOTAL" -lt "$STOP_AT" ] || { echo "RUNAWAY GUARD $STOP_AT tokens reached — stopping"; exit 4; }; }
run() { # fresh run: run <name> <out-dir> <wrapper or ''> <args...>
  local n=$1 out=$2 wrap=$3; shift 3; rm -rf "${out:?}"; echo "##### $n start $(date +%T)"; SECONDS=0
  if [ -n "$wrap" ]; then "$wrap" bin/memeval run "$@" --out "$out" > "$LOG/$n.out" 2>&1; else bin/memeval run "$@" --out "$out" > "$LOG/$n.out" 2>&1; fi
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$n.out" | cut -c1-160)"; after "$out"
}
resume() { # continue a P14 run in place: resume <name> <run-dir> <statuses> <args...>; identities, seed and history are preserved
  local n=$1 dir=$2 st=$3; shift 3; [ -f "$dir/report.json" ] || { echo "##### $n: no report at $dir, skipped"; return; }
  echo "##### $n resume ($st) start $(date +%T)"; SECONDS=0
  bin/memeval run --resume "$dir" --resume-statuses "$st" "$@" > "$LOG/$n-resume.out" 2>&1
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$n-resume.out" | cut -c1-160)"; after "$dir"
}
ensure() { bin/memeval env check "$1" --docker-context orbstack > /dev/null 2>&1 || { SECONDS=0; bin/memeval env install "$1" --docker-context orbstack > "$LOG/install-$(basename $(dirname $1))-$(basename $1 .json).out" 2>&1; echo "  install $1 exit=$? ${SECONDS}s"; }; }

if [ "$ONLY" = all ] || [ "$ONLY" = cogletnet ]; then
  echo "##### cogletnet: stage current ../cogletnet $(date +%T)"
  python3 -B scripts/stage_cogletnet.py --source ../cogletnet --out "$S/staged" > "$LOG/stage.out" 2>&1 || { echo "staging failed"; tail -5 "$LOG/stage.out"; exit 1; }
  CM=$S/staged/cogletnet/manifest.json
  SNAP=$(python3 -c "import json;print(json.loads(open('$LOG/stage.out').readline()).get('source_snapshot_sha256',''))")
  if [ "$SNAP" = "$V5" ]; then NAME=cogletnet-v5-beam100k-400; echo "  snapshot ${SNAP:0:12} = P15 v5 source → $NAME"; else NAME=cogletnet-v6-beam100k-400; echo "  snapshot ${SNAP:0:12} differs from v5 ${V5:0:12} → $NAME"; fi
  SECONDS=0; "$AUTH" bin/memeval env check "$CM" --docker-context orbstack > "$LOG/cogletnet-check.out" 2>&1 || "$AUTH" bin/memeval env install "$CM" --docker-context orbstack --sut-llm-model gpt-6-luna --sut-llm-effort low > "$LOG/cogletnet-install.out" 2>&1
  echo "  cogletnet env exit=$? ${SECONDS}s $(grep -o '"valid": [a-z]*' "$LOG/cogletnet-install.out" "$LOG/cogletnet-check.out" 2>/dev/null | head -1)"
  grep -q '"valid": true' "$LOG/cogletnet-install.out" "$LOG/cogletnet-check.out" 2>/dev/null || { echo "env not valid — see $LOG/cogletnet-install.out"; tail -8 "$LOG/cogletnet-install.out"; exit 1; }
  cp "$CM" "$LOG/$NAME-manifest.json"   # the staged manifest a later --resume must be given
  run "$NAME" "$OUT/$NAME" "$AUTH" --sut "$CM" --parallel 2 --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON
  python3 -B scripts/cogletnet_failures.py "$OUT/$NAME" --json "$LOG/$NAME-failures.json" 2>/dev/null | tail -6 || echo "  (failure attribution script not applicable)"
fi

if [ "$ONLY" = all ] || [ "$ONLY" = controls ]; then
  echo "##### controls: finish the P14 BEAM runs that did not complete $(date +%T)"
  for m in suts/mem0/manifest.json suts/hindsight/manifest.json suts/hindsight/manifest-gateway.json suts/cognee/manifest-gateway.json suts/mem0/manifest-infer.json; do ensure $m; done
  # one invalid case each (empty reader answer), cheap
  resume mem0-raw-local-beam100k-400 "$P14/mem0-raw-local-beam100k-400" invalid --sut suts/mem0/manifest.json --parallel 2 $COMMON
  resume hindsight-none-beam100k-400 "$P14/hindsight-none-beam100k-400" invalid --sut suts/hindsight/manifest.json --parallel 2 $COMMON
  # budget-capped gateway variants: errors (gateway_budget) + invalid + the unscheduled suffix recorded in report.interruption
  resume hindsight-llm-gateway-beam100k-400 "$P14/hindsight-llm-gateway-beam100k-400" error,invalid --sut suts/hindsight/manifest-gateway.json --parallel 2 --sut-llm-budget-tokens 45000000 $COMMON
  resume cognee-llm-gateway-beam100k-400 "$P14/cognee-llm-gateway-beam100k-400" error,invalid --sut suts/cognee/manifest-gateway.json --parallel 2 --sut-llm-budget-tokens 40000000 $COMMON
  resume mem0-infer-local-beam100k-400 "$P14/mem0-infer-local-beam100k-400" error,invalid --sut suts/mem0/manifest-infer.json --parallel 2 --sut-llm-budget-tokens 60000000 $COMMON
  # cognee none: all 400 timed out in P14; rerun from scratch with the raised request timeout, single worker
  TO=$(python3 -c "import json;print(json.load(open('suts/cognee/manifest.json')).get('adapter',{}).get('request_timeout_s'))")
  if [ "${TO:-0}" -ge 1800 ] 2>/dev/null; then ensure suts/cognee/manifest.json; run cognee-none-beam100k-400 "$OUT/cognee-none-beam100k-400" "" --sut suts/cognee/manifest.json --parallel 1 $COMMON
  else echo "##### cognee-none skipped: suts/cognee/manifest.json adapter.request_timeout_s=$TO (< 1800, would time out again as in P14)"; fi
fi

echo "##### summary $(date +%T)"
for d in "$OUT"/cogletnet-v*-beam100k-400 "$P14"/*-beam100k-400 "$OUT"/cognee-none-beam100k-400; do [ -f "$d/report.json" ] && { echo "$d"; summ "$d"; }; done
echo "##### done $(date +%T) cumulative gateway prompt tokens $TOTAL"
