"""Contract tests use a fake native process; no SDK, services or source writes."""
import importlib.util
import hashlib
import io
import json
import subprocess
import time
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'suts/_bridge/python'))
spec = importlib.util.spec_from_file_location('cogletnet_adapter', Path(__file__).with_name('adapter.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FakeNative:
    instances = []

    def __init__(self, binary, env):
        self.env, self.calls, self.closed = env, [], False
        self.rows = []
        self.documents, self.audits, self.snapshots = {}, {}, {}
        self.instances.append(self)

    def call(self, method, **data):
        self.calls.append((method, data))
        if method == 'add_batch':
            for event in data['events']:
                ident, text = event['id'], event['text']
                version = 1
                if data.get('write_mode') == 'scope_events':
                    assert set(event['scope']) == {'kind', 'external_id'}
                    assert isinstance(event['event_sequence'], int)
                    previous = self.documents.get('source-' + ident)
                    if 'expected_source_version' in event:
                        assert previous['source']['version'] == event['expected_source_version']
                        version = event['expected_source_version'] + 1
                    elif previous is not None:
                        assert previous['text'] == text  # Same-operation replay only.
                        version = previous['source']['version']
                body = {'reference': {'node': 'node-' + ident, 'body': 'body-' + ident}, 'source': 'source-' + ident,
                        'kind': 'Source', 'text': text, 'body_hash': hashlib.sha256(text.encode()).hexdigest()}
                self.documents['source-' + ident] = {'text': text, 'source': {'id': 'source-' + ident, 'version': version}}
                self.audits['op-' + ident] = {'operation': {'snapshot': 'snapshot-' + ident},
                                             'bodies': [body], 'summaries': [], 'calls': []}
                self.snapshots['snapshot-' + ident] = {'members': [{'body': body, 'level': 0, 'ordinal': 0,
                    'source_span': {'start': 0, 'end': len(text.encode())}, 'inputs': [], 'edge': None, 'underfill': False}],
                    'paths': {}, 'points': [{'id': 'point-' + ident, 'representation': 'FullPath', 'input_hash': 'input-' + ident, 'vector': [0.1, 0.2]}]}
            return {'events': [{'event_id': e['id'], 'operation_id': 'op-' + e['id'],
                               'source_ref': self.documents['source-' + e['id']]['source'], 'state': 'Ready',
                               'write': {'snapshot': 'snapshot-' + e['id']}} for e in data['events']], 'all_ready': True}
        if method == 'audit_build':
            return self.audits[data['operation_id']]
        if method == 'snapshot_manifest':
            return self.snapshots[data['snapshot_id']]
        if method == 'snapshot_manifest_view':
            snapshot = self.snapshots[data['snapshot_id']]
            return dict(snapshot, points=[{k: v for k, v in p.items() if k != 'vector'} for p in snapshot['points']])
        if method == 'get':
            return self.documents[data['source_id']]
        if method == 'search':
            result = {'items': self.rows, 'warnings': ['estimated token budget'], 'degraded': False,
                      'settings': {'jev_threshold': 0.5, 'limit': data['limit'], 'representation': 'FullPath',
                                   'jev_prompt_profile': 'memory-reference-v1',
                                   'jev_prompt_hash': 'ec0bc5a2c39779d9d00cf924f556290d0a862f39c484481598c2ad9b77b3ca2c',
                                   'jev_input_format': 'AncestorContextV1', 'selection_policy': {'kind': 'absolute_threshold'},
                                   'vector_representation': 'FullPath', 'leaf_k': 20, 'upper_k': 10, 'candidate_cap': 100,
                                   'evidence_max_rounds': 2, 'evidence_candidate_cap': 100, 'memory_tokens': 1000}}
            if data.get('trace', False):
                targets = [{'id': r['reference']['node'] + ('/' + r['reference']['body'] if 'body' in r['reference'] else ''),
                            'target_body': r.get('text', r['path']['text']), 'full_path': '',
                            'source_state': {'source': r['source'], 'latest_known_version': 1,
                                             'newer_version': None, 'read_at': 0}} for r in self.rows]
                result['jev_trace'] = [{'model': 'fake-jev', 'targets': targets,
                                        'probabilities': {t['id']: 0.8 for t in targets}, 'error_code': None}]
            if data.get('representation') == 'LeafMemory':
                result['settings'].update(representation='LeafMemory', vector_representation='LeafMemory',
                                          jev_prompt_profile='leaf-memory-reference-v1', jev_input_format='LeafMemoryV1',
                                          jev_output_format='ChoiceThreeClass', jev_admission_basis='Useful',
                                          jev_calibration='Uncalibrated')
                result['packed'] = {'memories': len(self.rows), 'visible_bodies': len(self.rows) + 1,
                                    'visible_level0': len(self.rows), 'visible_ancestors': 1}
                result['context_view'] = {'memories': [{'reference': r['reference'], 'source': r['source'],
                    'path_revision': 'fake-path', 'layers': [{'reference': {'node': 'layer-' + str(i), 'body': 'body-' + str(i)},
                    'current': layer['current'], 'relation': None if i == 0 else 'SummaryParent', 'items': []}
                    for i, layer in enumerate(r.get('memory') or [])]} for r in self.rows], 'visible': []}
                if data.get('trace'):
                    result['jev_trace'][0].pop('probabilities')
                    result['jev_trace'][0]['output_format'] = 'ChoiceThreeClass'
                    result['jev_trace'][0]['answers'] = {t['id']: {'outcome': {'kind': 'reference', 'useful': 0.8, 'not_useful': 0.1, 'uncertain': 0.1}}
                                                        for t in result['jev_trace'][0]['targets']}
            if data.get('representation') == 'Tree':
                result['settings'].update(representation='Tree', vector_representation='Body', upper_k=0,
                                          jev_prompt_profile='node-reference-v1', jev_input_format='NodeV1',
                                          jev_output_format='ChoiceThreeClass')
                result['context_view'] = {'nodes': [{'reference': r['reference'], 'source': r['source'],
                    'level': r.get('level'), 'leaf_range': {'start': 0, 'end': 2}, 'recorded': r.get('recorded'),
                    'sources': [{'id': source, 'version': 1} for source in r.get('reader_sources', [])]}
                    for r in self.rows]}
                if data.get('trace'):
                    result['jev_trace'][0].pop('probabilities')
                    result['jev_trace'][0]['output_format'] = 'ChoiceThreeClass'
                    result['jev_trace'][0]['answers'] = {t['id']: {'outcome': {'kind': 'reference', 'useful': 0.8, 'not_useful': 0.1, 'uncertain': 0.1}}
                                                        for t in result['jev_trace'][0]['targets']}
            return result
        return {}

    def close(self):
        self.closed = True


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        env = {'EP': self.tmp.name, 'HOST_GENERATION_MODEL': 'same-as-other-suts',
               'GENERATION_REASONING_EFFORT': 'low', 'MEMORY_SUMMARY_MODEL': 'summary',
               'MEMORY_SUMMARY_VALIDATION_MODEL': 'validator', 'COGLETNET_WORK_TRACE': 'full', 'JEV_THRESHOLD': '0.5',
               'JEV_PROMPT_PROFILE': 'memory-reference-v1', 'JEV_SELECTION_POLICY': 'absolute-threshold'}
        p = patch.dict(os.environ, env)
        p.start()
        self.addCleanup(p.stop)
        p = patch.object(module, 'Native', FakeNative)
        p.start()
        self.addCleanup(p.stop)
        self.adapter = module.Adapter({})
        self.addCleanup(self.adapter.close)
        self.stderr = io.StringIO()
        p = patch.object(sys, 'stderr', self.stderr)
        p.start()
        self.addCleanup(p.stop)

    def event(self, id, owner='user'):
        return {'id': id, 'owner': owner, 'content': 'original ' + id, 'role': 'user', 'attributes': {}}

    def request(self, **kwargs):
        return dict(query='question', user='user', budget={'top_k': 10, 'max_context_chars': 100}, **kwargs)

    def test_describe_is_offline_and_generation_matches_framework(self):
        before = len(FakeNative.instances)
        d = self.adapter.describe()
        self.assertEqual(len(FakeNative.instances), before)
        self.assertEqual(d['config']['host_generation_model'], 'same-as-other-suts')
        self.assertEqual(d['config']['generation_reasoning_effort'], 'low')
        self.assertTrue(d['config']['llm_notes'])
        self.assertEqual(d['config']['summary_candidate_policy'], os.environ.get('SUMMARY_CANDIDATE_POLICY'))
        self.assertEqual(d['config']['build_input_bounds'], 'conservative-bytes-v1')
        self.assertEqual(d['config']['jev_threshold'], 0.5)
        self.assertEqual(d['config']['jev_prompt_profile'], 'memory-reference-v1')
        self.assertEqual(d['config']['jev_selection_policy'], 'absolute-threshold')
        manifest = json.loads(Path(__file__).with_name('manifest.json').read_text())
        self.assertEqual(manifest['process']['env']['JEV_PROMPT_PROFILE'], 'memory-reference-v1')
        self.assertEqual(manifest['process']['env']['JEV_SELECTION_POLICY'], 'absolute-threshold')
        self.assertEqual(manifest['process']['env']['JEV_THRESHOLD'], '0.5')
        with patch.dict(os.environ, {'JEV_PROMPT_PROFILE': 'legacy-target-v1', 'JEV_SELECTION_POLICY': 'top-k'}):
            self.assertEqual(self.adapter.describe()['config']['jev_prompt_profile'], 'legacy-target-v1')
            self.assertEqual(self.adapter.describe()['config']['jev_selection_policy'], 'top-k')
        with patch.dict(os.environ):
            os.environ.pop('COGLETNET_WORK_TRACE', None)
            self.assertEqual(module.Adapter({}).work_trace, 'full')
        with patch.dict(os.environ, {'COGLETNET_WORK_TRACE': 'invalid'}):
            with self.assertRaisesRegex(ValueError, 'full or off'):
                module.Adapter({})

    def test_describe_effective_summary_models_and_optional_controls(self):
        before = len(FakeNative.instances)
        cases = [({}, ('host-model', 'host-model')),
                 ({'MEMORY_SUMMARY_MODEL': 'summary-model'}, ('summary-model', 'summary-model')),
                 ({'MEMORY_SUMMARY_VALIDATION_MODEL': 'validation-model'}, ('host-model', 'validation-model')),
                 ({'MEMORY_SUMMARY_MODEL': 'summary-model', 'MEMORY_SUMMARY_VALIDATION_MODEL': 'validation-model'},
                  ('summary-model', 'validation-model'))]
        for overrides, expected in cases:
            with self.subTest(overrides=overrides), patch.dict(os.environ, {'HOST_GENERATION_MODEL': 'host-model', **overrides}, clear=True):
                config = self.adapter.describe()['config']
                self.assertEqual((config['summary_model'], config['summary_validation_model']), expected)
                self.assertIsNone(config['summary_prompt_profile'])
                self.assertIsNone(config['summary_candidate_policy'])
                self.assertIsNone(config['summary_length_band'])
                self.assertIsNone(config['summary_copy_max_tokens'])
                for key in ('max_level', 'summary_ratio', 'summary_ratio_min', 'summary_ratio_max', 'summary_min_tokens'):
                    self.assertIsNone(config[key])
                self.assertIsNone(config['summary_language_guard'])
                self.assertIsNone(config['leaf_memory_embed_input'])
                self.assertNotIn('retain separate models', config['llm_notes'][1])
        with patch.dict(os.environ, {'SUMMARY_CANDIDATE_POLICY': 'model-only', 'SUMMARY_LENGTH_BAND': 'ratio-v1',
                                     'LEAF_MEMORY_EMBED_INPUT': 'memory', 'SUMMARY_PROMPT_PROFILE': 'abstractive-ratio-v3',
                                     'SUMMARY_COPY_MAX_TOKENS': '064', 'SUMMARY_LANGUAGE_GUARD': 'script-v1'}):
            config = self.adapter.describe()['config']
            self.assertEqual(config['summary_prompt_profile'], 'abstractive-ratio-v3')
            self.assertEqual(config['summary_copy_max_tokens'], '064')
            self.assertEqual(config['summary_language_guard'], 'script-v1')
            self.assertEqual(config['summary_candidate_policy'], 'model-only')
            self.assertEqual(config['summary_length_band'], 'ratio-v1')
            self.assertEqual(config['leaf_memory_embed_input'], 'memory')
        with patch.dict(os.environ, {'SUMMARY_LANGUAGE_GUARD': 'off'}):
            self.assertEqual(self.adapter.describe()['config']['summary_language_guard'], 'off')
        overrides = {'MAX_LEVEL': '1', 'SUMMARY_RATIO': '0.10', 'SUMMARY_RATIO_MIN': '0.07',
                     'SUMMARY_RATIO_MAX': '0.13', 'SUMMARY_MIN_TOKENS': '016'}
        with patch.dict(os.environ, overrides):
            config = self.adapter.describe()['config']
            for key, value in overrides.items():
                self.assertEqual(config[key.lower()], value)
        self.assertEqual(len(FakeNative.instances), before)

    def test_write_ready_search_provenance_budget_and_reset(self):
        self.adapter.reset('first')
        native = self.adapter.native
        h = self.adapter.ingest('first', [self.event('e1'), self.event('e2', 'other')])
        self.assertEqual(h['accepted'], 2)
        batch = next(data for method, data in native.calls if method == 'add_batch')
        self.assertEqual(batch['concurrency'], 4)
        self.assertEqual([e['id'] for e in batch['events']], ['e1', 'e2'])
        self.assertTrue(self.adapter.ready('first', h, None)['visible'])
        native.rows = [{'source': {'id': 'source-e1'}, 'reference': {'node': 'n1'},
                        'path': {'text': '实际🙂'}, 'text': 'not-full-path', 'rank_utility': 0.8}]
        out = self.adapter.retrieve('first', self.request())
        self.assertEqual(out['items'][0]['text'], '实际🙂')
        self.assertEqual(out['items'][0]['source_ids'], ['e1'])
        self.assertEqual(native.calls[-1][1]['sources'], ['source-e1'])
        req = self.request()
        req['budget']['max_context_chars'] = 2
        self.assertEqual(self.adapter.retrieve('first', req)['items'], [])
        self.adapter.reset('second')
        self.assertTrue(native.closed)
        self.assertNotEqual(native.env['STATE_PATH'], self.adapter.native.env['STATE_PATH'])
        self.assertEqual(self.adapter.retrieve('second', self.request())['items'], [])

    def test_unknown_source_and_nonready_writes_fail(self):
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1')])
        self.adapter.native.rows = [{'source': {'id': 'unknown'}, 'path': {'text': 'bad'}, 'reference': {'node': 'n'}}]
        with self.assertRaisesRegex(ValueError, 'unknown source'):
            self.adapter.retrieve('ns', self.request())
        with patch.object(self.adapter.native, 'call', return_value={'events': [{'event_id': 'e3', 'operation_id': 'op-e3', 'state': 'Failed'}]}):
            with self.assertRaisesRegex(RuntimeError, 'BATCH_NOT_READY'):
                self.adapter.ingest('ns', [self.event('e3')])

    def test_scope_and_asof_are_disclosed_not_simulated(self):
        limitations = ' '.join(self.adapter.describe()['limitations'])
        self.assertIn('scope', limitations)
        self.assertIn('as_of', limitations)
        with self.assertRaisesRegex(ValueError, 'HOST_GENERATION_MODEL'):
            with patch.dict(os.environ, {'HOST_GENERATION_MODEL': ''}):
                module.Adapter({})

    def test_legacy_batch_uses_native_timeout_clamp(self):
        self.adapter.config['batch_timeout_seconds'] = 3
        for value, expected in ((-1, 1), (0, 1), (1.9, 1), (9999, 3)):
            self.assertEqual(self.adapter._native_timeout(value), expected)
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1')])
        batch = next(d for m, d in self.adapter.native.calls if m == 'add_batch')
        self.assertEqual(batch['timeout_seconds'], 3)

    def test_partial_failure_preserves_operations_and_same_event_can_resume(self):
        from memeval_bridge.errors import error_data
        self.adapter.reset('ns')
        events = [self.event('e1'), self.event('e2'), self.event('e3')]
        rows = [
            {'event_id': 'e1', 'state': 'Ready', 'source_ref': {'id': 'source-e1'}, 'operation_id': 'op-e1'},
            {'event_id': 'e2', 'state': 'Failed', 'source_ref': {'id': 'source-e2'}, 'operation_id': 'op-e2',
             'error': {'kind': 'MODEL_RECOVERY_EXHAUSTED', 'operation': 'op-e2'}},
            {'event_id': 'e3', 'state': 'NotDispatched', 'operation_id': None},
        ]
        with patch.object(self.adapter.native, 'call', return_value={'events': rows, 'all_ready': False}):
            with self.assertRaises(module.NativeError) as caught:
                self.adapter.ingest('ns', events)
        self.assertEqual(error_data(caught.exception)['details']['cogletnet']['events'][1]['operation_id'], 'op-e2')
        with self.assertRaisesRegex(module.NativeError, 'HISTORY_NOT_READY'):
            self.adapter.retrieve('ns', self.request())
        with self.assertRaisesRegex(module.NativeError, 'HISTORY_NOT_READY'):
            self.adapter.ready('ns', {}, None)
        # One replay of the same event keys; the SDK owns retries and remaining budgets.
        self.adapter.ingest('ns', events)
        self.assertEqual(self.adapter.sources, {'source-e1': 'e1', 'source-e2': 'e2', 'source-e3': 'e3'})
        self.assertTrue(self.adapter.ready('ns', {}, None)['visible'])
        with self.assertRaisesRegex(ValueError, 'different payload'):
            self.adapter.ingest('ns', [dict(events[0], content='changed')])

    def test_different_events_cannot_share_source_and_reset_same_namespace_is_fresh(self):
        self.adapter.reset('ns')
        first = self.adapter.native.env['STATE_PATH']
        self.adapter.ingest('ns', [self.event('e1')])
        with patch.object(self.adapter.native, 'call', return_value={'events': [
                {'event_id': 'e2', 'state': 'Ready', 'source_ref': {'id': 'source-e1'}}]}):
            with self.assertRaisesRegex(ValueError, 'reused a source'):
                self.adapter.ingest('ns', [self.event('e2')])
        self.adapter.reset('ns')
        self.assertNotEqual(first, self.adapter.native.env['STATE_PATH'])
        self.assertFalse(self.adapter.events)

    def test_rpc_loss_is_unknown_and_next_attempt_uses_same_database(self):
        self.adapter.reset('ns')
        state = self.adapter.native.env['STATE_PATH']
        with patch.object(self.adapter.native, 'call', side_effect=TimeoutError('synthetic')):
            with self.assertRaisesRegex(module.NativeError, 'NATIVE_RPC_UNKNOWN'):
                self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(self.adapter.event_results['e1']['state'], 'Unknown')
        self.assertEqual(self.traces('build')[-1]['state'], 'Unknown')
        self.assertIsNone(self.traces('build')[-1]['audit'])
        self.assertTrue(self.traces('build')[-1]['audit_error'])
        self.assertIsNone(self.adapter.native)
        self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(state, self.adapter.native.env['STATE_PATH'])

    def traces(self, kind):
        return [row for line in self.stderr.getvalue().splitlines() if line.startswith('sut_trace ')
                for row in [json.loads(line[len('sut_trace '):])] if row['kind'] == kind]

    def test_legacy_build_trace_bytes_unchanged(self):
        self.adapter.reset('ns')
        events = [self.event('e1'), self.event('e2')]
        self.adapter.ingest('ns', events)
        native = self.adapter.native
        expected = []
        for seq, event in enumerate(events):
            ident = event['id']
            snapshot = native.snapshots['snapshot-' + ident]
            row = {'kind': 'build', 'seq': seq, 'event_id': ident,
                   'text_sha256': hashlib.sha256(event['content'].encode()).hexdigest(),
                   'state': 'Ready', 'operation_id': 'op-' + ident, 'snapshot_id': 'snapshot-' + ident,
                   'audit': native.audits['op-' + ident],
                   'snapshot': dict(snapshot, points=[{k: v for k, v in p.items() if k != 'vector'} for p in snapshot['points']]),
                   'audit_error': None}
            expected.append('sut_trace ' + json.dumps(row, ensure_ascii=False, separators=(',', ':')) + '\n')
        actual = [line for line in self.stderr.getvalue().splitlines(keepends=True)
                  if line.startswith('sut_trace {"kind":"build"')]
        self.assertEqual(''.join(actual).encode(), ''.join(expected).encode())
        self.assertEqual([m for m, _ in native.calls].count('audit_build'), 2)
        self.assertEqual([m for m, _ in native.calls].count('snapshot_manifest_view'), 2)

    def test_build_search_trace_and_vector_omission(self):
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1')])
        self.adapter.ingest('ns', [self.event('e2')])
        builds = self.traces('build')
        self.assertEqual([b['seq'] for b in builds], [0, 1])
        self.assertEqual(builds[0]['text_sha256'], hashlib.sha256(b'original e1').hexdigest())
        self.assertEqual(builds[0]['audit'], self.adapter.native.audits['op-e1'])
        self.assertEqual(builds[0]['snapshot']['points'], [{'id': 'point-e1', 'representation': 'FullPath', 'input_hash': 'input-e1'}])
        self.assertNotIn('snapshot_manifest', [m for m, _ in self.adapter.native.calls])
        self.assertIn('snapshot_manifest_view', [m for m, _ in self.adapter.native.calls])
        self.assertEqual(self.adapter.native.snapshots['snapshot-e1']['points'][0]['vector'], [0.1, 0.2])
        self.assertIsNone(builds[0]['audit_error'])
        self.adapter.native.rows = [{'source': {'id': 'source-e1'}, 'reference': {'node': 'node-e1', 'body': 'body-e1'},
                                     'path': {'text': 'full path'}, 'rank_utility': 0.5}]
        self.adapter.retrieve('ns', self.request())
        row = self.traces('search')[0]
        self.assertEqual(row['sources'], {'source-e1': 'e1', 'source-e2': 'e2'})
        self.assertEqual(row['result']['items'], self.adapter.native.rows)
        self.assertTrue(self.adapter.native.calls[-1][1]['trace'])
        self.assertEqual(row['result'], self.adapter.native.call('search', **self.adapter.native.calls[-1][1]))
        self.assertEqual(row['result']['jev_trace'][0]['probabilities'], {'node-e1/body-e1': 0.8})
        self.assertEqual(row['result']['settings']['jev_prompt_profile'], 'memory-reference-v1')
        self.assertEqual(row['result']['settings']['selection_policy'], {'kind': 'absolute_threshold'})
        self.assertIsNone(row['error'])
        # The existing review extractor must retain new settings and map the
        # new ancestor-context/source-state target without requiring full_path.
        spec = importlib.util.spec_from_file_location('case_review', ROOT / 'scripts/build_case_review.py')
        review = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(review)
        work = review.WorkTraces(Path(self.tmp.name))
        extracted = work.search(row, {'e1': 'original-source'}, {}, {}, ('ns', 0))
        self.assertEqual(extracted['settings'], row['result']['settings'])
        target = extracted['jev_trace'][0]['targets'][0]
        self.assertEqual((target['node'], target['source_id'], target['probability']), ('node-e1', 'original-source', 0.8))
        self.assertEqual(work.texts[target['path_text_hash']], '')
        self.assertFalse(work.mismatches)
        self.assertEqual((row['query'], row['limit'], row['jev_threshold']), ('question', 10, 0.5))
        self.assertEqual(self.adapter.describe()['config']['work_trace'], 'full')

    def test_failed_unknown_and_trace_errors_do_not_replace_results(self):
        self.adapter.reset('ns')
        native_call = self.adapter.native.call
        def failed(method, **args):
            if method == 'add_batch':
                return {'events': [{'event_id': 'e1', 'state': 'Failed', 'operation_id': 'op-e1'},
                                   {'event_id': 'e2', 'state': 'Unknown', 'operation_id': None}]}
            raise module.NativeError({'kind': 'AUDIT_UNAVAILABLE'})
        with patch.object(self.adapter.native, 'call', side_effect=failed):
            with self.assertRaisesRegex(module.NativeError, 'BATCH_NOT_READY'):
                self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])
        self.assertEqual([r['state'] for r in self.traces('build')], ['Failed', 'Unknown'])
        self.assertTrue(all(r['audit'] is None and r['audit_error'] for r in self.traces('build')))
        def no_audit(method, **args):
            if method == 'audit_build':
                raise module.NativeError({'kind': 'AUDIT_UNAVAILABLE'})
            return native_call(method, **args)
        with patch.object(self.adapter.native, 'call', side_effect=no_audit):
            self.assertEqual(self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])['accepted'], 2)
        self.assertEqual([r['seq'] for r in self.traces('build')], [0, 1, 0, 1])
        self.assertEqual(self.traces('build')[-1]['state'], 'Ready')
        self.assertIsNotNone(self.traces('build')[-1]['snapshot'])
        with patch.object(sys, 'stderr', io.StringIO()) as sink:
            with patch.object(sink, 'write', side_effect=OSError('broken diagnostic sink')):
                self.assertEqual(self.adapter.ingest('ns', [self.event('e3')])['accepted'], 1)

    def test_trace_transport_failure_reopens_state_and_off_reads_nothing(self):
        self.adapter.reset('ns')
        native = self.adapter.native
        original = native.call
        def failure(method, **args):
            if method == 'audit_build':
                raise TimeoutError('diagnostic stalled')
            return original(method, **args)
        with patch.object(native, 'call', side_effect=failure):
            self.assertEqual(self.adapter.ingest('ns', [self.event('e1')])['accepted'], 1)
        self.assertIsNone(self.adapter.native)
        self.assertEqual(self.adapter.retrieve('ns', self.request())['items'], [])
        self.assertEqual(native.env['STATE_PATH'], self.adapter.native.env['STATE_PATH'])
        with patch.dict(os.environ, {'COGLETNET_WORK_TRACE': 'off'}):
            off = module.Adapter({})
        self.addCleanup(off.close)
        self.stderr.seek(0)
        self.stderr.truncate()
        off.reset('off')
        off.ingest('off', [self.event('e1')])
        off.retrieve('off', self.request())
        self.assertEqual([m for m, _ in off.native.calls], ['open', 'add_batch', 'search'])
        self.assertFalse(off.native.calls[-1][1]['trace'])
        self.assertFalse(self.traces('build'))
        self.assertFalse(self.traces('search'))
        self.assertEqual(self.traces('config')[0]['work_trace'], 'off')

    def test_snapshot_view_fallback_preserves_metadata_and_business_result(self):
        self.adapter.reset('ns')
        native = self.adapter.native
        original = native.call
        def old_host(method, **args):
            if method == 'snapshot_manifest_view':
                raise module.NativeError({'kind': 'INVALID_ARGUMENT', 'message': 'unknown native method'})
            return original(method, **args)
        with patch.object(native, 'call', side_effect=old_host):
            self.assertEqual(self.adapter.ingest('ns', [self.event('e1')])['accepted'], 1)
        row = self.traces('build')[0]
        self.assertIn('snapshot_manifest_view', row['audit_error'])
        self.assertEqual(row['snapshot']['points'], [{'id': 'point-e1', 'representation': 'FullPath', 'input_hash': 'input-e1'}])
        self.assertEqual(native.snapshots['snapshot-e1']['points'][0]['vector'], [0.1, 0.2])
        def unavailable(method, **args):
            if method in ('snapshot_manifest_view', 'snapshot_manifest'):
                raise module.NativeError({'kind': 'READ_UNAVAILABLE'})
            return original(method, **args)
        with patch.object(native, 'call', side_effect=unavailable):
            self.assertEqual(self.adapter.ingest('ns', [self.event('e2')])['accepted'], 1)
        row = self.traces('build')[-1]
        self.assertIsNone(row['snapshot'])
        self.assertIn('snapshot_manifest_view', row['audit_error'])
        self.assertIn('snapshot_manifest:', row['audit_error'])

    def test_large_trace_is_complete_and_failed_search_preserves_error(self):
        self.adapter.reset('ns')
        event = dict(self.event('large'), content='正文🙂\n' * 30000)
        self.adapter.ingest('ns', [event])
        row = self.traces('build')[0]
        self.assertEqual(row['audit']['bodies'][0]['text'], event['content'])
        self.assertEqual(row['snapshot']['members'][0]['body']['text'], event['content'])
        details = {'kind': 'MODEL_UNAVAILABLE', 'message': 'model service failed',
                   'model_failure': {'role': 'jev', 'model': 'fake-jev', 'service': 'service-hash',
                                     'phase': 'status', 'status': 503, 'elapsed_ms': 812, 'retryable': True,
                                     'request_id': 'request-1', 'outcome_unknown': False, 'retry_after_ms': 2000}}
        error = module.NativeError(details)
        with patch.object(module.time, 'sleep'), patch.object(self.adapter.native, 'call', side_effect=error):
            with self.assertRaises(module.NativeError) as caught:
                self.adapter.retrieve('ns', self.request())
        self.assertIs(caught.exception, error)
        self.assertIsNone(self.traces('search')[0]['result'])
        self.assertEqual(self.traces('search')[0]['error'], details)
        self.assertEqual(caught.exception.public_error_data(), {'cogletnet': details})
        self.assertEqual(str(error), 'CogletNet native operation failed: MODEL_UNAVAILABLE '
                         '(role="jev", service="service-hash", phase="status", status=503, '
                         'elapsed_ms=812, retryable=true, request_id="request-1")')

    def test_native_message_only_contains_present_diagnostic_fields(self):
        self.assertEqual(str(module.NativeError({'kind': 'SEARCH_UNAVAILABLE'})),
                         'CogletNet native operation failed: SEARCH_UNAVAILABLE')
        error = module.NativeError({'kind': 'MODEL_UNAVAILABLE', 'message': 'private credential',
                                    'model_failure': {'status': None, 'retryable': False, 'elapsed_ms': 0,
                                                      'authorization': 'private credential'}})
        self.assertEqual(str(error), 'CogletNet native operation failed: MODEL_UNAVAILABLE '
                         '(elapsed_ms=0, retryable=false)')


