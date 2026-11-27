#!/usr/bin/env python3
"""Attribute failed cases of a CogletNet run to native causes.

Usage: python3 -B scripts/cogletnet_failures.py <report-dir>... [--json OUT]

For each report directory: groups non-scored cases by episode, reads that
episode's logs/*/rpc-errors.jsonl for the native per-event states
(rpc_error.data.details.cogletnet.events[].state / error.kind) and the final
`cogletnet_usage` close record in sut.stderr.log. Prints one table per run and
a totals line; failures are classified as CogletNet-native (SUMMARY_UNCOMPRESSIBLE,
BUDGET_EXCEEDED, MODEL_*, NATIVE_*), bridge/transport (rpc_timeout,
process_exited) or evaluator-side (reader/judge errors). Read-only; stdlib only.
"""
import glob
import json
import os
import re
import sys
from collections import Counter, defaultdict

NATIVE_PREFIXES = ("SUMMARY_", "BUDGET_", "MODEL_", "NATIVE_", "BATCH_", "HISTORY_", "OPERATION_")
EVALUATOR_TYPES = {"ReaderEmptyResponse", "LLMTransportError", "JudgeError", "ReaderError", "gateway_budget"}


def native_kinds(rpc_errors_path):
    if not os.path.exists(rpc_errors_path):
        return Counter(), [], Counter()
    with open(rpc_errors_path, encoding="utf-8", errors="replace") as fh:
        def records():
            for line in fh:
                try:
                    yield json.loads(line)
                except json.JSONDecodeError:
                    continue
        return native_records(records())


def native_records(records):
    """Shared native attribution for file streams or matched per-case RPC rows."""
    kinds, failed_events, states = Counter(), [], Counter()
    for rec in records:
        events = (((rec.get("rpc_error") or {}).get("data") or {}).get("details") or {}).get("cogletnet", {}).get("events") or []
        for ev in events:
            states[ev.get("state")] += 1
            err = ev.get("error") or {}
            if ev.get("state") == "Failed" or err:
                kind = err.get("kind") or "UNKNOWN"
                kinds[kind] += 1
                failed_events.append({"event_id": ev.get("event_id"), "kind": kind, "message": err.get("message")})
        if not events:
            m = re.search(r"rpc_error\(-?\d+\): ([A-Za-z]+): ([^\[]+)", rec.get("error") or "")
            if m:
                kinds[m.group(1) + ":" + m.group(2).strip()] += 1
    return kinds, failed_events, states


def classify(error_types, kinds, failed_events):
    """The same precedence is used by the CLI and offline review builders."""
    if any(t in ("rpc_timeout", "process_exited") for t in error_types) and not failed_events:
        return "bridge/transport"
    if kinds:
        return "cogletnet-native"
    if error_types and all(t in EVALUATOR_TYPES for t in error_types):
        return "evaluator"
    return "unclassified"


def usage(stderr_path):
    last = None
    if not os.path.exists(stderr_path):
        return None
    with open(stderr_path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            if "cogletnet_usage" in line and '"method":"close"' in line:
                try:
                    last = json.loads(line.split("cogletnet_usage", 1)[1].strip())["usage"]
                except (json.JSONDecodeError, KeyError, IndexError):
                    pass
    return last


def analyze(report_dir):
    with open(os.path.join(report_dir, "report.json"), encoding="utf-8") as fh:
        r = json.load(fh)
    cases = r.get("cases") or []
    by_ep = defaultdict(list)
    for c in cases:
        by_ep[(c.get("episode_id"), c.get("namespace"))].append(c)
    log_dirs = {os.path.basename(d).split("-", 1)[1]: d for d in glob.glob(os.path.join(report_dir, "logs", "*-paeval_*"))}
    rows, totals = [], Counter()
    usage_total = Counter()
    for (ep, ns), cs in sorted(by_ep.items(), key=lambda kv: int(kv[0][0]) if str(kv[0][0]).isdigit() else 0):
        d = log_dirs.get(ns)
        kinds, failed_events, states = native_kinds(os.path.join(d, "rpc-errors.jsonl")) if d else (Counter(), [], Counter())
        u = usage(os.path.join(d, "sut.stderr.log")) if d else None
        if u:
            for k in ("calls", "input_tokens", "output_tokens", "unknown_usage_calls"):
                usage_total[k] += int(u.get(k) or 0)
        failed = [c for c in cs if c.get("status") != "scored"]
        if not failed:
            continue
        etypes = Counter(c.get("error_type") or c.get("status") for c in failed)
        cls = classify(etypes, kinds, failed_events)
        totals[cls] += len(failed)
        rows.append({"episode": ep, "cases_failed": len(failed), "cases_total": len(cs), "error_types": dict(etypes),
                     "native_kinds": dict(kinds), "failed_events": failed_events, "event_states": dict(states),
                     "class": cls, "usage": u})
    status = (r.get("summary") or {}).get("status_counts") or {}
    return {"report": report_dir, "sut": (r.get("sut") or {}).get("name"), "variant": (r.get("sut") or {}).get("variant"),
            "status_counts": status, "wall_seconds": r.get("wall_seconds"), "episodes": rows,
            "failed_by_class": dict(totals), "native_usage_total": dict(usage_total)}


def main(argv):
    out = None
    if "--json" in argv:
        i = argv.index("--json")
        out = argv[i + 1]
        argv = argv[:i] + argv[i + 2:]
    results = [analyze(p) for p in argv]
    for res in results:
        print(f"== {res['report']} ({res['sut']}/{res['variant']}) status={res['status_counts']} wall={round((res['wall_seconds'] or 0)/60)}min")
        print(f"   failed by class: {res['failed_by_class']} | native usage: {res['native_usage_total']}")
        for row in res["episodes"]:
            ev = ", ".join(f"{e['kind']}" for e in row["failed_events"]) or ", ".join(f"{k}×{v}" for k, v in row["native_kinds"].items()) or "-"
            print(f"   ep {str(row['episode']):>3} {row['cases_failed']:>3}/{row['cases_total']:<3} {row['class']:<17} {row['error_types']} native events: {ev}")
    if out:
        with open(out, "w", encoding="utf-8") as fh:
            json.dump(results, fh, ensure_ascii=False, indent=1)
        print("wrote", out)


if __name__ == "__main__":
    main(sys.argv[1:])
