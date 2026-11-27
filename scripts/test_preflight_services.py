"""Offline service-probe contracts: no real credentials, DNS or HTTP calls."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

spec = importlib.util.spec_from_file_location('preflight', Path(__file__).with_name('preflight_services.py'))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class PreflightTests(unittest.TestCase):
    def test_jev_minimal_request_shape_and_path(self):
        url, body = preflight.probe_request('jev', 'https://example.invalid/api/', 'fake-jev')
        self.assertEqual(url, 'https://example.invalid/api/systemone')
        self.assertEqual(body['model'], 'fake-jev')
        self.assertEqual(set(body), {'model', 'state', 'questions'})
        self.assertEqual(len(body['questions']), 1)
        question = body['questions']['probe']
        self.assertEqual(question['type'], 'noul')
        self.assertEqual(set(question['criteria']), {'true', 'false'})
        self.assertEqual(question['instructions']['candidate_id'], 'probe')
        self.assertEqual(question['instructions']['target_body'], 'The sky is blue.')
        self.assertEqual(body['state']['task']['role'], 'assistant')

    def test_cli_env_resolution_status_and_no_credentials_in_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = Path(tmp) / 'test.env'
            env.write_text('JEV_BASE_URL=http://example.invalid:9999/v1\n'
                           'JEV_API_KEY=fake-private-key\nJEV_MODEL=fake-jev\n')
            args = ['--env', str(env), '--jev', 'JEV_BASE_URL', 'JEV_API_KEY', 'JEV_MODEL']
            for status, error in ((200, None), (503, {'type': 'unavailable', 'code': 'retry'})):
                with self.subTest(status=status), patch.object(preflight, 'request', return_value=(status, error)) as request, contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(preflight.main(args), 0 if status == 200 else 1)
                    self.assertEqual(request.call_args.args[0], 'http://example.invalid:9999/v1/systemone')
                    self.assertEqual(request.call_args.args[1], 'fake-private-key')
                    self.assertEqual(request.call_args.args[2]['model'], 'fake-jev')
                    self.assertTrue(output.getvalue().startswith(f'jev example.invalid:9999 fake-jev: {status} '))
                    self.assertNotIn('fake-private-key', output.getvalue())
                    self.assertEqual(len(output.getvalue().splitlines()), 1)

    def test_missing_key_never_dispatches(self):
        with patch.object(preflight, 'request') as request, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(preflight.main(['--jev', 'https://example.invalid', 'ABSENT_KEY', 'fake-jev']), 1)
            request.assert_not_called()

    def test_wire_request_and_error_body_whitelist(self):
        url, body = preflight.probe_request('jev', 'https://example.invalid', 'fake-jev')
        error = urllib.error.HTTPError(url, 503, 'unavailable', {}, io.BytesIO(json.dumps({
            'error': {'type': 'service_error', 'code': 'unavailable', 'message': 'private response text'}
        }).encode()))
        with patch.object(preflight.urllib.request, 'urlopen', side_effect=error) as opened:
            self.assertEqual(preflight.request(url, 'fake-key', body, 4),
                             (503, {'type': 'service_error', 'code': 'unavailable'}))
        request = opened.call_args.args[0]
        self.assertEqual(request.get_method(), 'POST')
        self.assertEqual(request.get_header('Authorization'), 'Bearer fake-key')
        self.assertEqual(json.loads(request.data), body)
        self.assertEqual(opened.call_args.kwargs['timeout'], 4)

    def test_existing_chat_embedding_requests_unchanged(self):
        self.assertEqual(preflight.probe_request('chat', 'http://example.invalid/v1', 'fake'),
                         ('http://example.invalid/v1/chat/completions',
                          {'model': 'fake', 'messages': [{'role': 'user', 'content': 'hi'}], 'max_tokens': 1}))
        self.assertEqual(preflight.probe_request('embedding', 'http://example.invalid/v1', 'fake'),
                         ('http://example.invalid/v1/embeddings', {'model': 'fake', 'input': 'hi'}))


if __name__ == '__main__':
    unittest.main(verbosity=2)
