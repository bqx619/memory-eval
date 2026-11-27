#!/bin/bash
# P17: BEAM 100K, all 400 cases, on the new CogletNet source (leaf-memory refactor, upstream Steps 1–9 of 2026-10-27/28),
# one run per adapter variant: `manifest.json` = legacy main chain (FullPath + binary JEV, the P16 configuration),
# `manifest-leaf-memory.json` = the new LeafMemory main chain (L0 full-path candidates, three-class JEV, explicit scopes).
# Gateway-class failures are retried automatically: `--auto-resume-rate-limited 2` inside the run, then up to MAXPASS
# `--resume --resume-statuses error` passes, including native/host-timeout cases when gateway failures coexist;
# stop after two consecutive passes without fewer gateway cases. Errors that survive retain their attribution rather
# than being assumed CogletNet-native (scripts/cogletnet_failures.py; design/p17-cogletnet-v6.md).
# Aborts before touching containers when a model service is unavailable (CogletNet's embedding / summary / validation /
# JEV endpoints, and the evaluator's own reader + judge, smoke-tested on one BEAM case with the null SUT).
# Usage: scripts/p17_cogletnet_v6.sh [manifest.json|manifest-leaf-memory.json] [--resume <scratch dir of the original launch>]
#   --resume skips build / smoke / staging / install and only runs the retry loop on the existing run directory with the
#   manifest staged at the original launch (same SUT identity), e.g. after a gateway outage outlasted the loop.
#   --redo-cases <file> builds a fresh SUT and replaces only listed scored cases in the existing run,
#   preserving all other rows/history; subsequent error retries use the new identity.
# Launch detached — scripts/detach.sh reports/p17-logs/p17-<variant>.out reports/p17-logs/p17-<variant>.pid bash scripts/p17_cogletnet_v6.sh manifest.json
# — never `nohup … &` from an agent shell (the job dies with the agent's turn).
set -u
cd "$(dirname "$0")/.." || exit 1
MANIFEST=${1:-manifest.json}
RESUME=""; REDO_CASES=""
case "${2:-}" in
  --resume) RESUME=${3:?"--resume needs the scratch dir of the original launch"} ;;
  --redo-cases) REDO_CASES=${3:?"--redo-cases needs a case ID file"} ;;
  "") ;;
  *) echo "unknown mode: $2 — use --resume <scratch> or --redo-cases <file>"; exit 1 ;;
esac
TAG=${REDO_CASES:+redo-}
VAR=$(basename "$MANIFEST" .json | sed -e 's/^manifest-//' -e 's/^manifest$//')
NAME=cogletnet-v6${VAR:+-$VAR}-beam100k-400
T=${TMPDIR:-/tmp}
if [ -n "$RESUME" ]; then S=$RESUME; else S=$T/memeval-p17${VAR:+-$VAR}-$(date +%Y%m%d%H%M%S); mkdir -p "$S"; fi
AUTH=${MEMEVAL_AUTH_RUN:?set MEMEVAL_AUTH_RUN to your local credential wrapper}
D() { docker --context orbstack "$@"; }
LOG=reports/p17-logs; mkdir -p "$LOG"; echo $$ > "$LOG/p17${VAR:+-$VAR}.pid"
OUT=reports/p17-beam; mkdir -p "$OUT"
BEAM=data/normalized/beam-100k@3205395e897e
SEED=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089   # same seed as every P14/P16 BEAM run
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
COMMON="--suite $BEAM --run-seed $SEED $READER --docker-context orbstack --log-file-cap 134217728 --auto-resume-rate-limited 2"
V5=c2d0770c3503e470be3b57418e4774a80c28a5c1a1b57bbc4ff9e55891c72e89   # P15/P16 v5 source snapshot: the run must differ
RUN4=551903ad8cadeab20af1631a4576c57338266a41ec36124c6647a174b2379c80
MAXPASS=6          # resume passes per launch; stop earlier after two passes without gateway progress
GATEWAY_WAIT=600

echo "##### preflight $(date +%T) variant=${VAR:-legacy} name=$NAME"
[ -f "suts/cogletnet/$MANIFEST" ] || { echo "suts/cogletnet/$MANIFEST does not exist — nothing started"; exit 1; }
preflight() { python3 -B scripts/preflight_services.py --env ../cogletnet/.env --timeout 90 \
  --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY MEMORY_SUMMARY_VALIDATION_MODEL \
  --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL; }
