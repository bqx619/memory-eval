#!/usr/bin/env python3
"""Compare M0 artifacts with the untouched Python goldens (stdlib only)."""
import argparse
import json
import math
from pathlib import Path

FIELDS = ('case_id', 'episode_id', 'category', 'cutoff', 'status', 'passed',
          'assertions', 'assertions_passed', 'failures', 'retrieved_source_ids',
          'returned_chars', 'metrics')

def differences(a, b, path='', *, golden_keys_only=False):
    """Compare actual a with golden b; only aggregates allow additive keys."""
    if type(a) is bool or type(b) is bool:
        return [] if type(a) is type(b) and a == b else [f'{path}: {a!r} != {b!r}']
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return [] if math.isclose(a, b, rel_tol=0, abs_tol=1e-9) else [f'{path}: {a} != {b}']
    if isinstance(a, dict) and isinstance(b, dict):
        out = []
        mismatched = b.keys() - a.keys() if golden_keys_only else a.keys() ^ b.keys()
        if mismatched:
            out.append(f'{path}: keys {sorted(mismatched)}')
        for key in sorted(a.keys() & b.keys()):
            out.extend(differences(a[key], b[key], f'{path}.{key}',
                                   golden_keys_only=golden_keys_only))
        return out
    if isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            return [f'{path}: length {len(a)} != {len(b)}']
        return [d for i, (x, y) in enumerate(zip(a, b))
                for d in differences(x, y, f'{path}[{i}]', golden_keys_only=golden_keys_only)]
    return [] if a == b and type(a) is type(b) else [f'{path}: {a!r} != {b!r}']

def check(actual, golden):
    def read(path):
        return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    a, b = read(actual / 'cases.jsonl'), read(golden / 'cases.jsonl')
    # Keep the M0 per-case contract strict, including missing versus null fields.
    errors = differences([{k: r[k] for k in FIELDS if k in r} for r in a],
                         [{k: r[k] for k in FIELDS if k in r} for r in b], 'cases')
    ar = json.loads((actual / 'report.json').read_text())
    br = json.loads((golden / 'report.json').read_text())
    def summary(s):
        return {k: v for k, v in s.items() if k not in ('timings', 'billing')}
    errors += differences(summary(ar['summary']), summary(br['summary']), 'summary',
                          golden_keys_only=True)
    errors += differences({k: summary(v) for k, v in ar['by_category'].items()},
                          {k: summary(v) for k, v in br['by_category'].items()}, 'by_category',
                          golden_keys_only=True)
    return errors

def main():
    p = argparse.ArgumentParser()
    p.add_argument('actual', type=Path)
    p.add_argument('golden', type=Path)
    args = p.parse_args()
    errors = check(args.actual, args.golden)
    if errors:
        print('\n'.join(errors))
        return 1
    return 0  # Empty output means empty diff.

if __name__ == '__main__':
    raise SystemExit(main())
