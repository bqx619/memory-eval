"""Run isolated, prequential-prefix queries with evaluator-private answers and labels."""
from __future__ import annotations
from collections import defaultdict
import copy
from datetime import datetime, timezone
import hashlib
import platform
from pathlib import Path
import sys
import time
import uuid
from typing import Any
from . import __version__
from .core import ContractError, UnsupportedCapability, digest, public_event, validate_suite
from .metrics import aggregate, lexical_answer_metrics, ordered_unique, retrieval_metrics, score_typed


def code_fingerprint() -> str:
    root = Path(__file__).parent
    entries = [(p.name, hashlib.sha256(p.read_bytes()).hexdigest()) for p in sorted(root.glob("*.py"))]
    return digest(entries)


def _validate_items(items, known):
    if not isinstance(items, list): raise ContractError("items/tier must be a list")
    for item in items:
        if not isinstance(item, dict) or not isinstance(item.get("text"), str):
            raise ContractError("Every retrieved item needs text:string")
        sources = item.get("source_ids")
        if not isinstance(sources, list) or any(not isinstance(i, str) or i not in known for i in sources):
            raise ContractError("source_ids must reference actual ingested event ids; no invented/future citations")
        if not sources:
            raise UnsupportedCapability("Cannot measure evidence retrieval without source_ids; use native-answer track")
        if len(sources) != len(set(sources)):
            raise ContractError("Repeated citations within an item")


def _result(response, known, budget, typed=False):
    if not isinstance(response, dict): raise ContractError("Adapter response must be a JSON object")
    if typed:
        tiers = response.get("tiers")
        if not isinstance(tiers, dict): raise ContractError("typed_context requires tiers")
        items = []
        for field in ("pinnedFacts", "relevantBeliefs", "openQuestions"):
            _validate_items(tiers.get(field), known)
            if any(len(i["source_ids"]) != 1 for i in tiers[field]):
                raise UnsupportedCapability("PMB typed diagnostic requires one source seed per item")
            items += tiers[field]
        if not isinstance(response.get("personaPrelude"), str): raise ContractError("Missing personaPrelude")
        if len(tiers["relevantBeliefs"]) > budget["top_k"]: raise ContractError("top_k exceeded")
        if "maxBeliefs" in budget and len(items) > budget["maxBeliefs"]: raise ContractError("maxBeliefs exceeded")
    else:
        items = response.get("items")
        _validate_items(items, known)
        if len(items) > budget["top_k"]: raise ContractError("top_k exceeded; evaluator never silently clips")
    chars = sum(len(i["text"]) for i in items) + len(response.get("personaPrelude", ""))
    if chars > budget["max_context_chars"]:
        raise ContractError("max_context_chars exceeded; no hidden post-retrieval clipping")
    return items, chars


def _score_answer(case, answer):
    if not isinstance(answer, dict): raise ContractError("answer must return an object")
    if case["kind"] == "mcq":
        choice = answer.get("choice")
        if not isinstance(choice, str) or choice not in case["options"]:
            raise ContractError("MCQ answer requires one exact choice key; no substring/regex guessing")
        ok = choice == case["gold"]["choice"]
        return {"status": "scored", "passed": ok, "metrics": {"choice_accuracy": float(ok)},
                "failures": [] if ok else ["Incorrect option"], "prediction": choice}
    text = answer.get("text")
    if not isinstance(text, str): raise ContractError("QA response requires text:string")
    if case["gold"].get("abstain"):
        if not isinstance(answer.get("abstain"), bool):
            return {"status": "unscored", "passed": None, "metrics": {}, "failures": [],
                    "note": "Abstention requires explicit abstain:boolean; official semantic judge not run", "prediction": text}
        return {"status": "scored", "passed": None, "metrics": {"abstention_flag_match_diagnostic": float(answer["abstain"])},
                "failures": [], "note": "Flag diagnostic only; does not prove answer contains no unsupported claims", "prediction": text}
    return {"status": "scored", "passed": None, "metrics": lexical_answer_metrics(text, case["gold"].get("answers", [])),
            "failures": [], "official_score": None, "note": "Lexical EM/F1 are not official semantic correctness", "prediction": text}


