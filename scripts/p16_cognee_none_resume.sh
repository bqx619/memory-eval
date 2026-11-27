#!/bin/bash
# P16 follow-up: resume the `error` cases of the cognee (none) BEAM 100K rerun in place. Episodes 9–17 and six cases of
# episode 18 (186 cases) failed overnight 03:58–08:40 because the evaluator's fixed reader could not reach the gateway's
# gpt-6-luna upstream (878 LLMTransportError in llm_ledger.jsonl, ~5 s each, 5 retries exhausted); cognee itself ingested
# and retrieved normally. Preconditions, all checked here: scripts/p16_cogletnet_resume.sh has printed "##### done" (the
# two resumes must not share the gateway), no memeval.run container is left, and a one-case null-SUT smoke run scores
# (the reader path that failed is exactly what the smoke exercises).
# Launch detached — scripts/detach.sh reports/p16-logs/p16-cognee-resume.out reports/p16-logs/p16-cognee-resume.pid bash scripts/p16_cognee_none_resume.sh
set -u
cd "$(dirname "$0")/.." || exit 1
D() { docker --context orbstack "$@"; }
S=${MEMEVAL_RESUME_SCRATCH:?set MEMEVAL_RESUME_SCRATCH to the original staged run directory}
LOG=reports/p16-logs; mkdir -p "$LOG"; echo $$ > "$LOG/p16-cognee-resume.pid"
NAME=cognee-none-beam100k-400
RUN=reports/p16-beam/$NAME
BEAM=data/normalized/beam-100k@3205395e897e
SEED=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
COMMON="--suite $BEAM --run-seed $SEED $READER --docker-context orbstack --log-file-cap 134217728 --auto-resume-rate-limited 2"

echo "##### cognee none resume: preconditions $(date +%T)"
grep -q '^##### done' "$LOG/p16-resume.out" 2>/dev/null || { echo "p16_cogletnet_resume.sh has not printed '##### done' — nothing started"; exit 1; }
[ -f "$RUN/report.json" ] || { echo "no $RUN/report.json — nothing started"; exit 1; }
n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); [ "$n" = 0 ] || { echo "RESIDUE $n memeval.run containers present — nothing started"; exit 3; }
TO=$(python3 -c "import json;print(json.load(open('suts/cognee/manifest.json')).get('adapter',{}).get('request_timeout_s'))")
[ "${TO:-0}" -ge 1800 ] 2>/dev/null || { echo "suts/cognee/manifest.json adapter.request_timeout_s=$TO (< 1800) — nothing started"; exit 1; }
python3 - "$RUN/report.json" <<'PY'
import json,sys,collections
r=json.load(open(sys.argv[1])); c=collections.Counter(x['status'] for x in r['cases'])
e=collections.Counter((x.get('error_type') or x['status']) for x in r['cases'] if x['status']!='scored')
print("  before:", dict(c), "| non-scored:", dict(e.most_common(6)))
PY
# evaluator smoke: the reader (gateway gpt-6-luna) and the TypeSafe judge must score one BEAM case; three attempts 5 min apart
smoke() { rm -rf "${S:?}/smoke-cognee"; bin/memeval run --suite "$BEAM" --sut null $READER --run-seed "$SEED" --limit 1 --out "$S/smoke-cognee" > "$LOG/smoke-cognee.out" 2>&1
  python3 - "$S/smoke-cognee/report.json" <<'PY'
import json,sys
r=json.load(open(sys.argv[1])); ok=sum(1 for c in r['cases'] if c['status']=='scored')
print("  evaluator smoke: scored", ok, "of", len(r['cases'])); sys.exit(0 if ok else 1)
PY
}
smoke || { sleep 300; echo "  smoke retry 2 $(date +%T)"; smoke; } || { sleep 300; echo "  smoke retry 3 $(date +%T)"; smoke; } || { echo "PREFLIGHT FAILED — evaluator reader/judge smoke run did not score after 3 attempts; see $LOG/smoke-cognee.out; nothing started"; exit 5; }
bin/memeval env check suts/cognee/manifest.json --docker-context orbstack > /dev/null 2>&1 || { echo "cognee env not valid — nothing started"; exit 1; }

echo "##### $NAME resume (error) start $(date +%T)"; SECONDS=0
bin/memeval run --resume "$RUN" --resume-statuses error --sut suts/cognee/manifest.json --parallel 1 $COMMON > "$LOG/$NAME-resume.out" 2>&1
echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-resume.out" | cut -c1-160)"

python3 - "$RUN/report.json" <<'PY'
import json,sys,collections
r=json.load(open(sys.argv[1])); s=r.get('summary') or {}
if not s: print("  REJECTED", (r.get('reason') or '')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],4),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None and k in ('beam.llm_judge_score','beam.event_ordering.final')}
ev=(r.get('billing') or {}).get('evaluator') or {}
errs=collections.Counter((c.get('error_type') or c['status']) for c in r['cases'] if c['status']!='scored')
print("  status", s.get('status_counts'), "| n", len(r['cases']), "|", top, "| ev prompt", ev.get('prompt_tokens'), f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(4)), "| resume", r.get('resume_count'))
PY
n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); echo "  residue run $n governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"
echo "##### done $(date +%T)"
[ "$n" = 0 ] || { echo "RESIDUE — containers left"; exit 3; }
