"""Cognee 1.6.5: one dataset per namespace; provenance only from identifiers Cognee returns.

Verified on 1.6.5 (host probe, 2026-10-18): ``cognee.add(list[str])`` returns one
``data_ingestion_info`` record per input item **in input order** (duplicate texts share one
``data_id`` with status ``PipelineRunAlreadyCompleted``); ``recall(..., CHUNKS)`` rows carry
``metadata.data_id`` equal to that id. Nothing is ever inferred from text.
"""
import os
from collections import defaultdict
from memeval_bridge import AdapterBase, Unsupported, handle, limited, visible

ADD_BATCH = 20  # cognee.add(data_per_batch=20): keep one dlt batch per call so the info list stays 1:1
OK_STATUSES = ('PipelineRunCompleted', 'PipelineRunAlreadyCompleted')


def record(value):
    if isinstance(value, dict):
        return value
    if callable(getattr(value, 'model_dump', None)):
        return value.model_dump()
    return vars(value) if hasattr(value, '__dict__') else {}


class Adapter(AdapterBase):
    def __init__(self, config):
        super().__init__(config)
        self.extractor = config.get('extractor', 'gliner_demo')
        if self.extractor not in ('llm', 'gliner_demo'):
            raise ValueError('unsupported Cognee extractor')
        if self.extractor == 'llm':
            for key in ('LLM_MODEL', 'LLM_ENDPOINT', 'LLM_API_KEY'):
                if not os.environ.get(key, '').strip():
                    raise ValueError('cognee llm requires ' + key + ' via manifest llm.env')
        self.sdk = None
        self.ns = None
        self.sequence = 0
        self.sources = defaultdict(list)  # data_id -> [event ids] (identical texts share a data_id)

    def describe(self):
        return {'name': 'cognee', 'version': '1.6.5',
                'capabilities': ['retrieve', 'read_only_queries'], 'read_only_queries': True,
                'config': {'llm_notes': ['cognee 1.6.5 exposes no reasoning_effort control; model only; embeddings local fastembed'] if self.extractor == 'llm' else []},
                'limitations': ['no per-message timestamp/as_of API; scope/as_of are not upstream filters',
                                 ('LLM graph extraction via injected gateway + local fastembed; reasoning_effort unavailable: no general extraction config key in Cognee 1.6.5'
                                  if self.extractor == 'llm' else 'GLiNER graph extraction + local fastembed; no LLM in none variant'),
                                 'dump unsupported: no verified complete enumeration API',
                                 'provenance = add() data_ingestion_info[i].data_id (input order) matched to recall metadata.data_id; '
                                 'identical event texts share one data_id and are all credited; other ids unsupported']}

    async def reset(self, ns):
        if self.sdk is None:
            import cognee
            self.sdk = cognee
        try:
            await self.sdk.forget(dataset=ns)
        except Exception as exc:  # fresh namespace: the dataset does not exist yet (DatasetNotFoundError, HTTP-style 404)
            if type(exc).__name__ != 'DatasetNotFoundError' and 'not found' not in str(exc).lower():
                raise
        self.sources = defaultdict(list)
        self.ns, self.sequence = ns, 0

    async def ingest(self, ns, events):
        for start in range(0, len(events), ADD_BATCH):
            batch = events[start:start + ADD_BATCH]
            texts = [event.get('role', 'user') + ': ' + event['content'] for event in batch]
            info = record(await self.sdk.add(texts, dataset_name=ns))
            if str(info.get('status', '')) not in OK_STATUSES:
                raise RuntimeError('Cognee add failed: ' + str(info.get('status')))
            items = [record(x) for x in (info.get('data_ingestion_info') or [])]
            if len(items) != len(batch):
                raise Unsupported(f'Cognee add returned {len(items)} ingestion records for {len(batch)} items; per-item provenance unverified')
            for event, item in zip(batch, items):
                status = str(record(item.get('run_info')).get('status', ''))
                if not item.get('data_id') or status not in OK_STATUSES:
                    raise RuntimeError('Cognee add item failed: ' + status)
                key = str(item['data_id'])
                if event['id'] not in self.sources[key]:
                    self.sources[key].append(event['id'])
        if events:
            runs = record(await self.sdk.cognify(datasets=[ns], extractor=self.extractor))
            for run in runs.values():
                status = str(record(run).get('status', ''))
                if status not in OK_STATUSES:
                    raise RuntimeError('Cognee cognify failed: ' + status)
        self.sequence += 1
        return handle(self.sequence, len(events))

    def ready(self, ns, ingest_handle, deadline):
        return visible()  # cognify completed synchronously inside ingest

    async def retrieve(self, ns, req):
        from cognee.api.v1.search import SearchType
        if not str(req.get('query') or '').strip():
            return limited([], req)  # cognee.recall('') raises; an empty query legitimately retrieves nothing (PMB edge cases)
        rows = await self.sdk.recall(req['query'], query_type=SearchType.CHUNKS,
                                     datasets=[ns], top_k=req['budget']['top_k'], auto_route=False)
        if not isinstance(rows, list):
            raise Unsupported('unverified Cognee recall response shape')
        items = []
        for value in rows:
            row = record(value)
            if not isinstance(row.get('text'), str):
                raise Unsupported('unverified Cognee memory unit shape')
            meta = record(row.get('metadata'))
            raw = record(row.get('raw'))
            data_id = meta.get('data_id') or raw.get('document_id')
            ids = list(self.sources.get(str(data_id), [])) if data_id else []
            items.append({'id': str(meta.get('chunk_id') or raw.get('id') or len(items)),
                          'text': row['text'], 'source_ids': ids, 'score': row.get('score'),
                          'status': 'ok' if ids else 'unsupported'})
        return limited(items, req)
