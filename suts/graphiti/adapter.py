import copy
import os
import sys
from pathlib import Path
from datetime import datetime, timezone
from memeval_bridge import AdapterBase, Unsupported, handle, visible, limited


# The gateway's strict subset does not accept these validation/annotation
# keywords. Visit schema slots, not arbitrary dictionaries: a property named
# "format" or "default" is still a real field and must not be deleted.
STRICT_UNSUPPORTED_KEYWORDS = frozenset({
    'default', 'format', 'minLength', 'maxLength', 'pattern',
    'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf',
    'minItems', 'maxItems', 'uniqueItems', 'contains', 'minContains', 'maxContains',
    'minProperties', 'maxProperties', 'patternProperties', 'propertyNames',
    'unevaluatedProperties', 'unevaluatedItems', 'dependentRequired', 'dependentSchemas',
    'not', 'if', 'then', 'else',
})


def normalize_strict_schema(schema):
    """Return a strict request copy and sorted removed keyword names; no I/O.

    Optional fields become required nullable fields. Keep reference and union
    structure, including nested definitions, without mutating SDK model schemas.
    """
    if not isinstance(schema, dict):
        raise ValueError('Graphiti response_format schema must be an object')
    result = copy.deepcopy(schema)
    removed = set()

    def nullable(node):
        if not isinstance(node, dict):
            return node is True
        kind = node.get('type')
        if kind == 'null' or isinstance(kind, list) and 'null' in kind:
            return True
        return any(nullable(branch) for key in ('anyOf', 'oneOf') for branch in node.get(key, []))

    def visit(node):
        if not isinstance(node, dict):
            return
        for key in STRICT_UNSUPPORTED_KEYWORDS.intersection(node):
            removed.add(key)
            del node[key]
        original_required = set(node.get('required', []))
        for key in ('$defs', 'definitions', 'properties'):
            for child in node.get(key, {}).values():
                visit(child)
        items = node.get('items')
        if isinstance(items, list):
            for child in items:
                visit(child)
        else:
            visit(items)
        for key in ('anyOf', 'oneOf', 'allOf', 'prefixItems'):
            for child in node.get(key, []):
                visit(child)
        kind = node.get('type')
        if kind == 'object' or isinstance(kind, list) and 'object' in kind or 'properties' in node:
            node['additionalProperties'] = False
            properties = node.setdefault('properties', {})
            for key, child in properties.items():
                if key not in original_required and not nullable(child):
                    properties[key] = {'anyOf': [child, {'type': 'null'}]}
            node['required'] = list(properties)

    visit(result)
    return result, tuple(sorted(removed))


class GatewayCompletions:
    """Transport shim for the SDK's generic chat client (which has no effort option).

    Keep upstream prompts/response processing; normalize only the outgoing schema.
    An explicitly configured effort selects the reasoning request shape, without a
    hard-coded model allowlist or a second endpoint.
    """
    def __init__(self, client, effort):
        self.client, self.effort = client, effort
        self._schema_warning_emitted = False

    async def create(self, **kwargs):
        kwargs['reasoning_effort'] = self.effort
        kwargs.pop('temperature', None)
        if 'max_tokens' in kwargs:
            kwargs['max_completion_tokens'] = kwargs.pop('max_tokens')
        response_format = kwargs.get('response_format')
        if isinstance(response_format, dict) and response_format.get('type') == 'json_schema':
            response_format = copy.deepcopy(response_format)
            spec = response_format['json_schema']
            spec['schema'], removed = normalize_strict_schema(spec['schema'])
            spec['strict'] = True
            kwargs['response_format'] = response_format
            if removed and not self._schema_warning_emitted:
                print('graphiti gateway strict schema: removed unsupported keywords: ' + ', '.join(removed),
                      file=sys.stderr, flush=True)
                self._schema_warning_emitted = True
        return await self.client.chat.completions.create(**kwargs)


