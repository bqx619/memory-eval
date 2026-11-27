"""Offline TypeSafe decision contract verification (supersedes emulated M3 paths)."""
import sys
sys.dont_write_bytecode = True
import json
import subprocess
from verification import ROOT, verification_main


def main(args, work):
    command = ['go', 'test', '-count=1', '-v', './internal/llm/decision', './internal/scoring/judge']
    with (work.out / 'typesafe-record-replay.log').open('w') as log:
        subprocess.run(command, cwd=ROOT, env=work.env, stdout=log,
                       stderr=subprocess.STDOUT, check=True)
    (work.out / 'acceptance.json').write_text(json.dumps({
        'status': 'passed', 'fixture_only': True, 'provider': 'typesafe',
        'legacy_emulated_and_llm_judges': 'removed by P5',
        'evidence': 'typesafe-record-replay.log'}, indent=2) + '\n')
    print('TypeSafe normalization, native wire, errors, all-family record/replay: PASS')


if __name__ == '__main__':
    verification_main(main)
