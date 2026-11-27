"""Hindsight 1.0.0 REST contract; all source identities come from document_id."""
import copy
import os
from memeval_bridge.http import Adapter as HTTPAdapter, HTTPError
from memeval_bridge import handle, limited


class Adapter(HTTPAdapter):
    def __init__(self, config):
        super().__init__(config)
        if os.environ.get('HINDSIGHT_API_LLM_PROVIDER', 'none') != 'none':
            for key in ('HINDSIGHT_API_LLM_MODEL', 'HINDSIGHT_API_LLM_BASE_URL', 'HINDSIGHT_API_LLM_API_KEY'):
                if not os.environ.get(key, '').strip():
                    raise ValueError('hindsight gateway requires ' + key + ' via manifest llm.env')

    def describe(self):
        d = copy.deepcopy(super().describe())
        d['limitations'] += [
            'recall supports budget low/mid/high, not exact top_k; <=100 low, <=300 mid, otherwise high; bridge truncates',
            'scope/as_of are not upstream recall filters; timestamp is retained as event timestamp',
            'Postgres and API use episode container network aliases; HOME/cwd isolated in episode',
            'none provider stores raw chunks without LLM extraction; gateway OpenAI chat/completions uses injected model and HINDSIGHT_API_LLM_REASONING_EFFORT; calls unmetered',
        ]
        d.setdefault('config', {})['llm_provider'] = os.environ.get('HINDSIGHT_API_LLM_PROVIDER', 'none')
        d['config']['llm_notes'] = (['HINDSIGHT_API_LLM_REASONING_EFFORT set; the SDK may adjust it per model; embeddings and reranker local']
                                   if d['config']['llm_provider'] != 'none' else [])
        return d

    def reset(self, ns):
        try:
            self.call('reset', {'ns': ns})
        except HTTPError as e:
            if e.status != 404:
                raise
        self.call('create', {'ns': ns})
        self.seq = 0

    def ingest(self, ns, events):
        items = [{'content': e['content'], 'timestamp': e.get('timestamp'),
                  'document_id': e['id'], 'metadata': {'source_id': e['id'], 'role': e.get('role', '')}}
                 for e in events]
        if items:
            result = self.call('ingest', {'ns': ns, 'items': items})
            # 1.0.0 serialises RetainResponse.is_async under its alias "async" (verified 2026-10-18); accept both spellings.
            is_async = result.get('is_async', result.get('async'))
            if result.get('success') is not True or result.get('items_count') != len(items) or is_async is not False:
                # Only structural fields are reported (no memory text): counts, flags and key names.
                raise ValueError('Hindsight synchronous retain accounting/visibility mismatch: sent=%d success=%r items_count=%r async=%r keys=%s'
                                 % (len(items), result.get('success'), result.get('items_count'), is_async, sorted(result.keys())))
        self.seq += 1
        return handle(self.seq, len(events))

    def retrieve(self, ns, req):
        req = dict(req)
        if not str(req.get('query') or '').strip():
            return limited([], req)  # Hindsight recall rejects empty/whitespace queries; retrieving nothing is the correct answer
        k = req['budget']['top_k']
        req['hindsight_budget'] = 'low' if k <= 100 else 'mid' if k <= 300 else 'high'
        return super().retrieve(ns, req)
