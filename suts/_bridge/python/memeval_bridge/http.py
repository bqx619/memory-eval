"""Declarative HTTP bridge for container service endpoints. JSONPath subset: $.key and [index]."""
import copy
import http.client
import json
import os
import re
import socket
import time
from urllib.parse import quote, urlencode, urlsplit
from . import AdapterBase, Unsupported, handle, visible, limited


class HTTPError(RuntimeError):
    def __init__(self, status, body=b'', headers=None):
        self.status, self.body, self.headers = status, body.decode('utf-8', errors='replace'), headers or {}
        super().__init__('local HTTP status ' + str(status) + ': ' + self.body)


def timeout_seconds(config):
    rpc_timeout = config.get('request_timeout_s')
    default = max(1, rpc_timeout - 5) if rpc_timeout is not None else 30
    return config.get('timeout_s', default)


def select(value, path):
    if path in ("$", ""):
        return value
    if not path.startswith("$."):
        raise ValueError("JSONPath must start with $.")
    for key, index in re.findall(r"(?:^|\.)([A-Za-z_][\w-]*)|\[(\d+)\]", path[2:]):
        value = value[int(index)] if index else value[key]
    # Reject unsupported grammar instead of silently evaluating a partial path.
    if not re.fullmatch(r"\$\.[A-Za-z_][\w-]*(?:\.[A-Za-z_][\w-]*|\[\d+\])*", path):
        raise ValueError("unsupported JSONPath")
    return value


def render(value, slots):
    if isinstance(value, dict):
        if set(value) == {"$"}:
            return select(slots, "$." + value["$"])
        return {k: render(v, slots) for k, v in value.items()}
    if isinstance(value, list):
        return [render(v, slots) for v in value]
    return value


class UnixConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self.socket_path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.socket_path)


