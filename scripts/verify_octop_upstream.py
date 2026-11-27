"""Run with the locked venv Python. Offline checks; never calls extract/LLM."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import inspect
import json
import os
from pathlib import Path
import subprocess
from datetime import datetime, timezone
UTC = timezone.utc
from typing import get_args

def main(args, work):
    from octop_memory.adapters.bridge import server
    from octop_memory.application.runtime import MemoryRuntime, _build_llm_client
    from octop_memory.ports.llm.openai_compat import OpenAICompatClient
    from octop_memory.types import RawEventType

    bridge = str(Path(sys.executable).parent / "octopmemory-bridge")
    repo = Path(__file__).resolve().parents[1]
    def globals_snapshot():
        roots = [Path.home() / ".octopmemory", Path.home() / ".octop-memory"]
        return {str(p): p.stat().st_mtime_ns for root in roots if root.exists()
                for p in [root, *root.rglob("*")]}

    before = globals_snapshot()
    facts = {}
    tmp = str(work.out / "runtime")
    Path(tmp).mkdir()
    if True:
        env = {"HOME": tmp, "XDG_CONFIG_HOME": tmp, "PATH": "/usr/bin:/bin",
               "PYTHONDONTWRITEBYTECODE": "1", "PYTHONHASHSEED": "0"}
        facts["probe"] = json.loads(subprocess.check_output([bridge, "--probe"], env=env))
        help_text = subprocess.check_output([bridge, "--help"], env=env, text=True)
        facts["flags"] = {k: k in help_text for k in ["--namespace", "--db-path", "--backend", "--config-json", "--probe", "--log-level"]}
        # --probe short circuits before argparse, so it is intentionally absent in --help.
        facts["flags"]["--probe"] = facts["probe"]["ok"]
        assert all(facts["flags"].values())
        cfg = {"capture": {"min_message_chars": 0, "include_roles": ["user", "assistant"]},
               "privacy": {"redact_secrets": False}, "recall": {"raw_policy": "always"}}
        db = str(Path(tmp) / "memory.sqlite")
        p = subprocess.Popen([bridge, "--namespace", "contract", "--db-path", db, "--backend", "sqlite", "--config-json", json.dumps(cfg), "--log-level", "warning"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env, cwd=tmp)
        rid = 0
        def call(method, params):
            nonlocal rid
            rid += 1
            p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params}) + "\n")
            p.stdin.flush()
            result = json.loads(p.stdout.readline())
            assert result["id"] == rid and result["jsonrpc"] == "2.0", result
            if "error" in result:
                return {"error": result["error"]}
            return result["result"]
        try:
            facts["handshake"] = call("handshake", {"client_version": "memeval-facts"})
            assert facts["handshake"]["namespace"] == "contract"
            facts["capture"] = call("capture", {"session_id": "session-contract", "thread_id": None, "user": None, "host": "memeval", "events": [
                {"id": "evt-a", "event_type": "user_message", "content": "quartzviolet telescope uses a cobalt lens", "timestamp_iso": "2023-05-08T13:56:00Z"},
                {"id": "evt-b", "event_type": "assistant_message", "content": "ok", "timestamp_iso": "2023-05-08T13:57:00Z"},
                {"id": "evt-think", "event_type": "user_message", "content": "<think>hidden</think> visible goldberry", "timestamp_iso": "invalid"},
                {"id": "evt-echo", "event_type": "assistant_message", "content": "## Memory Recall feedback"},
                {"id": "evt-tool", "event_type": "tool_result", "content": "tool result"},
            ]})
            assert facts["capture"] == {"accepted": 3, "skipped_recall_marker": 1, "skipped_too_short": 0, "skipped_role": 1, "skipped_tool_result": 0}
            facts["raw_search"] = call("memory_search", {"query": "quartzviolet", "maxResults": 10, "corpus": "memory"})
            hit = facts["raw_search"]["hits"][0]
            assert hit["source_id"] == "evt-a" and hit["path"] == "raw/2023-05-08/evt-a.md" and hit["score"] is None
            facts["raw_document"] = call("memory_get", {"path": hit["path"]})
            assert facts["raw_document"]["truncated"] is False
            converted = call("get_raw_event", {"event_id": "evt-think"})
            facts["converted_capture"] = converted
            assert converted["content"] == "visible goldberry", converted
            assert abs((datetime.now(UTC) - datetime.fromisoformat(converted["timestamp"])).total_seconds()) < 60
            for kind in ["merged", "page"]:
                subprocess.run([sys.executable, "-I", str(repo / "internal/adapter/octop/testdata/seed.py"), db, "contract", kind], env=env, cwd=tmp, check=True, capture_output=True)
            facts["promotion"] = call("promote_candidate", {"candidate_id": "candidate-contract"})
            atoms = call("memory_search", {"query": "quartzviolet", "maxResults": 10, "corpus": "memory"})
            atom = next(h for h in atoms["hits"] if h["layer"] == "atom")
            facts["atom"] = call("get_atom", {"atom_id": atom["source_id"]})
            assert set(facts["atom"]["raw_event_ids"]) == {"evt-a", "evt-b"}
            facts["page_search"] = call("memory_search", {"query": "Zirconia", "maxResults": 10, "corpus": "memory"})
            page = next(h for h in facts["page_search"]["hits"] if h["source_id"] == "page-contract")
            assert page["layer"] == "atom" and page["path"] == "atom/page-contract.md"
            facts["page_get_atom"] = call("get_atom", {"atom_id": page["source_id"]})
            facts["page_memory_get"] = call("memory_get", {"path": page["path"]})
            assert facts["page_get_atom"]["error"]["code"] == -32010
            assert facts["page_memory_get"]["error"]["code"] == -32010
            facts["stats"] = call("stats", {})
            assert facts["stats"]["counts"]["raw_events"] == 3
        finally:
            p.stdin.close()
            p.wait(timeout=5)
            facts["stderr"] = p.stderr.read()
            assert p.returncode == 0
        from octop_memory.application.runtime import _prepare_content, _parse_iso
        facts["transformations"] = {"paired": _prepare_content("<think>hidden</think>visible", {"redact_secrets": False}),
            "orphan_close": _prepare_content("hidden</think>visible", {"redact_secrets": False}),
            "unclosed_open": _prepare_content("visible<think>hidden", {"redact_secrets": False}),
            "redacted": _prepare_content("sk-" + "a" * 40, {"redact_secrets": True}),
            "invalid_time": _parse_iso("invalid")}
        assert facts["transformations"]["redacted"] == "[REDACTED]"
        # A synthetic environment key proves api_key_env is consumed; no HTTP method is called.
        os.environ["MEMEVAL_DUMMY_KEY"] = "fixture-only-not-a-credential"
        client = OpenAICompatClient.from_config({"endpoint": "http://127.0.0.1:1/v1", "model": "fixture", "api_key_env": "MEMEVAL_DUMMY_KEY"})
        assert client._api_key == "fixture-only-not-a-credential"
        del os.environ["MEMEVAL_DUMMY_KEY"]
        from octop_memory.ports.llm.openai_compat import DEFAULT_LLM_API_KEY_ENV
        facts["llm_config_source_checked"] = {"endpoint": client._base_url, "models": client._models, "api_key_env_resolved": True, "default_api_key_env": DEFAULT_LLM_API_KEY_ENV, "missing_config_client": type(_build_llm_client({})).__name__, "extract_called": False, "http_requests": 0}
        facts["event_types"] = list(get_args(RawEventType))
        source = inspect.getsource(MemoryRuntime._extract_impl)
        facts["extract_source_fields"] = {k: ('"' + k + '"') in source for k in ["session_id", "incremental", "promote", "regen_pages", "failure_reason", "warnings", "cap_warning", "llm_calls", "promotion", "pages", "episodes"]}
        assert all(facts["extract_source_fields"].values())
        facts["default_db"] = str(server._default_db_path("fixture"))
    facts["global_paths_unchanged"] = before == globals_snapshot()
    assert facts["global_paths_unchanged"]
    print(json.dumps(facts, ensure_ascii=False, indent=2))
    (work.out / "upstream-facts.json").write_text(json.dumps(facts, ensure_ascii=False, indent=2)+"\n")

if __name__ == '__main__':
    verification_main(main)