def run_suite(suite: dict, adapter, *, mode: str = "retrieval", budget: dict | None = None,
              limit: int | None = None, batch_size: int = 64, trace_text: bool = False, reader=None) -> dict:
    """mode retrieval / native_answer / fixed_reader; defaults never call a paid model.

    Each case starts from a fresh namespace; imports only events[:cutoff].
    No query-category, reference answer, evidence label or original case ID reaches
    the adapter. Fact metadata is supplied ONLY in the explicit seeded-state lane.
    A Python plugin is trusted and can access disk; use an isolated external service
    for adversarial submissions. This function is not a security sandbox.
    """
    counts = validate_suite(suite)
    if mode not in {"retrieval", "native_answer", "fixed_reader"}: raise ValueError("Invalid mode")
    if mode == "fixed_reader" and reader is None: raise ValueError("fixed_reader needs a reader adapter")
    if batch_size < 1 or (limit is not None and limit < 1): raise ValueError("batch_size and limit must be positive")
    base_budget = {"top_k": 10, "max_context_chars": 100_000, **(budget or {})}
    for k, v in base_budget.items():
        if isinstance(v, bool) or not isinstance(v, int) or v < 0: raise ValueError(f"Invalid budget {k}")
    descriptor = adapter.describe()
    if not isinstance(descriptor, dict) or not isinstance(descriptor.get("capabilities"), list):
        raise ContractError("describe must return capabilities:list")
    reader_descriptor = reader.describe() if reader else None
    run_id = str(uuid.uuid4())
    rows = []
    wall = time.perf_counter()
    started = datetime.now(timezone.utc).isoformat()
    try:
        for ep in suite["episodes"]:
            for case in ep["cases"]:
                if limit is not None and len(rows) >= limit: break
                case_budget = {**base_budget, **case.get("budget", {})}
                row = {"case_id": case["id"], "episode_id": ep["id"], "category": case.get("category", "unknown"),
                       "cluster_id": case.get("cluster_id", ep["id"]), "timings": {}, "budget": case_budget,
                       "cutoff": case["cutoff"], "failures": [], "metrics": {}, "passed": None}
                events = ep["events"][:case["cutoff"]]
                event_map = {e["source_id"]: "e_" + uuid.uuid4().hex for e in events}
                inverse = {v: k for k, v in event_map.items()}
                owners = {x: "u_" + uuid.uuid4().hex for x in {case["user"], *(e["owner"] for e in events)}}
                sessions = {s: "s_" + uuid.uuid4().hex for s in {e.get("session", "") for e in events}}
                public = [public_event(e, event_map, owners, sessions, i) for i, e in enumerate(events)]
                namespace = "paeval_" + uuid.uuid4().hex
                request = {"query": case["query"], "user": owners[case["user"]], "scope": copy.deepcopy(case.get("scope", [])),
                           "as_of": case.get("query_time"), "budget": case_budget,
                           "query_invocation_id": uuid.uuid4().hex,
                           "output_format": "typed_context" if case["kind"] == "typed_context" else "retrieval"}
                if case.get("options"): request["options"] = copy.deepcopy(case["options"])
                row["public_input_sha256"] = digest({"events": public, "request": request})
                try:
                    required = "typed_context" if case["kind"] == "typed_context" else ("answer" if mode == "native_answer" else "retrieve")
                    if required not in descriptor["capabilities"]:
                        raise UnsupportedCapability(f"Adapter does not advertise {required}")
                    t = time.perf_counter(); adapter.reset(namespace); row["timings"]["reset_ms"] = (time.perf_counter()-t)*1000
                    t = time.perf_counter()
                    for off in range(0, len(public), batch_size): adapter.ingest(namespace, public[off:off+batch_size])
                    row["timings"]["ingest_ms"] = (time.perf_counter()-t)*1000
                    t = time.perf_counter(); adapter.ready(namespace); row["timings"]["ready_ms"] = (time.perf_counter()-t)*1000
                    response = None
                    if case["kind"] == "typed_context" or mode != "native_answer":
                        t = time.perf_counter(); response = adapter.retrieve(namespace, copy.deepcopy(request)); row["timings"]["query_ms"] = (time.perf_counter()-t)*1000
                        items, row["returned_chars"] = _result(response, inverse, case_budget, typed=case["kind"] == "typed_context")
                        raw_ids = ordered_unique(s for item in items for s in item["source_ids"])
                        returned_ids = [inverse[x] for x in raw_ids]
                        row["retrieved_source_ids"] = returned_ids
                        # Inspect without filtering: a bad backend must expose its own mistakes.
                        source = {e["source_id"]: e for e in events}
                        row["diagnostics"] = {"cross_user_source_count": sum(source[i]["owner"] != case["user"] for i in returned_ids),
                                              "duplicate_source_occurrences": sum(len(i["source_ids"]) for i in items) - len(raw_ids)}
                        if case["kind"] == "typed_context":
                            converted = {"tiers": {key: [inverse[i["source_ids"][0]] for i in value] for key, value in response["tiers"].items()},
                                         "personaPrelude": response["personaPrelude"]}
                            row.update(score_typed(case["gold"]["expect"], converted, k=case_budget["top_k"]))
                        else:
                            pred = returned_ids
                            if case["gold"].get("evidence_unit") == "session":
                                pred = ordered_unique(source[i].get("session", "") for i in pred)
                            if "evidence_ids" in case["gold"] and case["gold"]["evidence_ids"] and not case["gold"].get("abstain"):
                                row.update(status="scored", metrics=retrieval_metrics(pred, case["gold"]["evidence_ids"], case["gold"].get("exhaustive", False), k=case_budget["top_k"]))
                                row["note"] = "Source-unit retrieval diagnostics; summarized-source citations are adapter-reported, not entailed-text verification"
                            else:
                                row.update(status="unscored", note="No positive source labels; retrieval has no ground-truth score here")
                    if mode in {"native_answer", "fixed_reader"} and case["kind"] != "typed_context":
                        t = time.perf_counter()
                        if mode == "native_answer":
                            answer = adapter.answer(namespace, copy.deepcopy(request))
                        else:
                            # Fixed reader receives only query/options and returned evidence, never source gold.
                            answer = reader.answer({"query": request["query"], "options": request.get("options"),
                                                    "as_of": request.get("as_of"), "evidence": response["items"]})
                        row["timings"]["answer_ms"] = (time.perf_counter()-t)*1000
                        retrieval = row.get("metrics", {})
                        row.update(_score_answer(case, answer))
                        row["metrics"] = {**retrieval, **row["metrics"]}
                    if trace_text:
                        row["query"] = case["query"]
                        row["response"] = response
                except UnsupportedCapability as e:
                    row.update(status="unsupported", note=str(e), metrics={}, passed=None)
                except Exception as e:
                    row.update(status="error", error_type=type(e).__name__, note="Operation failed; details redacted to prevent logging private text", metrics={}, passed=None)
                    # External exceptions can contain credentials; no exception body is serialized.
                rows.append(row)
            if limit is not None and len(rows) >= limit: break
    finally:
        adapter.close()
    groups = defaultdict(list)
    for row in rows: groups[row["category"]].append(row)
    return {"framework_version": __version__, "run_id": run_id, "started_at_runtime_utc": started,
            "research_snapshot_date": "2026-10-15", "wall_seconds": time.perf_counter()-wall,
            "suite_id": suite["suite_id"], "suite_sha256": digest(suite), "source": suite["provenance"],
            "source_counts": counts, "source_limitations": suite.get("limitations", []),
            "mode": mode, "adapter": descriptor, "reader": reader_descriptor,
            "protocol": {"version": "paeval-v1", "budget": base_budget, "batch_size": batch_size,
                         "reset_policy": "fresh-namespace-per-case", "scoring": "deterministic-source-assertions-and-diagnostics-v1",
                         "case_limit": limit, "gold_hidden_from_adapter": True,
                         "process_security": "trusted Python adapters; no OS sandbox", "trace_text": trace_text,
                         "cost_model": "No API billing measured; null is not zero"},
            "environment": {"python": sys.version.split()[0], "platform": platform.platform(), "code_sha256": code_fingerprint()},
            "summary": aggregate(rows), "by_category": {k: aggregate(v) for k,v in sorted(groups.items())}, "cases": rows,
            "warnings": ["This run is not an official upstream leaderboard submission", "Synthetic source fixtures are not real-user logs",
                         "Scores on partial coverage are not comparable to complete supported runs", "Single-corpus diagnostics do not support population-level confidence intervals"]}