class Adapter(AdapterBase):
    def __init__(self, config):
        super().__init__(config)
        with open(config["routes"], encoding="utf-8") as f:
            self.routes = json.load(f)
        if self.routes.get("version") != 1:
            raise ValueError("HTTP routes version 1 required")
        self.base = config["base_url"]
        if any(c.isspace() for c in self.base) or "?" in self.base or "#" in self.base:
            raise ValueError("HTTP bridge base_url must not contain whitespace, query or fragment")
        self.seq = 0
        self.headers = {"Content-Type": "application/json"}
        keys = config.get("headers_env") or []
        if len(keys) > 1:
            raise ValueError("generic bearer bridge accepts at most one headers_env key; use adapter override for other authentication")
        if keys and os.environ.get(keys[0]):
            self.headers["Authorization"] = "Bearer " + os.environ[keys[0]]
        if self.base.startswith("http://unix:"):
            self.socket_path = self.base[len("http://unix:"):]
            if not os.path.isabs(self.socket_path):
                raise ValueError("absolute Unix socket required")
            self.address = None
        else:
            u = urlsplit(self.base)
            if (u.scheme != 'http' or not u.hostname or not u.port or u.username is not None
                    or u.password is not None or u.path not in ('', '/')
                    or not re.fullmatch(r'[A-Za-z0-9.:-]+', u.hostname)):
                raise ValueError('HTTP bridge requires http://host:port without userinfo, query or fragment')
            self.address, self.socket_path = (u.hostname, u.port), None

    def describe(self):
        d = copy.deepcopy(self.routes['describe'])
        host = self.address[0] if self.address else 'unix'
        d.setdefault('limitations', []).append('HTTP bridge host=' + host + '; address syntax only, network isolation is enforced by the container')
        d.setdefault('config', {})['bridge_base_url_host'] = host
        return d

    def request(self, route, slots):
        path = re.sub(r"\{([A-Za-z_][\w.]*)\}", lambda m: quote(str(select(slots, "$." + m[1])), safe=""), route["path"])
        if not path.startswith("/") or path.startswith("//"):
            raise ValueError("route path must be relative to local service")
        query = {k: v for k, v in render(route.get("query", {}), slots).items() if v is not None}
        if query:
            path += ("&" if "?" in path else "?") + urlencode(query)
        data = json.dumps(render(route["body"], slots), ensure_ascii=False).encode() if "body" in route else None
        timeout = timeout_seconds(self.config)
        conn = UnixConnection(self.socket_path, timeout) if self.socket_path else http.client.HTTPConnection(*self.address, timeout=timeout)
        try:
            conn.request(route.get("method", "POST"), path, data, self.headers)
            response = conn.getresponse()
            raw = response.read(16 * 1024 * 1024 + 1)
            if len(raw) > 16 * 1024 * 1024:
                raise ValueError("HTTP response frame too large")
            if not 200 <= response.status < 300:
                raise HTTPError(response.status, raw, dict(response.getheaders()))
            return json.loads(raw) if raw else {}
        finally:
            conn.close()

    def call(self, name, slots):
        route = self.routes.get("methods", {}).get(name)
        if route is None:
            raise Unsupported(name)
        slots = dict(slots, page=0, offset=0, cursor=None)
        pagination = route.get("pagination")
        limit = pagination.get("limit", 100) if pagination else 0
        slots["limit"] = limit
        out = []
        seen = set()
        for page in range(pagination.get("max_pages", 100) if pagination else 1):
            slots["page"], slots["offset"] = page, page * limit
            response = self.request(route, slots)
            if "items" not in route:
                return render(route.get("response", {"$": "response"}), dict(slots, response=response))
            items = select(response, route["items"])
            if not isinstance(items, list):
                raise ValueError("HTTP items mapping must be a list")
            for item in items:
                mapped = {}
                for key, path in route["map"].items():
                    try:
                        mapped[key] = select(item, path)
                    except KeyError:
                        if key != 'source_ids':
                            raise
                        mapped[key] = []  # Observation has no provenance; runner selects answer policy.
                    if key == "source_ids" and mapped[key] is None:
                        mapped[key] = []  # Explicit null also means no provenance in an HTTP mapping.
                if not isinstance(mapped.get("id"), str) or mapped["id"] in seen:
                    raise ValueError("duplicate/invalid HTTP memory id")
                seen.add(mapped["id"])
                if isinstance(mapped.get("source_ids"), str):
                    mapped["source_ids"] = [mapped["source_ids"]]
                out.append(mapped)
            if not pagination:
                return {"items": out}
            if pagination.get("kind", "offset") == "cursor":
                slots["cursor"] = select(response, pagination["next"])
                if slots["cursor"] in (None, ""):
                    return {"items": out}
            elif len(items) < limit:
                return {"items": out}
        if pagination:
            raise RuntimeError("HTTP pagination exhausted; refusing partial result")
        return {"items": out}

    def reset(self, ns):
        self.call("reset", {"ns": ns})
        self.seq = 0

    def ingest(self, ns, events):
        route = self.routes["methods"]["ingest"]
        if route.get("foreach") == "events":
            for event in events:
                self.call("ingest", {"ns": ns, "event": event, "events": events})
            response = {}
        else:
            response = self.call("ingest", {"ns": ns, "events": events})
        self.seq += 1
        h = handle(self.seq, len(events))
        for k in ("accepted", "filtered", "failed"):
            if k in response:
                h[k] = response[k]
        return h

    def ready(self, ns, h, deadline):
        if "ready" not in self.routes["methods"]:
            if self.routes.get("ready_strategy") != "sync":
                raise Unsupported("ready: no verified visibility strategy")
            return visible()
        route = self.routes["methods"]["ready"]
        end = time.monotonic() + self.config.get("ready_timeout_s", timeout_seconds(self.config))
        if deadline:
            from datetime import datetime
            end = min(end, time.monotonic() + max(0, datetime.fromisoformat(deadline.replace("Z", "+00:00")).timestamp() - time.time()))
        while True:
            r = self.call("ready", {"ns": ns, "handle": h, "deadline": deadline})
            if r.get("visible") is True:
                return dict(r, visibility="weak")
            if time.monotonic() >= end:
                raise TimeoutError("HTTP visibility deadline")
            time.sleep(0.05)

    def retrieve(self, ns, req):
        return limited(self.call("retrieve", {"ns": ns, "request": req})["items"], req)

    def answer(self, ns, req):
        return self.call("answer", {"ns": ns, "request": req})

    def dump(self, ns):
        return self.call("dump", {"ns": ns})