class LeafMemoryTests(unittest.TestCase):
    setUp = AdapterTests.setUp
    event = AdapterTests.event
    request = AdapterTests.request
    traces = AdapterTests.traces

    def leaf(self, filename='manifest-leaf-memory.json'):
        manifest = json.loads(Path(__file__).with_name(filename).read_text())
        env = patch.dict(os.environ, manifest['process']['env'])
        env.start()
        self.addCleanup(env.stop)
        self.adapter = module.Adapter(manifest['bridge']['config'])
        self.addCleanup(self.adapter.close)
        return manifest

    def test_leaf_embed_manifest_changes_only_declared_variant_fields(self):
        baseline = json.loads(Path(__file__).with_name('manifest-leaf-memory.json').read_text())
        manifest = self.leaf('manifest-leaf-memory-leaf-embed.json')
        self.assertEqual(manifest['schema_version'], '3')
        schema = json.loads((ROOT / 'schemas/sut-manifest.v3.json').read_text())['$defs']['Manifest']
        # The Go loader supplies the zero-value config when omitted (as in
        # the existing baseline); the variant must not add a new omission.
        self.assertTrue(set(schema['required']) <= {'config': {}, **manifest}.keys())
        self.assertTrue(manifest.keys() <= schema['properties'].keys())
        self.assertEqual(manifest['variant'], 'native-leaf-memory-leaf-embed-jev3-openai-embedding')
        suffix = (' LeafMemory vector points embed the L0 leaf text only (leaf-memory-embed-leaf-v1); '
                  'retrieval, Jev input and the returned L0–LN layer array are unchanged.')
        self.assertEqual(manifest['notes'], baseline['notes'] + suffix)
        env = manifest['process']['env']
        self.assertEqual(set(env) - set(baseline['process']['env']), {'LEAF_MEMORY_EMBED_INPUT'})
        self.assertEqual(set(baseline['process']['env']) - set(env), set())
        self.assertEqual(env['LEAF_MEMORY_EMBED_INPUT'], 'leaf')
        self.assertEqual(env['MEMORY_TOKENS'], '200000')
        self.assertEqual(env['GENERATION_CONTEXT_TOKENS'], '200000')
        self.assertEqual(env['INDEX_REPRESENTATIONS'], 'leaf_memory')
        self.adapter.reset('ns')
        self.assertEqual(self.adapter.native.env['LEAF_MEMORY_EMBED_INPUT'], 'leaf')
        # Exact structural equality also guards nested runtime/adapter/sidecar
        # fields and any source_snapshot_sha256 stamp added to the baseline.
        unchanged = json.loads(json.dumps(manifest))
        unchanged['variant'] = baseline['variant']
        unchanged['notes'] = baseline['notes']
        del unchanged['process']['env']['LEAF_MEMORY_EMBED_INPUT']
        self.assertEqual(unchanged, baseline)

    def test_abstractive_manifest_changes_only_declared_variant_fields(self):
        baseline = json.loads(Path(__file__).with_name('manifest-leaf-memory.json').read_text())
        manifest = json.loads(Path(__file__).with_name('manifest-leaf-memory-abstractive.json').read_text())
        schema = json.loads((ROOT / 'schemas/sut-manifest.v3.json').read_text())['$defs']['Manifest']
        self.assertEqual(manifest['schema_version'], '3')
        self.assertTrue(set(schema['required']) <= {'config': {}, **manifest}.keys())
        self.assertTrue(manifest.keys() <= schema['properties'].keys())
        self.assertEqual(manifest['variant'], 'native-leaf-memory-abstractive-jev3-openai-embedding')
        suffix = (' Whole-array LeafMemory embedding; abstractive-ratio-v3 summaries; '
                  'manifest 不钉摘要长度键，按 staging 快照的上游默认；HEAD 10f356d 起默认 SUMMARY_RATIO 0.10、'
                  'band 0.07–0.13、SUMMARY_MIN_TOKENS 16、SUMMARY_COPY_MAX_TOKENS 32、MAX_LEVEL 63; '
                  'summary and validation models follow the host llm.model.'
                  ' V2 uses shortening/expansion retries and upstream SoftV2 soft recovery.'
                  ' V3 adds script-aware instructions and a model language guard.')
        self.assertEqual(manifest['notes'], baseline['notes'] + suffix)
        env, previous = manifest['process']['env'], baseline['process']['env']
        self.assertEqual(set(env) - set(previous), {'SUMMARY_LENGTH_BAND', 'LEAF_MEMORY_EMBED_INPUT'})
        self.assertEqual(set(previous) - set(env), {'MEMORY_SUMMARY_MODEL', 'MEMORY_SUMMARY_VALIDATION_MODEL'})
        changed = {'SUMMARY_PROMPT_PROFILE': 'abstractive-ratio-v3', 'SUMMARY_CANDIDATE_POLICY': 'model-only',
                   'SUMMARY_LENGTH_BAND': 'ratio-v1', 'LEAF_MEMORY_EMBED_INPUT': 'memory'}
        for key, value in changed.items():
            self.assertEqual(env[key], value)
        self.assertEqual(env['MEMORY_TOKENS'], '200000')
        self.assertEqual(env['GENERATION_CONTEXT_TOKENS'], '200000')
        self.assertNotIn('SUMMARY_COPY_MAX_TOKENS', env)
        self.assertNotIn('SUMMARY_MIN_TOKENS', env)
        for key in ('SUMMARY_RATIO', 'SUMMARY_RATIO_MIN', 'SUMMARY_RATIO_MAX', 'MAX_LEVEL'):
            self.assertNotIn(key, env)
        self.assertNotIn('SUMMARY_LANGUAGE_GUARD', env)
        self.assertFalse(any('API_KEY' in key for key in env))
        for key in ('summary', 'validation'):
            self.assertEqual(manifest['declared']['llm'][key], 'injected via llm.model (same as host)')
        # Guard every other nested field, effort knob, and optional stamp.
        unchanged = json.loads(json.dumps(manifest))
        unchanged['variant'], unchanged['notes'] = baseline['variant'], baseline['notes']
        for key in changed:
            if key in previous:
                unchanged['process']['env'][key] = previous[key]
            else:
                del unchanged['process']['env'][key]
        for key in ('MEMORY_SUMMARY_MODEL', 'MEMORY_SUMMARY_VALIDATION_MODEL'):
            unchanged['process']['env'][key] = previous[key]
        for key in ('summary', 'validation'):
            unchanged['declared']['llm'][key] = baseline['declared']['llm'][key]
        self.assertEqual(unchanged, baseline)
        with patch.dict(os.environ, {**env, 'HOST_GENERATION_MODEL': 'injected-host', 'EP': self.tmp.name}, clear=True):
            self.adapter = module.Adapter(manifest['bridge']['config'])
            self.addCleanup(self.adapter.close)
            config = self.adapter.describe()['config']
            self.assertEqual(config['summary_model'], 'injected-host')
            self.assertEqual(config['summary_validation_model'], 'injected-host')
            self.assertEqual(config['representation'], 'LeafMemory')
            self.assertEqual(config['summary_candidate_policy'], 'model-only')
            self.assertEqual(config['summary_length_band'], 'ratio-v1')
            self.assertEqual(config['leaf_memory_embed_input'], 'memory')
            self.assertEqual(config['summary_prompt_profile'], 'abstractive-ratio-v3')
            self.assertIsNone(config['summary_copy_max_tokens'])
            self.assertIsNone(config['summary_language_guard'])
            self.adapter.reset('abstractive')
            self.assertEqual(self.adapter.native.env['LEAF_MEMORY_EMBED_INPUT'], 'memory')
            self.assertNotIn('MEMORY_SUMMARY_MODEL', self.adapter.native.env)
            self.assertNotIn('MEMORY_SUMMARY_VALIDATION_MODEL', self.adapter.native.env)

    def test_abstractive_lv1_manifest_only_caps_the_hierarchy(self):
        baseline = json.loads(Path(__file__).with_name('manifest-leaf-memory-abstractive.json').read_text())
        manifest = self.leaf('manifest-leaf-memory-abstractive-lv1.json')
        self.assertEqual(manifest['variant'], 'native-leaf-memory-abstractive-lv1-jev3-openai-embedding')
        self.assertEqual(manifest['process']['env']['MAX_LEVEL'], '1')
        self.assertEqual(set(manifest['process']['env']) - set(baseline['process']['env']), {'MAX_LEVEL'})
        self.assertEqual(self.adapter.describe()['config']['max_level'], '1')
        for phrase in ('L0 叶子', 'L1 摘要', '无父根', '更高层摘要', '其余与 abstractive 变体完全一致'):
            self.assertIn(phrase, manifest['notes'])
        unchanged = json.loads(json.dumps(manifest))
        unchanged['variant'], unchanged['notes'] = baseline['variant'], baseline['notes']
        del unchanged['process']['env']['MAX_LEVEL']
        self.assertEqual(unchanged, baseline)

    def row(self):
        memory = [{'info': ['native leaf🙂'], 'current': None},
                  {'info': ['sibling summary', 'current summary'], 'current': 1}]
        # Fake native supplies the renderer output: Python must not synthesize
        # it from path.text, row.text, input events or the layer structure.
        return {'source': {'id': 'source-e1'}, 'reference': {'node': 'node-e1', 'body': 'body-e1'},
                'path': {'text': 'LEGACY PATH MUST NOT REPLACE MEMORY'}, 'text': 'legacy body',
                'memory': memory, 'reader_text': 'leaf-memory-context-v1\n' + json.dumps({'memories': [memory]}, ensure_ascii=False),
                'reader_sources': ['source-e1', 'source-e2'], 'rank_utility': 0.8,
                'jev_outcome': {'kind': 'reference', 'useful': 0.8, 'not_useful': 0.1, 'uncertain': 0.1}}

    def test_manifest_and_describe_select_native_three_class_defaults(self):
        manifest = self.leaf()
        config = self.adapter.describe()['config']
        self.assertEqual(config['representation'], 'LeafMemory')
        self.assertEqual(config['selection'], 'Jev')
        self.assertEqual(config['failure_policy'], 'Strict')
        self.assertEqual(config['jev_admission_basis'], 'useful')
        self.assertEqual(config['jev_output_format'], 'ChoiceThreeClass')
        self.assertEqual(config['jev_calibration'], 'Uncalibrated')
        self.assertEqual(config['jev_threshold'], 0.5)
        self.assertEqual(config['jev_class_sum_tolerance'], 0.02)
        self.assertEqual(config['jev_selection_policy'], 'absolute-threshold')
        self.assertEqual(config['ingest_concurrency'], 1)
        self.assertIn('scope and as_of unsupported', self.adapter.describe()['limitations'][0])
        self.assertTrue(any("source_ids name only the memory's own L0 source; summary-layer dependencies stay in the trace (reader_sources)"
                            in note for note in self.adapter.describe()['limitations']))
        self.assertFalse(any('API_KEY' in key for key in manifest['process']['env']))
        self.assertEqual(manifest['llm']['env']['HOST_GENERATION_MODEL'], '{llm.model}')
        legacy = json.loads(Path(__file__).with_name('manifest.json').read_text())
        self.assertEqual(legacy['bridge']['config'], {})
        self.assertEqual(legacy['process']['env']['JEV_PROMPT_PROFILE'], 'memory-reference-v1')
        self.assertEqual(legacy['process']['env']['GENERATION_TOKENIZER'], 'bytes')
        self.assertEqual(manifest['process']['env']['GENERATION_TOKENIZER'], 'tiktoken:o200k_base')
        with patch.dict(os.environ, {'INDEX_REPRESENTATIONS': 'body,full_path'}):
            with self.assertRaisesRegex(ValueError, 'leaf_memory index'):
                module.Adapter({'retrieval_mode': 'leaf_memory'})

    def test_shared_scope_diagnostics_written_once_per_batch(self):
        self.leaf()
        self.adapter.reset('ns')
        native = self.adapter.native
        original = native.call
        events = [dict(self.event('e' + str(i)), session_id='shared') for i in range(3)]
        def shared(method, **data):
            reply = original(method, **data)
            if method == 'add_batch':
                native.audits['op-e0']['scope_payload'] = 'audit text ' * 400
                for result in reply['events']:
                    result.update(operation_id='op-e0', write={'snapshot': 'snapshot-e0'})
            return reply
        with patch.object(native, 'call', side_effect=shared):
            self.assertEqual(self.adapter.ingest('ns', events)['accepted'], 3)
            rows = self.traces('build')
            self.assertEqual([r['trace_batch'] for r in rows], [0, 0, 0])
            self.assertEqual(rows[0]['audit'], native.audits['op-e0'])
            for row in rows[1:]:
                self.assertEqual(row['audit'], {'ref_seq': 0})
                self.assertEqual(row['snapshot'], {'ref_seq': 0})
            self.assertEqual([m for m, _ in native.calls].count('audit_build'), 1)
            self.assertEqual([m for m, _ in native.calls].count('snapshot_manifest_view'), 1)
            expanded = [dict(r, audit=rows[0]['audit'], snapshot=rows[0]['snapshot']) for r in rows]
            self.assertLess(len(json.dumps(rows)), len(json.dumps(expanded)) * 0.5)
            # The next ingest has fresh diagnostics, even when event seq and
            # operation IDs repeat. A capped later batch cannot use stale data.
            self.adapter.ingest('ns', events)
            rows = self.traces('build')[3:]
            self.assertEqual([r['trace_batch'] for r in rows], [1, 1, 1])
            self.assertIn('operation', rows[0]['audit'])
            self.assertEqual(rows[1]['audit'], {'ref_seq': 0})
            self.assertEqual([m for m, _ in native.calls].count('audit_build'), 2)

    def test_snapshot_cache_is_independent_and_failed_audit_does_not_fail_ingest(self):
        self.leaf()
        self.adapter.reset('ns')
        native, original = self.adapter.native, self.adapter.native.call
        events = [self.event('e0'), self.event('e1'), self.event('e2')]
        seen = []
        def diagnostics(method, **data):
            seen.append(method)
            if method == 'audit_build' and data['operation_id'] == 'op-e0':
                raise module.NativeError({'kind': 'AUDIT_UNAVAILABLE'})
            reply = original(method, **data)
            if method == 'add_batch':
                reply['events'][1]['operation_id'] = 'op-e0'
                for result in reply['events']:
                    result['write']['snapshot'] = 'snapshot-e0'
            return reply
        with patch.object(native, 'call', side_effect=diagnostics):
            self.assertEqual(self.adapter.ingest('ns', events)['accepted'], 3)
        rows = self.traces('build')
        self.assertIsNone(rows[0]['audit'])
        self.assertIsNone(rows[1]['audit'])
        self.assertEqual(rows[0]['audit_error'], rows[1]['audit_error'])
        self.assertIn('AUDIT_UNAVAILABLE', rows[0]['audit_error'])
        self.assertIn('operation', rows[2]['audit'])
        self.assertEqual(rows[1]['snapshot'], {'ref_seq': 0})
        self.assertEqual(rows[2]['snapshot'], {'ref_seq': 0})
        self.assertEqual(seen.count('audit_build'), 2)
        self.assertEqual(seen.count('snapshot_manifest_view'), 1)

    def test_scope_mapping_event_order_and_timestamp_are_public_only(self):
        self.leaf()
        self.adapter.reset('ns')
        events = [dict(self.event('e1'), session_id='conversation', timestamp='2026-10-28T08:00:00.123456+08:00'),
                  dict(self.event('e2'), session_id='conversation'),
                  dict(self.event('e3', 'other'), session_id='conversation'),
                  dict(self.event('e4'), attributes={'turn_group': 'turn'}),
                  dict(self.event('e5'), turn_group='turn'), self.event('e6'), self.event('e7')]
        self.assertEqual(self.adapter.ingest('ns', events)['accepted'], 7)
        batch = next(d for m, d in self.adapter.native.calls if m == 'add_batch')
        self.assertEqual(batch['write_mode'], 'scope_events')
        rows = batch['events']
        self.assertEqual(rows[0]['scope'], rows[1]['scope'])
        self.assertNotEqual(rows[0]['scope'], rows[2]['scope'])
        self.assertEqual(rows[3]['scope'], rows[4]['scope'])
        self.assertNotEqual(rows[5]['scope'], rows[6]['scope'])
        self.assertEqual([r['event_sequence'] for r in rows], list(range(7)))
        delta = module.datetime(2026, 10, 28, tzinfo=module.timezone.utc) - module.datetime(1970, 1, 1, tzinfo=module.timezone.utc)
        self.assertEqual(rows[0]['occurred_at'], delta.days * 86400 * 1000000 + 123456)
        self.assertEqual([b['seq'] for b in self.traces('build')], list(range(7)))
        self.assertEqual(self.adapter.dump('ns')['items'][0]['text'], events[0]['content'])

    def test_replace_retains_source_sequence_and_replay_does_not_append_again(self):
        self.leaf()
        self.adapter.reset('ns')
        event = dict(self.event('e1'), session_id='conversation')
        self.adapter.ingest('ns', [event])
        changed = dict(event, content='updated text')
        self.adapter.ingest('ns', [changed])
        calls = [d for m, d in self.adapter.native.calls if m == 'add_batch']
        self.assertEqual(calls[-1]['events'][0]['expected_source_version'], 1)
        self.assertEqual(calls[-1]['events'][0]['event_sequence'], 0)
        self.assertEqual(self.adapter.event_results['e1']['source_ref']['version'], 2)
        self.assertEqual(self.adapter.sources, {'source-e1': 'e1'})
        self.adapter.ingest('ns', [changed])
        self.assertEqual(len([m for m, _ in self.adapter.native.calls if m == 'add_batch']), 2)
        with self.assertRaisesRegex(ValueError, 'owner or scope'):
            self.adapter.ingest('ns', [dict(changed, session_id='different')])

    def test_partial_failure_keeps_pending_payload_and_blocks_partial_history(self):
        self.leaf()
        self.adapter.reset('ns')
        event = self.event('e1')
        self.adapter.ingest('ns', [event])
        original = self.adapter.native.call
        pending = dict(self.event('e2'), session_id='new-session')
        failed_rows = []
        def fail(method, **data):
            if method == 'add_batch':
                failed_rows.extend(data['events'])
                return {'events': [{'event_id': 'e2', 'state': 'Pending', 'operation_id': 'op-e2'}]}
            if method == 'audit_build' and data['operation_id'] == 'op-e2':
                raise module.NativeError({'kind': 'NOT_FOUND'})
            return original(method, **data)
        with patch.object(self.adapter.native, 'call', side_effect=fail):
            self.assertEqual(self.adapter.ingest('ns', [event, pending])['accepted'], 2)
        self.assertEqual([r['id'] for r in failed_rows], ['e2'])
        with self.assertRaisesRegex(ValueError, 'same payload'):
            self.adapter.ingest('ns', [dict(pending, content='changed')])
        with self.assertRaisesRegex(module.NativeError, 'HISTORY_NOT_READY'):
            self.adapter.retrieve('ns', self.request())
        def finish(method, **data):
            if method == 'wait_operation':
                self.assertEqual(data['operation_id'], 'op-e2')
                self.assertEqual(data['events'], failed_rows)
                # Simulate the already adopted operation completing, not another append.
                return original('add_batch', events=failed_rows, write_mode='scope_events')
            return original(method, **data)
        with patch.object(self.adapter.native, 'call', side_effect=finish):
            self.adapter.ingest('ns', [event, pending])
        self.assertTrue(self.adapter.ready('ns', {}, None)['visible'])

    def test_native_rendering_direct_source_packing_and_complete_closure_in_trace(self):
        self.leaf()
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])
        row = self.row()
        self.adapter.native.rows = [row]
        req = self.request()
        req['budget']['max_context_chars'] = len(row['reader_text'])
        result = self.adapter.retrieve('ns', req)
        self.assertEqual(result['items'][0]['text'], row['reader_text'])
        self.assertEqual(result['items'][0]['source_ids'], ['e1'])
        search = self.adapter.native.calls[-1][1]
        self.assertEqual(search['representation'], 'LeafMemory')
        self.assertTrue(search['trace'])
        traced = self.traces('search')[-1]['result']
        self.assertEqual(traced['items'][0]['memory'], row['memory'])
        self.assertEqual(traced['items'][0]['reader_sources'], ['source-e1', 'source-e2'])
        self.assertEqual(traced['items'][0]['jev_outcome'], row['jev_outcome'])
        self.assertEqual(traced['packed']['memories'], 1)
        self.assertEqual(traced['settings']['jev_output_format'], 'ChoiceThreeClass')
        self.assertEqual(traced['jev_trace'][0]['answers']['node-e1/body-e1']['outcome'], row['jev_outcome'])
        self.assertEqual([layer['current'] for layer in traced['context_view']['memories'][0]['layers']], [None, 1])
        req['budget']['max_context_chars'] -= 1
        self.assertEqual(self.adapter.retrieve('ns', req)['items'], [])

    def test_new_trace_remains_readable_by_existing_review_extractor(self):
        self.leaf()
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])
        self.adapter.native.rows = [self.row()]
        self.adapter.retrieve('ns', self.request())
        spec = importlib.util.spec_from_file_location('case_review', ROOT / 'scripts/build_case_review.py')
        review = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(review)
        logs = Path(self.tmp.name) / 'logs/0001-ns'
        logs.mkdir(parents=True)
        (logs / 'sut.stderr.log').write_text(self.stderr.getvalue())
        traces = review.WorkTraces(self.tmp.name)
        search = traces.search(traces.logs['ns'][-1], {'e1': 'public-e1', 'e2': 'public-e2'}, {}, {}, ('ns', 0))
        self.assertEqual(search['items'][0]['source_id'], 'public-e1')
        self.assertEqual(search['settings']['jev_output_format'], 'ChoiceThreeClass')
        self.assertFalse(traces.mismatches)
        # This old page contract has no three-class field. Never invent a
        # binary probability; complete native answers remain in sut_trace.
        self.assertIsNone(search['jev_trace'][0]['targets'][0]['probability'])

    def test_missing_timestamp_remains_unknown_and_naive_timestamp_is_rejected(self):
        self.leaf()
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1')])
        row = next(d for m, d in self.adapter.native.calls if m == 'add_batch')['events'][0]
        self.assertNotIn('occurred_at', row)
        with self.assertRaisesRegex(ValueError, 'timezone'):
            self.adapter.ingest('ns', [dict(self.event('e2'), timestamp='2026-10-28T00:00:00')])

    def test_bad_layer_closure_is_rejected_not_washed_to_empty(self):
        self.leaf()
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1'), self.event('e2'), self.event('other', 'other-owner')])
        for refs in ([], ['source-e1', 'source-e1'], ['source-e1', 'missing'], ['source-e1', 'source-other'],
                     ['source-e2'], ['source-e1', 7], 'source-e1', None):
            with self.subTest(refs=refs):
                self.adapter.native.rows = [dict(self.row(), reader_sources=refs)]
                with self.assertRaisesRegex(ValueError, 'layer closure'):
                    self.adapter.retrieve('ns', self.request())
        for missing in ('reader_text', 'memory'):
            self.adapter.native.rows = [dict(self.row(), **{missing: None})]
            with self.assertRaisesRegex(ValueError, 'memory/rendered text'):
                self.adapter.retrieve('ns', self.request())

    def test_trace_off_and_strict_failure_are_unchanged(self):
        self.leaf()
        self.adapter.work_trace = 'off'
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1')])
        self.adapter.native.rows = []
        self.adapter.retrieve('ns', self.request())
        self.assertFalse(self.adapter.native.calls[-1][1]['trace'])
        self.assertFalse(self.traces('build'))
        self.assertFalse(self.traces('search'))
        self.assertNotIn('audit_build', [m for m, _ in self.adapter.native.calls])
        error = module.NativeError({'kind': 'MODEL_UNAVAILABLE'})
        with patch.object(module.time, 'sleep'), patch.object(self.adapter.native, 'call', side_effect=error):
            with self.assertRaises(module.NativeError) as caught:
                self.adapter.retrieve('ns', self.request())
        self.assertIs(caught.exception, error)


