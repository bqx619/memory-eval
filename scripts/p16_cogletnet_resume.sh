#!/bin/bash
# P16 follow-up: resume the `error` cases of the CogletNet BEAM 100K run in place. Two episodes (40 questions) were lost
# when the gateway governor's circuit opened and the adapter reported BATCH_NOT_READY instead of gateway_exhausted (F90),
# one episode (20 questions) to the upstream summary-fidelity gate; --auto-resume-rate-limited covered neither.
# Preconditions, all checked here: scripts/p16_beam_full.sh has printed "##### done" (the run's report.json exists and the
# control runs no longer share the gateway), no memeval.run container is left, the staged source of the 15:04 launch is
# still in its scratch directory (it is the run's identity; a re-staged tree would only be acceptable with the same
# snapshot hash), and the CogletNet model services answer the preflight probe.
# Launch detached — scripts/detach.sh reports/p16-logs/p16-resume.out reports/p16-logs/p16-resume.pid bash scripts/p16_cogletnet_resume.sh
# — never `nohup … &` from an agent shell (the job dies with the agent's turn).
set -u
cd "$(dirname "$0")/.." || exit 1
D() { docker --context orbstack "$@"; }
T=${TMPDIR:-/tmp}
AUTH=${MEMEVAL_AUTH_RUN:?set MEMEVAL_AUTH_RUN to your local credential wrapper}
S=${MEMEVAL_RESUME_SCRATCH:?set MEMEVAL_RESUME_SCRATCH to the original staged run directory}
LOG=reports/p16-logs; mkdir -p "$LOG"; echo $$ > "$LOG/p16-resume.pid"
NAME=cogletnet-v5-beam100k-400
RUN=reports/p16-beam/$NAME
BEAM=data/normalized/beam-100k@3205395e897e
SEED=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
COMMON="--suite $BEAM --run-seed $SEED $READER --docker-context orbstack --log-file-cap 134217728 --auto-resume-rate-limited 2"
V5=c2d0770c3503e470be3b57418e4774a80c28a5c1a1b57bbc4ff9e55891c72e89
CM=$S/staged/cogletnet/manifest.json

echo "##### cogletnet resume: preconditions $(date +%T)"
grep -q '^##### done' "$LOG/p16.out" || { echo "p16_beam_full.sh has not printed '##### done' — nothing started"; exit 1; }
[ -f "$RUN/report.json" ] || { echo "no $RUN/report.json — nothing started"; exit 1; }
[ -x "$AUTH" ] || { echo "authrun missing at $AUTH — nothing started"; exit 1; }
[ -f "$CM" ] || { echo "staged manifest $CM is gone — re-stage ../cogletnet with scripts/stage_cogletnet.py and require snapshot $V5 before resuming; nothing started"; exit 1; }
SNAP=$(python3 -c "import json;print(json.loads(open('$LOG/stage.out').readline()).get('source_snapshot_sha256',''))")
[ "$SNAP" = "$V5" ] || { echo "staged snapshot ${SNAP:0:12} is not v5 ${V5:0:12} — nothing started"; exit 1; }
n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); [ "$n" = 0 ] || { echo "RESIDUE $n memeval.run containers present — nothing started"; exit 3; }
echo "  binary $(shasum -a 256 bin/memeval | cut -c1-12) (run used ec6ef00b001c)"
python3 - "$RUN/report.json" <<'PY'
import json,sys,collections
r=json.load(open(sys.argv[1])); c=collections.Counter(x['status'] for x in r['cases'])
e=collections.Counter((x.get('error_type') or x['status']) for x in r['cases'] if x['status']!='scored')
print("  before:", dict(c), "| non-scored:", dict(e.most_common(6)))
PY

preflight() { python3 -B scripts/preflight_services.py --env ../cogletnet/.env --timeout 90 \
  --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_VALIDATION_MODEL \
  --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL; }
preflight || { sleep 30; echo "  preflight retry 2 $(date +%T)"; preflight; } || { sleep 30; echo "  preflight retry 3 $(date +%T)"; preflight; } || { echo "PREFLIGHT FAILED — a CogletNet model service is unavailable after 3 attempts; nothing started"; exit 5; }

echo "##### $NAME resume (error) start $(date +%T)"; SECONDS=0
"$AUTH" bin/memeval run --resume "$RUN" --resume-statuses error --sut "$CM" --parallel 2 \
  --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON > "$LOG/$NAME-resume.out" 2>&1
echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-resume.out" | cut -c1-160)"

python3 - "$RUN/report.json" <<'PY'
import json,sys,collections
r=json.load(open(sys.argv[1])); s=r.get('summary') or {}
if not s: print("  REJECTED", (r.get('reason') or '')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],4),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None and k in ('beam.llm_judge_score','beam.event_ordering.final')}
sut=(r.get('billing') or {}).get('sut') or {}; ev=(r.get('billing') or {}).get('evaluator') or {}
errs=collections.Counter((c.get('error_type') or c['status']) for c in r['cases'] if c['status']!='scored')
print("  status", s.get('status_counts'), "| n", len(r['cases']), "|", top, "| sut prompt", sut.get('prompt_tokens'), "| ev prompt", ev.get('prompt_tokens'), f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(4)), "| resume", r.get('resume_count'))
PY
n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); echo "  residue run $n governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"
python3 -B scripts/cogletnet_failures.py "$RUN" --json "$LOG/$NAME-failures-after-resume.json" 2>/dev/null | tail -6 || echo "  (failure attribution script not applicable)"
echo "##### done $(date +%T)"
[ "$n" = 0 ] || { echo "RESIDUE — containers left"; exit 3; }
