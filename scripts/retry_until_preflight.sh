#!/bin/bash
# Re-run a run script until its preflight passes. Run scripts (scripts/p16_beam_full.sh, scripts/p18_mem0_openai_embed.sh, …)
# exit 5 when a precondition outside our control is missing — Docker daemon down, a model endpoint unavailable, the judge
# refusing (e.g. HTTP 402 billing) — after a cheap one-case smoke and before any image build or container start. This wrapper
# retries only that exit code; every other exit (success, residue, env failure) ends the loop and is reported as is.
# Usage: scripts/retry_until_preflight.sh MAX_ATTEMPTS INTERVAL_SECONDS CMD [ARGS…]
# Launch detached: scripts/detach.sh LOGFILE PIDFILE bash scripts/retry_until_preflight.sh 48 900 bash scripts/<run>.sh run
set -u
[ $# -ge 3 ] || { echo "usage: $0 MAX_ATTEMPTS INTERVAL_SECONDS CMD [ARGS...]" >&2; exit 2; }
MAX=$1 INTERVAL=$2; shift 2
for ((attempt = 1; attempt <= MAX; attempt++)); do
  echo "##### attempt $attempt/$MAX $(date '+%F %T')"
  "$@"; rc=$?
  if [ "$rc" -ne 5 ]; then echo "##### command finished exit=$rc after $attempt attempt(s) $(date '+%F %T')"; exit "$rc"; fi
  [ "$attempt" -lt "$MAX" ] && { echo "##### preflight failed (exit 5); next attempt in ${INTERVAL}s"; sleep "$INTERVAL"; }
done
echo "##### gave up after $MAX attempts $(date '+%F %T')"; exit 5
