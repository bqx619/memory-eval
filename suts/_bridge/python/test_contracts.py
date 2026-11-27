"""Synthetic SDK contract fixtures, not evidence of real upstream compatibility."""
import importlib.util
import os
from pathlib import Path
import sys
import types
import unittest
from datetime import datetime, timezone
from memeval_bridge import Dispatcher
from unittest.mock import patch
import asyncio
import copy
import io
import json
from contextlib import redirect_stderr

ROOT = Path(__file__).resolve().parents[2]


def load(project):
    spec = importlib.util.spec_from_file_location(project + '_adapter', ROOT / project / 'adapter.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.Adapter


def module(name, **values):
    sys.modules[name] = types.SimpleNamespace(**values)


class Memory:
    config = None
    @classmethod
    def from_config(cls, config):
        assert os.environ.get("FASTEMBED_CACHE_PATH", "").endswith("/models")
        assert os.environ.get("OPENAI_API_KEY")
        assert config["vector_store"]["config"]["embedding_model_dims"] == 384
        cls.config = config
        return cls()
    def __init__(self):
        self.data = {}
    def delete_all(self, *, user_id):
        self.data[user_id] = []
    def add(self, messages, *, user_id, run_id, metadata, infer):
        assert isinstance(infer, bool) and run_id
        row = {'id': metadata['source_ids'][0], 'memory': messages[0]['content'], 'metadata': metadata}
        self.data[user_id].append(row)
        return {'results': [row]}
    def get_all(self, *, filters, top_k):
        return {'results': self.data[filters['user_id']][:top_k]}
    def search(self, query, *, top_k, filters):
        return {'results': self.data[filters['user_id']][:top_k]}


class Graph:
    def __init__(self, *, graph_driver, llm_client, embedder, cross_encoder):
        assert isinstance(llm_client, GenericClient)
        assert embedder and cross_encoder
        self.driver = graph_driver
        self.driver.rows = []
    async def build_indices_and_constraints(self):
        pass
    async def add_episode(self, *, name, episode_body, source_description, reference_time, source, group_id):
        assert isinstance(reference_time, datetime) and reference_time.tzinfo
        row = types.SimpleNamespace(uuid=name, content=episode_body, valid_at=reference_time)
        self.driver.rows.append(row)
        return types.SimpleNamespace(episode=row)
    async def search(self, query, *, group_ids, num_results):
        return [types.SimpleNamespace(uuid=r.uuid, fact=r.content, episodes=[r.uuid]) for r in self.driver.rows[:num_results]]
    async def close(self):
        pass


class FakeOpenAI:
    calls = []
    def __init__(self, *, base_url, api_key):
        assert base_url == 'http://llm-gateway:8317/v1' and api_key == 'fixture-no-network'
        self.chat = types.SimpleNamespace(completions=self)
    async def create(self, **kwargs):
        self.calls.append(kwargs)
        return {'ok': True}


class GenericClient:
    def __init__(self, *, config, client):
        assert config.model == config.small_model == 'fixture-model'
        self.client = client


class Embedding:
    def __init__(self, *, model_name, cache_dir, local_files_only):
        assert model_name == 'BAAI/bge-small-en-v1.5' and cache_dir.endswith('/models')
        assert local_files_only is True
    def embed(self, texts):
        return [[0.25]*384 for _ in texts]


class DB:
    def __init__(self, *, dbfilename):
        assert Path(dbfilename).parent == Path(os.environ['EP'])


class SyncDB:
    def __init__(self, *, dbfilename, unix_socket_path):
        assert Path(dbfilename).parent == Path(os.environ['EP'])
        assert Path(unix_socket_path) == Path(os.environ['EP']) / 'r.sock'


class Driver:
    def __init__(self, *, falkor_db):
        self.db = falkor_db


async def clear(driver, *, group_ids):
    driver.rows = []


class Episodes:
    @staticmethod
    async def get_by_group_ids(driver, groups):
        return driver.rows


class Entities:
    @staticmethod
    async def get_by_group_ids(driver, groups):
        return [types.SimpleNamespace(uuid='entity', summary='summary')] if driver.rows else []


async def empty_edges(driver, groups):
    return []


class Contracts(unittest.TestCase):
    def setUp(self):
        module('mem0', Memory=Memory)
        module('graphiti_core', Graphiti=Graph)
        sys.modules.pop('falkordblite', None)
        module('redislite', AsyncFalkorDB=DB, FalkorDB=SyncDB)
        module('httpx')
        os.environ.update(OPENAI_API_KEY='fixture-no-network', OPENAI_BASE_URL='http://llm-gateway:8317/v1',
                          MEM0_LLM_MODEL='fixture-model', MEM0_LLM_REASONING_EFFORT='low',
                          GRAPHITI_LLM_MODEL='fixture-model', GRAPHITI_LLM_REASONING_EFFORT='low',
                          FASTEMBED_CACHE_PATH='/image/models')
        module('openai', AsyncOpenAI=FakeOpenAI)
        module('fastembed', TextEmbedding=Embedding)
        module('graphiti_core.llm_client.config', LLMConfig=types.SimpleNamespace)
        module('graphiti_core.llm_client.openai_generic_client', OpenAIGenericClient=GenericClient)
        module('graphiti_core.embedder.client', EmbedderClient=object)
        module('graphiti_core.cross_encoder.client', CrossEncoderClient=object)
        module('graphiti_core.driver.falkordb_driver', FalkorDriver=Driver)
        module('graphiti_core.edges', EntityEdge=types.SimpleNamespace(get_by_group_ids=empty_edges))
        module('graphiti_core.nodes', EpisodeType=types.SimpleNamespace(message='message'), EpisodicNode=Episodes, EntityNode=Entities)
        module('graphiti_core.utils.maintenance.graph_data_operations', clear_data=clear)

    def test_contracts(self):
        for project, config in [('mem0', {'infer': False}), ('mem0', {'infer': True}), ('graphiti', {})]:
            with self.subTest(project=project, config=config):
                d = Dispatcher(load(project)(config))
                description = d.call('describe', {})
                self.assertIn('dump', description['capabilities'])
                expected_notes = []
                if project == 'graphiti':
                    expected_notes = ['reasoning_effort added to chat/completions by adapter wrapper; response_format schema normalised to OpenAI strict for the gateway; embeddings local fastembed; reranker disabled']
                    self.assertTrue(any('optional fields coerced to nullable for strict schema' in text for text in description['limitations']))
                elif config['infer']:
                    expected_notes = ['reasoning_effort forwarded via mem0 OpenAIConfig.reasoning_effort (is_reasoning_model=True); embeddings local fastembed']
                self.assertEqual(description['config']['llm_notes'], expected_notes)
                d.call('reset', {'ns': 'ns'})
                events = [{'id': str(i), 'content': 'text '+str(i), 'role': 'user', 'session_id': 's1', 'timestamp': '2020-01-02T03:04:05Z'} for i in range(300)]
                h = d.call('ingest', {'ns': 'ns', 'events': events})
                self.assertEqual(h['accepted'], 300)
                self.assertTrue(d.call('ready', {'ns': 'ns', 'handle': h})['visible'])
                req = {'query': 'text', 'scope': {'owner': 'anon'}, 'as_of': '2020-01-01T00:00:00Z', 'budget': {'top_k': 5, 'max_context_chars': 1000}}
                found = d.call('retrieve', {'ns': 'ns', 'request': req})['items']
                self.assertEqual([r['source_ids'] for r in found], [[str(i)] for i in range(5)])
                dump = d.call('dump', {'ns': 'ns'})['items']
                self.assertEqual(len(dump), 301 if project == 'graphiti' else 300)
                self.assertEqual(dump, d.call('dump', {'ns': 'ns'})['items'])
                self.assertTrue(any('2020-01-02T03:04:05' in i.get('timestamp', '') for i in dump))
                if project == 'mem0':
                    self.assertTrue(Memory.config['vector_store']['config']['path'].startswith(os.environ['EP']))
                    if config['infer']:
                        self.assertEqual(Memory.config['llm']['config'], {'model':'fixture-model',
                            'openai_base_url':'http://llm-gateway:8317/v1', 'api_key':'fixture-no-network',
                            'reasoning_effort':'low', 'is_reasoning_model':True})
                    self.assertIn('httpx==0.28.1', (ROOT/'graphiti/requirements.in').read_text())
                    d.adapter.memory.data['ns'][0]['metadata']['source_ids'] = ['private-unknown']
                    d.adapter.sources.clear()
                else:
                    d.adapter.sources.clear()
                if project == 'mem0':
                    with self.assertRaisesRegex(ValueError, 'invalid source_ids'):
                        d.call('retrieve', {'ns': 'ns', 'request': req})
                else:
                    bad = d.call('retrieve', {'ns': 'ns', 'request': req})['items'][0]
                    self.assertNotIn('status', bad)
                    self.assertEqual(bad['source_ids'], [])
                d.call('reset', {'ns': 'ns'})
                self.assertEqual(d.call('dump', {'ns': 'ns'})['items'], [])
                with self.assertRaises(ValueError):
                    d.call('dump', {'ns': 'other'})
                d.call('close', {})
                d.loop.close()

    def test_gateway_required_model(self):
        for project, config, key in [('mem0', {'infer': True}, 'MEM0_LLM_MODEL'), ('graphiti', {}, 'GRAPHITI_LLM_MODEL')]:
            with patch.dict(os.environ, {key: ''}):
                with self.assertRaisesRegex(ValueError, key): load(project)(config)

    def test_graphiti_chat_and_local_embedding(self):
        spec = importlib.util.spec_from_file_location('graphiti_gateway', ROOT/'graphiti/adapter.py')
        a = importlib.util.module_from_spec(spec); spec.loader.exec_module(a)
        llm, embedder, reranker = a.gateway_clients()
        async def check():
            with patch('socket.socket.connect', side_effect=AssertionError('embedding must be offline')):
                self.assertEqual(await embedder.create('text'), [0.25]*384)
                self.assertEqual(len(await embedder.create_batch(['a','b'])), 2)
                self.assertEqual(await reranker.rank('q', ['b','a']), [('b',1.0),('a',1.0)])
            await llm.client.chat.completions.create(model='fixture-model', messages=[], temperature=0, max_tokens=42)
            self.assertEqual(FakeOpenAI.calls[-1], dict(model='fixture-model', messages=[], reasoning_effort='low', max_completion_tokens=42))
        asyncio.run(check())

    def test_graphiti_prepare_fixture(self):
        import tempfile, json
        spec = importlib.util.spec_from_file_location('graphiti_prepare', ROOT/'graphiti/prepare_models.py')
        a = importlib.util.module_from_spec(spec); spec.loader.exec_module(a)
        with tempfile.TemporaryDirectory() as tmp:
            def embed(*, model_name, cache_dir):
                self.assertEqual(model_name, 'BAAI/bge-small-en-v1.5')
                (Path(cache_dir)/'weights').write_bytes(b'fixture')
            with patch.dict(sys.modules, {'fastembed': types.SimpleNamespace(TextEmbedding=embed)}):
                a.main(Path(tmp))
            self.assertEqual(list(json.loads((Path(tmp)/'model-assets.json').read_text())['sha256']), ['weights'])


class StrictSchemaContracts(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location('graphiti_strict', ROOT/'graphiti/adapter.py')
        self.adapter = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.adapter)

    def assert_strict_objects(self, schema):
        count = 0
        def visit(node):
            nonlocal count
            if isinstance(node, dict):
                kind = node.get('type')
                if kind == 'object' or isinstance(kind, list) and 'object' in kind or 'properties' in node:
                    count += 1
                    self.assertIs(node['additionalProperties'], False)
                    self.assertEqual(node['required'], list(node.get('properties', {})))
                # Schema containers: their keys may themselves be named "default" etc.
                for key in ('$defs', 'definitions', 'properties'):
                    for value in node.get(key, {}).values(): visit(value)
                for key in ('anyOf', 'oneOf', 'allOf', 'prefixItems'):
                    for value in node.get(key, []): visit(value)
                items = node.get('items')
                if isinstance(items, list):
                    for value in items: visit(value)
                else: visit(items)
                self.assertFalse(self.adapter.STRICT_UNSUPPORTED_KEYWORDS.intersection(node))
        visit(schema)
        self.assertGreater(count, 0)

    def test_real_graphiti_model_schemas(self):
        fixture = json.loads((Path(__file__).parent/'testdata/graphiti-0.31.1-schemas.json').read_text())
        self.assertEqual(fixture['provenance']['version'], '0.31.1')
        self.assertTrue({'ExtractedEntities','ExtractedEdges','NodeDuplicate'} <= fixture['schemas'].keys())
        for name, schema in fixture['schemas'].items():
            with self.subTest(model=name):
                before = copy.deepcopy(schema)
                normalized, removed = self.adapter.normalize_strict_schema(schema)
                self.assertEqual(schema, before)
                self.assert_strict_objects(normalized)
                again, extra = self.adapter.normalize_strict_schema(normalized)
                self.assertEqual(normalized, again)
                self.assertEqual(extra, ())
                self.assertEqual(removed, tuple(sorted(removed)))
        normalized, _ = self.adapter.normalize_strict_schema(fixture['schemas']['ExtractedEntities'])
        entity = normalized['$defs']['ExtractedEntity']
        self.assertEqual(entity['properties']['episode_indices']['anyOf'], [
            fixture['schemas']['ExtractedEntities']['$defs']['ExtractedEntity']['properties']['episode_indices'], {'type':'null'}])
        self.assertEqual(normalized['properties']['extracted_entities']['items'], {'$ref':'#/$defs/ExtractedEntity'})
        normalized, removed = self.adapter.normalize_strict_schema(fixture['schemas']['ExtractedEdges'])
        edge = normalized['$defs']['Edge']
        self.assertEqual(edge['properties']['valid_at']['anyOf'], [{'type':'string'},{'type':'null'}])
        self.assertEqual(removed, ('default',))

    def test_nested_schema_keywords_optional_fields_and_refs(self):
        obj = {'type':'object','properties':{'field':{'type':'string','minLength':1}},'additionalProperties':True}
        schema = {'type':'object','required':['required','nullable'], 'properties':{
            'required':{'type':'integer','minimum':0,'maximum':10,'multipleOf':2},
            'nullable':{'anyOf':[{'type':'string','format':'date-time'},{'type':'null'}],'default':None},
            'optional':{'type':'string','default':'secret-schema-value','pattern':'^[a-z]+$','maxLength':8},
            'format':{'type':'string'}, 'default':{'type':'string'},
            'array':{'type':'array','minItems':1,'maxItems':5,'uniqueItems':True,'items':copy.deepcopy(obj)},
            'union':{'oneOf':[copy.deepcopy(obj),{'type':'string'}]},
            'intersection':{'allOf':[copy.deepcopy(obj)]},
            'reference':{'$ref':'#/$defs/Linked'},
            'list_nullable':{'type':['string','null']},
            'nullable_object':{'type':['object','null'],'properties':{'k':{'type':'boolean'}}},
        }, '$defs':{'Linked':copy.deepcopy(obj)}, 'definitions':{'default':copy.deepcopy(obj)}}
        before = copy.deepcopy(schema)
        normalized, removed = self.adapter.normalize_strict_schema(schema)
        self.assertEqual(schema, before)
        self.assert_strict_objects(normalized)
        self.assertEqual(removed, ('default','format','maxItems','maxLength','maximum','minItems','minLength','minimum','multipleOf','pattern','uniqueItems'))
        self.assertEqual(normalized['properties']['required'], {'type':'integer'})
        self.assertEqual(normalized['properties']['optional'], {'anyOf':[{'type':'string'},{'type':'null'}]})
        self.assertEqual(normalized['properties']['list_nullable'], {'type':['string','null']})
        self.assertEqual(normalized['properties']['reference'], {'anyOf':[{'$ref':'#/$defs/Linked'},{'type':'null'}]})
        self.assertIn('format', normalized['properties'])
        self.assertIn('default', normalized['properties'])
        self.assertIn('default', normalized['definitions'])
        self.assertEqual(self.adapter.normalize_strict_schema(normalized), (normalized, ()))
        with self.assertRaisesRegex(ValueError, 'schema must be an object'):
            self.adapter.normalize_strict_schema([])

    def test_gateway_request_copy_strict_flag_and_warning_once(self):
        calls = []
        sentinel = object()
        async def create(**kwargs):
            calls.append(kwargs)
            return sentinel
        client = types.SimpleNamespace(chat=types.SimpleNamespace(completions=types.SimpleNamespace(create=create)))
        shim = self.adapter.GatewayCompletions(client, 'low')
        fixture = json.loads((Path(__file__).parent/'testdata/graphiti-0.31.1-schemas.json').read_text())
        requests = [{'type':'json_schema','json_schema':{'name':name,'strict':False,'schema':schema}}
                    for name, schema in fixture['schemas'].items()]
        before = copy.deepcopy(requests)
        async def exercise():
            for response_format in requests * 2:
                response = await shim.create(model='fixture-model', messages=[{'role':'user','content':'unchanged prompt'}],
                                            response_format=response_format, temperature=0, max_tokens=42)
                self.assertIs(response, sentinel)
                sent = calls[-1]
                self.assertIs(sent['response_format']['json_schema']['strict'], True)
                self.assert_strict_objects(sent['response_format']['json_schema']['schema'])
                self.assertEqual(sent['model'], 'fixture-model')
                self.assertEqual(sent['messages'], [{'role':'user','content':'unchanged prompt'}])
                self.assertEqual(sent['max_completion_tokens'], 42)
                self.assertEqual(sent['reasoning_effort'], 'low')
                self.assertNotIn('max_tokens', sent); self.assertNotIn('temperature', sent)
            await shim.create(response_format={'type':'json_object'})
            self.assertEqual(calls[-1]['response_format'], {'type':'json_object'})
            await shim.create()
            self.assertNotIn('response_format', calls[-1])
        stderr = io.StringIO()
        with redirect_stderr(stderr): asyncio.run(exercise())
        self.assertEqual(requests, before)
        self.assertEqual(stderr.getvalue().splitlines(), ['graphiti gateway strict schema: removed unsupported keywords: default'])


class GovernorErrors(unittest.TestCase):
    def test_sdk_wrapped_body_and_headers(self):
        from memeval_bridge.errors import error_data
        for reason in ('exhausted', 'budget'):
            payload = {'error': {'type': 'memeval_governor', 'reason': reason, 'retry_after_s': 60}}
            for body in (payload, json.dumps(payload), repr(payload)):
                e = RuntimeError('SDK failed: ' + str(body))
                e.body = body
                self.assertEqual(error_data(e)['governor'], reason)
                wrapper = RuntimeError('wrapped SDK error')
                wrapper.__cause__ = e
                self.assertEqual(error_data(wrapper)['governor'], reason)
            e = RuntimeError('503')
            e.response = types.SimpleNamespace(headers={'X-Memeval-Governor': reason})
            self.assertEqual(error_data(e)['governor'], reason)
        for text in ('503 exhausted budget', '{"error":{"type":"other","reason":"budget"}}',
                     '{"error":{"type":"memeval_governor","reason":"unknown"}}'):
            self.assertNotIn('governor', error_data(RuntimeError(text)))

    def test_http_retains_governor_response(self):
        from memeval_bridge.http import Adapter, HTTPError
        from memeval_bridge.errors import error_data
        a = Adapter.__new__(Adapter)
        a.config, a.headers, a.socket_path, a.address = {}, {}, None, ('api', 8888)
        for reason in ('exhausted', 'budget'):
            response = types.SimpleNamespace(status=503, read=lambda _: json.dumps({'error': {
                'type':'memeval_governor', 'reason':reason}}).encode(),
                getheaders=lambda: [('X-Memeval-Governor', reason)])
            connection = types.SimpleNamespace(request=lambda *a: None, getresponse=lambda: response, close=lambda: None)
            with patch('memeval_bridge.http.http.client.HTTPConnection', return_value=connection):
                with self.assertRaises(HTTPError) as raised:
                    a.request({'path':'/memories'}, {})
            self.assertEqual(error_data(raised.exception)['governor'], reason)
            self.assertIn('memeval_governor', str(raised.exception))

    def test_wire_error_data(self):
        import subprocess
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, 'fixture.py').write_text("from memeval_bridge import AdapterBase\nclass Adapter(AdapterBase):\n def describe(self): raise RuntimeError(\"SDK 503 {'error': {'type': 'memeval_governor', 'reason': 'exhausted'}}\")\n")
            env = dict(os.environ, PYTHONPATH=tmp+os.pathsep+str(Path(__file__).parent))
            p = subprocess.run([sys.executable, '-B', '-m', 'memeval_bridge', '--adapter', 'fixture:Adapter'],
                input=json.dumps({'jsonrpc':'2.0','id':1,'method':'describe'})+'\n', text=True, capture_output=True, env=env)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertEqual(json.loads(p.stdout)['error']['data']['governor'], 'exhausted')


