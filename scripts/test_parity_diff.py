#!/usr/bin/env python3
"""Exercise parity against read-only M0 goldens, including CLI regressions."""
import copy
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
from verification import verification_main
from parity_diff import check

ROOT = Path(__file__).resolve().parents[1]
GOLDEN = ROOT / 'docs/reports/bm25'


class ParityDiffTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="parity-", dir=self.output)
        self.addCleanup(temp.cleanup)
        self.actual = Path(temp.name)
        for name in ('cases.jsonl', 'report.json'):
            shutil.copyfile(GOLDEN / name, self.actual / name)
        self.cases = [json.loads(line) for line in
                      (self.actual / 'cases.jsonl').read_text().splitlines()]
        self.report = json.loads((self.actual / 'report.json').read_text())

    def write_cases(self, rows):
        (self.actual / 'cases.jsonl').write_text(
            ''.join(json.dumps(row) + '\n' for row in rows))

    def write_report(self, report):
        (self.actual / 'report.json').write_text(json.dumps(report))

    def cli(self):
        return subprocess.run(
            [sys.executable, '-B', str(ROOT / 'scripts/parity_diff.py'),
             str(self.actual), str(GOLDEN)], capture_output=True, text=True)

    def test_additive_summary_and_category_keys_pass_cli(self):
        for summary in [self.report['summary'], *self.report['by_category'].values()]:
            summary['untraceable_hits'] = 7
            summary['new_diagnostic'] = {'count': 2}
            summary['metrics']['new_metric'] = {'mean': 0.5, 'n': 3}
            summary['metrics']['recall']['new_statistic'] = 0.75
            # Timing and billing remain excluded, as in the original M0 check.
            summary['timings'] = {'changed': True}
            summary['billing'] = {'changed': True}
        self.report['by_category']['new_category'] = {'planned': 1}
        self.write_report(self.report)
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout, '')

    def test_changed_case_score_fails_cli(self):
        # Deliberate real regression: the first BM25 golden scores this as 1.0.
        self.assertEqual(self.cases[0]['metrics']['empty_result_correct'], 1.0)
        self.cases[0]['metrics']['empty_result_correct'] = 0.0
        self.write_cases(self.cases)
        result = self.cli()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('cases[0].metrics.empty_result_correct: 0.0 != 1.0', result.stdout)

    def test_case_metric_keys_stay_strict(self):
        for change in ('added', 'missing'):
            with self.subTest(change=change):
                rows = copy.deepcopy(self.cases)
                if change == 'added':
                    rows[0]['metrics']['new_metric'] = 0.5
                else:
                    del rows[0]['metrics']['hit']  # Null in the golden is still required.
                self.write_cases(rows)
                self.assertTrue(any('cases[0].metrics: keys' in error
                                    for error in check(self.actual, GOLDEN)))

    def test_missing_null_case_field_fails(self):
        index = next(i for i, row in enumerate(self.cases) if row['passed'] is None)
        del self.cases[index]['passed']
        self.write_cases(self.cases)
        self.assertIn(f"cases[{index}]: keys ['passed']", check(self.actual, GOLDEN))

    def test_case_count_and_order_stay_strict(self):
        for change, rows in (('missing', self.cases[:-1]), ('reordered', self.cases[::-1])):
            with self.subTest(change=change):
                self.write_cases(rows)
                self.assertTrue(check(self.actual, GOLDEN))

    def test_changed_golden_aggregate_score_fails_cli(self):
        for section in ('summary', 'by_category'):
            with self.subTest(section=section):
                report = copy.deepcopy(self.report)
                target = report['summary'] if section == 'summary' else next(iter(report['by_category'].values()))
                target['pass_rate_on_scored'] += 0.25
                self.write_report(report)
                result = self.cli()
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn(section, result.stdout)
                self.assertIn('pass_rate_on_scored', result.stdout)

    def test_missing_golden_aggregate_key_fails(self):
        for section in ('summary', 'by_category'):
            with self.subTest(section=section):
                report = copy.deepcopy(self.report)
                target = report['summary'] if section == 'summary' else next(iter(report['by_category'].values()))
                del target['metrics']['recall']['mean']
                self.write_report(report)
                errors = check(self.actual, GOLDEN)
                self.assertTrue(any(section in error and "metrics.recall: keys ['mean']" in error
                                    for error in errors), errors)

    def test_missing_golden_category_fails(self):
        category = next(iter(self.report['by_category']))
        del self.report['by_category'][category]
        self.write_report(self.report)
        self.assertIn(f'by_category: keys {[category]!r}', check(self.actual, GOLDEN))


def main(args, work):
    ParityDiffTests.output = work.out
    tests = unittest.defaultTestLoader.loadTestsFromTestCase(ParityDiffTests)
    result = unittest.TextTestRunner(stream=sys.stdout, verbosity=2).run(tests)
    if not result.wasSuccessful():
        raise SystemExit(1)


ParityDiffTests.output = None  # unittest discovery also stays in system temp.
if __name__ == '__main__':
    verification_main(main)