class TreeTests(unittest.TestCase):
    setUp = AdapterTests.setUp
    event = AdapterTests.event
    request = AdapterTests.request
    traces = AdapterTests.traces

    def leaf(self):
        # Reuse the same scoped-write contract tests for both retrieval modes.
        return LeafMemoryTests.leaf(self, 'manifest-tree.json')

    test_scoped_writes_preserve_public_order_and_recording_time = LeafMemoryTests.test_scope_mapping_event_order_and_timestamp_are_public_only
    test_scoped_trace_cache_is_shared = LeafMemoryTests.test_shared_scope_diagnostics_written_once_per_batch
    test_scoped_replace_and_replay_are_unchanged = LeafMemoryTests.test_replace_retains_source_sequence_and_replay_does_not_append_again

    def row(self):
        return {'source': {'id': 'source-e1'}, 'reference': {'node': 'summary-node', 'body': 'summary-body'},
                'path': {'text': 'ANCESTOR PATH MUST NOT BE RETURNED'}, 'text': 'own node summary🙂',
                'memory': None, 'reader_text': 'own node summary🙂',
                'reader_sources': ['source-e2', 'source-e1'], 'rank_utility': 0.75,
                'level': 1, 'recorded': {'start': 1793491200000000, 'end': 1793577600000000}}

    def test_tree_requires_body_index_and_node_profile(self):
        with patch.dict(os.environ, {'INDEX_REPRESENTATIONS': 'body', 'JEV_PROMPT_PROFILE': 'node-reference-v1'}):
            self.assertEqual(module.Adapter({'retrieval_mode': 'tree'}).retrieval_mode, 'tree')
            for key, value in [('INDEX_REPRESENTATIONS', 'tree'), ('INDEX_REPRESENTATIONS', 'leaf_memory'),
                               ('INDEX_REPRESENTATIONS', 'body,full_path'),
                               ('JEV_PROMPT_PROFILE', 'leaf-memory-reference-v1')]:
                with self.subTest(key=key, value=value), patch.dict(os.environ, {key: value}):
                    with self.assertRaisesRegex(ValueError, 'tree requires body index and node-reference-v1'):
                        module.Adapter({'retrieval_mode': 'tree'})
            with self.assertRaisesRegex(ValueError, 'retrieval_mode'):
                module.Adapter({'retrieval_mode': 'unknown'})

    def test_tree_manifest_only_changes_declared_variant_fields(self):
        tree = self.leaf()
        base = json.loads(Path(__file__).with_name('manifest-leaf-memory-abstractive.json').read_text())
        self.assertEqual(tree['variant'], 'native-tree-jev3-openai-embedding')
        self.assertEqual(tree['bridge']['config']['retrieval_mode'], 'tree')
        env = tree['process']['env']
        self.assertEqual(env['INDEX_REPRESENTATIONS'], 'body')
        self.assertEqual(env['JEV_PROMPT_PROFILE'], 'node-reference-v1')
        self.assertEqual(env['SUMMARY_PROMPT_PROFILE'], 'abstractive-ratio-v4')
        self.assertEqual(env['SUMMARY_RECORD_TIME'], 'recorded-v1')
        self.assertNotIn('LEAF_MEMORY_EMBED_INPUT', env)
        self.assertFalse(any('API_KEY' in key for key in env))
        self.assertIn('RAPTOR', tree['notes'])
        self.assertIn('node-reference-v1', tree['declared']['llm']['selection'])
        base['variant'], base['notes'], base['declared']['llm'] = tree['variant'], tree['notes'], tree['declared']['llm']
        base['bridge']['config']['retrieval_mode'] = 'tree'
        base['process']['env'].update(INDEX_REPRESENTATIONS='body', JEV_PROMPT_PROFILE='node-reference-v1',
                                      SUMMARY_PROMPT_PROFILE='abstractive-ratio-v4', SUMMARY_RECORD_TIME='recorded-v1')
        base['process']['env'].pop('LEAF_MEMORY_EMBED_INPUT')
        self.assertEqual(tree, base)

    def test_tree_describe_declares_own_text_and_scoped_writes(self):
        self.leaf()
        described = self.adapter.describe()
        config = described['config']
        self.assertEqual(config['representation'], 'Tree')
        self.assertEqual(config['retrieval_mode'], 'tree')
        self.assertEqual(config['write_mode'], 'scope_events')
        self.assertEqual(config['selection'], 'Jev')
        self.assertEqual(config['failure_policy'], 'Strict')
        self.assertEqual(config['index_representations'], 'body')
        self.assertEqual(config['summary_record_time'], 'recorded-v1')
        self.assertEqual(config['summary_prompt_profile'], 'abstractive-ratio-v4')
        for notes in (config['llm_notes'], described['limitations']):
            self.assertIn('own node text', ' '.join(notes))
            self.assertIn('no ancestors', ' '.join(notes))
        self.assertNotIn('render_leaf_memories', ' '.join(described['limitations']))

    def test_tree_returns_node_text_and_all_direct_sources_in_order(self):
        self.leaf()
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])
        native = self.adapter.native
        self.assertTrue(next(d for m, d in native.calls if m == 'open')['scheduled_writes'])
        native.rows = [self.row()]
        out = self.adapter.retrieve('ns', self.request())
        self.assertEqual(out['items'], [{'id': 'summary-node', 'text': 'own node summary🙂',
                                        'source_ids': ['e2', 'e1'], 'score': 0.75}])
        search = next(d for m, d in native.calls if m == 'search')
        self.assertEqual(search['representation'], 'Tree')
        trace = self.traces('search')[-1]['result']
        self.assertEqual(trace['context_view']['nodes'][0]['sources'],
                         [{'id': 'source-e2', 'version': 1}, {'id': 'source-e1', 'version': 1}])
        self.assertEqual(trace['items'][0]['recorded'], self.row()['recorded'])
        self.assertEqual(trace['items'][0]['level'], 1)
        self.assertEqual(trace['settings']['vector_representation'], 'Body')
        self.assertEqual(trace['settings']['upper_k'], 0)
        request = self.request()
        request['budget']['max_context_chars'] = 2
        self.assertEqual(self.adapter.retrieve('ns', request)['items'], [])

    def test_tree_rejects_missing_or_foreign_direct_sources(self):
        self.leaf()
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1'), self.event('e2'), self.event('other', 'other-owner')])
        for refs in (None, [], ['source-e2'], ['source-e1', 'source-e1'], [3],
                     ['source-e1', 'source-unknown'], ['source-e1', 'source-other']):
            with self.subTest(refs=refs):
                row = self.row()
                row['reader_sources'] = refs
                # Check the adapter at the native result boundary even for
                # malformed output that the fake trace itself cannot render.
                with patch.object(self.adapter.native, 'call', return_value={'items': [row]}):
                    with self.assertRaisesRegex(ValueError, 'Tree.*source'):
                        self.adapter.retrieve('ns', self.request())
        row = self.row()
        row.pop('reader_text')
        with patch.object(self.adapter.native, 'call', return_value={'items': [row]}):
            with self.assertRaisesRegex(ValueError, 'Tree.*text'):
                self.adapter.retrieve('ns', self.request())


