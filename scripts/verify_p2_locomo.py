#!/usr/bin/env python3
"""Offline cross-check of the scoring function imported by upstream evaluate_qa.py.
Run with MEMEVAL_ORACLE_PYTHON (numpy/nltk/regex already installed).
"""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
from upstream import require_upstream
import contextlib
import importlib.util
import io
import json
import types
from collections import defaultdict
from pathlib import Path
root = Path(__file__).resolve().parents[1]
def main(args, work):
    path = require_upstream('locomo/task_eval/evaluation.py', root)
    bert = types.ModuleType('bert_score')
    bert.score = lambda *a, **k: None
    sys.modules['bert_score'] = bert
    sys.modules['rouge'] = types.ModuleType('rouge')
    spec = importlib.util.spec_from_file_location('locomo_evaluation', path)
    evaluation = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(evaluation)
    run = args.run
    report = json.loads((run / 'report.json').read_text())
    actual = {(c['episode_id'], c['repeat_index']): [] for c in report['cases']}
    for c in report['cases']:
        actual[c['episode_id'], c['repeat_index']].append(c['metrics']['locomo_official.overall'])
    by_category = defaultdict(list)
    max_diff = 0.0
    n = 0
    for line in (run / 'predictions.jsonl').read_text().splitlines():
        record = json.loads(line)
        with contextlib.redirect_stdout(io.StringIO()):
            scores, _, _ = evaluation.eval_question_answering(record['qa'], eval_key='prediction')
        expected = actual[record['sample_id'], record.get('repeat_index', 0)]
        assert len(scores) == len(expected)
        for q, a, b in zip(record['qa'], scores, expected):
            max_diff = max(max_diff, abs(a-b))
            assert abs(a-b) <= 1e-12
            by_category[q['category']].append(float(a))
            n += 1
    metrics = {f'locomo_official.f1.cat{k}': sum(v)/len(v) for k,v in by_category.items()}
    metrics['locomo_official.f1'] = sum(sum(v) for k,v in by_category.items() if k != 5) / sum(len(v) for k,v in by_category.items() if k != 5)
    metrics['locomo_official.abstain_acc'] = metrics['locomo_official.f1.cat5']
    metrics['locomo_official.overall'] = sum(sum(v) for v in by_category.values()) / n
    for k,v in metrics.items():
        diff = abs(v-report['scores']['official']['metrics'][k])
        max_diff = max(max_diff,diff)
        assert diff <= 1e-12, (k,v,report['scores']['official']['metrics'][k])
    result = dict(cases=n, max_abs_diff=max_diff, metrics=metrics, oracle='evaluation.py:eval_question_answering (imported by evaluate_qa.py)', overall='micro mean of categories 1–5, per evaluation_stats.py')
    print(json.dumps(result, indent=2))
    (work.out/'oracle.json').write_text(json.dumps(result,indent=2)+'\n')

if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument('run', type=Path)
    verification_main(main, parser)
