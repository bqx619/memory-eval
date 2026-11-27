#!/usr/bin/env python3
"""Transport fixture only: echoes public events, not a memory-quality baseline."""
import json
import os
import socket
import sys
import time

events = []
sequence = 0
namespace = None


def call(method, params):
    global events, sequence, namespace
    if method == "handshake":
        return {"protocol": "paeval-v2", "transport": "jsonrpc-stdio"}
    if method == "describe":
        return {"name": "echo", "version": "1", "capabilities": ["retrieve", "typed_context", "dump"],
                "limitations": ["Transport fixture: no semantic search, scope or as_of filtering"],
                "read_only_queries": True, "config": {}}
    if method == "reset":
        events, sequence = [], 0
        namespace = params["ns"]
        return {}
    if method == "ingest":
        batch = params["events"]
        events.extend(batch)
        sequence += 1
        return {"batch_seq": sequence, "accepted": len(batch), "filtered": 0, "failed": 0}
    if method == "ready":
        return {"visible": True, "stages": [{"name": "memory", "status": "ok"}], "warnings": []}
    if method == "dump":
        if params["ns"] != namespace or namespace is None:
            raise ValueError("namespace is not active")
        return {"items": [{"id": e["id"], "text": e["content"], "timestamp": e.get("timestamp", ""), "layer": "raw"} for e in events]}
    if method == "retrieve":
        request = params["request"]
        budget = request["budget"]
        cap = min(budget["top_k"], budget.get("maxBeliefs", 20))
        remaining = budget["max_context_chars"]
        items = []
        for event in events:
            if len(items) >= cap:
                break
            if len(event["content"]) > remaining:
                continue
            items.append({"id": event["id"], "text": event["content"], "source_ids": [event["id"]]})
            remaining -= len(event["content"])
        if request["output_format"] == "typed_context":
            return {"tiers": {"pinnedFacts": [], "relevantBeliefs": items, "openQuestions": []}, "personaPrelude": ""}
        return {"items": items}
    if method == "echo":
        return params
    if method == "sleep":
        time.sleep(params["ms"] / 1000)
        return {}
    if method == "big":
        return {"text": "x" * params["bytes"]}
    if method == "crash":
        print("echo deliberate crash; token=fixture-secret", file=sys.stderr, flush=True)
        os._exit(17)
    if method == "write_outside":
        with open(params["path"], "x") as stream:
            stream.write("probe")
        return {"written": True}
    if method == "connect":
        with socket.create_connection((params["host"], params["port"]), timeout=1):
            return {"connected": True}
    if method == "close":
        return {}
    raise ValueError("unknown method")


for line in sys.stdin:
    request = json.loads(line)
    try:
        response = {"jsonrpc": "2.0", "id": request["id"], "result": call(request["method"], request.get("params", {}))}
    except Exception as error:
        response = {"jsonrpc": "2.0", "id": request.get("id"),
                    "error": {"code": -32000, "message": str(error), "data": {"type": type(error).__name__}}}
    print(json.dumps(response, ensure_ascii=False), flush=True)
    if request["method"] == "close":
        break
