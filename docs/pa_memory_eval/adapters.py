"""Three transparent baselines plus Python factory and JSON-over-HTTP integration."""
from __future__ import annotations
from collections import Counter
import copy
import importlib
import json
import math
import os
import re
import urllib.parse
import urllib.request
import urllib.error
from typing import Any
from .core import ContractError, UnsupportedCapability

# Deliberately generic, not learned or tuned on the bundled fixture labels.
STOP = set("a an the is are was were be been to of for from in on at and or as by with my me i we our you your it this that these those how what why when where should would can could do does did about any all so use using us its into has have had tell give please".split())

def tokens(text: str) -> list[str]:
    return [x for x in re.findall(r"[a-z0-9]+|[\u3400-\u9fff]", text.lower()) if x not in STOP]


def bm25_scores(query: str, documents: list[str]) -> list[float]:
    """Okapi BM25 with k1=1.5, b=.75, generic tokenization and positive IDF."""
    if not documents:
        return []
    docs = [tokens(x) for x in documents]
    qt = set(tokens(query))
    counts = [Counter(x) for x in docs]
    df = Counter(w for doc in docs for w in set(doc))
    avg = sum(map(len, docs)) / len(docs) or 1
    n = len(docs)
    out = []
    for doc, c in zip(docs, counts):
        s = 0.0
        for word in qt:
            tf = c.get(word, 0)
            if tf:
                idf = math.log(1 + (n - df[word] + .5) / (df[word] + .5))
                s += idf * tf * 2.5 / (tf + 1.5 * (.25 + .75 * len(doc) / avg))
        out.append(s)
    return out


class BaselineAdapter:
    """BM25, recent, or empty context; no LLM, hidden cases, or answer generator.

    typed_context is a fixture-specific structured-state diagnostic track.
    Filtering is implemented HERE in the system under test, not in the scorer.
    'recent' is most recently ingested, not a claim about source event timestamps.
    """
    def __init__(self, strategy: str = "bm25", **kwargs: Any):
        if strategy not in {"bm25", "recent", "null"}:
            raise ValueError("Unknown baseline")
        self.strategy = strategy
        self.store: dict[str, list[dict[str, Any]]] = {}

    def describe(self):
        return {"name": self.strategy, "version": "0.1.0", "capabilities": ["retrieve", "typed_context"],
                "read_only_queries": True, "implementation": "stdlib lexical diagnostic baseline",
                "config": {"k1": 1.5, "b": .75, "positive_score_only": True,
                           "policy": "metadata owner/scope/state; no learned judgments"}}

    def reset(self, namespace):
        self.store[namespace] = []

    def ingest(self, namespace, events):
        if namespace not in self.store:
            raise ContractError("reset namespace before ingest")
        old = {e["id"]: e for e in self.store[namespace]}
        for e in events:
            old[e["id"]] = copy.deepcopy(e)
        self.store[namespace] = list(old.values())

    def ready(self, namespace):
        if namespace not in self.store:
            raise ContractError("Unknown namespace")

    @staticmethod
    def _item(e, score=None):
        return {"id": e["id"], "text": e["content"], "source_ids": [e["id"]], "score": score}

    def _rank(self, query, events):
        if not query.strip():
            return []
        if self.strategy == "recent":
            return [(e, None) for e in reversed(events)]
        docs = [" ".join([e["content"], e.get("attributes", {}).get("canonical_name", "").replace("_", " "),
                          *e.get("attributes", {}).get("aliases", [])]) for e in events]
        scores = bm25_scores(query, docs)
        return [(events[i], scores[i]) for i in sorted(range(len(events)), key=lambda i: (-scores[i], i)) if scores[i] > 0]

    def retrieve(self, namespace, request):
        mode = request.get("output_format", "retrieval")
        owner = request["user"]
        eligible = [e for e in self.store[namespace] if e["owner"] == owner]
        k = request.get("budget", {}).get("top_k", 10)
        cap = request.get("budget", {}).get("max_context_chars", 100_000)
        if self.strategy == "null":
            return {"items": [], **({"tiers": {"pinnedFacts": [], "relevantBeliefs": [], "openQuestions": []},
                                     "personaPrelude": ""} if mode == "typed_context" else {})}
        if mode != "typed_context":
            items = []
            used = 0
            for e, score in self._rank(request["query"], eligible):
                if len(items) >= k:
                    break
                if used + len(e["content"]) <= cap:
                    items.append(self._item(e, score)); used += len(e["content"])
            return {"items": items}
        scopes = set(request.get("scope", []))
        active = [e for e in eligible if e.get("attributes", {}).get("epistemic_status") not in {"superseded", "archived"}
                  and not e.get("attributes", {}).get("resolved_at") and not e.get("attributes", {}).get("superseded_by")]
        universal = [e for e in active if "user:universal" in e["attributes"].get("scope", [])]
        domain = [e for e in active if e not in universal and scopes.intersection(e["attributes"].get("scope", []))]
        prelude = "\n".join(e["content"] for e in universal)
        # Prefix trimming is explicit in the baseline, and measured by the runner.
        prelude = prelude[:cap]
        remaining = cap - len(prelude)
        max_beliefs = request.get("budget", {}).get("maxBeliefs", 20)
        tiers = {"pinnedFacts": [], "relevantBeliefs": [], "openQuestions": []}
        pinned = [e for e in domain if e["attributes"].get("pinned") and e["attributes"].get("type") != "open_question"]
        questions = [e for e in domain if e["attributes"].get("pinned") and e["attributes"].get("type") == "open_question"]
        candidates = [e for e in domain if not e["attributes"].get("pinned") and e["attributes"].get("type") != "open_question"]
        count = 0
        for tier, rows in [("pinnedFacts", [(e, None) for e in pinned]),
                           ("openQuestions", [(e, None) for e in questions]),
                           ("relevantBeliefs", self._rank(request["query"], candidates)[:k])]:
            for e, score in rows:
                if count < max_beliefs and len(e["content"]) <= remaining:
                    tiers[tier].append(self._item(e, score)); count += 1; remaining -= len(e["content"])
        return {"items": [], "tiers": tiers, "personaPrelude": prelude}

    def answer(self, namespace, request):
        raise UnsupportedCapability("Baseline has no answer model; do not score extracted snippets as generated answers")

    def close(self):
        self.store.clear()


