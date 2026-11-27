#!/bin/bash
# P21: BEAM 100K, all 400 cases, on the CogletNet source at upstream HEAD 6056022 (2026-11-03: RAPTOR-style cross-layer Tree
# retrieval over Body vectors with node-reference-v1 three-class Jev admission; abstractive-ratio-v4 summaries with recorded-v1
# recording-time tags). One run, `v9` (manifest-tree.json, retrieval_mode tree, INDEX_REPRESENTATIONS=body): run name
# cogletnet-v9-beam100k-400; protocol, seed, reader and judge are those of every P14–P20 BEAM run (design/p21-cogletnet-v9.md).
# Gateway-class failures are retried automatically: `--auto-resume-rate-limited 2` inside the run, then up to MAXPASS
# `--resume --resume-statuses error` passes, including native/host-timeout cases when gateway failures coexist; stop after two
# consecutive passes without fewer gateway cases. Surviving errors keep their attribution (scripts/cogletnet_failures.py).
# Run only on an explicitly configured Docker host; no personal host path is assumed.
# no per-machine TMPDIR, the upstream source and its .env synced to the same absolute paths; only the manifest's env_passthrough keys
# are copied from the upstream .env into the evaluator's configuration file.
# Aborts before touching containers when the Docker daemon is unavailable (never started from here) or a model service is down
# (CogletNet's embedding / summary+validation / JEV endpoints and the evaluator's own reader + judge, smoke-tested on one BEAM
# case with the null SUT): exit 5, so scripts/retry_until_preflight.sh can retry the launch later.
# Usage: scripts/p21_cogletnet_v9.sh run <manifest-*.json> <label> | resume <manifest-*.json> <label> <scratch dir> | cleanup
#   run     — preflight, staging, image build, the 400-case run and the retry loop. A preflight failure exits 5 before anything is
#             started so scripts/retry_until_preflight.sh can relaunch; a finished run is never restarted (an existing report.json stops `run`).
#   resume  — skips smoke / staging / install and only runs the retry loop on the existing run directory with the manifest staged at
#             the original launch (same SUT identity), e.g. after a gateway outage outlasted the loop.
#   cleanup — after the run directory has been copied off the host: destroy memeval images/envs, gc abandoned memeval resources,
#             prune only the build-cache records created since this script's own image build (the daemon is shared with other
#             projects: no blanket `builder prune`), remove this script's scratch directories (they hold the merged env file).
# Launch detached on the host — scripts/detach.sh reports/p21-logs/watch.out reports/p21-logs/watch.pid \
#   bash scripts/retry_until_preflight.sh 48 900 bash scripts/p21_cogletnet_v9.sh run manifest-tree.json v9
# — never `nohup … &` from an agent shell (see AGENTS.md).
set -u
cd "$(dirname "$0")/.." || exit 1
MODE=${1:-run}
D() { docker --context orbstack "$@"; }
LOG=reports/p21-logs; mkdir -p "$LOG"
OUT=reports/p21-beam; mkdir -p "$OUT"
UP=${MEMEVAL_UPSTREAM_ROOT:-../cogletnet}  # read-only upstream source
CENV=${MEMEVAL_UPSTREAM_ENV:-$UP/.env}  # private credentials; never printed
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
  rm -rf /private/tmp/memeval-p21-*
  echo "  remaining: memeval.env images $(D images -q --filter label=memeval.env | wc -l | tr -d ' '), memeval/* images $(D images --format '{{.Repository}}' | grep -c '^memeval/'), memeval.run containers $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ')"
  disk; echo "##### cleanup done $(date +%T)"; exit 0
fi

MANIFEST=${2:?"$MODE needs the manifest file name, e.g. manifest-tree.json"}
LABEL=${3:?"$MODE needs the run label, e.g. v9"}
case "$LABEL" in *[!A-Za-z0-9._-]*|"") echo "label must be [A-Za-z0-9._-]+ — nothing started"; exit 1 ;; esac
NAME=cogletnet-$LABEL-beam100k-400
echo $$ > "$LOG/p21-$LABEL.pid"   # own pid (the launcher, scripts/detach.sh, exits at once)
case "$MODE" in
  run) S=/private/tmp/memeval-p21-$LABEL-$(date +%Y%m%d%H%M%S); mkdir -p "$S" ;;   # literal scratch root: TMPDIR differs between hosts
  resume) S=${4:?"resume needs the scratch dir of the original launch"}; [ -d "$S/staged" ] || { echo "no staged tree under $S — nothing started"; exit 1; } ;;
  *) echo "unknown mode: $MODE — use run <manifest> <label> | resume <manifest> <label> <scratch> | cleanup"; exit 1 ;;