wait_services() { preflight || { sleep 30; echo "  preflight retry 2 $(date +%T)"; preflight; } || { sleep 30; echo "  preflight retry 3 $(date +%T)"; preflight; }; }
# Validate local redo inputs before any service probe or container work.
validate_redo_cases() { python3 - "$OUT/$NAME/report.json" "$REDO_CASES" <<'PYCODE'
import json,sys
from pathlib import Path
report,ids=map(Path,sys.argv[1:])
if not report.is_file() or not ids.is_file():
    sys.exit("redo requires an existing report.json and case ID file — nothing started")
try:
    r=json.loads(report.read_text())
    selected=[x.strip() for x in ids.read_text().splitlines() if x.strip() and not x.lstrip().startswith('#')]
    known={key for case in r['cases'] for key in (case['case_id'],case['sample_id'])}
except (OSError,ValueError,KeyError) as e:
    sys.exit(f"invalid redo input: {e}")
if not selected: sys.exit("redo case ID list is empty — nothing started")
if any(x not in known for x in selected): sys.exit("redo list contains unknown case/sample IDs — nothing started")
print("  redo case list:",len(selected),"IDs")
PYCODE
}
if [ -n "$REDO_CASES" ]; then validate_redo_cases || exit 1; fi
wait_services || { echo "PREFLIGHT FAILED — a CogletNet model service is unavailable after 3 attempts; nothing started"; exit 5; }
[ -x "$AUTH" ] || { echo "authrun missing at $AUTH"; exit 1; }
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
print("  status", s.get('status_counts'), "| n", len(r['cases']), "|", top, "| ev prompt", ev.get('prompt_tokens'), f"| {r['wall_seconds']/60:.0f} min", "| errors", dict(errs.most_common(6)), "| resume", r.get('resume_count'), "| interruption", it.get('reason'), it.get('unscheduled_samples'))
PY
}
errors() { python3 - "$1" <<'PY'
import json,sys
try: r=json.load(open(sys.argv[1]+'/report.json')); print(sum(1 for c in r['cases'] if c['status']=='error'))
except Exception: print(-1)
PY
}
residue() { local n; n=$(D ps -aq --filter label=memeval.run | wc -l | tr -d ' '); echo "  residue run $n governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"; [ "$n" = 0 ] || { echo "RESIDUE — stopping"; exit 3; }; }
# gateway | native | host_timeout | unknown | none — gateway = every failed model attempt of the failing episodes is an HTTP 5xx / 429 / timeout /
# connection failure (the gateway or its upstream), or the case failed in the evaluator's own reader / transport; native =
# Native failures belong to CogletNet; INVALID_ARGUMENT/PROTOCOL_*/NATIVE_RPC_* are host contract failures.
classify() { python3 - "$1" <<'PY'
import json,sys,glob,os,re,collections

def parse_trace(raw):
    try: return json.loads(raw)
    except json.JSONDecodeError:
        # A historical redactor consumed the backslash before a string-internal
        # quote. Look past whitespace, but never repair a real closing delimiter.
        return json.loads(re.sub(r'(\[REDACTED\])"(?=\s*[^,}\]:\s])', r'\1\\"', raw))

run=sys.argv[1]
r=json.load(open(run+'/report.json'))
err=[c for c in r['cases'] if c['status']=='error']
if not err: print('none 0 gateway=0 native=0 host_timeout=0 host=0 unattributed=0 corrupt_trace_lines=0'); sys.exit(0)
dirs={os.path.basename(d).split('-',1)[1]:d for d in glob.glob(run+'/logs/*-paeval_*')}
GW_KINDS={'MODEL_UNAVAILABLE','MODEL_TIMEOUT','MODEL_TRANSPORT','MODEL_RATE_LIMITED','MODEL_RECOVERY_EXHAUSTED','OPERATION_OUTCOME_UNKNOWN'}
# MODEL_* kinds are gateway failures; message text alone cannot classify a host/native fault.
def is_host(kind):
    return kind=='INVALID_ARGUMENT' or kind.startswith(('PROTOCOL_', 'NATIVE_RPC_'))
EVAL_GW={'LLMTransportError','LLMHTTPError','rpc_timeout','process_exited','gateway_exhausted'}
native=collections.Counter(); gateway=collections.Counter(); host=collections.Counter(); host_timeout=collections.Counter(); unknown=0; corrupt_trace_lines=0
counts=collections.Counter()
episodes=collections.defaultdict(list)
for c in err:
    episodes[c.get('episode_id') or c.get('namespace') or c['case_id']].append(c)
for episode,cases in sorted(episodes.items()):
    etypes={c.get('error_type') for c in cases}
    found=False; bad=False; host_bad=False; terminated=False; native_kind=False
    for ns in sorted({c.get('namespace') for c in cases if c.get('namespace')}):
        d=dirs.get(ns)
        if not d: continue
        for line in open(d+'/sut.stderr.log', errors='replace'):
            i=line.find('sut_trace ')
            if i<0: continue
            try: t=parse_trace(line[i+10:])
            except json.JSONDecodeError:
                corrupt_trace_lines+=1; continue
            if t.get('kind')!='build': continue
            for c in (t.get('audit') or {}).get('calls') or []:
                for a in c.get('attempts') or []:
                    f=a.get('failure')
                    if not f: continue
                    found=True; k=str(f.get('kind')); m=str(f.get('message') or '')
                    if is_host(k): host_bad=True; host[k+': '+m[:60]]+=1
                    elif k.startswith('MODEL_') or k in GW_KINDS: gateway[k+': '+re.sub(r'\d+','#',m)[:48]]+=1
                    else: bad=True; native[k+': '+m[:60]]+=1
        try:
            for l in open(d+'/rpc-errors.jsonl', errors='replace'):
                try: rec=json.loads(l)
                except Exception: continue
                terminated |= bool(re.search(r'process_exited\(143\)', str(rec.get('error') or '')))
                detail=((((rec.get('rpc_error') or {}).get('data') or {}).get('details') or {}).get('cogletnet') or {})
                failures=[ev.get('error') or {} for ev in detail.get('events') or []]
                if detail.get('kind') != 'BATCH_NOT_READY': failures.append(detail)
                if isinstance(detail.get('cause'),dict): failures.append(detail['cause'])
                for failure in failures:
                    k=failure.get('kind')
                    native_kind |= bool(k)
                    if k and is_host(k): host_bad=True; host[k]+=1; found=True; continue
                    if k and (k.startswith('MODEL_') or k in GW_KINDS): gateway[k]+=1; found=True; continue
                    if k and k not in ('MODEL_RECOVERY_EXHAUSTED','BATCH_NOT_READY') and not k.startswith('MODEL_'): bad=True; native[k]+=1; found=True
        except FileNotFoundError: pass
    if terminated and not native_kind:
        host_timeout[episode]=len(cases); kind='host_timeout'
    elif host_bad:
        kind='host'
    elif bad:
        kind='native'
    elif found or etypes & EVAL_GW:
        kind='gateway'
        if not found: gateway['evaluator: '+','.join(sorted(x for x in etypes if x))]+=1
    else:
        kind='unattributed'; unknown+=1
    counts[kind]+=len(cases)
cls=next((kind for kind in ('host_timeout','host','native','gateway') if counts[kind]),'unknown')
fields=' '.join(f'{kind}={counts[kind]}' for kind in ('gateway','native','host_timeout','host','unattributed'))
print(cls, len(err), fields, f'corrupt_trace_lines={corrupt_trace_lines}', '| gateway:', dict(gateway.most_common(4)), '| native:', dict(native.most_common(4)), '| host:', dict(host.most_common(4)), '| host_timeout episodes:', len(host_timeout), 'cases:', sum(host_timeout.values()), '| unattributed episodes:', unknown)
PY
}
# Four inputs: classification line, previous gateway count, passes used, MAXPASS.
# The caller appends stalled=<n> to the line (not classify's output): consecutive
# non-improving passes BEFORE the comparison represented by the current line.
decide() { python3 - "$1" "$2" "$3" "$4" <<'PY'
import re,sys
line,previous,used,maximum=sys.argv[1:]
fields=dict(re.findall(r'(?:^|\s)(gateway|host)=(\d+)(?=\s|$)',line.split('|',1)[0]))
if 'gateway' not in fields: print('stop invalid classification'); sys.exit(0)
g=int(fields['gateway']); previous=int(previous); used=int(used); maximum=int(maximum)
progress=re.search(r'(?:^|\s)stalled=(\d+)$',line)
stalled=int(progress[1]) if progress else 0
if g==0: print('stop host errors remain' if int(fields.get('host', 0))>0 else 'stop only native/host_timeout remain')
elif used>=maximum: print('stop max passes')
elif used>0 and previous>=0 and g>=previous and stalled+1>=2: print('stop no gateway progress')
else: print('continue')
PY
}
attribute() { python3 -B scripts/cogletnet_failures.py "$OUT/$NAME" --json "$LOG/$NAME-failures-${TAG}$1.json" 2>/dev/null | tail -n 30 || echo "  (failure attribution script not applicable)"; }
# 压缩率 Σtokens(Lk)/Σtokens(Lk-1) per build layer, tracked for every run (scripts/cogletnet_compression.py; design/followups.md F107).
compression() { python3 -B scripts/cogletnet_compression.py "$OUT/$NAME" --json "$LOG/$NAME-compression.json" 2>/dev/null | head -n 12 || echo "  (compression script not applicable)"; }