class SearchRetryTests(unittest.TestCase):
    event = AdapterTests.event
    request = AdapterTests.request
    traces = AdapterTests.traces
    leaf = LeafMemoryTests.leaf

    def setUp(self):
        AdapterTests.setUp(self)
        self.now, self.sleeps = 0, []
        def sleep(seconds):
            self.sleeps.append(seconds)
            self.now += seconds
        for p in (patch.object(module.time, 'monotonic', side_effect=lambda: self.now),
                  patch.object(module.time, 'sleep', side_effect=sleep)):
            p.start()
            self.addCleanup(p.stop)

    def prepare(self):
        self.adapter.reset('ns')
        self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])
        self.adapter.native.rows = [LeafMemoryTests.row(self)]

    def test_retry_recovers_in_both_variants_and_records_retry_after(self):
        for leaf in (False, True):
            with self.subTest(leaf=leaf):
                if leaf:
                    self.leaf()
                self.prepare()
                self.sleeps.clear()
                failure = module.NativeError({'kind': 'MODEL_UNAVAILABLE', 'model_failure': {
                    'retryable': True, 'phase': 'connect', 'outcome_unknown': False, 'retry_after_ms': 58000}})
                response = self.adapter.native.call('search', query='question', sources=['source-e1', 'source-e2'],
                    limit=10, trace=True, representation='LeafMemory' if leaf else 'FullPath')
                request = self.request()
                request['budget']['max_context_chars'] = 2000
                with patch.object(self.adapter.native, 'call', side_effect=[failure, response]) as call:
                    out = self.adapter.retrieve('ns', request)
                self.assertEqual(len(out['items']), 1)
                self.assertEqual(call.call_count, 2)
                self.assertEqual(self.sleeps, [60])
                self.assertEqual(call.call_args_list[0].kwargs['timeout'] - call.call_args_list[1].kwargs['timeout'], 60)
                trace = self.traces('search')[-1]
                self.assertIsNone(trace['error'])
                self.assertEqual(trace['retries'], [{'kind': 'MODEL_UNAVAILABLE', 'backoff_s': 60, 'retry_after_s': 58}])

    def test_nonretryable_error_is_preserved_without_waiting(self):
        self.prepare()
        error = module.NativeError({'kind': 'INVALID_ARGUMENT'})
        with patch.object(self.adapter.native, 'call', side_effect=error) as call:
            with self.assertRaises(module.NativeError) as caught:
                self.adapter.retrieve('ns', self.request())
        self.assertIs(caught.exception, error)
        self.assertEqual(call.call_count, 1)
        self.assertEqual(self.sleeps, [])
        self.assertEqual(self.traces('search')[-1]['retries'], [])

    def test_insufficient_rpc_window_preserves_error_without_retry(self):
        self.prepare()
        for elapsed, hint, timeout in ((10, None, 70), (0, 58000, 100)):
            with self.subTest(elapsed=elapsed, hint=hint):
                self.adapter.config['request_timeout_s'] = timeout
                error = module.NativeError({'kind': 'MODEL_TRANSPORT', 'model_failure': {'retry_after_ms': hint}})
                def fail(*args, **kwargs):
                    self.now += elapsed
                    raise error
                with patch.object(self.adapter.native, 'call', side_effect=fail) as call:
                    with self.assertRaises(module.NativeError) as caught:
                        self.adapter.retrieve('ns', self.request())
                self.assertIs(caught.exception, error)
                self.assertEqual(call.call_count, 1)
                self.assertEqual(self.sleeps, [])
                self.assertEqual(self.traces('search')[-1]['retries'], [])

    def test_all_retryable_kinds_stop_after_three_attempts(self):
        self.prepare()
        for kind in ('MODEL_UNAVAILABLE', 'MODEL_TIMEOUT', 'MODEL_TRANSPORT', 'MODEL_RATE_LIMITED',
                     'MODEL_RECOVERY_EXHAUSTED', 'OTHER_RETRYABLE'):
            with self.subTest(kind=kind):
                self.sleeps.clear()
                error = module.NativeError({'kind': kind, 'model_failure': {'retryable': kind == 'OTHER_RETRYABLE'}})
                with patch.object(self.adapter.native, 'call', side_effect=error) as call:
                    with self.assertRaises(module.NativeError) as caught:
                        self.adapter.retrieve('ns', self.request())
                self.assertIs(caught.exception, error)
                self.assertEqual(call.call_count, 3)
                self.assertEqual(self.sleeps, [10, 20])
                self.assertEqual(self.traces('search')[-1]['retries'], [
                    {'kind': kind, 'backoff_s': wait, 'retry_after_s': None} for wait in (10, 20)])

    def test_bridge_config_overrides_and_validates_retry_limits(self):
        self.adapter = module.Adapter({'search_retry_max_attempts': 2, 'search_retry_initial_s': 7})
        self.addCleanup(self.adapter.close)
        self.prepare()
        error = module.NativeError({'kind': 'MODEL_TIMEOUT'})
        with patch.object(self.adapter.native, 'call', side_effect=error) as call:
            with self.assertRaises(module.NativeError):
                self.adapter.retrieve('ns', self.request())
        self.assertEqual(call.call_count, 2)
        self.assertEqual(self.sleeps, [7])
        for key, values in {'search_retry_max_attempts': (0, -1, True, 1.5),
                            'search_retry_initial_s': (0, -1, True, float('inf'))}.items():
            for value in values:
                with self.subTest(key=key, value=value), self.assertRaisesRegex(ValueError, key):
                    module.Adapter({key: value})


