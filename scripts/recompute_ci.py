#!/usr/bin/env python3
"""Independent stdlib replication of fixed-index, paired cluster percentile CI."""
import argparse
import json
import math
from pathlib import Path
from statistics import mean

def calculate(left, right, indices, metric='pass'):
    a = json.loads(Path(left).read_text())
    b = json.loads(Path(right).read_text())
    paired = {(r['case_id'], r['repeat_index']): r for r in b['cases']}
    per_case, clusters = {}, {}
    for x in a['cases']:
        y = paired[x['case_id'], x['repeat_index']]
        assert x['status'] == y['status'] and x['cluster_id'] == y['cluster_id']
        if x['status'] != 'scored':
            continue
        ax = x['passed'] if metric == 'pass' else x['metrics'].get(metric)
        by = y['passed'] if metric == 'pass' else y['metrics'].get(metric)
        assert (ax is None) == (by is None)
        if ax is None:
            continue
        per_case.setdefault(x['case_id'], []).append(float(by) - float(ax))
        clusters[x['case_id']] = x['cluster_id']
    groups = {}
    for cid, values in sorted(per_case.items()):
        groups.setdefault(clusters[cid], []).append(mean(values))
    keys = sorted(groups)
    draws = [json.loads(line) for line in Path(indices).read_text().splitlines()]
    samples = []
    for picks in draws:
        assert len(picks) == len(keys) and all(type(i) is int and 0 <= i < len(keys) for i in picks)
        samples.append(mean(v for i in picks for v in groups[keys[i]]))
    samples.sort()
    def percentile(q):
        i = (len(samples) - 1) * q
        lo, hi = int(i), math.ceil(i)
        return samples[lo] + (samples[hi] - samples[lo]) * (i - lo)
    return {'delta_right_minus_left': mean(v for g in groups.values() for v in g),
            'ci95': [percentile(.025), percentile(.975)], 'clusters': len(keys), 'draws': len(draws)}

def main():
    p = argparse.ArgumentParser()
    p.add_argument('left'); p.add_argument('right'); p.add_argument('indices')
    p.add_argument('--metric', default='recall'); p.add_argument('--check')
    args = p.parse_args()
    actual = calculate(args.left, args.right, args.indices, args.metric)
    if args.check:
        expected = json.loads(Path(args.check).read_text())
        assert math.isclose(actual['delta_right_minus_left'], expected['delta_right_minus_left'], abs_tol=1e-12)
        assert all(math.isclose(x, y, abs_tol=1e-12) for x, y in zip(actual['ci95'], expected['ci95']))
    print(json.dumps(actual))

if __name__ == '__main__':
    main()
