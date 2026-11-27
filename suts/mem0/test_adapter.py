"""Offline Mem0 contracts with a synthetic SDK; never import the real project."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'suts/_bridge/python'))
spec = importlib.util.spec_from_file_location('mem0_adapter', Path(__file__).with_name('adapter.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FakeEmbedding:
    def __init__(self):
        self.calls = []
        self.max_chars = None
        self.error = None
        self.oversize_message = "Invalid 'input': maximum context length is 8192 tokens."

    def evaluate(self, texts):
        if self.error is not None:
            raise self.error
        if self.max_chars is not None and any(len(text) > self.max_chars for text in texts):
            raise ValueError(self.oversize_message)
        return [[len(text)] for text in texts]

    def embed(self, text, memory_action=None, **kwargs):
        self.calls.append(('embed', text, memory_action, kwargs))
        return self.evaluate([text])[0]

    def embed_batch(self, texts, memory_action=None, **kwargs):
        self.calls.append(('embed_batch', list(texts), memory_action, kwargs))
        return self.evaluate(texts)


class FakeMemory:
    instances = []

    @classmethod
    def from_config(cls, settings):
        memory = cls()
        memory.settings = copy.deepcopy(settings)
        memory.embedding_model = FakeEmbedding()
        memory.data = {}
        cls.instances.append(memory)
        return memory

    def delete_all(self, user_id):
        self.data[user_id] = []

    def add(self, messages, user_id, run_id, metadata, infer):
        text = messages[0]['content']
        self.embedding_model.embed(text, 'add')
        row = {'id': metadata['source_ids'][0], 'memory': text, 'metadata': metadata}
        self.data[user_id].append(row)
        return {'results': [row]}

    def search(self, query, top_k, filters):
        self.embedding_model.embed(query, 'search')
        return {'results': self.data[filters['user_id']][:top_k]}

    def get_all(self, filters, top_k):
        return {'results': self.data[filters['user_id']][:top_k]}


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='memeval-mem0-')
        self.addCleanup(self.temp.cleanup)
        env = patch.dict(os.environ, {'EP': self.temp.name}, clear=True)
        env.start()
        self.addCleanup(env.stop)
        sdk = patch.dict(sys.modules, {'mem0': types.SimpleNamespace(Memory=FakeMemory)})
        sdk.start()
        self.addCleanup(sdk.stop)
        FakeMemory.instances = []

    def adapter(self, config=None, reset=True):
        adapter = module.Adapter(config or {})
        self.addCleanup(adapter.close)
        if reset:
            adapter.reset('ns')
        return adapter

    def remote(self, **overrides):
        embedder = {'provider': 'openai', 'model': 'text-embedding-3-large', 'dims': 3072,
                    'base_url': 'https://embedding.invalid/v1', 'api_key_env': 'EMBED_TEST_KEY',
                    'max_input_chars': 32768}
        embedder.update(overrides)
        return {'infer': False, 'embedder': embedder}

    def llm_env(self):
        return {'MEM0_LLM_MODEL': 'test-model', 'MEM0_LLM_REASONING_EFFORT': 'low',
                'OPENAI_BASE_URL': 'http://gateway.invalid/v1', 'OPENAI_API_KEY': 'fake-llm-key'}

    def test_local_settings_preserved_except_explicit_raw_llm_placeholder(self):
        models = str(Path(module.__file__).resolve().parents[1] / 'models')
        for infer in (False, True):
            with self.subTest(infer=infer), patch.dict(os.environ, self.llm_env() if infer else {}):
                a = self.adapter({'infer': infer})
                expected = {'vector_store': {'provider': 'qdrant', 'config': {'collection_name': 'memeval',
                            'path': str(Path(self.temp.name) / 'qdrant'), 'embedding_model_dims': 384}},
                            'history_db_path': str(Path(self.temp.name) / 'history.sqlite'),
                            'embedder': {'provider': 'fastembed', 'config': {'model': 'BAAI/bge-small-en-v1.5',
                                'model_kwargs': {'cache_dir': models, 'local_files_only': True}}}}
                if infer:
                    expected['llm'] = {'provider': 'openai', 'config': {'model': 'test-model',
                        'openai_base_url': 'http://gateway.invalid/v1', 'api_key': 'fake-llm-key',
                        'reasoning_effort': 'low', 'is_reasoning_model': True}}
                else:
                    expected['llm'] = {'provider': 'openai', 'config': {'api_key': 'memeval-placeholder-no-llm'}}
                    self.assertEqual(os.environ['OPENAI_API_KEY'], 'memeval-placeholder-no-llm')
                self.assertEqual(a.memory.settings, expected)

    def test_openai_configuration_uses_named_key_and_matching_dimensions(self):
        os.environ.update(EMBED_TEST_KEY='fake-embedding-key', OPENAI_API_KEY='fake-unrelated-key')
        a = self.adapter(self.remote(model='other-model', dims=1536))
        settings = a.memory.settings
        self.assertEqual(settings['embedder'], {'provider': 'openai', 'config': {'model': 'other-model',
            'embedding_dims': 1536, 'api_key': 'fake-embedding-key', 'openai_base_url': 'https://embedding.invalid/v1'}})
        self.assertEqual(settings['vector_store']['config']['embedding_model_dims'], 1536)
        self.assertEqual(settings['llm'], {'provider': 'openai', 'config': {'api_key': 'memeval-placeholder-no-llm'}})

    def test_missing_or_empty_key_fails_before_placeholder_or_sdk(self):
        for value in (None, '', '  '):
            for key in ('OPENAI_API_KEY', 'EMBED_TEST_KEY'):
                with self.subTest(value=value, key=key), patch.dict(os.environ, {'EP': self.temp.name}, clear=True):
                    if value is not None:
                        os.environ[key] = value
                    a = self.adapter(self.remote(api_key_env=key), reset=False)
                    a.describe()  # Offline describe needs no credential or client.
                    with self.assertRaisesRegex(ValueError, '需要 manifest env_passthrough 透传 ' + key):
                        a.reset('ns')
                    self.assertFalse(FakeMemory.instances)
                    self.assertNotEqual(os.environ.get(key), 'memeval-placeholder-no-llm')

    def test_raw_openai_key_is_never_given_to_llm_client(self):
        os.environ['OPENAI_API_KEY'] = 'fake-embedding-only-key'
        a = self.adapter(self.remote(api_key_env='OPENAI_API_KEY'))
        self.assertEqual(a.memory.settings['embedder']['config']['api_key'], 'fake-embedding-only-key')
        self.assertEqual(a.memory.settings['llm']['config'], {'api_key': 'memeval-placeholder-no-llm'})
        self.assertEqual(os.environ['OPENAI_API_KEY'], 'fake-embedding-only-key')

    def test_cap_changes_only_embedding_input_not_memory_or_citations(self):
        os.environ['EMBED_TEST_KEY'] = 'fake-key'
        a = self.adapter(self.remote(max_input_chars=4))
        text = '中🙂abcdef'
        a.ingest('ns', [{'id': 'e1', 'role': 'user', 'content': text, 'session_id': 's'}])
        inner = a.memory.embedding_model.inner
        self.assertEqual(inner.calls, [('embed', text[:4], 'add', {})])
        self.assertEqual(a.memory.data['ns'][0]['memory'], text)
        result = a.retrieve('ns', {'query': 'ok', 'budget': {'top_k': 1, 'max_context_chars': 100}})
        self.assertEqual(inner.calls[-1], ('embed', 'ok', 'search', {}))
        self.assertEqual(result['items'][0]['text'], text)
        self.assertEqual(result['items'][0]['source_ids'], ['e1'])
        dump = a.dump('ns')
        self.assertEqual(dump['items'][0]['text'], text)
        self.assertEqual(dump['embed_stats'], {'capped': 1, 'halved': 0})
        self.assertIn(module.EMBEDDING_LIMITATION, dump['limitations'])

    def test_default_embedder_does_not_cap_long_text(self):
        a = self.adapter()
        text = 'x' * 40000
        self.assertEqual(a.memory.embedding_model.embed(text, 'add'), [len(text)])
        self.assertEqual(a.memory.embedding_model.inner.calls[0][1], text)
        self.assertEqual(a.embed_stats, {'capped': 0, 'halved': 0})

    def test_batch_cap_preserves_order_and_forwarded_arguments(self):
        os.environ['EMBED_TEST_KEY'] = 'fake-key'
        a = self.adapter(self.remote(max_input_chars=4))
        model = a.memory.embedding_model
        self.assertEqual(model.embed_batch(['🙂' * 7, 'ok'], memory_action='add', marker=1), [[4], [2]])
        self.assertEqual(model.inner.calls, [('embed_batch', ['🙂' * 4, 'ok'], 'add', {'marker': 1})])
        self.assertEqual(a.embed_stats, {'capped': 1, 'halved': 0})

    def test_context_error_halves_until_success(self):
        a = self.adapter()
        model = a.memory.embedding_model
        model.inner.max_chars = 700
        self.assertEqual(model.embed('x' * 2400, memory_action='search'), [600])
        self.assertEqual([len(call[1]) for call in model.inner.calls], [2400, 1200, 600])
        self.assertEqual(a.embed_stats, {'capped': 0, 'halved': 2})

    def test_batch_context_error_does_not_shorten_valid_neighbours(self):
        a = self.adapter()
        model = a.memory.embedding_model
        model.inner.max_chars = 700
        self.assertEqual(model.embed_batch(['x' * 2400, 'y' * 600, 'z'], 'add'), [[600], [600], [1]])
        calls = model.inner.calls
        self.assertEqual([list(map(len, call[1])) for call in calls], [[2400, 600, 1], [2400], [1200], [600], [600], [1]])
        self.assertTrue(all(call[2] == 'add' for call in calls))
        self.assertEqual(a.embed_stats, {'capped': 0, 'halved': 2})

    def test_context_error_stops_below_512_and_preserves_error(self):
        for method, value in (('embed', 'x' * 2048), ('embed_batch', ['x' * 2048])):
            with self.subTest(method=method):
                a = self.adapter()
                model = a.memory.embedding_model
                error = ValueError('MAXIMUM CONTEXT LENGTH rejected')
                model.inner.error = error
                with self.assertRaises(ValueError) as raised:
                    getattr(model, method)(value, 'add')
                self.assertIs(raised.exception, error)
                self.assertEqual(len(model.inner.calls), 4)
                self.assertEqual(a.embed_stats, {'capped': 0, 'halved': 3})

    def test_oversize_predicate_is_case_insensitive_and_not_generic_400(self):
        for message in ('maximum context length', 'MAXIMUM INPUT LENGTH',
                        'maximum\t input\nlength', 'Too Many Tokens'):
            with self.subTest(message=message):
                self.assertTrue(module.is_oversize_error(ValueError(message)))
        for message in ('Error code: 400', 'BadRequestError', "Error code: 400 - Invalid 'model'",
                        'context length option unsupported', 'maximum output length', 'input invalid'):
            with self.subTest(message=message):
                self.assertFalse(module.is_oversize_error(ValueError(message)))

    def test_input_length_and_token_errors_cap_halve_and_preserve_batch_neighbours(self):
        real_message = ('Error code: 400 - {\'error\': {\'message\': "Invalid \'input[0]\': maximum input length is 8192 tokens.", '
                        "'type': 'invalid_request_error', 'param': None, 'code': None}}")
        os.environ['EMBED_TEST_KEY'] = 'fake-key'
        for message in (real_message, 'maximum context length is 8192 tokens.', 'TOO MANY TOKENS'):
            for method in ('embed', 'embed_batch'):
                with self.subTest(message=message, method=method):
                    a = self.adapter(self.remote(max_input_chars=2400))
                    model = a.memory.embedding_model
                    model.inner.max_chars, model.inner.oversize_message = 700, message
                    if method == 'embed':
                        self.assertEqual(model.embed('x' * 4800, 'add'), [600])
                        self.assertEqual([len(c[1]) for c in model.inner.calls], [2400, 1200, 600])
                    else:
                        self.assertEqual(model.embed_batch(['x' * 4800, 'y' * 600], 'add'), [[600], [600]])
                        self.assertEqual([list(map(len, c[1])) for c in model.inner.calls],
                                         [[2400, 600], [2400], [1200], [600], [600]])
                    self.assertEqual(a.embed_stats, {'capped': 1, 'halved': 2})

    def test_other_400_errors_are_not_retried_or_isolated(self):
        class BadRequestError(Exception):
            pass
        for method, value in (('embed', 'x' * 2400), ('embed_batch', ['x' * 2400, 'y'])):
            with self.subTest(method=method):
                a = self.adapter()
                error = BadRequestError("Error code: 400 - Invalid 'model'")
                a.memory.embedding_model.inner.error = error
                with self.assertRaises(BadRequestError) as raised:
                    getattr(a.memory.embedding_model, method)(value)
                self.assertIs(raised.exception, error)
                self.assertEqual(len(a.memory.embedding_model.inner.calls), 1)
                self.assertEqual(a.embed_stats, {'capped': 0, 'halved': 0})

    def test_other_errors_are_not_retried(self):
        for method, value in (('embed', 'x' * 2048), ('embed_batch', ['x' * 2048, 'y'])):
            with self.subTest(method=method):
                a = self.adapter()
                error = RuntimeError('authentication unavailable')
                a.memory.embedding_model.inner.error = error
                with self.assertRaises(RuntimeError) as raised:
                    getattr(a.memory.embedding_model, method)(value)
                self.assertIs(raised.exception, error)
                self.assertEqual(len(a.memory.embedding_model.inner.calls), 1)
                self.assertEqual(a.embed_stats['halved'], 0)

    def test_stats_persist_across_namespace_reset(self):
        a = self.adapter({'embedder': {'provider': 'fastembed', 'model': 'local-model', 'dims': 384, 'max_input_chars': 2}})
        model = a.memory.embedding_model
        model.embed('long')
        a.reset('next')
        model.embed('again')
        self.assertIs(a.memory.embedding_model, model)
        self.assertEqual(a.embed_stats, {'capped': 2, 'halved': 0})

    def test_describe_uses_actual_embedder_and_never_exposes_keys(self):
        a = self.adapter(reset=False)
        self.assertEqual(a.describe()['config']['embedder'], {'provider': 'fastembed', 'model': 'BAAI/bge-small-en-v1.5',
            'dims': 384, 'local': True, 'max_input_chars': None})
        self.assertEqual(a.describe()['config']['llm_notes'], [])
        os.environ.update(self.llm_env(), EMBED_TEST_KEY='fake-embedding-secret')
        a = self.adapter({'infer': True}, reset=False)
        self.assertEqual(a.describe()['config']['llm_notes'], [
            'reasoning_effort forwarded via mem0 OpenAIConfig.reasoning_effort (is_reasoning_model=True); embeddings local fastembed'])
        a = self.adapter(dict(self.remote(), infer=True), reset=False)
        description = a.describe()
        self.assertEqual(description['config']['embedder'], {'provider': 'openai', 'model': 'text-embedding-3-large',
            'dims': 3072, 'local': False, 'max_input_chars': 32768})
        self.assertIn('embeddings remote openai text-embedding-3-large', description['config']['llm_notes'][0])
        self.assertNotIn('fake-embedding-secret', json.dumps(description))
        self.assertIn(module.EMBEDDING_LIMITATION, description['limitations'])
        self.assertFalse(FakeMemory.instances)

    def test_invalid_embedder_configuration(self):
        for override in ({'provider': 'unknown'}, {'model': ''}, {'dims': 0}, {'dims': True},
                         {'max_input_chars': 0}, {'max_input_chars': 1.5}, {'max_input_chars': True},
                         {'base_url': ''}, {'api_key_env': ''}):
            with self.subTest(override=override), self.assertRaisesRegex(ValueError, 'mem0 embedder'):
                self.adapter(self.remote(**override), reset=False)

    def test_openai_manifest_schema_shape_and_configuration_agree(self):
        manifest = json.loads(Path(__file__).with_name('manifest-openai-embed.json').read_text())
        baseline = json.loads(Path(__file__).with_name('manifest.json').read_text())
        schema = json.loads((ROOT / 'schemas/sut-manifest.v3.json').read_text())['$defs']['Manifest']
        self.assertEqual(manifest['schema_version'], '3')
        self.assertTrue(set(schema['required']) <= {'config': {}, **manifest}.keys())
        self.assertTrue(manifest.keys() <= schema['properties'].keys())
        self.assertEqual(manifest['name'], 'mem0-openai-embed')
        self.assertEqual(manifest['variant'], 'raw-openai-embed')
        self.assertFalse(manifest['bridge']['config']['infer'])
        self.assertEqual(manifest['adapter']['request_timeout_s'], 1800)
        self.assertEqual(manifest['sandbox'], {'backend': 'container', 'network': 'open', 'limits': {'timeout_s': 3600}})
        config = manifest['bridge']['config']['embedder']
        self.assertEqual(config, {'provider': 'openai', 'model': 'text-embedding-3-large', 'dims': 3072,
            'base_url': 'https://api.openai.com/v1', 'api_key_env': 'OPENAI_API_KEY', 'max_input_chars': 32768})
        self.assertEqual(manifest['process']['env_passthrough'], [config['api_key_env']])
        self.assertEqual(manifest['declared']['embedding'], {'provider': 'OpenAI direct', 'model': config['model'],
            'dimensions': config['dims'], 'local': False})
        self.assertEqual(manifest['llm'], {'required': False})
        self.assertEqual(manifest['telemetry_env'], baseline['telemetry_env'])
        self.assertEqual(manifest['runtime'], baseline['runtime'])
        self.assertEqual(manifest['ready'], baseline['ready'])


if __name__ == '__main__':
    unittest.main()