class ScheduledWriteTests(unittest.TestCase):
    setUp = AdapterTests.setUp
    event = AdapterTests.event
    request = AdapterTests.request
    traces = AdapterTests.traces
    leaf = LeafMemoryTests.leaf

    def scheduled(self, states, resume_errors=0, failure=None):
        self.leaf()
        self.adapter.reset('ns')
        self.now, self.sleeps, self.resume_errors = 0, [], resume_errors
        self.clock = patch.object(module.time, 'monotonic', side_effect=lambda: self.now)
        self.clock.start(); self.addCleanup(self.clock.stop)
        def sleep(seconds):
            self.sleeps.append(seconds)
            self.now += seconds
        sleeper = patch.object(module.time, 'sleep', side_effect=sleep)
        sleeper.start(); self.addCleanup(sleeper.stop)
        self.plan, self.ready_results, self.order = list(states), {}, []
        original = self.adapter.native.call
        self.failure = failure or {'kind': 'MODEL_RECOVERY_EXHAUSTED', 'model_failure': {'retryable': True}}
        def call(method, **data):
            self.order.append((method, data))
            if method == 'add_batch':
                result = original(method, **data)
                operation = result['events'][0]['operation_id']
                for row in result['events']:
                    row['operation_id'] = operation
                self.ready_results[operation] = result['events']
            elif method in ('wait_operation', 'resume'):
                original(method, **data)
                operation = data['operation_id']
                if method == 'resume':
                    if self.resume_errors:
                        self.resume_errors -= 1
                        raise module.NativeError(dict(self.failure, operation=operation))
                    self.plan = ['Ready']
                    return {'operation_id': operation, 'scheduled': True}
            else:
                return original(method, **data)
            state = self.plan.pop(0) if self.plan else 'Ready'
            if state == 'Pending':
                self.now += data['timeout_seconds']
            results = [dict(row, state=state) for row in self.ready_results[operation]]
            if state == 'Failed':
                for row in results:
                    row['error'] = dict(self.failure, operation=operation)
            return {'events': results, 'all_ready': state == 'Ready'}
        patched = patch.object(self.adapter.native, 'call', side_effect=call)
        patched.start(); self.addCleanup(patched.stop)

    def test_scheduled_window_expires_next_ingest_waits_before_append(self):
        self.scheduled(['Pending', 'Ready', 'Ready'])
        from memeval_bridge import Dispatcher
        dispatcher = Dispatcher(self.adapter)
        self.addCleanup(dispatcher.loop.close)
        dispatcher.ns = 'ns'
        first = dispatcher.call('ingest', {'ns': 'ns', 'events': [self.event('e1')]})
        self.assertEqual(first['accepted'], 1)
        self.assertEqual(self.adapter.event_results['e1']['state'], 'Pending')
        self.assertEqual(self.adapter.event_results['e1']['operation_id'], 'op-e1')
        with self.assertRaisesRegex(module.NativeError, 'HISTORY_NOT_READY'):
            self.adapter.retrieve('ns', self.request())
        second = dispatcher.call('ingest', {'ns': 'ns', 'events': [self.event('e2')]})
        methods = [m for m, _ in self.order if m in ('add_batch', 'wait_operation')]
        self.assertEqual(methods, ['add_batch', 'wait_operation', 'add_batch'])
        self.assertEqual(second['accepted'], 1)
        self.assertTrue(dispatcher.call('ready', {'ns': 'ns', 'handle': second, 'deadline': None})['visible'])
        self.assertTrue(all(r['state'] == 'Ready' for r in self.adapter.event_results.values()))
        self.assertEqual([d['scheduled_writes'] for m, d in self.adapter.native.calls if m == 'open'], [True])

    def test_ready_barrier_continues_same_operation_and_emits_windows(self):
        self.scheduled(['Pending', 'Ready'])
        h = self.adapter.ingest('ns', [self.event('e1')])
        self.assertTrue(self.adapter.ready('ns', h, None)['visible'])
        self.assertEqual([m for m, _ in self.order].count('add_batch'), 1)
        row = self.traces('build')[-1]
        self.assertEqual(row['operation_id'], 'op-e1')
        self.assertEqual(row['resume_count'], 0)
        self.assertEqual([w['method'] for w in row['wait_windows']], ['add_batch', 'wait_operation'])
        self.assertTrue(all(0 < w['window_s'] <= 1740 for w in row['wait_windows']))
        self.assertTrue(all(w['retry_after_s'] is None for w in row['wait_windows']))

    def test_new_batch_is_accepted_but_never_appended_over_pending_operation(self):
        self.scheduled(['Pending', 'Pending', 'Ready', 'Ready'])
        self.adapter.ingest('ns', [self.event('e1')])
        h = self.adapter.ingest('ns', [self.event('e2')])
        self.assertEqual(h['accepted'], 1)
        self.assertEqual([m for m, _ in self.order].count('add_batch'), 1)
        self.assertEqual(self.adapter.event_results['e2']['state'], 'NotDispatched')
        self.assertTrue(self.adapter.ready('ns', h, None)['visible'])
        self.assertEqual([m for m, _ in self.order if m in ('add_batch', 'wait_operation')],
                         ['add_batch', 'wait_operation', 'wait_operation', 'add_batch'])

    def test_recovery_succeeds_with_same_operation_and_finite_backoff(self):
        self.scheduled(['Failed'], resume_errors=1)
        h = self.adapter.ingest('ns', [self.event('e1')])
        self.assertTrue(self.adapter.ready('ns', h, None)['visible'])
        self.assertEqual(self.sleeps, [30, 60])
        recoveries = [d for m, d in self.order if m == 'resume']
        self.assertEqual([d['operation_id'] for d in recoveries], ['op-e1', 'op-e1'])
        self.assertTrue(all(d['scheduled'] for d in recoveries))
        self.assertEqual([m for m, _ in self.order].count('add_batch'), 1)
        self.assertEqual(self.traces('build')[-1]['resume_count'], 2)
        self.assertEqual(self.adapter.event_results['e1']['write']['snapshot'], 'snapshot-e1')

    def test_recovery_honors_largest_retry_after_in_operation(self):
        self.scheduled(['Failed'], failure={'kind': 'MODEL_RECOVERY_EXHAUSTED',
                       'model_failure': {'retryable': True, 'retry_after_ms': 58000}})
        original = self.adapter.native.call
        def call(method, **data):
            reply = original(method, **data)
            if method == 'add_batch':
                reply['events'][1]['error'] = {'kind': 'MODEL_RECOVERY_EXHAUSTED',
                    'model_failure': {'retryable': True, 'retry_after_ms': 89000}}
            return reply
        with patch.object(self.adapter.native, 'call', side_effect=call):
            h = self.adapter.ingest('ns', [self.event('e1'), self.event('e2')])
        self.assertTrue(self.adapter.ready('ns', h, None)['visible'])
        self.assertEqual(self.sleeps, [91])
        windows = self.traces('build')[-1]['wait_windows']
        resume = next(w for w in windows if w['method'] == 'resume')
        self.assertEqual(resume['retry_after_s'], 89)
        self.assertEqual(resume['backoff_s'], 91)
        self.assertTrue(all(w['retry_after_s'] is None for w in windows if w['method'] != 'resume'))

    def test_retry_after_exceeding_window_fails_without_sleep_or_resume(self):
        self.scheduled(['Failed'], failure={'kind': 'MODEL_RECOVERY_EXHAUSTED',
                       'model_failure': {'retryable': True, 'retry_after_ms': 58000}})
        self.adapter.config['request_timeout_s'] = 100  # 50 seconds after RPC headroom
        with self.assertRaises(module.NativeError) as raised:
            self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(raised.exception.details['kind'], 'BATCH_NOT_READY')
        self.assertEqual(raised.exception.details['operation_ids'], ['op-e1'])
        self.assertEqual(self.sleeps, [])
        self.assertEqual(self.now, 0)
        self.assertNotIn('resume', [m for m, _ in self.order])

    def test_retry_delay_is_capped_by_max_wait_and_remaining_window(self):
        self.scheduled(['Failed'], failure={'kind': 'MODEL_RECOVERY_EXHAUSTED',
                       'model_failure': {'retryable': True, 'retry_after_ms': 1000}})
        self.adapter.config['request_timeout_s'] = 100
        self.adapter.recovery.update(operation_resume_initial_s=500, operation_resume_max_wait_s=40)
        self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(self.sleeps, [39])  # min(500, 40, remaining 50 - 11)
        resume = next(w for w in self.adapter.wait_windows['op-e1'] if w['method'] == 'resume')
        self.assertEqual(resume['retry_after_s'], 1)
        self.assertEqual(resume['window_s'], 1)

    def test_resume_sleep_overshoot_defers_without_spending_attempt(self):
        self.scheduled(['Failed'])
        self.adapter.config['request_timeout_s'] = 61.01  # ~11 seconds of native window remain
        def oversleep(seconds):
            self.sleeps.append(seconds)
            self.now += seconds + 0.02
        with patch.object(module.time, 'sleep', side_effect=oversleep):
            h = self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(h['accepted'], 1)
        self.assertEqual(self.adapter.resume_counts, {})
        self.assertNotIn('resume', [m for m, _ in self.order])
        self.assertEqual(self.adapter.event_results['e1']['state'], 'Failed')
        self.assertEqual([w['method'] for w in self.adapter.wait_windows['op-e1']], ['add_batch'])
        self.adapter.config['request_timeout_s'] = 1800
        self.assertTrue(self.adapter.ready('ns', h, None)['visible'])
        self.assertEqual(self.adapter.resume_counts, {'op-e1': 1})
        self.assertTrue(all(1 <= d['timeout_seconds'] <= 1740 for _, d in self.order if 'timeout_seconds' in d))

    def test_scheduled_methods_share_native_timeout_clamp(self):
        self.scheduled(['Pending', 'Failed'])
        self.adapter.config['batch_timeout_seconds'] = 3
        h = self.adapter.ingest('ns', [self.event('e1')])
        self.assertTrue(self.adapter.ready('ns', h, None)['visible'])
        calls = [(m, d) for m, d in self.order if 'timeout_seconds' in d]
        self.assertEqual({m for m, _ in calls}, {'add_batch', 'wait_operation', 'resume'})
        self.assertTrue(all(d['timeout_seconds'] == 3 for _, d in calls))

    def test_recovery_exhaustion_preserves_native_error_and_does_not_reappend(self):
        self.scheduled(['Failed'], resume_errors=99)
        with self.assertRaises(module.NativeError) as raised:
            self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(raised.exception.details['kind'], 'BATCH_NOT_READY')
        self.assertEqual(raised.exception.details['operation_ids'], ['op-e1'])
        self.assertEqual(raised.exception.details['events'][0]['error']['kind'], 'MODEL_RECOVERY_EXHAUSTED')
        self.assertEqual(self.adapter.resume_counts, {'op-e1': 4})
        self.assertEqual([m for m, _ in self.order].count('resume'), 4)
        self.assertEqual(self.sleeps, [30, 60, 120, 240])
        self.assertEqual(self.now, 450)
        with self.assertRaises(module.NativeError):
            self.adapter.ready('ns', {}, None)
        self.assertEqual([m for m, _ in self.order].count('resume'), 4)
        self.assertEqual(self.now, 450)  # repeated ready must not idle to a new deadline

    def test_ready_exhaustion_fails_without_waiting_out_deadline(self):
        self.scheduled(['Pending', 'Failed'], resume_errors=99)
        h = self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual(h['accepted'], 1)
        start = self.now
        with self.assertRaises(module.NativeError) as raised:
            self.adapter.ready('ns', h, None)
        self.assertEqual(self.now - start, 450)
        self.assertEqual(self.sleeps, [30, 60, 120, 240])
        self.assertEqual(raised.exception.details['operation_ids'], ['op-e1'])
        self.assertEqual(raised.exception.details['resume_counts'], {'op-e1': 4})
        self.assertEqual([m for m, _ in self.order].count('add_batch'), 1)

    def test_exhausted_failure_with_other_pending_work_keeps_barrier(self):
        self.scheduled(['Failed'], resume_errors=99)
        with self.assertRaises(module.NativeError):
            self.adapter.ingest('ns', [self.event('e1')])
        self.adapter.event_results['e2'] = {'event_id': 'e2', 'state': 'Pending'}
        before = len(self.order)
        self.assertFalse(self.adapter._advance_leaf(self.now + 100))
        self.assertEqual(len(self.order), before)

    def test_exhausted_failure_with_not_dispatched_work_fails_immediately(self):
        self.scheduled(['Failed'], resume_errors=99)
        with self.assertRaises(module.NativeError):
            self.adapter.ingest('ns', [self.event('e1')])
        for ident in ('e2', 'e3', 'e4'):
            self.adapter.event_results[ident] = {'event_id': ident, 'state': 'NotDispatched'}
        before, now, sleeps = len(self.order), self.now, list(self.sleeps)
        with self.assertRaises(module.NativeError) as raised:
            self.adapter._advance_leaf(self.now + 1750)
        self.assertEqual(raised.exception.details['kind'], 'BATCH_NOT_READY')
        self.assertEqual(raised.exception.details['operation_ids'], ['op-e1'])
        self.assertEqual([r['state'] for r in raised.exception.details['events']],
                         ['Failed', 'NotDispatched', 'NotDispatched', 'NotDispatched'])
        self.assertEqual(len(self.order), before)
        self.assertEqual(self.now, now)
        self.assertEqual(self.sleeps, sleeps)

    def test_deterministic_failure_never_calls_resume(self):
        self.scheduled(['Failed'], failure={'kind': 'SUMMARY_UNCOMPRESSIBLE'})
        with self.assertRaises(module.NativeError):
            self.adapter.ingest('ns', [self.event('e1')])
        self.assertEqual([m for m, _ in self.order].count('resume'), 0)

    def test_ready_deadline_has_operation_id_and_does_not_report_visible(self):
        self.scheduled(['Pending', 'Pending'])
        h = self.adapter.ingest('ns', [self.event('e1')])
        with self.assertRaises(module.NativeError) as raised:
            self.adapter.ready('ns', h, None)
        self.assertEqual(raised.exception.details['operation_ids'], ['op-e1'])
        before = len(self.order)
        with self.assertRaises(module.NativeError):
            self.adapter.ready('ns', h, '2000-01-01T00:00:00Z')
        self.assertEqual(len(self.order), before)

    def test_recovery_configuration_and_manifest_episode_budget(self):
        manifest = self.leaf()
        self.assertEqual(manifest['sandbox']['limits']['timeout_s'], 14400)
        self.assertEqual(self.adapter.recovery, {'operation_resume_max_attempts': 4,
                         'operation_resume_initial_s': 30, 'operation_resume_max_wait_s': 300})
        self.assertEqual(json.loads(Path(__file__).with_name('manifest.json').read_text())['sandbox']['limits']['timeout_s'], 3600)
        for value in (-1, True, 1.5):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'invalid operation_resume_max_attempts'):
                module.Adapter(dict(manifest['bridge']['config'], operation_resume_max_attempts=value))
        self.adapter.recovery['operation_resume_max_attempts'] = 0
        self.assertEqual(self.adapter.describe()['config']['operation_resume_max_attempts'], 0)


