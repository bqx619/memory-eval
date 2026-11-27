"""Honcho session API adapter; endpoint/client factory is a reviewed project hook."""
import asyncio
import importlib
import time
from datetime import datetime
from memeval_bridge import AdapterBase, Unsupported, handle, limited, visible


class Adapter(AdapterBase):
    def __init__(self, config):
        super().__init__(config)
        self.client = None
        self.sequence = 0
        self.resets = 0
        self.events = {}

    def describe(self):
        return {'name': 'honcho', 'version': '3.3.0', 'capabilities': ['retrieve', 'read_only_queries'],
                'read_only_queries': True,
                'limitations': ['created_at preserved as first-class datetime; as_of search not verified',
                                 'ready polls queue_status; shape must match the reviewed client contract',
                                 'dump unsupported until complete SDK enumeration is verified',
                                 'server distribution and client factory await real API verification']}

    def reset(self, ns):
        # Explicit factory avoids inventing an undocumented Unix transport/client
        # constructor. It is part of the immutable project, not a global plugin.
        factory = self.config.get('client_factory')
        if not factory:
            raise Unsupported('Honcho client_factory requires verified Unix HTTP transport')
        module, name = factory.split(':', 1)
        self.client = getattr(importlib.import_module(module), name)()
        self.resets += 1
        self.session = self.client.session(ns + ":" + str(self.resets))
        self.peer = self.client.peer(ns)
        self.events = {}
        self.ns = ns
        self.sequence = 0

    def ingest(self, ns, events):
        messages = []
        for e in events:
            date = datetime.fromisoformat(e['timestamp'].replace('Z', '+00:00')) if e.get('timestamp') else None
            messages.append(self.peer.message(e['content'], created_at=date))
        result = self.session.add_messages(messages)
        # Associate returned IDs with accepted inputs, never fuzzy text matching.
        if not isinstance(result, list) or len(result) != len(events):
            raise ValueError('Honcho accepted message count/DTO is unverified')
        for message, event in zip(result, events):
            key = message['id'] if isinstance(message, dict) else message.id
            if key in self.events:
                raise ValueError('duplicate Honcho message id')
            self.events[key] = event
        self.sequence += 1
        return handle(self.sequence, len(events))

    async def ready(self, ns, ingest_handle, deadline):
        end = time.monotonic() + self.config.get('ready_timeout_s', 30)
        if deadline:
            end = min(end, time.monotonic() + max(0, datetime.fromisoformat(deadline.replace('Z', '+00:00')).timestamp() - time.time()))
        while True:
            status = self.session.queue_status()
            if not isinstance(status, dict) or type(status.get('pending')) is not int:
                raise Unsupported('unverified queue_status DTO')
            if status['pending'] == 0:
                return visible()
            if time.monotonic() >= end:
                return {'visible': False, 'stages': [], 'warnings': ['deriver queue not drained']}
            await asyncio.sleep(0.05)

    def retrieve(self, ns, req):
        rows = self.session.search(req['query'], limit=req['budget']['top_k'])
        items = []
        for row in rows:
            key, text = (row['id'], row['content']) if isinstance(row, dict) else (row.id, row.content)
            source = self.events.get(key)
            items.append({'id': str(key), 'text': text, 'source_ids': [source['id']] if source else [],
                          'status': 'ok' if source else 'unsupported'})
        return limited(items, req)