def gateway_clients():
    from types import SimpleNamespace
    from openai import AsyncOpenAI
    from graphiti_core.llm_client.config import LLMConfig
    from graphiti_core.llm_client.openai_generic_client import OpenAIGenericClient
    from graphiti_core.embedder.client import EmbedderClient
    from graphiti_core.cross_encoder.client import CrossEncoderClient
    from fastembed import TextEmbedding

    class LocalEmbedder(EmbedderClient):
        def __init__(self):
            self.model = TextEmbedding(model_name='BAAI/bge-small-en-v1.5',
                                       cache_dir=os.environ['FASTEMBED_CACHE_PATH'], local_files_only=True)

        async def create(self, input_data):
            if isinstance(input_data, str):
                text = input_data
            elif isinstance(input_data, list) and input_data and all(isinstance(x, str) for x in input_data):
                text = input_data[0]  # SDK OpenAIEmbedder.create() returns the first vector too.
            else:
                raise ValueError('local Graphiti embedder accepts text only')
            return (await self.create_batch([text]))[0]

        async def create_batch(self, input_data_list):
            rows = [list(map(float, row)) for row in self.model.embed(input_data_list)]
            if len(rows) != len(input_data_list) or any(len(row) != 384 for row in rows):
                raise ValueError('local Graphiti embedding must return one 384-dimensional vector per text')
            return rows

    class NoReranker(CrossEncoderClient):
        async def rank(self, query, passages):
            return [(text, 1.0) for text in passages]  # stable incoming order; no LLM reranking

    config = LLMConfig(model=os.environ['GRAPHITI_LLM_MODEL'], small_model=os.environ['GRAPHITI_LLM_MODEL'],
                       base_url=os.environ['OPENAI_BASE_URL'], api_key=os.environ['OPENAI_API_KEY'])
    client = AsyncOpenAI(base_url=config.base_url, api_key=config.api_key)
    chat = SimpleNamespace(chat=SimpleNamespace(completions=GatewayCompletions(client, os.environ['GRAPHITI_LLM_REASONING_EFFORT'])))
    return OpenAIGenericClient(config=config, client=chat), LocalEmbedder(), NoReranker()


