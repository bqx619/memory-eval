"""Public adapter contract and strict normalized-suite validation (stdlib only)."""
from __future__ import annotations
import copy
import hashlib
import json
from pathlib import Path
from typing import Any, Protocol

SCHEMA_VERSION = "1.0"

class UnsupportedCapability(RuntimeError):
    """An operation is not implemented; never treat it as a scored failure/success."""

class ContractError(ValueError):
    """Dataset or adapter protocol violation."""

class MemoryAdapter(Protocol):
    """Only evaluator-selected inputs cross this interface; gold labels never do.

    Implementations are trusted code, NOT OS-isolated by this Python Protocol.
    reset is namespace-scoped; production stores must never be globally deleted.
    ready is a visibility barrier: return only when all acknowledged writes are searchable.
    retrieve must not persist the query or mutate memory in independent-query tracks.
    """
    def describe(self) -> dict[str, Any]: ...
    def reset(self, namespace: str) -> None: ...
    def ingest(self, namespace: str, events: list[dict[str, Any]]) -> None: ...
    def ready(self, namespace: str) -> None: ...
    def retrieve(self, namespace: str, request: dict[str, Any]) -> dict[str, Any]: ...
    def answer(self, namespace: str, request: dict[str, Any]) -> dict[str, Any]: ...
    def close(self) -> None: ...


def stable_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)


def digest(value: Any) -> str:
    return hashlib.sha256(stable_json(value).encode()).hexdigest()


def read_json(path: str | Path) -> Any:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def write_json(path: str | Path, value: Any) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n", encoding="utf-8")
    tmp.replace(path)


def validate_suite(suite: dict[str, Any]) -> dict[str, int]:
    if suite.get("schema_version") != SCHEMA_VERSION:
        raise ContractError("Unsupported suite schema_version")
    if not suite.get("suite_id") or not isinstance(suite.get("provenance"), dict):
        raise ContractError("suite_id and source provenance are required")
    if not suite["provenance"].get("source_url"):
        raise ContractError("A source_url is required; label any unit-test source explicitly")
    if not isinstance(suite.get("episodes"), list) or not suite["episodes"]:
        raise ContractError("episodes must be a nonempty list")
    eps, all_cases, nevents = set(), set(), 0
    for ep in suite["episodes"]:
        eid = ep["id"]
        if eid in eps:
            raise ContractError("Duplicate episode id")
        eps.add(eid)
        events = ep["events"]
        ids = [e["source_id"] for e in events]
        if len(ids) != len(set(ids)):
            raise ContractError(f"Duplicate event id in {eid}")
        nevents += len(events)
        for event in events:
            if not isinstance(event.get("content"), str) or not event.get("owner"):
                raise ContractError("Every event needs content:string and owner")
        for case in ep["cases"]:
            if case["id"] in all_cases:
                raise ContractError("Duplicate case id across suite")
            all_cases.add(case["id"])
            cutoff = case["cutoff"]
            if isinstance(cutoff, bool) or not isinstance(cutoff, int) or not 0 <= cutoff <= len(events):
                raise ContractError("cutoff is an exclusive integer event-prefix boundary")
            if case["kind"] not in {"typed_context", "qa", "mcq"}:
                raise ContractError("Unknown case kind; environment tasks must use their upstream runner")
            if not isinstance(case.get("query"), str) or not isinstance(case.get("gold"), dict):
                raise ContractError("query:string and evaluator-private gold are required")
            if case["kind"] == "mcq":
                choices = case.get("options", {})
                if not choices or case["gold"].get("choice") not in choices:
                    raise ContractError("MCQ labels must resolve to the supplied options")
            valid = ({e.get("session", "") for e in events[:cutoff]} if case["gold"].get("evidence_unit") == "session" else set(ids[:cutoff]))
            evidence = set(case["gold"].get("evidence_ids", []))
            if not evidence <= valid:
                raise ContractError(f"Missing/future gold evidence in case {case['id']}")
    return {"episodes": len(eps), "events": nevents, "cases": len(all_cases)}


# Metadata disclosed to a memory backend; no question labels or has_answer markers.
EVENT_ATTRIBUTES = {"scope", "type", "canonical_name", "aliases", "epistemic_status", "pinned",
                    "superseded_by", "resolved_at", "user_edited", "participants", "speaker"}

def public_event(event: dict[str, Any], event_map: dict[str, str], owner_map: dict[str, str],
                 session_map: dict[str, str], sequence: int) -> dict[str, Any]:
    attrs = {k: copy.deepcopy(v) for k, v in event.get("attributes", {}).items() if k in EVENT_ATTRIBUTES}
    for name in ("participants",):
        if name in attrs:
            attrs[name] = [event_map[x] for x in attrs[name] if x in event_map]
    if attrs.get("superseded_by"):
        attrs["superseded_by"] = event_map.get(attrs["superseded_by"])
    return {"id": event_map[event["source_id"]], "owner": owner_map[event["owner"]],
            "session_id": session_map[event.get("session", "")], "sequence": sequence,
            "role": event.get("role", "observation"), "content": event["content"],
            "time": event.get("time"), "attributes": attrs}
