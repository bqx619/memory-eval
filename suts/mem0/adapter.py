import os
import re
from pathlib import Path
from memeval_bridge import AdapterBase, Unsupported, handle, visible, limited


EMBEDDING_LIMITATION = (
    'embedding vectors use at most configured max_input_chars Unicode characters per text; '
    'maximum context/input length or too many tokens errors halve only the embedding input until success or a failed input is shorter than 512 characters; '
    'batch oversize errors are isolated per text; stored memory text and source metadata are unchanged; '
    'dump.embed_stats counts capped inputs and halving retries for this adapter process')


def is_oversize_error(error):
    return re.search(r'\bmaximum\s+(?:context|input)\s+length\b|\btoo\s+many\s+tokens\b',
                     str(error), re.IGNORECASE) is not None


class BoundedEmbedder:
    """Keep SDK configuration/API intact while bounding vector inputs only."""
    def __init__(self, inner, max_input_chars, stats):
        self.inner, self.max_input_chars, self.stats = inner, max_input_chars, stats

    def __getattr__(self, name):
        return getattr(self.inner, name)

    def cap(self, text):
        if self.max_input_chars is not None and len(text) > self.max_input_chars:
            self.stats['capped'] += 1
            return text[:self.max_input_chars]
        return text

    def retry(self, text, call):
        while True:
            try:
                return call(text)
            except Exception as error:
                if not is_oversize_error(error) or len(text) < 512:
                    raise
                text = text[:len(text) // 2]
                self.stats['halved'] += 1

    def embed(self, text, *args, **kwargs):
        return self.retry(self.cap(text), lambda value: self.inner.embed(value, *args, **kwargs))

    def embed_batch(self, texts, *args, **kwargs):
        texts = [self.cap(text) for text in texts]
        if len(texts) == 1:
            return self.retry(texts[0], lambda value: self.inner.embed_batch([value], *args, **kwargs))
        try:
            return self.inner.embed_batch(texts, *args, **kwargs)
        except Exception as error:
            if not texts or not is_oversize_error(error):
                raise
        # A batch error does not identify its offending input. Isolate texts
        # before shortening so valid neighbours retain their original vectors.
        result = []
        for text in texts:
            result.extend(self.retry(text, lambda value: self.inner.embed_batch([value], *args, **kwargs)))
        return result


class Adapter(AdapterBase):
    def __init__(self, config):
        super().__init__(config)
        self.embedder = {'provider': 'fastembed', 'model': 'BAAI/bge-small-en-v1.5',
                         'dims': 384, 'local': True, 'max_input_chars': None}
        embedder = config.get('embedder')
        if embedder is not None:
            if not isinstance(embedder, dict) or embedder.get('provider') not in ('fastembed', 'openai'):
                raise ValueError('mem0 embedder.provider must be fastembed or openai')
            if not isinstance(embedder.get('model'), str) or not embedder['model'].strip():
                raise ValueError('mem0 embedder.model is required')
            if type(embedder.get('dims')) is not int or embedder['dims'] <= 0:
                raise ValueError('mem0 embedder.dims must be a positive integer')
            cap = embedder.get('max_input_chars')
            if cap is not None and (type(cap) is not int or cap <= 0):
                raise ValueError('mem0 embedder.max_input_chars must be a positive integer')
            if embedder['provider'] == 'openai':
                for key in ('base_url', 'api_key_env'):
                    if not isinstance(embedder.get(key), str) or not embedder[key].strip():
                        raise ValueError('mem0 embedder.' + key + ' is required')
            self.embedder = {key: embedder.get(key) for key in ('provider', 'model', 'dims', 'max_input_chars')}
            self.embedder['local'] = embedder['provider'] == 'fastembed'
        self.embed_stats = {'capped': 0, 'halved': 0}
        if config.get('infer'):
            for key in ('MEM0_LLM_MODEL', 'MEM0_LLM_REASONING_EFFORT', 'OPENAI_BASE_URL', 'OPENAI_API_KEY'):
                if not os.environ.get(key, '').strip():
                    raise ValueError('mem0 infer requires ' + key + ' via manifest llm.env')
        self.memory = None
        self.ns = None
        self.sequence = 0
        self.sources = {}
        self.times = {}

    def describe(self):
        from mem0 import Memory  # import preflight inside the sandbox
        return {"name": "mem0", "version": "2.2.1", "capabilities": ["retrieve", "dump", "read_only_queries"],
                "read_only_queries": True, "config": {"infer": self.config.get("infer", False),
                    "embedder": dict(self.embedder),
                    "llm_notes": ["reasoning_effort forwarded via mem0 OpenAIConfig.reasoning_effort (is_reasoning_model=True); embeddings " +
                                  ('local fastembed' if self.embedder['local'] else 'remote ' + self.embedder['provider'] + ' ' + self.embedder['model'])] if self.config.get('infer') else []},
                "limitations": ["as_of ignored: OSS has no timestamp argument; timestamps stored in metadata.created_at",
                                ('LLM uses injected OpenAI-compatible chat/completions; explicit reasoning effort selects SDK reasoning parameter branch; provider calls unmetered'
                                 if self.config.get('infer') else 'direct provider traffic is unmetered'), "source_ids require returned metadata or observed add result IDs",
                                "get_all top_k exhaustion is required for complete dump; real SDK smoke pending",
                                EMBEDDING_LIMITATION]}

    def reset(self, ns):
        if self.memory is None:
            embedding_key = None
            if self.embedder['provider'] == 'openai':
                key_name = self.config['embedder']['api_key_env']
                embedding_key = os.environ.get(key_name)
                if not embedding_key or not embedding_key.strip():
                    raise ValueError('mem0 OpenAI embedder 需要 manifest env_passthrough 透传 ' + key_name)
            # mem0's fastembed embedder ignores model_kwargs.cache_dir; fastembed reads FASTEMBED_CACHE_PATH.
            os.environ.setdefault("FASTEMBED_CACHE_PATH", str(Path(__file__).resolve().parents[1] / "models"))
            if not self.config.get("infer", False):
                # mem0 OSS constructs an OpenAI LLM client at init even with infer=False; a placeholder key avoids
                # the credential check. The explicit LLM config below keeps a
                # real embedding credential out of that unused LLM client.
                os.environ.setdefault("OPENAI_API_KEY", "memeval-placeholder-no-llm")
            from mem0 import Memory
            ep = Path(os.environ["EP"])
            settings = {"vector_store": {"provider": "qdrant", "config": {"collection_name": "memeval", "path": str(ep / "qdrant"), "embedding_model_dims": 384}},
                        "history_db_path": str(ep / "history.sqlite"),
                        "embedder": {"provider": "fastembed", "config": {"model": "BAAI/bge-small-en-v1.5",
                                     "model_kwargs": {"cache_dir": str(Path(__file__).resolve().parents[1] / "models"), "local_files_only": True}}}}
            settings['vector_store']['config']['embedding_model_dims'] = self.embedder['dims']
            if self.embedder['provider'] == 'openai':
                settings['embedder'] = {'provider': 'openai', 'config': {
                    'model': self.embedder['model'], 'embedding_dims': self.embedder['dims'],
                    'api_key': embedding_key, 'openai_base_url': self.config['embedder']['base_url']}}
            else:
                settings['embedder']['config']['model'] = self.embedder['model']
            if self.config.get("infer", False):
                settings["llm"] = {"provider": "openai", "config": {'model': os.environ['MEM0_LLM_MODEL'],
                                                               'openai_base_url': os.environ['OPENAI_BASE_URL'],
                                                               'api_key': os.environ['OPENAI_API_KEY'],
                                                               'reasoning_effort': os.environ['MEM0_LLM_REASONING_EFFORT'],
                                                               'is_reasoning_model': True}}
            else:
                settings['llm'] = {'provider': 'openai', 'config': {'api_key': 'memeval-placeholder-no-llm'}}
            self.memory = Memory.from_config(settings)
            embedder = getattr(self.memory, 'embedding_model', None)
            if embedder is not None:
                self.memory.embedding_model = BoundedEmbedder(embedder, self.embedder['max_input_chars'], self.embed_stats)
        if self.ns is not None:
            self.memory.delete_all(user_id=self.ns)
        self.memory.delete_all(user_id=ns)
        self.ns, self.sequence, self.sources, self.times = ns, 0, {}, {}

    def ingest(self, ns, events):
        for e in events:
            metadata = {"source_ids": [e["id"]], "created_at": e.get("timestamp", ""), "session_id": e.get("session_id", "")}
            result = self.memory.add([{"role": e["role"], "content": e["content"]}], user_id=ns,
                                     run_id=e.get("session_id") or ns, metadata=metadata, infer=self.config.get("infer", False))
            rows = result.get("results", []) if isinstance(result, dict) else result
            for row in rows:
                if row.get("id"):
                    self.sources.setdefault(row["id"], set()).add(e["id"])
                    self.times[row["id"]] = e.get("timestamp", "")
        self.sequence += 1
        return handle(self.sequence, len(events))

    def ready(self, ns, h, deadline):
        return visible()

    def rows(self, value):
        return value.get("results", []) if isinstance(value, dict) else value

    def item(self, row):
        metadata = row.get("metadata") or {}
        sources = set(metadata.get("source_ids") or []) | self.sources.get(row["id"], set())
        return {"id": row["id"], "text": row["memory"], "source_ids": sorted(sources),
                "score": row.get("score"), "timestamp": metadata.get("created_at") or self.times.get(row["id"], "")}

    def retrieve(self, ns, req):
        if not (req.get("query") or "").strip():
            # mem0 search() rejects empty/whitespace queries; PMB boundary cases expect an empty result set.
            return {"items": []}
        # as_of is ignored (mem0 OSS has no time filter) and scope is implied by the per-namespace user_id;
        # both are disclosed in describe().limitations instead of rejecting the request.
        if req["budget"]["top_k"] <= 0:
            return {"items": []}
        rows = self.rows(self.memory.search(req["query"], top_k=req["budget"]["top_k"], filters={"user_id": ns}))
        return limited([self.item(row) for row in rows], req)

    def dump(self, ns):
        limit = 128
        while True:
            rows = self.rows(self.memory.get_all(filters={"user_id": ns}, top_k=limit))
            if len(rows) < limit:
                break
            limit *= 2
        return {"embed_stats": dict(self.embed_stats), "limitations": [EMBEDDING_LIMITATION],
                "items": [{"id": r["id"], "text": r["memory"], "layer": "extracted" if self.config.get("infer") else "raw",
                           "timestamp": (r.get("metadata") or {}).get("created_at", "")} for r in rows]}

    def close(self):
        if self.memory is not None:
            client = getattr(getattr(self.memory, "vector_store", None), "client", None)
            if client is not None:
                client.close()
            self.memory = None