if [ -z "$RESUME" ]; then
go build -o bin/memeval ./cmd/memeval && bash scripts/build_linux_memeval.sh arm64 | tail -1 || exit 1
echo "##### binary $(shasum -a 256 bin/memeval | cut -c1-12) scratch $S $(date +%T)"
rm -rf "${S:?}/smoke"; bin/memeval run --suite "$BEAM" --sut null $READER --run-seed "$SEED" --limit 1 --out "$S/smoke" > "$LOG/smoke${VAR:+-$VAR}.out" 2>&1
python3 - "$S/smoke/report.json" <<'PY' || { echo "PREFLIGHT FAILED — evaluator reader/judge smoke run did not score; see $LOG/smoke*.out"; exit 5; }
import json,sys
r=json.load(open(sys.argv[1])); ok=sum(1 for c in r['cases'] if c['status']=='scored')
print("  evaluator smoke: scored", ok, "of", len(r['cases'])); sys.exit(0 if ok else 1)
PY


echo "##### cogletnet: stage current ../cogletnet $(date +%T)"
python3 -B scripts/stage_cogletnet.py --source ../cogletnet --out "$S/staged" > "$LOG/stage${VAR:+-$VAR}.out" 2>&1 || { echo "staging failed"; tail -5 "$LOG/stage${VAR:+-$VAR}.out"; exit 1; }
SNAP=$(python3 -c "import json;print(json.loads(open('$LOG/stage${VAR:+-$VAR}.out').readline()).get('source_snapshot_sha256',''))")
[ -n "$SNAP" ] || { echo "no snapshot hash in staging output — nothing started"; exit 1; }
[ "$SNAP" != "$V5" ] || { echo "staged snapshot ${SNAP:0:12} is still the v5 source — not a new version; nothing started"; exit 1; }
if [ -n "$REDO_CASES" ] && [ "$SNAP" = "$RUN4" ]; then
  echo "staged snapshot ${SNAP:0:16} is still run 4 — MEMORY_TOKENS source update is missing; nothing started"; exit 1