esac
BEAM=data/normalized/beam-100k@3205395e897e
SEED=fb7ffb25f4ffbd026f8b045117dd50b75a242f9e76f0d496acbedb8b974c1089   # same seed as every P14–P19 BEAM run
READER="--mode fixed_reader --reset-policy incremental_per_episode --reader-input items --reader-max-tokens 2048 --llm record"
COMMON="--suite $BEAM --run-seed $SEED $READER --docker-context orbstack --log-file-cap 134217728 --auto-resume-rate-limited 2"
# Source snapshots already evaluated (P15/P16 v5; P17 v6 legacy, leaf-memory, leaf-embed; P19 first v7 attempt 0e976e8 that failed
# on abstractive-ratio-v1; second attempt 7d1c093 / v2 aborted for language drift;
# P19 v3 run a8702436 at 9821e1e; P20 v8-LV1 / v8 6a3cbd20 at 10f356d): a new run must stage something new.
OLD_SNAPS="c2d0770c3503e470be3b57418e4774a80c28a5c1a1b57bbc4ff9e55891c72e89 551903ad8cadeab20af1631a4576c57338266a41ec36124c6647a174b2379c80 0b4058cbf43300ed6905ce40e1016b44c5c5ac31d2a3c1374c91b7f9a8c3ae87 d6fba286472b504778a6d7e6773b7b5f45f77654022122cb3df4629c469731f9 272523652c8a82af6a4a6d84849322dbddfc9e78b0da3c82c965b4e8137b09eb 609d100eadbd891da2cd4382d7c2c295c89b9ae18990eb0eee5ee097cd34f004 a870243673f66638068fbefbb4451c2c6ac56c3abb7e6306cce16c4c226b026d 6a3cbd206956a33d0337d31c21eaf393e3a2e1ca8ffc1acb2c4578997448302d"
MAXPASS=6          # resume passes per launch; stop earlier after two passes without gateway progress
GATEWAY_WAIT=600

echo "##### preflight $(date +%T) mode=$MODE label=$LABEL manifest=$MANIFEST name=$NAME"
[ -f "suts/cogletnet/$MANIFEST" ] || { echo "suts/cogletnet/$MANIFEST does not exist — nothing started"; exit 1; }
[ -x bin/memeval ] && [ -f bin/memeval-linux-arm64 ] || { echo "bin/memeval or bin/memeval-linux-arm64 missing — build on the dev machine and sync (not rebuilt here)"; exit 1; }
NEWER=$(find cmd internal -name '*.go' -newer bin/memeval | wc -l | tr -d ' '); [ "$NEWER" = 0 ] || echo "  note: $NEWER Go source files are newer than bin/memeval; the binary is used as is"
[ -f "$CENV" ] || { echo "$CENV missing — CogletNet service credentials are not on this host; nothing started"; exit 1; }
[ -d "$UP/crates" ] || { echo "$UP has no crates/ — upstream source not synced to this host; nothing started"; exit 1; }
D info > /dev/null 2>&1 || { echo "PREFLIGHT FAILED — Docker daemon (context orbstack) unavailable; start OrbStack first, nothing started"; exit 5; }
# The evaluator reads one configuration file (--env-file; default .env) and its manifest env_passthrough values come from that same
# file, so .env plus ONLY the passthrough keys of the upstream .env are joined into a 0600 file under the scratch root (removed by
# cleanup). Every non-public value of that file is also a redaction target (internal/config/dotenv Settings.Secrets), so copying the
# whole upstream .env would turn its short values (token counts, model names) into [REDACTED] everywhere and invalidate judge cassettes.
KEYS=$(python3 -c "import json,sys;print(' '.join(json.load(open(sys.argv[1]))['process'].get('env_passthrough') or []))" "suts/cogletnet/$MANIFEST")
ENVF=$S/env/.env; ( umask 077; mkdir -p "$S/env" && { cat .env; echo; for k in $KEYS; do grep -E "^$k=" "$CENV" || { echo "passthrough key $k missing in $CENV" >&2; exit 1; }; done; } > "$ENVF" ) || { echo "cannot write $ENVF"; exit 1; }
echo "  env file: .env + $(echo $KEYS | wc -w | tr -d ' ') passthrough key(s) from $CENV"
ME() { bin/memeval --env-file "$ENVF" "$@"; }
# Summary and validation default to the host model (HOST_GENERATION_MODEL, injected as the evaluator's --sut-llm-model); probe that.
preflight() { python3 -B scripts/preflight_services.py --env .env --env "$CENV" --timeout 90 \
  --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
  --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY gpt-6-luna \
  --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL; }