class AnswerInputContracts(unittest.TestCase):
    def test_dispatcher_preserves_provenance_or_rejects(self):
        from memeval_bridge import AdapterBase, handle, Unsupported
        class Fixture(AdapterBase):
            def reset(self, ns): pass
            def ingest(self, ns, events): return handle(1, len(events))
            def retrieve(self, ns, req):
                if req.get('unsupported'): raise Unsupported('explicitly unsupported')
                return self.result
        a = Fixture({})
        d = Dispatcher(a)
        try:
            d.call('reset', {'ns':'test'})
            d.call('ingest', {'ns':'test', 'events':[{'id':'a'}]})
            for ids in ([], ['a']):
                a.result = {'items':[{'text':'actual memory', 'source_ids':ids}]}
                before = copy.deepcopy(a.result)
                self.assertEqual(d.call('retrieve', {'ns':'test','request':{}}), before)
                self.assertEqual(a.result, before)
            # Empty first item must not hide a later invalid citation. Cover
            # typed tiers too; validation must not mutate the SDK's response.
            for ids in (['unknown'], ['future'], ['a','a'], None, 'a', [1], ['a',1]):
                for tiered in (False, True):
                    with self.subTest(ids=ids, tiered=tiered):
                        item = {'text':'bad', 'source_ids':ids}
                        a.result = {'items':[{'text':'unattributed', 'source_ids':[]}]}
                        if tiered: a.result['tiers']={'relevantBeliefs':[item]}
                        else: a.result['items'].append(item)
                        before=copy.deepcopy(a.result)
                        with self.assertRaises(ValueError): d.call('retrieve', {'ns':'test','request':{}})
                        self.assertEqual(a.result, before)
            a.result={'items':[{'text':'missing list'}]}
            with self.assertRaises(ValueError): d.call('retrieve', {'ns':'test','request':{}})
            with self.assertRaises(Unsupported): d.call('retrieve', {'ns':'test','request':{'unsupported':True}})
        finally:
            d.loop.close()


if __name__ == '__main__':
    unittest.main()
