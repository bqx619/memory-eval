"""Standard-library-only, sequential paeval-v2 bridge."""
import inspect
import asyncio


class Unsupported(NotImplementedError):
    pass


class AdapterBase:
    def __init__(self, config):
        self.config = config

    def describe(self):
        raise Unsupported("describe")

    def reset(self, ns):
        raise Unsupported("reset")

    def ingest(self, ns, events):
        raise Unsupported("ingest")

    def ready(self, ns, handle, deadline):
        raise Unsupported("ready")

    def retrieve(self, ns, req):
        raise Unsupported("retrieve")

    def answer(self, ns, req):
        raise Unsupported("answer")

    def dump(self, ns):
        raise Unsupported("dump")

    def close(self):
        pass


class Dispatcher:
    def __init__(self, adapter):
        self.adapter = adapter
        self.ns = None
        self.sources = set()
        self.handles = {}
        self.loop = asyncio.new_event_loop()

    def invoke(self, fn, *args):
        value = fn(*args)
        return self.loop.run_until_complete(value) if inspect.isawaitable(value) else value

    def call(self, method, p):
        a = self.adapter
        if method == "handshake":
            return {"protocol": "paeval-v2", "transport": "jsonrpc-stdio"}
        if method == "describe":
            return self.invoke(a.describe)
        if method == "close":
            self.invoke(a.close)
            return {}
        if method == "reset":
            if not isinstance(p.get("ns"), str) or not p["ns"]:
                raise ValueError("namespace required")
            self.invoke(a.reset, p["ns"])
            self.ns, self.sources, self.handles = p["ns"], set(), {}
            return {}
        if self.ns is None or p.get("ns") != self.ns:
            raise ValueError("namespace is not active")
        if method == "ingest":
            events = p["events"]
            ids = [e["id"] for e in events]
            if len(set(ids)) != len(ids) or self.sources.intersection(ids):
                raise ValueError("duplicate event id")
            h = self.invoke(a.ingest, self.ns, events)
            if any(type(h.get(k)) is not int or h[k] < 0 for k in ("batch_seq", "accepted", "filtered", "failed")) or sum(h[k] for k in ("accepted", "filtered", "failed")) != len(events):
                raise ValueError("ingest accounting mismatch")
            if h["batch_seq"] in self.handles:
                raise ValueError("duplicate batch sequence")
            self.sources.update(ids)
            self.handles[h["batch_seq"]] = h
            return h
        if method == "ready":
            h = p["handle"]
            if self.handles.get(h.get("batch_seq")) != h:
                raise ValueError("unknown ingest handle")
            return self.invoke(a.ready, self.ns, h, p.get("deadline"))
        if method in ("retrieve", "answer"):
            r = self.invoke(getattr(a, method), self.ns, p["request"])
            if method == "retrieve":
                items = list(r.get("items", []))
                for tier in r.get("tiers", {}).values():
                    items += tier
                for item in items:
                    ids = item.get("source_ids")
                    if not isinstance(ids, list) or any(not isinstance(x, str) for x in ids):
                        raise ValueError("source_ids must be a list of strings")
                    if len(set(ids)) != len(ids) or not set(ids).issubset(self.sources):
                        raise ValueError("invalid source_ids: unknown/future or duplicate citation")
                    # [] means genuinely unattributed. The runner decides
                    # whether the selected answer mode can use that text.
            return r
        if method == "dump":
            r = self.invoke(a.dump, self.ns)
            ids = [i["id"] for i in r["items"]]
            if len(set(ids)) != len(ids) or any(not isinstance(i.get("text"), str) for i in r["items"]):
                raise ValueError("invalid dump")
            r["items"].sort(key=lambda i: i["id"])
            return r
        raise Unsupported(method)


def handle(sequence, count):
    return {"batch_seq": sequence, "accepted": count, "filtered": 0, "failed": 0}


def visible():
    return {"visible": True, "stages": [{"name": "memory", "status": "ok"}], "warnings": []}


def limited(items, req):
    budget = req["budget"]
    limit = max(0, budget["top_k"])
    remaining = max(0, budget["max_context_chars"])
    out = []
    for item in items:
        if len(out) >= limit:
            break
        if len(item["text"]) > remaining:
            continue
        out.append(item)
        remaining -= len(item["text"])
    return {"items": out}