class HTTPAdapter:
    """POST JSON {method, namespace, payload} to a user-owned bridge endpoint.

    Never points at an arbitrary PA's API without a bridge implementing this protocol.
    Credentials come only from named environment variable. No retries for write operations.
    HTTPS is required except explicit local-loopback testing; redirects are rejected.
    """
    def __init__(self, endpoint: str, token_env: str | None = None, timeout: float = 60,
                 allow_http_local: bool = False, allow_data_egress: bool = False):
        url = urllib.parse.urlsplit(endpoint)
        if url.username or url.password or url.fragment:
            raise ValueError("Credential-bearing/fragment endpoint URLs are rejected")
        local = url.hostname in {"127.0.0.1", "localhost", "::1"}
        if url.scheme != "https" and not (url.scheme == "http" and local and allow_http_local):
            raise ValueError("HTTPS required; opt in to loopback HTTP for local tests")
        if not local and not allow_data_egress:
            raise ValueError("Remote endpoint needs allow_data_egress=true; histories will leave this process")
        if not 0 < timeout <= 3600:
            raise ValueError("timeout must be in (0,3600]")
        self.endpoint, self.timeout = endpoint, timeout
        self.token = os.environ.get(token_env, "") if token_env else ""
        if token_env and not self.token:
            raise ValueError(f"Credential environment variable {token_env} is not set")
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                raise ContractError("HTTP redirects are not allowed")
        self.opener = urllib.request.build_opener(NoRedirect())

    def _call(self, method, namespace=None, payload=None):
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        body = json.dumps({"method": method, "namespace": namespace, "payload": payload}, ensure_ascii=False).encode()
        req = urllib.request.Request(self.endpoint, data=body, headers=headers, method="POST")
        try:
            with self.opener.open(req, timeout=self.timeout) as response:
                raw = response.read(8_000_001)
        except urllib.error.HTTPError as e:
            if e.code == 501:
                raise UnsupportedCapability(f"Remote {method} unsupported") from None
            raise RuntimeError(f"Bridge HTTP {e.code}; response body omitted") from None
        if len(raw) > 8_000_000:
            raise ContractError("Bridge response exceeds 8MB")
        obj = json.loads(raw)
        if obj.get("status") == "unsupported":
            raise UnsupportedCapability(f"Remote {method} unsupported")
        if obj.get("status") != "ok":
            raise RuntimeError(f"Bridge reported failure for {method}; private body omitted")
        return obj.get("result")

    def describe(self): return self._call("describe")
    def reset(self, namespace): return self._call("reset", namespace)
    def ingest(self, namespace, events): return self._call("ingest", namespace, {"events": events})
    def ready(self, namespace): return self._call("ready", namespace)
    def retrieve(self, namespace, request): return self._call("retrieve", namespace, request)
    def answer(self, namespace, request): return self._call("answer", namespace, request)
    def close(self): pass  # No remote global close/reset; namespace cleanup is the bridge operator's policy.


def create_adapter(spec: str, config: dict | None = None):
    config = config or {}
    if spec in {"bm25", "recent", "null"}:
        return BaselineAdapter(spec, **config)
    if spec == "http":
        return HTTPAdapter(**config)
    if ":" not in spec:
        raise ValueError("Adapter must be bm25/recent/null/http or module:factory")
    module, factory = spec.split(":", 1)
    return getattr(importlib.import_module(module), factory)(**config)
