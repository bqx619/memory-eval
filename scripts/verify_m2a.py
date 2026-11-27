#!/usr/bin/env python3
"""Offline M2a artifact audit; no imports from or writes to docs/."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
from upstream import require_upstream
import argparse
import importlib.util
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
OUT = None
PREFIX = 'longmemeval_official.'
METRICS = ([f'session.{m}@{k}' for k in (5, 10) for m in ('recall_all', 'ndcg_any')]
           + [f'turn.{m}@{k}' for k in (5, 10, 50) for m in ('recall_any', 'recall_all', 'ndcg_any')])

def main(args, work):
    path = require_upstream('longmemeval/src/retrieval/eval_utils.py', ROOT)
    global OUT
    OUT = work.out
    subset = args.subset
    import numpy as np
    if not hasattr(np, 'asfarray'):
        np.asfarray = lambda a: np.asarray(a, dtype=float)
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location('official', path)
    oracle = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(oracle)
    suite = json.loads((ROOT / 'data/normalized/longmemeval-s.json').read_text())
    episodes = {e['id']: e for e in suite['episodes']}
    stats = json.loads((ROOT / 'reports/m2a-verification/import.json').read_text())['statistics']
    assert (stats['questions'], stats['abstentions'], stats['sessions'], stats['events']) == (500, 30, 23867, 246750)
    assert stats['question_types'] == {'multi-session': 133, 'temporal-reasoning': 133, 'knowledge-update': 78, 'single-session-user': 70, 'single-session-assistant': 56, 'single-session-preference': 30}
    assert stats['session_time_parse_failures'] == stats['question_time_parse_failures'] == 0
    assert len(suite['episodes'][0]['cases'][0]['gold']['evidence_ids']) == 2
    expected = [f'{n}-{s}-{b}' for n, suts in ((50, ('octop', 'bm25', 'recent', 'null')), (500, ('octop', 'bm25'))) for s in suts for b in ('research', 'default')]
    if subset:
        expected = [name for name in expected if name.startswith('50-')]
    rows = []; max_diff = 0.; checks = 0
    for name in expected:
        r = json.loads((ROOT / f'reports/m2a-{name}/report.json').read_text())
        n = int(name.split('-')[0]); denom = 50 if n == 50 else 470
        assert len(r['cases']) == n
        assert r['summary']['execution_errors'] == r['summary']['unsupported'] == 0, name
        assert r['summary']['status_counts'].get('scored', 0) == denom
        assert r['source']['cluster_basis'] == 'episode'
        assert r['protocol']['query_time_policy'] == 'reference_only'
        assert r['protocol']['budget']['top_k'] == (50 if name.endswith('research') else 10)
        assert r['protocol']['budget']['max_context_chars'] == (200000 if name.endswith('research') else 100000)
        if '-octop-' in name:
            assert r['isolation']['network_mode'] == 'none' and r['isolation']['network_enforced']
        values = {m: [] for m in METRICS}
        for c in r['cases']:
            ep = episodes[c['episode_id']]; gold = ep['cases'][0]['gold']
            assert c['cluster_id'] == ep['id']
            if gold.get('abstain'):
                assert not any(k.startswith(PREFIX) for k in c['metrics'])
                continue
            index = {e['source_id']: i for i, e in enumerate(ep['events'])}
            rankings = [index[x] for x in c['retrieved_source_ids']]
            corpus = [e['source_id'] for e in ep['events']]
            # The official session function removes the final underscore token.
            # Synthetic turn suffixes preserve the suite's explicit session map.
            session_corpus = [e['session'] + '_' + str(i) for i, e in enumerate(ep['events'])]
            session_gold = [s + '_gold' for s in gold.get('evidence_session_ids', [])]
            want = {}
            for k in (5, 10, 50):
                for m, v in zip(('recall_any', 'recall_all', 'ndcg_any'), oracle.evaluate_retrieval(rankings, gold['evidence_ids'], corpus, k)):
                    want[f'turn.{m}@{k}'] = float(v)
            for k in (5, 10):
                _, all_, ndcg = oracle.evaluate_retrieval_turn2session(rankings, session_gold, session_corpus, k)
                want[f'session.recall_all@{k}'] = float(all_)
                want[f'session.ndcg_any@{k}'] = float(ndcg)
            for m, v in want.items():
                diff = abs(v - c['metrics'][PREFIX + m]); max_diff = max(max_diff, diff); checks += 1
                assert diff < 1e-9, (name, c['case_id'], m, diff)
                values[m].append(v)
        means = {}
        for m, vals in values.items():
            unit, metric = m.split('.')
            entry = r['scores']['memory_only']['longmemeval_official'][unit][metric]
            assert entry['n'] == denom
            means[m] = sum(vals) / denom
            assert abs(entry['mean'] - means[m]) < 1e-9
        rows.append({'run': name, 'n': denom, 'wall_seconds': r['wall_seconds'], **means})
    comparisons = []
    for n in ((50,) if subset else (50, 500)):
        for budget in ('research', 'default'):
            left = ROOT / f'reports/m2a-{n}-bm25-{budget}/report.json'
            right = ROOT / f'reports/m2a-{n}-octop-{budget}/report.json'
            dest = OUT / f'compare-{n}-{budget}' / 'delta.json'
            subprocess.run(work.cli('compare', str(left), str(right), '--metric', PREFIX + 'session.recall_all@10', '--out', str(dest)), cwd=ROOT, env=work.env, check=True, stdout=subprocess.DEVNULL)
            c = json.loads(dest.read_text()); assert c['clusters'] == (50 if n == 50 else 470)
            assert len(c['ci95']) == 2 and 'cluster_basis=episode' in c['note'] and 'share' in c['note']
            indices = np.array([json.loads(line) for line in (dest.parent / 'resample_indices.jsonl').read_text().splitlines()])
            lrows = {row['cluster_id']: row for row in json.loads(left.read_text())['cases'] if row['status'] == 'scored'}
            rrows = {row['cluster_id']: row for row in json.loads(right.read_text())['cases'] if row['status'] == 'scored'}
            deltas = np.array([rrows[key]['metrics'][c['metric']] - lrows[key]['metrics'][c['metric']] for key in c['cluster_order']])
            ci = np.percentile(deltas[indices].mean(axis=1), [2.5, 97.5])
            ci_diff = float(np.max(np.abs(ci - c['ci95'])))
            assert ci_diff < 1e-9
            comparisons.append({'fixed_indices_python_max_diff': ci_diff, 'run': f'{n}-{budget}', **{k:c[k] for k in ('metric', 'delta_right_minus_left', 'ci95', 'clusters', 'note')}})
    output = {'statistics': stats, 'oracle_case_metric_checks': checks, 'max_abs_diff': max_diff, 'runs': rows, 'comparisons': comparisons}
    (OUT / ('subset-acceptance.json' if subset else 'acceptance.json')).write_text(json.dumps(output, indent=2) + '\n')
    lines = ['# M2a memory-only 官方检索指标', '', '所有数值是检索诊断，不是答案正确率或官方排行榜分数。CI 为 octop − BM25，按题目 episode 配对重采样 2000 次；填充会话可能来自共享语料池。', '', '| run | n | seconds | ' + ' | '.join(METRICS) + ' |', '|---|---:|---:|' + '---:|' * len(METRICS)]
    for row in rows:
        lines.append('| ' + row['run'] + f" | {row['n']} | {row['wall_seconds']:.2f} | " + ' | '.join(f'{row[m]:.6f}' for m in METRICS) + ' |')
    lines += ['', '| compare | delta | 95% CI | clusters |', '|---|---:|---|---:|']
    for c in comparisons:
        lines.append(f"| {c['run']} | {c['delta_right_minus_left']:.6f} | [{c['ci95'][0]:.6f}, {c['ci95'][1]:.6f}] | {c['clusters']} |")
    (OUT / ('subset-metrics.md' if subset else 'metrics.md')).write_text('\n'.join(lines) + '\n')
    print(f"PASS: {len(rows)} runs; {checks} oracle metric checks; max diff {max_diff:.3g}; {len(comparisons)} CIs")

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument("--subset", action="store_true", help="audit the eight 50-question runs only")
    verification_main(main, parser)
