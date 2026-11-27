import argparse
import importlib
import json
import os
import sys
import traceback
from . import Dispatcher, Unsupported
from .errors import error_data


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--adapter", required=True)
    p.add_argument("--config", default="{}")
    p.add_argument("--max-frame-bytes", type=int, default=16 * 1024 * 1024)
    args = p.parse_args()
    if not 1 <= args.max_frame_bytes <= 256 * 1024 * 1024:
        raise ValueError("invalid frame limit")
    # Reserve the original wire descriptor, then redirect SDK/native stdout.
    wire = os.fdopen(os.dup(1), "wb", buffering=0)
    os.dup2(2, 1)
    sys.stdout = sys.stderr
    module, cls = args.adapter.split(":", 1)
    d = Dispatcher(getattr(importlib.import_module(module), cls)(json.loads(args.config)))
    try:
        while True:
            raw = sys.stdin.buffer.readline(args.max_frame_bytes + 1)
            if not raw:
                break
            req = {}
            try:
                if len(raw) > args.max_frame_bytes:
                    raise ValueError("frame too large")
                req = json.loads(raw)
                if not isinstance(req, dict) or req.get("jsonrpc") != "2.0" or "id" not in req:
                    raise ValueError("invalid JSON-RPC request")
                result = d.call(req["method"], req.get("params", {}))
                reply = {"jsonrpc": "2.0", "id": req["id"], "result": result}
            except Exception as err:
                traceback.print_exc(file=sys.stderr)
                reply = {"jsonrpc": "2.0", "id": req.get("id") if isinstance(req, dict) else None,
                         "error": {"code": -32601 if isinstance(err, Unsupported) else -32000,
                                   "message": str(err), "data": error_data(err)}}
            try:
                data = json.dumps(reply, ensure_ascii=False, allow_nan=False).encode() + b"\n"
            except (TypeError, ValueError) as err:
                data = json.dumps({"jsonrpc": "2.0", "id": req.get("id"), "error": {"code": -32000, "message": str(err)}}).encode() + b"\n"
            if len(data) > args.max_frame_bytes:
                data = json.dumps({"jsonrpc": "2.0", "id": req.get("id"), "error": {"code": -32000, "message": "response frame too large"}}).encode() + b"\n"
            wire.write(data)
            if len(raw) > args.max_frame_bytes or isinstance(req, dict) and req.get("method") == "close":
                break
    finally:
        d.invoke(d.adapter.close)
        d.loop.close()
        wire.close()


if __name__ == "__main__":
    main()