fi
echo "  snapshot ${SNAP:0:16} (v5 was ${V5:0:12}) → $NAME"
find "$S/staged" -exec touch {} +   # F93: copied files keep old mtimes and the daily tmp cleanup would delete them mid-run
CM=$S/staged/cogletnet/$MANIFEST
[ -f "$CM" ] || { echo "staged manifest $CM missing — nothing started"; exit 1; }
python3 - "$CM" "$SNAP" <<'PY' || exit 1
import json,sys
m=json.load(open(sys.argv[1])); got=((m.get('bridge') or {}).get('config') or {}).get('source_snapshot_sha256')
if got!=sys.argv[2]: print(f"  staged manifest {sys.argv[1]} carries snapshot {str(got)[:12]} ≠ {sys.argv[2][:12]} — staging must stamp every manifest; nothing started"); sys.exit(1)
print("  manifest variant:", m.get('variant'), "| snapshot stamped")
PY
SECONDS=0; "$AUTH" bin/memeval env check "$CM" --docker-context orbstack > "$LOG/$NAME-${TAG}check.out" 2>&1 || "$AUTH" bin/memeval env install "$CM" --docker-context orbstack --sut-llm-model gpt-6-luna --sut-llm-effort low > "$LOG/$NAME-${TAG}install.out" 2>&1
echo "  cogletnet env exit=$? ${SECONDS}s $(grep -o '"valid": [a-z]*' "$LOG/$NAME-${TAG}install.out" "$LOG/$NAME-${TAG}check.out" 2>/dev/null | head -1)"
grep -q '"valid": true' "$LOG/$NAME-${TAG}install.out" "$LOG/$NAME-${TAG}check.out" 2>/dev/null || { echo "ENV NOT VALID — the external host did not build or install against the new source; see $LOG/$NAME-${TAG}install.out"; tail -12 "$LOG/$NAME-${TAG}install.out"; exit 1; }
if [ -n "$REDO_CASES" ]; then
  "$AUTH" bin/memeval env check "$CM" --docker-context orbstack > "$LOG/$NAME-${TAG}check.out" 2>&1 && grep -q '"valid": true' "$LOG/$NAME-${TAG}check.out" || { echo "ENV NOT VALID for redo manifest — nothing started"; exit 1; }
  previous="$LOG/$NAME-manifest-$(date +%Y%m%d%H%M%S).json"
  [ -f "$LOG/$NAME-manifest.json" ] && [ ! -e "$previous" ] || { echo "previous launch manifest missing or backup already exists — nothing started"; exit 1; }
  mv "$LOG/$NAME-manifest.json" "$previous" || exit 1
