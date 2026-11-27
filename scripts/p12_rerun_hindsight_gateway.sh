#!/bin/bash
# Rerun only hindsight-gateway in the P12 answer-track probe after the null document_id fix.
cd "$(dirname "$0")/.." || exit 1
A=reports/p12-answer-track/logs; mkdir -p "$A"; D() { docker --context orbstack "$@"; }
go build -o bin/memeval ./cmd/memeval || exit 1
m=suts/hindsight/manifest-gateway.json
bin/memeval env check $m --docker-context orbstack > "$A/check-hindsight-rerun.out" 2>&1 || { SECONDS=0; bin/memeval env install $m --docker-context orbstack > "$A/install-hindsight-rerun.out" 2>&1; echo "install exit=$? ${SECONDS}s"; }
out=reports/p12-answer-track/hindsight-llm-gateway-items-mab-cr-l50; rm -rf "${out:?}"; SECONDS=0
bin/memeval run --suite data/normalized/mab-conflict-resolution@7ea066982b14 --sut $m --mode fixed_reader --reset-policy incremental_per_episode --reader-input items --limit 50 --llm record --sut-llm-budget-tokens 20000000 --out "$out" --docker-context orbstack > "$A/hindsight-rerun.out" 2>&1
echo "exit=$? ${SECONDS}s $(tail -1 "$A/hindsight-rerun.out" | cut -c1-200)"
python3 -c "
import json; r=json.load(open('$out/report.json')); s=r.get('summary') or {}; m=s.get('metrics') or {}
print('status', s.get('status_counts'), '| EM', m.get('mab_exact_match'), '| F1', m.get('mab_f1'), '| sut prompt', ((r.get('billing') or {}).get('sut') or {}).get('prompt_tokens'))"
echo "residue run $(D ps -aq --filter label=memeval.run | wc -l | tr -d ' ') governors $(D ps -aq --filter label=memeval.role=governor | wc -l | tr -d ' ')"
