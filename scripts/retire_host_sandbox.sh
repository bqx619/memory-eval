#!/bin/bash
set -euo pipefail
# All roots must be supplied explicitly. No HOME/TMPDIR defaults are consulted.
# Dry-run is the default; the policy also protects overlapping retained roots.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 -B "$script_dir/retire_host_sandbox.py" "$@"
