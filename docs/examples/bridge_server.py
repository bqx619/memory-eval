"""Loopback JSON bridge demo; replace factory with YOUR adapter.

Run: python examples/bridge_server.py --port 8765 --adapter bm25
This is a local development server, NOT an authenticated production deployment.
It refuses non-loopback listening and limits request size.
"""
from __future__ import annotations
import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import sys
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from pa_memory_eval.adapters import create_adapter
from pa_memory_eval.core import UnsupportedCapability


def make_server(adapter,port=8765):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args): pass
        def do_POST(self):
            if self.path!="/memory": self.send_error(404); return
            try:
                length=int(self.headers.get("Content-Length","0"))
                if not 0<length<=64_000_000: self.send_error(413); return
                obj=json.loads(self.rfile.read(length)); method=obj["method"]; ns=obj.get("namespace"); payload=obj.get("payload") or {}
                if method=="describe": result=adapter.describe()
                elif not isinstance(ns,str) or not ns.startswith("paeval_"): raise ValueError("Namespace required")
                elif method=="reset": result=adapter.reset(ns)
                elif method=="ingest": result=adapter.ingest(ns,payload["events"])
                elif method=="ready": result=adapter.ready(ns)
                elif method=="retrieve": result=adapter.retrieve(ns,payload)
                elif method=="answer": result=adapter.answer(ns,payload)
                else: raise UnsupportedCapability("Unsupported method")
                data={"status":"ok","result":result}; status=200
            except UnsupportedCapability:
                data={"status":"unsupported"}; status=501
            except Exception:
                data={"status":"error"}; status=500
            raw=json.dumps(data,ensure_ascii=False).encode()
            self.send_response(status); self.send_header("Content-Type","application/json"); self.send_header("Content-Length",str(len(raw))); self.end_headers(); self.wfile.write(raw)
    return HTTPServer(("127.0.0.1",port),Handler)

if __name__=="__main__":
    p=argparse.ArgumentParser(); p.add_argument("--port",type=int,default=8765); p.add_argument("--adapter",default="bm25")
    args=p.parse_args(); a=create_adapter(args.adapter); s=make_server(a,args.port)
    print(f"Local bridge on http://127.0.0.1:{s.server_port}/memory",flush=True)
    try:s.serve_forever()
    except KeyboardInterrupt:pass
    finally:s.server_close(); a.close()
