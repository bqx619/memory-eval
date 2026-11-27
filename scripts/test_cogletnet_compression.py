"""Synthetic build traces: latest attempt per episode, exact per-layer sums, per-kind ratios."""
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parent))
import cogletnet_compression as cc  # noqa: E402


def member(body, level, text):
    return {'body': {'reference': {'body': body, 'node': 'n-' + body}, 'text': text}, 'level': level}


def summary(body, kind, tokens_in, tokens_out, accepted=True, **extra):
    return {'body': body, 'accepted': accepted, 'candidate_kind': kind, 'input_tokens': tokens_in, 'output_tokens': tokens_out, **extra}


def build_line(event_id, members, summaries, final=True):
    record = {'kind': 'build', 'event_id': event_id, 'state': 'Ready',
              'audit': {'operation': {'config': {'summary_ratio': 0.3, 'summary_max': 320, 'summary_policy': 'EvidenceFirstV1'}},
                        'summaries': summaries},
              'snapshot': {'members': members} if final else {}}
    return 'sut_trace ' + json.dumps(record, separators=(',', ':')) + '\n'


def write_attempt(root, rel, lines):
    d = Path(root) / rel
    d.mkdir(parents=True)
    (d / 'sut.stderr.log').write_text('cogletnet_usage {"method":"open"}\n' + ''.join(lines), encoding='utf-8')


class CompressionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.run = self.tmp.name
        # episode A: root attempt (stale numbers) then resume-1 attempt (authoritative)
        stale = [build_line('ev-a', [member('a0', 0, 'x'), member('a1', 1, 'y')],
                            [summary('a0', 'ExtractivePrefix', 1000, 300), summary('a1', 'ExtractivePrefix', 300, 90)])]
        fresh = [build_line('ev-a', [], [summary('a0', 'ExtractivePrefix', 500, 150)], final=False),
                 build_line('ev-a', [member('a0', 0, 'x'), member('b0', 0, 'z'), member('a1', 1, 'y')],
                            [summary('b0', 'ExactCopy', 100, 100), summary('a1', 'Model', 250, 80),
                             summary('a1', 'ExtractivePrefix', 250, 75, accepted=False)])]
        write_attempt(self.run, 'logs/0002-paeval_a', stale)
        write_attempt(self.run, 'logs/resume-1/0002-paeval_a2', fresh)
        # episode B: root only
        write_attempt(self.run, 'logs/0003-paeval_b', [build_line('ev-b', [member('c0', 0, 'x'), member('c1', 1, 'y')],
                                                                   [summary('c0', 'ExtractivePrefix', 400, 120), summary('c1', 'ExtractivePrefix', 120, 36)])])
        # describe dir without traces is ignored
        (Path(self.run) / 'logs' / '0001-describe').mkdir(parents=True)

    def tearDown(self):
        self.tmp.cleanup()

    def test_latest_attempt_and_layer_sums(self):
        result = cc.analyse(self.run)
        self.assertEqual(result['episodes'], 2)
        self.assertEqual((result['attempts_root'], result['attempts_resume']), (1, 1))
        layers = {row['level']: row for row in result['layers']}
        # L0 = a0 500 (from the resume attempt, not the stale 1000) + b0 100 + c0 400; L1 = a1 250 + c1 120
        self.assertEqual(layers[0]['tokens'], 1000)
        self.assertEqual(layers[1]['tokens'], 370)
        self.assertAlmostEqual(layers[1]['ratio'], 0.37)
        self.assertIsNone(layers[0]['ratio'])
        self.assertEqual(result['missing_token_counts'], {})
        self.assertAlmostEqual(result['upper_vs_l0'], 0.37)
        self.assertEqual(sorted(layers[1]['episode_min'] and [layers[1]['episode_min'], layers[1]['episode_max']]), [0.3, 250 / 600])

    def test_kinds_and_rejections(self):
        result = cc.analyse(self.run)
        kinds = {row['kind']: row for row in result['kinds']}
        self.assertEqual(kinds['ExactCopy']['ratio'], 1.0)
        self.assertEqual(kinds['Model']['n'], 1)
        self.assertEqual(kinds['ExtractivePrefix']['n'], 3)  # a0 (fresh), c0, c1
        self.assertEqual(result['rejected_attempts'], {'ExtractivePrefix': 1})
        self.assertEqual(result['config'][0]['summary_ratio'], 0.3)

    def test_missing_token_counts_are_reported(self):
        write_attempt(self.run, 'logs/0004-paeval_d', [build_line('ev-d', [member('d0', 0, 'x'), member('d1', 1, 'y')],
                                                                   [summary('d0', 'ExtractivePrefix', 10, 3)])])
        result = cc.analyse(self.run)
        self.assertEqual(result['missing_token_counts'], {1: 1})

    def test_parented_member_keeps_input_tokens_and_root_uses_input_summaries(self):
        root = member('root', 1, 'summary a + summary b')
        root['inputs'] = [{'node': 'n-a', 'body': 'a'}, {'node': 'n-b', 'body': 'b'}]
        members = [member('a', 0, 'source a'), member('b', 0, 'source b'), root]
        records = [summary('a', 'Model', 100, 10), summary('b', 'Model', 60, 5)]
        for own_tokens in (None, 16, 0):
            with self.subTest(own_tokens=own_tokens):
                summaries = records + ([summary('root', 'Model', own_tokens, 3)] if own_tokens is not None else [])
                found = [json.loads(build_line('event', members, summaries).split(' ', 1)[1])]
                tokens, counts, missing, _, _, _, fallback = cc.episode_layers(found)
                self.assertEqual(tokens, {0: 160, 1: 15 if own_tokens is None else own_tokens})
                self.assertEqual(counts, {0: 2, 1: 1})
                self.assertEqual(missing, {})
                self.assertEqual(fallback, {1: 1} if own_tokens is None else {})

    def test_fallback_requires_complete_accepted_output_counts(self):
        for inputs, second in (([], summary('b', 'Model', 60, 5)),
                               (['a', 'missing'], summary('b', 'Model', 60, 5)),
                               (['a', 'b'], summary('b', 'Model', 60, 5, accepted=False)),
                               (['a', 'b'], summary('b', 'Model', 60, None)),
                               (['a', 'b'], summary('b', 'Model', 60, True))):
            with self.subTest(inputs=inputs, second=second):
                root = member('root', 1, 'summary a + summary b')
                root['inputs'] = [{'body': body} for body in inputs]
                found = [json.loads(build_line('event', [root], [summary('a', 'Model', 100, 10), second]).split(' ', 1)[1])]
                tokens, _, missing, _, _, _, fallback = cc.episode_layers(found)
                self.assertEqual(tokens, {})
                self.assertEqual(missing, {1: 1})
                self.assertEqual(fallback, {})

    def test_fallback_disclosure_in_text_and_json_only_when_used(self):
        unchanged = cc.analyse(self.run)
        with contextlib.redirect_stdout(io.StringIO()) as before:
            cc.print_report(unchanged)
        self.assertNotIn('tokens_from_input_summaries', unchanged)
        self.assertNotIn('tokens from input summaries', before.getvalue())
        root = member('d1', 1, 'summary d')
        root['inputs'] = [{'body': 'd0', 'node': 'n-d0'}]
        write_attempt(self.run, 'logs/0004-paeval_d', [build_line('ev-d', [member('d0', 0, 'source d'), root],
                                                              [summary('d0', 'Model', 100, 10)])])
        output = Path(self.run) / 'fallback.json'
        with contextlib.redirect_stdout(io.StringIO()) as stdout:
            self.assertEqual(cc.main([self.run, '--json', str(output), '--per-episode']), 0)
        self.assertIn('tokens from input summaries: {1: 1}', stdout.getvalue())
        result = json.loads(output.read_text())[0]
        self.assertEqual(result['tokens_from_input_summaries'], {'1': 1})
        self.assertEqual(result['missing_token_counts'], {})
        self.assertEqual(result['layers'][1]['tokens'], 380)

    def test_soft_acceptance_flags_and_attempts_are_counted(self):
        write_attempt(self.run, 'logs/0005-paeval_e', [build_line('ev-e', [member('e0', 0, 'x'), member('e1', 0, 'y'), member('e2', 1, 'z')],
                                                                   [summary('e0', 'Model', 300, 140, ordinal=2, length_overflow=True),
                                                                    summary('e1', 'ExtractivePrefix', 400, 120, ordinal=0, degraded='extractive_prefix'),
                                                                    summary('e2', 'Model', 260, 70, ordinal=1)])])
        result = cc.analyse(self.run)
        self.assertEqual(result['outcomes'], {'length_overflow': 1, 'degraded:extractive_prefix': 1})
        self.assertEqual(result['attempts'], {'1': 1, '2': 1, '3': 1})

    def test_cli_json(self):
        out = os.path.join(self.run, 'out.json')
        self.assertEqual(cc.main([self.run, '--json', out]), 0)
        with open(out, encoding='utf-8') as handle:
            data = json.load(handle)
        self.assertEqual(data[0]['layers'][1]['tokens'], 370)


if __name__ == '__main__':
    unittest.main()