wait_services() { preflight || { sleep 30; echo "  preflight retry 2 $(date +%T)"; preflight; } || { sleep 30; echo "  preflight retry 3 $(date +%T)"; preflight; }; }
wait_services || { echo "PREFLIGHT FAILED — a CogletNet model service is unavailable after 3 attempts; nothing started"; exit 5; }

summ() { python3 - "$1" <<'PY'
import json,sys,collections
p=sys.argv[1]
try: r=json.load(open(p+'/report.json'))
except Exception as e: print("  no report:", e); sys.exit()
s=r.get('summary') or {}
if not s: print("  REJECTED", (r.get('reason') or '')[:160]); sys.exit()
m=s.get('metrics') or {}
top={k:(round(v['mean'],4),v['n']) for k,v in m.items() if isinstance(v,dict) and v.get('mean') is not None and k in ('beam.llm_judge_score','beam.evidence_recall','beam.event_ordering.final')}
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
# gateway | native | host_timeout | host | unattributed — gateway = every failed model attempt of the failing episodes is an HTTP 5xx /
# 429 / timeout / connection failure (the gateway or its upstream), or the case failed in the evaluator's own reader / transport;
# native failures belong to CogletNet; INVALID_ARGUMENT/PROTOCOL_*/NATIVE_RPC_* are host contract failures. Same as P17.
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
# Four inputs: classification line, previous gateway count, passes used, MAXPASS. The caller appends stalled=<n> to the line.
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
attribute() { python3 -B scripts/cogletnet_failures.py "$OUT/$NAME" --json "$LOG/$NAME-failures-$1.json" 2>/dev/null | tail -n 30 || echo "  (failure attribution script not applicable)"; }
# 压缩率 Σtokens(Lk)/Σtokens(Lk-1) per build layer, tracked for every run (scripts/cogletnet_compression.py; design/followups.md F107).
compression() { python3 -B scripts/cogletnet_compression.py "$OUT/$NAME" --json "$LOG/$NAME-compression.json" 2>/dev/null | head -n 12 || echo "  (compression script not applicable)"; }

if [ "$MODE" = run ]; then
echo "##### binary $(shasum -a 256 bin/memeval | cut -c1-12) scratch $S $(date +%T)"
# evaluator smoke: one BEAM case with the null SUT exercises the fixed reader (gpt-6-luna) and the TypeSafe judge (a 402 here = judge billing)
rm -rf "${S:?}/smoke"; ME run --suite "$BEAM" --sut null $READER --run-seed "$SEED" --limit 1 --out "$S/smoke" > "$LOG/smoke-$LABEL.out" 2>&1
python3 - "$S/smoke/report.json" "$S/smoke/llm_ledger.jsonl" <<'PY' || { echo "PREFLIGHT FAILED — evaluator reader/judge smoke run did not score; see $LOG/smoke*.out"; exit 5; }
import json,sys
r=json.load(open(sys.argv[1])); ok=sum(1 for c in r['cases'] if c['status']=='scored')
for l in open(sys.argv[2]):
    e=json.loads(l)
    if e.get('error'): print("  smoke ledger:", e.get('purpose'), e.get('http_status'), e.get('error'))
print("  evaluator smoke: scored", ok, "of", len(r['cases'])); sys.exit(0 if ok else 1)
PY

echo "##### cogletnet: stage $UP $(date +%T)"
python3 -B scripts/stage_cogletnet.py --source "$UP" --out "$S/staged" > "$LOG/stage-$LABEL.out" 2>&1 || { echo "staging failed"; tail -5 "$LOG/stage-$LABEL.out"; exit 1; }
SNAP=$(python3 -c "import json;print(json.loads(open('$LOG/stage-$LABEL.out').readline()).get('source_snapshot_sha256',''))")
[ -n "$SNAP" ] || { echo "no snapshot hash in staging output — nothing started"; exit 1; }
case " $OLD_SNAPS " in *" $SNAP "*) echo "staged snapshot ${SNAP:0:12} was already evaluated (v5 / v6 / v7 / v8) — not a new version; nothing started"; exit 1 ;; esac
echo "  snapshot ${SNAP:0:16} → $NAME"
find "$S/staged" -exec touch {} +   # F93: copied files keep old mtimes and a periodic tmp cleanup would delete them mid-run
CM=$S/staged/cogletnet/$MANIFEST
[ -f "$CM" ] || { echo "staged manifest $CM missing — nothing started"; exit 1; }
python3 - "$CM" "$SNAP" <<'PY' || exit 1
import json,sys
m=json.load(open(sys.argv[1])); got=((m.get('bridge') or {}).get('config') or {}).get('source_snapshot_sha256')
if got!=sys.argv[2]: print(f"  staged manifest {sys.argv[1]} carries snapshot {str(got)[:12]} ≠ {sys.argv[2][:12]} — staging must stamp every manifest; nothing started"); sys.exit(1)
print("  manifest variant:", m.get('variant'), "| snapshot stamped")
PY
[ -s "$LOG/buildcache-before.txt" ] || cache_ids > "$LOG/buildcache-before.txt"   # cleanup prunes only records newer than this
SECONDS=0; ME env check "$CM" --docker-context orbstack > "$LOG/$NAME-check.out" 2>&1 || ME env install "$CM" --docker-context orbstack --sut-llm-model gpt-6-luna --sut-llm-effort low > "$LOG/$NAME-install.out" 2>&1
echo "  cogletnet env exit=$? ${SECONDS}s $(grep -o '"valid": [a-z]*' "$LOG/$NAME-install.out" "$LOG/$NAME-check.out" 2>/dev/null | head -1)"
grep -q '"valid": true' "$LOG/$NAME-install.out" "$LOG/$NAME-check.out" 2>/dev/null || { echo "ENV NOT VALID — the external host did not build or install against the new source; see $LOG/$NAME-install.out"; tail -12 "$LOG/$NAME-install.out"; exit 1; }
cp "$CM" "$LOG/$NAME-manifest.json"   # the staged manifest a later resume must be given
[ -e "$OUT/$NAME/report.json" ] && { echo "$OUT/$NAME/report.json already exists — a finished run is never restarted; move it away first (nothing started)"; exit 1; }
rm -rf "${OUT:?}/$NAME"
echo "##### $NAME start $(date +%T)"; SECONDS=0
ME run --sut "$CM" --parallel 2 --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON --out "$OUT/$NAME" > "$LOG/$NAME.out" 2>&1
echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME.out" | cut -c1-160)"
summ "$OUT/$NAME"; residue
attribute pass0
else
  CM=$S/staged/cogletnet/$MANIFEST
  echo "##### $NAME resume-only mode: scratch $S $(date +%T)"
  [ -f "$CM" ] || { echo "staged manifest $CM missing — nothing started"; exit 1; }
  [ -f "$OUT/$NAME/report.json" ] || { echo "no $OUT/$NAME/report.json — nothing started"; exit 1; }
  cmp -s "$CM" "$LOG/$NAME-manifest.json" || { echo "staged manifest differs from the launch copy $LOG/$NAME-manifest.json — nothing started"; exit 1; }
  find "$S/staged" -exec touch {} +
  ME env check "$CM" --docker-context orbstack > "$LOG/$NAME-check.out" 2>&1 && grep -q '"valid": true' "$LOG/$NAME-check.out" || { echo "ENV NOT VALID for the staged manifest — nothing started"; exit 1; }
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
  ME run --resume "$OUT/$NAME" --resume-statuses error --sut "$CM" --parallel 2 --sut-llm-model gpt-6-luna --sut-llm-effort low --trace-text $COMMON > "$LOG/$NAME-resume$pass.out" 2>&1
  echo "  exit=$? ${SECONDS}s $(tail -1 "$LOG/$NAME-resume$pass.out" | cut -c1-160)"
  summ "$OUT/$NAME"; residue
  attribute "pass$pass"
  prev=$(errors "$OUT/$NAME")
done
echo "  final error cases: $prev after $pass resume pass(es)"
attribute final
compression
disk
echo "##### done $(date +%T) — copy $OUT/$NAME off this host, then: bash scripts/p21_cogletnet_v9.sh cleanup"
