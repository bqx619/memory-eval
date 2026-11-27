#!/usr/bin/env python3
"""Probe the model services a run depends on before starting containers.

Usage:
  python3 -B scripts/preflight_services.py --env ../cogletnet/.env \
      --chat CLIPROXY_BASE_URL CLIPROXY_API_KEY gpt-6-luna \
      --embedding EMBEDDING_BASE_URL EMBEDDING_API_KEY EMBEDDING_MODEL \
      --jev JEV_BASE_URL JEV_API_KEY JEV_MODEL

Each probe sends the smallest possible request (one token) and prints only the HTTP
status plus the error body's `type`/`code`. Credential values never reach stdout or
stderr. Exit status 0 when every probe returns 200, 1 otherwise. Standard library only.
"""
import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request


def load_env(path):
    env = {}
    with open(path, encoding='utf-8') as handle:
        for raw in handle:
            line = raw.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            key, value = line.split('=', 1)
            value = value.split(' #')[0].strip().strip('"').strip("'")
            env[key.strip()] = value
    return env


def resolve(env, name):
    """A probe argument is an env var name or, when not present in the env, a literal."""
    return env.get(name, name)


def request(url, key, payload, timeout):
    body = json.dumps(payload).encode('utf-8')
    req = urllib.request.Request(url, data=body, headers={
        'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, None
    except urllib.error.HTTPError as err:
        text = err.read()[:2000].decode('utf-8', 'replace')
        try:
            error = json.loads(text).get('error') or {}
        except ValueError:
            error = {}
        if isinstance(error, dict):
            return err.code, {'type': error.get('type'), 'code': error.get('code')}
        return err.code, {'type': None, 'code': None}
    except Exception as err:  # network / TLS
        return None, {'type': type(err).__name__, 'code': None}


def probe_request(kind, base, model):
    """Build synthetic requests without loading credentials or contacting services."""
    if kind == 'chat':
        return base.rstrip('/') + '/chat/completions', {
            'model': model, 'messages': [{'role': 'user', 'content': 'hi'}], 'max_tokens': 1}
    if kind == 'embedding':
        return base.rstrip('/') + '/embeddings', {'model': model, 'input': 'hi'}
    if kind == 'jev':
        # HttpClient::evaluate posts to systemone. JevPromptProfile::request
        # wraps each candidate in a Noul question with structured instructions.
        return base.rstrip('/') + '/systemone', {
            'model': model,
            'state': {'task': {'role': 'assistant', 'purpose': 'Check memory relevance.',
                               'user_message': 'What color is the sky?'}},
            'questions': {'probe': {
                'type': 'noul',
                'instructions': {'candidate_id': 'probe', 'target_body': 'The sky is blue.',
                                 'question': 'Should the assistant refer to this memory? '},
                'criteria': {'true': 'The memory is relevant.', 'false': 'The memory is unrelated.'}}}}
    raise ValueError('unknown probe kind')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    parser.add_argument('--env', action='append', default=[], help='.env file to read (repeatable; later files override)')
    parser.add_argument('--chat', nargs=3, action='append', default=[], metavar=('BASE_URL', 'API_KEY', 'MODEL'),
                        help='chat completion probe: env names (or literals) for base URL, key and model')
    parser.add_argument('--embedding', nargs=3, action='append', default=[], metavar=('BASE_URL', 'API_KEY', 'MODEL'),
                        help='embedding probe: env names (or literals) for base URL, key and model')
    parser.add_argument('--jev', nargs=3, action='append', default=[], metavar=('BASE_URL', 'API_KEY', 'MODEL'),
                        help='JEV Noul probe: env names (or literals) for base URL and model, env name for key')
    parser.add_argument('--timeout', type=float, default=40.0)
    args = parser.parse_args(argv)
    env = {}
    for path in args.env:
        env.update(load_env(path))
    ok = True
    probes = ([('chat', spec) for spec in args.chat] + [('embedding', spec) for spec in args.embedding]
              + [('jev', spec) for spec in args.jev])
    if not probes:
        print('no probes requested', file=sys.stderr)
        return 2
    for kind, (base_name, key_name, model_name) in probes:
        base, key, model = resolve(env, base_name), env.get(key_name), resolve(env, model_name)
        if not base or not key:
            print(f'{kind} {base_name}: missing base URL or key ({key_name} empty)')
            ok = False
            continue
        url, payload = probe_request(kind, base, model)
        status, error = request(url, key, payload, args.timeout)
        parsed = urllib.parse.urlsplit(base)
        host = parsed.hostname or ''
        if parsed.port is not None:
            host += ':' + str(parsed.port)
        if status == 200:
            suffix = ' ' + json.dumps({'type': None, 'code': None}) if kind == 'jev' else ''
            print(f'{kind} {host} {model}: 200' + suffix)
        else:
            ok = False
            print(f'{kind} {host} {model}: {status} {json.dumps(error)}')
    return 0 if ok else 1


if __name__ == '__main__':
    sys.exit(main())