class Adapter(AdapterBase):
    def __init__(self, config):
        super().__init__(config)
        for key in ('GRAPHITI_LLM_MODEL', 'GRAPHITI_LLM_REASONING_EFFORT', 'OPENAI_BASE_URL', 'OPENAI_API_KEY', 'FASTEMBED_CACHE_PATH'):
            if not os.environ.get(key, '').strip():
                raise ValueError('graphiti gateway requires ' + key + ' via manifest environment')
        self.graph = None
        self.ns = None
        self.sequence = 0
        self.sources = {}

    def describe(self):
        from graphiti_core import Graphiti
        from redislite import AsyncFalkorDB  # falkordblite wheel installs module "redislite"
        from graphiti_core.driver.falkordb_driver import FalkorDriver
        return {"name": "graphiti-falkordblite", "version": "0.31.1", "capabilities": ["retrieve", "dump", "read_only_queries"],
                "read_only_queries": True, "config": {"llm_notes": ["reasoning_effort added to chat/completions by adapter wrapper; response_format schema normalised to OpenAI strict for the gateway; embeddings local fastembed; reranker disabled"]},
                "limitations": ["LLM: OpenAIGenericClient chat/completions via injected gateway; effort forwarded by adapter, max_completion_tokens replaces max_tokens and temperature omitted; costs unmetered",
                                "optional fields coerced to nullable for strict schema; unsupported validation keywords removed from gateway requests (warning once per client)",
                                "embedding: local fastembed BAAI/bge-small-en-v1.5 384 dimensions, local_files_only; no OpenAI embeddings",
                                "LLM cross-encoder reranking disabled: stable incoming order with constant score 1",
                                "reference_time is passed explicitly; absent timestamps use 2000-01-01 UTC",
                                "as_of/scope filtering unsupported", "FalkorDB Lite child executable must resolve inside env",
                                "dump contains persisted episodes, entities and fact edges; source-less facts are unsupported retrieval hits",
                                "driver binding and local process startup require real SDK smoke"]}

    async def reset(self, ns):
        if self.graph is None:
            from redislite import AsyncFalkorDB  # falkordblite wheel installs module "redislite"
            from graphiti_core import Graphiti
            from graphiti_core.driver.falkordb_driver import FalkorDriver
            # The local client owns the embedded child. No external DB service.
            # macOS limits Unix socket paths to 104 bytes; redislite's default tmp subdir under the long episode
            # path overflows (F28), and AsyncFalkorDB drops unix_socket_path. Start the embedded server with the
            # sync client on a short socket directly under EP, then attach the async client via the registry.
            import redislite
            ep_dir = Path(os.environ["EP"])
            self._sync_db = redislite.FalkorDB(dbfilename=str(ep_dir / "graph.rdb"), unix_socket_path=str(ep_dir / "r.sock"))
            self.db = AsyncFalkorDB(dbfilename=str(ep_dir / "graph.rdb"))
            driver = FalkorDriver(falkor_db=self.db)
            llm, embedder, reranker = gateway_clients()
            self.graph = Graphiti(graph_driver=driver, llm_client=llm, embedder=embedder, cross_encoder=reranker)
            await self.graph.build_indices_and_constraints()
        from graphiti_core.utils.maintenance.graph_data_operations import clear_data
        await clear_data(self.graph.driver, group_ids=list({ns, self.ns} - {None}))
        self.ns, self.sequence, self.sources = ns, 0, {}

    async def ingest(self, ns, events):
        from graphiti_core.nodes import EpisodeType
        for e in events:
            stamp = datetime.fromisoformat(e["timestamp"].replace("Z", "+00:00")) if e.get("timestamp") else datetime(2000, 1, 1, tzinfo=timezone.utc)
            result = await self.graph.add_episode(name=e["id"], episode_body=e["role"] + ": " + e["content"],
                                                 source_description=e.get("session_id", ""), reference_time=stamp,
                                                 source=EpisodeType.message, group_id=ns)
            self.sources[result.episode.uuid] = e["id"]
        self.sequence += 1
        return handle(self.sequence, len(events))

    async def ready(self, ns, h, deadline):
        return visible()

    async def retrieve(self, ns, req):
        # as_of/scope are ignored (no point-in-time or sub-scope search in Graphiti search(); group_id already scopes);
        # disclosed in describe().limitations instead of rejecting the request.
        if req["budget"]["top_k"] <= 0:
            return {"items": []}
        rows = await self.graph.search(req["query"], group_ids=[ns], num_results=req["budget"]["top_k"])
        items = []
        for r in rows:
            refs = getattr(r, "episodes", [])
            sources = [self.sources[x] for x in refs if x in self.sources]
            # Partial provenance is not upgraded to complete provenance.
            if len(sources) != len(refs):
                sources = []
            items.append({"id": r.uuid, "text": r.fact, "score": None, "source_ids": sources})
        return limited(items, req)

    async def dump(self, ns):
        from graphiti_core.nodes import EpisodicNode, EntityNode
        from graphiti_core.edges import EntityEdge
        episodes = await EpisodicNode.get_by_group_ids(self.graph.driver, [ns])
        entities = await EntityNode.get_by_group_ids(self.graph.driver, [ns])
        edges = await EntityEdge.get_by_group_ids(self.graph.driver, [ns])
        return {"items": ([{"id": "episode:" + r.uuid, "text": r.content, "timestamp": r.valid_at.isoformat(), "layer": "episode"} for r in episodes]
                          + [{"id": "entity:" + r.uuid, "text": r.summary, "layer": "entity"} for r in entities]
                          + [{"id": "edge:" + r.uuid, "text": r.fact, "layer": "fact"} for r in edges])}

    async def close(self):
        if self.graph is not None:
            await self.graph.close()
            self.graph = None