class TreeScheduledWriteTests(unittest.TestCase):
    setUp = AdapterTests.setUp
    event = AdapterTests.event
    request = AdapterTests.request
    traces = AdapterTests.traces
    leaf = TreeTests.leaf
    scheduled = ScheduledWriteTests.scheduled
    test_scheduled_window_waits_before_append = ScheduledWriteTests.test_scheduled_window_expires_next_ingest_waits_before_append
    test_ready_barrier_continues_same_operation = ScheduledWriteTests.test_ready_barrier_continues_same_operation_and_emits_windows
    test_recovery_preserves_operation_and_snapshot = ScheduledWriteTests.test_recovery_succeeds_with_same_operation_and_finite_backoff


class NativeTransportTests(unittest.TestCase):
    def test_stalled_fake_process_times_out_without_blocking_readline(self):
        # This fixture is a stdlib pipe process, not the SUT or SDK.
        native = module.Native.__new__(module.Native)
        native.process = subprocess.Popen([sys.executable, '-c',
            'import sys,time; sys.stdin.buffer.readline(); time.sleep(0.3)'],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
        self.addCleanup(native.process.stderr.close)
        os.set_blocking(native.process.stdin.fileno(), False)
        os.set_blocking(native.process.stdout.fileno(), False)
        start = time.monotonic()
        with self.assertRaises(TimeoutError):
            native.call('add_batch', timeout=0.05)
        self.assertLess(time.monotonic() - start, 1)
        native.close()
        self.assertIsNotNone(native.process.poll())

    def test_close_deadline_covers_rpc_then_escalates_and_reaps(self):
        from unittest.mock import Mock
        native = module.Native.__new__(module.Native)
        process = Mock()
        process.poll.return_value = None
        process.wait.side_effect = [subprocess.TimeoutExpired('fake', 5), 0]
        native.process = process
        with patch.object(native, 'call', side_effect=TimeoutError('synthetic')) as call:
            native.close()
        self.assertEqual(call.call_args.kwargs['timeout'], 30)
        process.terminate.assert_called_once()
        process.kill.assert_called_once()
        for invocation in process.wait.call_args_list:
            self.assertGreaterEqual(invocation.kwargs['timeout'], 0)
            self.assertLessEqual(invocation.kwargs['timeout'], 45)
        process.stdin.close.assert_called_once()
        process.stdout.close.assert_called_once()

    def test_structured_native_error_survives_transport(self):
        native = module.Native.__new__(module.Native)
        reply = json.dumps({'error': {'kind': 'MODEL_REQUEST_REJECTED', 'operation': 'op-1'}})
        native.process = subprocess.Popen([sys.executable, '-c',
            'import sys; sys.stdin.buffer.readline(); print(' + repr(reply) + ')'],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
        self.addCleanup(native.process.stderr.close)
        os.set_blocking(native.process.stdin.fileno(), False)
        os.set_blocking(native.process.stdout.fileno(), False)
        with self.assertRaises(module.NativeError) as caught:
            native.call('resume', timeout=1, operation_id='op-1')
        self.assertEqual(caught.exception.details['operation'], 'op-1')
        native.close()


if __name__ == '__main__':
    unittest.main()