fi
cp "$CM" "$LOG/$NAME-manifest.json"   # the staged manifest a later --resume must be given

if [ -n "$REDO_CASES" ]; then
  echo "##### $NAME redo listed scored cases $(date +%T)"; SECONDS=0
  "$AUTH" bin/memeval run --resume "$OUT/$NAME" --resume-statuses scored --resume-cases "@$REDO_CASES" --resume-sut-change --sut "$CM" --parallel 2 --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON > "$LOG/$NAME-redo.out" 2>&1
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-redo.out" | cut -c1-160)"
else
rm -rf "${OUT:?}/$NAME"
echo "##### $NAME start $(date +%T)"; SECONDS=0
"$AUTH" bin/memeval run --sut "$CM" --parallel 2 --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON --out "$OUT/$NAME" > "$LOG/$NAME.out" 2>&1
echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME.out" | cut -c1-160)"
fi
summ "$OUT/$NAME"; residue
attribute pass0
else
  CM=$S/staged/cogletnet/$MANIFEST
  echo "##### $NAME resume-only mode: scratch $S $(date +%T)"
  [ -f "$CM" ] || { echo "staged manifest $CM missing — nothing started"; exit 1; }
  [ -f "$OUT/$NAME/report.json" ] || { echo "no $OUT/$NAME/report.json — nothing started"; exit 1; }
  cmp -s "$CM" "$LOG/$NAME-manifest.json" || { echo "staged manifest differs from the launch copy $LOG/$NAME-manifest.json — nothing started"; exit 1; }
  find "$S/staged" -exec touch {} +
  "$AUTH" bin/memeval env check "$CM" --docker-context orbstack > "$LOG/$NAME-${TAG}check.out" 2>&1 && grep -q '"valid": true' "$LOG/$NAME-${TAG}check.out" || { echo "ENV NOT VALID for the staged manifest — nothing started"; exit 1; }
  summ "$OUT/$NAME"
fi

prev=$(errors "$OUT/$NAME")
pass=$(ls "$LOG/$NAME-resume"*.out 2>/dev/null | wc -l | tr -d ' ')   # continue numbering across resume-only launches
start=$pass
prev_gateway=-1; stalled=0
while [ "$prev" -gt 0 ]; do
  cls=$(classify "$OUT/$NAME"); echo "  remaining errors: $cls"
  decision=$(decide "$cls stalled=$stalled" "$prev_gateway" "$((pass-start))" "$MAXPASS")
  case "$decision" in
    continue) ;;
    *)
      echo "  $decision"
      if [ "$decision" = "stop only native/host_timeout remain" ]; then
        counts=${cls%%|*}
        [[ "$counts" =~ host_timeout=[1-9][0-9]* ]] && echo "  host timeout failures remain — not retried with the same execution limit"
        [[ "$counts" =~ native=[1-9][0-9]* ]] && echo "  CogletNet-native failures remain — not retried; see the attribution JSON for the upstream report"
      fi
      break ;;
  esac
  g=${cls#* gateway=}; g=${g%% *}
  if [ "$pass" -gt "$start" ] && [ "$g" -ge "$prev_gateway" ]; then stalled=$((stalled+1)); else stalled=0; fi
  prev_gateway=$g
  echo "  gateway-class failures — waiting ${GATEWAY_WAIT}s before the next pass $(date +%T)"; sleep "$GATEWAY_WAIT"
  pass=$((pass+1))
  echo "##### $NAME resume pass $pass (error cases: $prev) $(date +%T)"
  wait_services || { echo "PREFLIGHT FAILED before resume pass $pass — services unavailable; waiting ${GATEWAY_WAIT}s and retrying the probe once $(date +%T)"; sleep "$GATEWAY_WAIT"; wait_services || { echo "PREFLIGHT FAILED twice — stopping the retry loop"; break; }; }
  SECONDS=0
  "$AUTH" bin/memeval run --resume "$OUT/$NAME" --resume-statuses error --sut "$CM" --parallel 2 --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON > "$LOG/$NAME-resume$pass.out" 2>&1
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-resume$pass.out" | cut -c1-160)"
  summ "$OUT/$NAME"; residue
  attribute "pass$pass"
  prev=$(errors "$OUT/$NAME")
done
echo "  final error cases: $prev after $pass resume pass(es)"
attribute final
compression
echo "##### done $(date +%T)"
