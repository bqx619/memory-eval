"""Offline, source-shaped contracts; real project smoke remains Claude's acceptance."""
import importlib.util
import json
import sys
import types
import unittest
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from memeval_bridge import Dispatcher, Unsupported

SUTS = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name + '_adapter', SUTS / name / 'adapter.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def events(n=2):
    return [{'id': 'source-' + str(i), 'content': 'memory ' + str(i), 'role': 'user',
             'timestamp': '2020-01-02T03:04:05Z'} for i in range(n)]


def request(k=100):
    return {'query': 'memory', 'budget': {'top_k': k, 'max_context_chars': 100000}}


class Contracts(unittest.TestCase):
    def test_cognee(self):
        rows, calls = [], []
        async def forget(*, data_id=None, dataset=None, dataset_id=None, everything=False, memory_only=False, user=None):
            self.assertEqual(dataset, 'test'); self.assertFalse(everything)
            rows.clear()
        ids, batches = {}, []
        async def add(data, dataset_name='main_dataset', **kwargs):
            # Verified 1.6.5 shape: one data_ingestion_info record per input item, in input order;
            # identical texts reuse the data_id with status PipelineRunAlreadyCompleted.
            self.assertEqual(dataset_name, 'test'); self.assertIsInstance(data, list); self.assertLessEqual(len(data), 20)
            batches.append(len(data)); info = []
            for text in data:
                self.assertTrue(text.startswith('user: memory '))
                fresh = text not in ids
                ids.setdefault(text, f'data-{len(ids)}')
                if fresh:
                    rows.append(types.SimpleNamespace(text=text, score=0.8, metadata={'data_id': ids[text], 'chunk_id': f'chunk-{len(rows)}'}))
                info.append({'run_info': {'status': 'PipelineRunCompleted' if fresh else 'PipelineRunAlreadyCompleted'}, 'data_id': ids[text]})
            return types.SimpleNamespace(status='PipelineRunCompleted', data_ingestion_info=info)
        async def cognify(datasets=None, **kwargs):
            self.assertEqual(datasets, ['test'])
            self.assertEqual(kwargs['extractor'], 'gliner_demo')
            return {'dataset-uuid': types.SimpleNamespace(status='PipelineRunCompleted')}
        async def recall(query_text, query_type=None, *, datasets=None, dataset_ids=None, top_k=15,
                         auto_route=True, scope=None, session_id=None, only_context=False, include_references=False, **kwargs):
            self.assertEqual(query_text, 'memory'); self.assertEqual(datasets, ['test'])
            self.assertEqual(query_type, 'CHUNKS'); self.assertFalse(auto_route)
            return rows[:top_k-1] + [types.SimpleNamespace(text='derived', metadata={'data_id': 'missing'})]
        sys.modules['cognee'] = types.SimpleNamespace(add=add, cognify=cognify, recall=recall, forget=forget)
        sys.modules['cognee.api.v1.search'] = types.SimpleNamespace(SearchType=types.SimpleNamespace(CHUNKS='CHUNKS'))
        d = Dispatcher(load('cognee').Adapter({}))
        self.assertEqual(d.call('describe', {})['config']['llm_notes'], [])
        try:
            for n in (75, 50):
                d.call('reset', {'ns': 'test'}); ids.clear(); batches.clear()
                h = d.call('ingest', {'ns': 'test', 'events': events(n)})
                self.assertEqual(h['accepted'], n); self.assertEqual(sum(batches), n); self.assertEqual(max(batches), 20)
                self.assertTrue(d.call('ready', {'ns': 'test', 'handle': h})['visible'])
                result = d.call('retrieve', {'ns': 'test', 'request': dict(request(), scope=['owner'], as_of='2020-01-01')})
                self.assertEqual(result['items'][0]['source_ids'], ['source-0'])
                self.assertEqual(result['items'][-1]['status'], 'unsupported')
                with self.assertRaises(Unsupported): d.call('dump', {'ns': 'test'})
            # Empty query: recall is never called (cognee.recall('') raises) and nothing is retrieved.
            self.assertEqual(d.call('retrieve', {'ns': 'test', 'request': dict(request(), query='   ')})['items'], [])
            # Duplicate texts share one data_id: both events are credited, nothing is inferred from text.
            d.call('reset', {'ns': 'test'}); ids.clear(); batches.clear()
            dup = events(2); dup[1]['content'] = dup[0]['content']
            d.call('ingest', {'ns': 'test', 'events': dup})
            result = d.call('retrieve', {'ns': 'test', 'request': request()})
            self.assertEqual(result['items'][0]['source_ids'], [dup[0]['id'], dup[1]['id']])
            d.call('reset', {'ns': 'test'}); self.assertEqual(rows, [])
        finally:
            d.loop.close()

    def test_hindsight_real_routes(self):
        rows, requests, banks = [], [], set()
        test = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def response(self, data, status=200):
                raw = json.dumps(data).encode(); self.send_response(status)
                self.send_header('Content-Length', str(len(raw))); self.end_headers(); self.wfile.write(raw)
            def do_PUT(self):
                test.assertEqual(self.path, '/v1/default/banks/test'); banks.add('test'); self.response({'bank_id': 'test'})
            def do_DELETE(self):
                status = 200 if 'test' in banks else 404
                banks.discard('test'); rows.clear(); self.response({}, status)
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                requests.append((self.path, body))
                if self.path.endswith('/recall'):
                    self.response({'results': rows + [{'id':'orphan','text':'unknown','document_id':'absent'}]})
                else:
                    test.assertEqual(self.path, '/v1/default/banks/test/memories')
                    test.assertFalse(body['async'])
                    for item in body['items']:
                        test.assertEqual(item['timestamp'], '2020-01-02T03:04:05Z')
                        rows.append({'id':'unit-'+item['document_id'], 'text':item['content'], 'document_id':item['document_id']})
                    self.response({'success':True,'items_count':len(body['items']),'async':False})
            def do_GET(self):
                from urllib.parse import urlsplit, parse_qs
                u = urlsplit(self.path); test.assertEqual(u.path, '/v1/default/banks/test/memories/list')
                q = parse_qs(u.query); start=int(q['offset'][0]); limit=int(q['limit'][0])
                self.response({'items':rows[start:start+limit], 'total':len(rows),'offset':start,'limit':limit})
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        a = load('hindsight').Adapter({'base_url': 'http://localhost:'+str(server.server_port), 'routes':str(SUTS/'hindsight/routes.json')})
        d = Dispatcher(a)
        try:
            self.assertTrue(any('not exact top_k' in s for s in a.describe()['limitations']))
            d.call('reset', {'ns':'test'})
            h = d.call('ingest', {'ns':'test','events':events(205)})
            self.assertEqual(d.call('retrieve',{'ns':'test','request':dict(request(10), query='   ')})['items'], [])  # blank query never reaches the server
            self.assertEqual(h['accepted'],205)
            self.assertTrue(d.call('ready', {'ns':'test','handle':h})['visible'])
            for k,budget in [(1,'low'),(100,'low'),(101,'mid'),(300,'mid'),(301,'high')]:
                if k > 205:
                    with self.assertRaisesRegex(ValueError, 'invalid source_ids'):
                        d.call('retrieve',{'ns':'test','request':dict(request(k), as_of='2020-01-01',scope=['owner'])})
                else:
                    r=d.call('retrieve',{'ns':'test','request':dict(request(k), as_of='2020-01-01',scope=['owner'])})
                    self.assertLessEqual(len(r['items']),k)
                    self.assertEqual(r['items'][0]['source_ids'],['source-0'])
                self.assertEqual(requests[-1][1],{'query':'memory','budget':budget})
            self.assertEqual(len(d.call('dump',{'ns':'test'})['items']),205)
            d.call('reset',{'ns':'test'});self.assertEqual(d.call('dump',{'ns':'test'})['items'],[])
        finally:
            d.loop.close();server.shutdown();server.server_close();thread.join()

    def test_cognee_prepare_records_resolved_revision(self):
        import hashlib
        import io
        import tarfile
        import os
        import subprocess
        from unittest.mock import patch
        spec=importlib.util.spec_from_file_location('cognee_prepare',SUTS/'cognee/prepare_models.py')
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)/'models'
            archive=root/'lbug/fixture.tar.gz';archive.parent.mkdir(parents=True)
            with tarfile.open(archive, 'w:gz') as tar:
                for name in ['liblbug.dylib', 'liblbug.so']:
                    data=b'fixture-native-library'
                    entry=tarfile.TarInfo('lib/'+name);entry.size=len(data)
                    tar.addfile(entry, io.BytesIO(data))
            archive_sha=hashlib.sha256(archive.read_bytes()).hexdigest()
            sources={key:('https://fixture.invalid/fixture.tar.gz',archive_sha) for key in module.LBUG_SOURCES}
            purelib=Path(tmp)/'site-packages'
            def snapshot(model, *, cache_dir, revision=None, allow_patterns=None):
                if model == 'fastino/gliner2.5-base-v1':
                    self.assertEqual(revision,'main');self.assertIsNone(allow_patterns)
                    p=Path(cache_dir)/'snapshots'/('54785d51'+'0'*32)
                    p.mkdir(parents=True);(p/'weights').write_bytes(b'fixture')
                else:
                    self.assertIn(model, ['BAAI/bge-small-en-v1.5','microsoft/deberta-v3-base'])
                    self.assertIsNone(revision);self.assertIn('*.json',allow_patterns)
                    p=Path(cache_dir)/model.replace('/','--')/'snapshots'/'fixture-tokenizer'
                    p.mkdir(parents=True);(p/'tokenizer.json').write_text('{}')
                return str(p)
            def embedding(*,model_name,cache_dir):
                self.assertEqual(model_name,'BAAI/bge-small-en-v1.5');p=Path(cache_dir);p.mkdir();(p/'weights').write_bytes(b'fixture')
            def encoding(name):
                self.assertIn(name, ['cl100k_base','o200k_base'])
                (Path(os.environ['TIKTOKEN_CACHE_DIR'])/name).write_bytes(b'fixture-encoding')
            with patch.dict(sys.modules,{'huggingface_hub':types.SimpleNamespace(snapshot_download=snapshot),'fastembed':types.SimpleNamespace(TextEmbedding=embedding),'tiktoken':types.SimpleNamespace(get_encoding=encoding)}), patch.dict('os.environ',{}), patch.object(module,'LBUG_SOURCES',sources), patch('sysconfig.get_paths',return_value={'purelib':str(purelib)}), patch('subprocess.run',side_effect=AssertionError('prepare must not run platform-specific binary modification tools')), patch('urllib.request.urlopen',side_effect=AssertionError('offline contract must not access the network')) as download:
                module.main(root)
                download.assert_not_called()
            assets=json.loads((root/'model-assets.json').read_text())
            self.assertEqual(assets['resolved_revision'],'54785d51'+'0'*32)
            expected={str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest()
                      for p in root.rglob('*') if p.is_file() and p.name!='model-assets.json'}
            self.assertEqual(assets['sha256'],expected)
            self.assertEqual(set(expected),{'hf/hub/snapshots/'+assets['resolved_revision']+'/weights',
                                            'hf/hub/BAAI--bge-small-en-v1.5/snapshots/fixture-tokenizer/tokenizer.json',
                                            'hf/hub/microsoft--deberta-v3-base/snapshots/fixture-tokenizer/tokenizer.json',
                                            'tiktoken/cl100k_base','tiktoken/o200k_base',
                                            'fastembed/weights','lbug/fixture.tar.gz','lbug/installed.json','lbug/lib/liblbug.dylib','lbug/lib/liblbug.so',
                                            ('lbug/lib/liblbug.0.dylib' if sys.platform == 'darwin' else 'lbug/lib/liblbug.so.0')})
            self.assertEqual(assets['sha256']['lbug/fixture.tar.gz'],archive_sha)
            self.assertFalse(purelib.exists(), 'prepare must not write into the venv')
            self.assertTrue(all(p.is_relative_to(root) for p in Path(tmp).rglob('*') if p.is_file()))
            for name in ['liblbug.dylib','liblbug.so']:
                self.assertEqual((root/'lbug/lib'/name).read_bytes(),b'fixture-native-library')


    def test_hindsight_prepare_models_only(self):
        import hashlib
        from unittest.mock import patch
        spec=importlib.util.spec_from_file_location('hindsight_prepare',SUTS/'hindsight/prepare_models.py')
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            def snapshot(model, *, cache_dir):
                p=Path(cache_dir)/model.replace('/','--')/'snapshots'/'fixture-sha';p.mkdir(parents=True)
                (p/'weights').write_bytes(b'fixture');return str(p)
            with patch.dict(sys.modules,{'huggingface_hub':types.SimpleNamespace(snapshot_download=snapshot)}), patch.dict('os.environ',{}), patch('subprocess.Popen', side_effect=AssertionError('prepare must not start a database')) as spawn:
                module.main(root/'models')
                spawn.assert_not_called()
            assets=json.loads((root/'models/model-assets.json').read_text())
            self.assertEqual(len(assets['resolved_snapshots']),2)
            self.assertEqual(assets['sha256'], {str(p.relative_to(root/'models')):hashlib.sha256(p.read_bytes()).hexdigest()
                                              for p in (root/'models').rglob('*') if p.is_file() and p.name!='model-assets.json'})
            self.assertFalse((root/'models/pg0-home').exists())

    def test_cognee_llm_sdk_environment(self):
        import os
        from unittest.mock import patch
        observed = []
        async def forget(*, dataset): pass
        async def add(data, *, dataset_name):
            return types.SimpleNamespace(status='PipelineRunCompleted', data_ingestion_info=[{'data_id':'d','run_info':{'status':'PipelineRunCompleted'}}])
        async def cognify(*, datasets, extractor):
            self.assertEqual(extractor, 'llm')
            # Mirror the 1.6.5 LLMConfig BaseSettings names; no SDK import or external request.
            observed.append({k:os.environ[k] for k in ('LLM_PROVIDER','LLM_MODEL','LLM_ENDPOINT','LLM_API_KEY','EMBEDDING_PROVIDER')})
            return {'d': types.SimpleNamespace(status='PipelineRunCompleted')}
        env = dict(LLM_PROVIDER='openai', LLM_MODEL='configured-model', LLM_ENDPOINT='http://llm-gateway:8317/v1',
                   LLM_API_KEY='fixture-key', EMBEDDING_PROVIDER='fastembed')
        with patch.dict(os.environ, env, clear=True), patch.dict(sys.modules, {'cognee':types.SimpleNamespace(forget=forget,add=add,cognify=cognify)}):
            d = Dispatcher(load('cognee').Adapter({'extractor':'llm'}))
            try:
                d.call('reset', {'ns':'test'})
                d.call('ingest', {'ns':'test','events':events(1)})
                self.assertEqual(observed, [env])
            finally: d.loop.close()

    def test_http_container_host_syntax(self):
        from memeval_bridge.http import Adapter
        cfg = {'routes': str(SUTS/'hindsight/routes.json')}
        for host in ['api', 'api.service', '127.0.0.1', '[::1]', 'example.com']:
            a = Adapter(dict(cfg, base_url='http://' + host + ':80'))
            d = a.describe()
            self.assertEqual(d['config']['bridge_base_url_host'], host.strip('[]'))
            self.assertTrue(any(host.strip('[]') in text for text in d['limitations']))
            self.assertEqual(d, a.describe())
        for url in ['https://api:8888','http://user:pass@api:8888','http://@api:8888',
                    'http://api:8888?q=x','http://api:8888#x','http://api:8888?',
                    'http://api:8888#','http://api','http://api:0','http://api:8888/path']:
            with self.subTest(url=url), self.assertRaises(ValueError): Adapter(dict(cfg, base_url=url))

    def test_http_timeout_defaults_and_ready(self):
        from unittest.mock import patch
        from memeval_bridge.http import Adapter, timeout_seconds
        for config, expected in [({}, 30), ({'request_timeout_s': 300}, 295),
                                 ({'request_timeout_s': 10}, 5), ({'request_timeout_s': 10.5}, 5.5),
                                 ({'request_timeout_s': 3}, 1),
                                 ({'request_timeout_s': 10, 'timeout_s': 1}, 1)]:
            with self.subTest(config=config):
                self.assertEqual(timeout_seconds(config), expected)
        for config, expected in [({}, 30), ({'request_timeout_s': 10}, 5),
                                 ({'request_timeout_s': 3}, 1),
                                 ({'request_timeout_s': 10, 'timeout_s': 1}, 1),
                                 ({'request_timeout_s': 10, 'timeout_s': 1, 'ready_timeout_s': 8}, 8)]:
            with self.subTest(ready=config):
                a = Adapter.__new__(Adapter)
                a.config, a.routes = config, {'methods': {'ready': {}}}
                with patch.object(a, 'call', return_value={'visible': False}) as call, \
                     patch('memeval_bridge.http.time.monotonic', side_effect=[0, expected + 0.1]), \
                     patch('memeval_bridge.http.time.sleep', side_effect=AssertionError('ready deadline ignored')):
                    with self.assertRaisesRegex(TimeoutError, 'HTTP visibility deadline'):
                        a.ready('test', {}, None)
                    self.assertEqual(call.call_count, 1)

    def test_gateway_manifests_and_required_model(self):
        import os
        from unittest.mock import patch
        cases = [('cognee', 'LLM_MODEL', {'extractor':'llm'}),
                 ('hindsight', 'HINDSIGHT_API_LLM_MODEL', {'base_url':'http://api:8888','routes':str(SUTS/'hindsight/routes.json')})]
        for name, model_key, config in cases:
            m = json.loads((SUTS/name/'manifest-gateway.json').read_text())
            self.assertEqual(m['sandbox']['network'], 'gateway'); self.assertTrue(m['llm']['required'])
            values = {'{llm.base_url}':'http://llm-gateway:8317/v1', '{llm.api_key}':'fixture-secret',
                      '{llm.model}':'fixture-model', '{llm.reasoning_effort}':'low'}
            env = {key: values.get(value,value) for key,value in m['llm']['env'].items()}
            with patch.dict(os.environ, env, clear=True):
                a = load(name).Adapter(config)
                description = a.describe()
                self.assertNotIn('fixture-secret', json.dumps(description))
                expected_note = ('cognee 1.6.5 exposes no reasoning_effort control; model only; embeddings local fastembed'
                                 if name == 'cognee' else 'HINDSIGHT_API_LLM_REASONING_EFFORT set; the SDK may adjust it per model; embeddings and reranker local')
                self.assertEqual(description['config']['llm_notes'], [expected_note])
                self.assertEqual(description, a.describe())
                with patch.dict(os.environ, {model_key:''}):
                    with self.assertRaisesRegex(ValueError, model_key): load(name).Adapter(config)
            with patch.dict(os.environ, {'HINDSIGHT_API_LLM_PROVIDER':'none'}, clear=True):
                none_config = dict(config, extractor='gliner_demo') if name == 'cognee' else config
                self.assertEqual(load(name).Adapter(none_config).describe()['config']['llm_notes'], [])
            if name == 'hindsight':
                self.assertEqual(m['declared']['bridge_base_url_host'], a.describe()['config']['bridge_base_url_host'])
                self.assertEqual(env['HINDSIGHT_API_LLM_REASONING_EFFORT'], 'low')
                self.assertEqual(env['HINDSIGHT_API_LLM_BASE_URL'], values['{llm.base_url}'])
                self.assertEqual(m['process']['env']['HINDSIGHT_API_EMBEDDINGS_PROVIDER'], 'local')
                self.assertEqual(m['process']['env']['HINDSIGHT_API_RERANKER_PROVIDER'], 'local')
                self.assertNotIn('image', m['sidecars'][1])  # generic env image inherits the expanded SUT environment
            else:
                self.assertNotIn('LLM_REASONING_EFFORT', env)
                self.assertEqual(m['telemetry_env']['EMBEDDING_PROVIDER'], 'fastembed')
                self.assertEqual(m['process']['env']['LBUG_C_API_LIB_PATH'], '{env}/models/lbug/lib/liblbug.so')


if __name__ == '__main__':
    unittest.main()
