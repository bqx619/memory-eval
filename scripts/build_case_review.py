#!/usr/bin/env python3
"""Build the P15 offline case-review data (stdlib only, no model calls).

generated_at is the latest recorded resume timestamp, or started_at_utc, not
the wall clock: the same inputs must produce the same bytes. Missing facts
remain null. RPC batches are mapped to source IDs only when their complete
ordered event list matches events_written in length; partial batches remain
unmapped. Native usage sums the last close record of each retained log stream.

Page contract: top-level glossary is an id-sorted list of MetricDef objects
(id/group/name_zh/definition_zh/formula/range/higher_is_better/aggregation/
caveats/applies_to), restricted to keys in summary.metrics or cases[*].metrics.
Definitions come unchanged from --glossary (an exported JSON list or a results
index object's glossary), otherwise bin/memeval results glossary. The single
source is internal/scoring/glossary.go; unavailable exports yield [] and one
stderr warning, never handwritten fallback definitions. Determinism requires
the same report inputs and glossary export/binary.

Tree work uses the scoped v3 build contract with representation="tree".
Search items add level, recorded [UTC-microsecond start,end], reader_text_hash,
reader_sources_n, mapped direct sources and three-class jev; no memory layers.
context_view.nodes preserves aligned node/body/source, coverage and recording
windows; unknown source mappings stay null. Packed is included only if traced.
"""

import argparse
from collections import Counter, defaultdict
import copy
from datetime import datetime
import hashlib
import json
import math
from pathlib import Path
import re
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
VERDICTS = ("hit_all", "hit_partial", "miss", "empty_correct", "empty_wrong", "error")
STAGES = ("ingest", "retrieve", "bridge", "evaluator", "unclassified")
METRICS = ("recall", "mrr", "ndcg", "precision", "empty_result_correct", "hit", "all_evidence")
EVALUATOR_ERRORS = {"ReaderEmptyResponse", "LLMTransportError", "LLMHTTPError",
                    "JudgeError", "ReaderError", "gateway_budget"}
USAGE_FIELDS = ("calls", "input_tokens", "output_tokens", "unknown_usage_calls")


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def parse_work_trace(raw):
    """Retry the historical redactor's lost JSON escape, never alter valid JSON.

    Look past whitespace so a lost escape before ` http...` is repairable,
    while real string terminators before whitespace + , } ] : stay intact.
    """
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        repaired = re.sub(r'(\[REDACTED\])"(?=\s*[^,}\]:\s])', r'\1\\"', raw)
        return json.loads(repaired)


def load_glossary(metric_ids, path=None):
    try:
        if path is not None:
            value = read_json(path)
        else:
            binary = ROOT / "bin/memeval"
            if not binary.is_file():
                raise FileNotFoundError("bin/memeval")
            exported = subprocess.run([str(binary), "results", "glossary"], cwd=ROOT,
                                      check=True, capture_output=True, text=True, timeout=30)
            value = json.loads(exported.stdout)
        if isinstance(value, dict):
            value = value.get("glossary")
        if not isinstance(value, list) or any(
                not isinstance(entry, dict) or not isinstance(entry.get("id"), str) for entry in value):
            raise ValueError("expected a MetricDef list or an index with glossary")
        return sorted((entry for entry in value if entry["id"] in metric_ids), key=lambda entry: entry["id"])
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"build_case_review: glossary unavailable ({type(error).__name__}); emitting glossary=[]; "
              "use --glossary <path> to supply an export", file=sys.stderr)
        return []


def display_path(path):
    path = Path(path).resolve()
    try:
        return path.relative_to(ROOT).as_posix()
    except ValueError:
        return str(path)


def pick(obj, keys):
    return {key: (obj or {}).get(key) for key in keys}


def load_report(directory):
    path = Path(directory) / "report.json"
    raw = path.read_bytes()
    report = json.loads(raw)
    if not isinstance(report.get("cases"), list) or not isinstance(report.get("summary"), dict):
        raise ValueError(f"{path}: report requires cases and summary")
    return report, {"report_path": display_path(path),
                    "report_sha256": hashlib.sha256(raw).hexdigest()}


def sample_key(case):
    # Never align different repeats or episodes by list position.
    return (case.get("episode_id"), case.get("case_id"),
            case.get("sample_id"), case.get("repeat_index"))


def case_map(cases):
    result = {}
    for case in cases:
        key = sample_key(case)
        if key in result:
            raise ValueError(f"duplicate report sample: {key}")
        result[key] = case
    return result


def protocol_value(report, key):
    protocol = report.get("protocol") or {}
    if key in ("top_k", "max_context_chars"):
        return (protocol.get("budget") or {}).get(key)
    return protocol.get(key)


def check_control(report, control, name):
    for key in ("suite_sha256", "reset_policy", "top_k"):
        left = report.get(key) if key == "suite_sha256" else protocol_value(report, key)
        right = control.get(key) if key == "suite_sha256" else protocol_value(control, key)
        if left is None or right is None or left != right:
            raise ValueError(f"control {name}: {key} differs or is missing ({left!r} != {right!r})")


def verdict(case, gold, mode):
    if case.get("status") != "scored":
        return "error"
    metrics = case.get("metrics") or {}
    if mode == "fixed_reader":
        if isinstance(case.get("passed"), bool):
            return "hit_all" if case["passed"] else "miss"
        value = metrics.get("beam.llm_judge_score")
        if value in (0, 0.5, 1):
            return {0: "miss", 0.5: "hit_partial", 1: "hit_all"}[value]
        value = metrics.get("mab_exact_match")
        if value in (0, 1):
            return "hit_all" if value == 1 else "miss"
        return None
    if case.get("kind") == "retrieval":
        if gold.get("evidence_ids"):
            value = metrics.get("recall")
            if value == 1:
                return "hit_all"
            if value is not None and 0 < value < 1:
                return "hit_partial"
            if value == 0:
                return "miss"
        elif gold.get("evidence_ids") == [] and gold.get("exhaustive") is True:
            value = metrics.get("empty_result_correct")
            if value is not None:
                return "empty_correct" if value == 1 else "empty_wrong"
    return None


def metrics_of(case):
    return {**dict.fromkeys(METRICS), **(case.get("metrics") or {})}


def output_of(case):
    items = (case.get("response") or {}).get("items")
    if items is not None:
        items = [{"rank": i, **pick(item, ("id", "text", "source_ids", "score"))}
                 for i, item in enumerate(items, 1)]
    return {"retrieved_source_ids": case.get("retrieved_source_ids"), "items": items,
            "returned_chars": case.get("returned_chars")}


def jsonl(path):
    if path.exists():
        with path.open(encoding="utf-8", errors="replace") as stream:
            for line in stream:
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    # Episode logs may have a cap/omission marker or a partial tail.
                    continue
                if isinstance(record, dict):
                    yield record


def log_index(directory):
    logs = defaultdict(list)
    for path in sorted((Path(directory) / "logs").rglob("rpc-errors.jsonl")):
        meta_path = path.parent / "episode.json"
        namespace = (read_json(meta_path).get("namespace") if meta_path.exists()
                     else path.parent.name.partition("-")[2])
        if namespace:
            logs[namespace].extend(jsonl(path))
    return logs


def native_detail(record, written):
    data = ((record.get("rpc_error") or {}).get("data") or {})
    events = ((data.get("details") or {}).get("cogletnet") or {}).get("events")
    if not events:
        return None
    states, failed = Counter(), []
    for index, event in enumerate(events):
        state = event.get("state")
        if state is not None:
            states[state] += 1
        if state == "Ready":
            continue
        err = event.get("error") or {}
        details = err.get("details") or {}
        entry = {"index": index,
                 "source_id": written[index] if written is not None and len(events) == len(written) else None,
                 **pick(event, ("event_id", "state", "operation_id")),
                 **pick(err, ("kind", "message"))}
        for key in ("summary_failure", "budget_failure"):
            entry[key] = event.get(key, err.get(key, details.get(key)))
        failed.append(entry)
    return {"failed_events": failed, "batch_states": dict(states)}


def error_of(case, records, written):
    if case.get("status") == "scored":
        return None
    # Match the recorded error, rather than attaching a later close/reset error.
    detail = case.get("error_detail")
    error_type = case.get("error_type")
    matching = [r for r in records if detail and detail in (r.get("detail"), r.get("error"))]
    # An evaluator failure must not inherit an unrelated RPC failure from an
    # earlier attempt in the same namespace's retained logs.
    candidates = [] if error_type in EVALUATOR_ERRORS else records
    relevant = matching or [r for r in candidates if r.get("method") in ("ingest", "retrieve")]
    record = (relevant or candidates or [{}])[-1]
    method = record.get("method")
    native = native_detail(record, written)
    if case.get("status") == "ingest_failed" or method == "ingest":
        stage = "ingest"
    elif method == "retrieve":
        stage = "retrieve"
    elif error_type in ("rpc_timeout", "process_exited") or (error_type == "*jsonrpc.RPCError" and not native):
        stage = "bridge"
    elif error_type in EVALUATOR_ERRORS:
        stage = "evaluator"
    else:
        stage = "unclassified"
    return {"stage": stage, "type": error_type, "detail": detail,
            "rpc_method": method, "native": native}


def native_usage(directory):
    usages = []
    for path in sorted((Path(directory) / "logs").rglob("sut.stderr.log")):
        last = None
        with path.open(encoding="utf-8", errors="replace") as stream:
            for line in stream:
                if "cogletnet_usage" not in line:
                    continue
                try:
                    record = json.loads(line.split("cogletnet_usage", 1)[1].strip())
                except json.JSONDecodeError:
                    continue
                if record.get("method") == "close" and isinstance(record.get("usage"), dict):
                    last = record["usage"]
        if last is not None:
            usages.append(last)
    if not usages:
        return None

    def totals(rows):
        return {k: sum(row[k] for row in rows) if all(isinstance(row.get(k), (int, float)) for row in rows)
                else None for k in USAGE_FIELDS}

    roles = defaultdict(list)
    for usage in usages:
        for role, values in (usage.get("by_role") or {}).items():
            roles[role].append(values)
    return {**totals(usages), "by_role": {role: totals(rows) for role, rows in roles.items()}}


class WorkTraces:
    """Decode optional diagnostic streams without substituting gold/source text.

    Local text hashes are explicitly prefixed sha256-16:. Mapping mismatch
    counts refer to distinct build records or misaligned JEV batches used by
    cases, not repeated views of one incremental episode. Episode extraction
    selects the newest attempt containing each question's search, retaining
    the majority attempt for segment-level diagnostics. Failed JEV batches
    with null probabilities are unavailable evidence, not a fabricated
    array-length mismatch.

    Scoped build diagnostics may contain {"ref_seq": N}. References use
    the same namespace, trace_batch, field and operation/snapshot identity,
    and only an earlier full build record may be their target. Resolve once
    while reading, sharing the immutable diagnostic objects rather than
    copying them. Missing/capped targets remain null and count as corrupt.
    """

    def __init__(self, directory):
        self.logs, self.cursors, self.texts, self.mismatches = {}, {}, {}, set()
        self.paths, self.corrupt_lines = {}, {}
        self.attempts = defaultdict(list)
        self.directory = Path(directory)
        report_path = Path(directory) / "report.json"
        self.variant = ((read_json(report_path).get("sut") or {}).get("variant") or "") if report_path.is_file() else ""
        paths = (Path(directory) / "logs").rglob("sut.stderr.log")
        def log_order(path):
            # Committed resume directories have numeric generation IDs, not
            # timestamps. Never let mtimes or resume-10/resume-2 lexical order
            # decide which invocation supplies a question's diagnostics.
            relative = path.relative_to(directory)
            generation = next((int(part[7:]) for part in relative.parts if re.fullmatch(r"resume-\d+", part)), 0)
            return generation, [int(x) if x.isdigit() else x for x in re.split(r"(\d+)", relative.as_posix())]
        for path in sorted(paths, key=log_order):
            meta_path = path.parent / "episode.json"
            namespace = (read_json(meta_path).get("namespace") if meta_path.exists()
                         else path.parent.name.partition("-")[2])
            records, corrupt, unresolved = self.read_records(path)
            if namespace:
                if unresolved:
                    print(f"warning: {path}: {unresolved} unresolved build trace reference lines "
                          f"(namespace {namespace}); diagnostics unavailable", file=sys.stderr)
                self.logs[namespace] = records
                self.paths[namespace] = path.relative_to(directory).as_posix()
                self.corrupt_lines[namespace] = corrupt
                self.attempts[namespace].append({
                    # Retain only the query index for historical attempts: a
                    # scope snapshot can be large. Read a selected older log
                    # again on demand rather than keep every resume in memory.
                    "queries": {r["query"] for r in records if r.get("kind") == "search"
                                and isinstance(r.get("query"), str)},
                    "path": self.paths[namespace], "corrupt_lines": corrupt,
                    "attempt": next((part for part in path.relative_to(directory).parts
                                     if re.fullmatch(r"resume-\d+", part)), "root")})

    @classmethod
    def read_records(cls, path):
        records, corrupt, unresolved, full_builds = [], 0, 0, {}
        with path.open(encoding="utf-8", errors="replace") as stream:
            for line in stream:
                if not line.startswith("sut_trace "):
                    continue
                try:
                    row = parse_work_trace(line[len("sut_trace "):])
                except json.JSONDecodeError:
                    corrupt += 1
                    continue  # A capped log's partial line is not a record.
                if isinstance(row, dict):
                    if row.get("kind") == "build" and cls.resolve_build_refs(row, full_builds):
                        corrupt += 1
                        unresolved += 1
                    records.append(row)
        return records, corrupt, unresolved

    @staticmethod
    def resolve_build_refs(row, full_builds):
        """Return whether this line has broken references; never use future data."""
        failures = []
        batch, seq = row.get("trace_batch"), row.get("seq")
        valid_batch = batch is None or (type(batch) is int and batch >= 0)
        for field, identity_key in (("audit", "operation_id"), ("snapshot", "snapshot_id")):
            value, identity = row.get(field), row.get(identity_key)
            if not isinstance(value, dict):
                continue
            if "ref_seq" in value:
                reference = value.get("ref_seq")
                valid = (set(value) == {"ref_seq"} and type(reference) is int and reference >= 0
                         and valid_batch and isinstance(identity, str) and bool(identity))
                target = full_builds.get((batch, field, reference, identity)) if valid else None
                row[field] = target
                if target is None:
                    failures.append(f"{field}: unresolved build trace reference")
                # Never register another reference as a full record: this also
                # refuses cycles/chains without recursive traversal.
            elif valid_batch and type(seq) is int and seq >= 0 and isinstance(identity, str) and identity:
                full_builds[batch, field, seq, identity] = value
        if failures:
            row["audit_error"] = "; ".join(filter(None, [row.get("audit_error"), *failures]))
        return bool(failures)

    def text(self, text, upstream_hash=None):
        if not isinstance(text, str):
            return None
        key = upstream_hash or ("sha256-16:" + hashlib.sha256(text.encode()).hexdigest()[:16])
        if key in self.texts and self.texts[key] != text:
            raise ValueError(f"work trace text hash collision: {key}")
        self.texts[key] = text
        return key

    def summary(self, attempt):
        verdict = attempt.get("verdict")
        output = attempt.get("output") or {}
        span, failure = attempt.get("copied_span"), attempt.get("failure")
        return {**pick(attempt, ("body", "ordinal", "candidate_kind", "stage", "accepted", "output_tokens", "error_code")),
                "copied_span": [span.get("start"), span.get("end")] if isinstance(span, dict) else span,
                "input_tokens": attempt.get("input_tokens"),
                "failure": f"{failure.get('kind')}: {failure.get('message')}" if isinstance(failure, dict) else failure,
                "output_hash": self.text(output.get("text"), attempt.get("output_hash") or output.get("hash")),
                "verdict": None if verdict is None else {
                    **pick(verdict, ("faithful", "model")),
                    "reason_hash": self.text(verdict.get("reason"), verdict.get("reason_hash"))}}

    def input_summary(self, summary):
        """Project a saved summary identically in member inputs and pending."""
        verdict = summary.get("validation") or {}
        return {**pick(summary, ("body", "model", "attempts")),
                "output_hash": self.text(summary.get("text"), summary.get("output_hash")),
                "faithful": verdict.get("faithful"),
                "reason_hash": self.text(verdict.get("reason"), verdict.get("reason_hash"))}

    def leaf_memory(self, records):
        # New upstream snapshots also expose scopes/memories on the legacy
        # chain. Only the selected retrieval mode (or declared variant when
        # no search succeeded) establishes the LeafMemory contract.
        return "leaf-memory" in self.variant or any(
            settings.get("representation") == "LeafMemory" and settings.get("jev_input_format") == "LeafMemoryV1"
            for settings in (((r.get("result") or {}).get("settings") or {}) for r in records))

    def tree(self, records):
        return "tree" in self.variant or any(
            ((r.get("result") or {}).get("settings") or {}).get("representation") == "Tree"
            for r in records if r.get("kind") == "search")

    def representation(self, records, record_key):
        leaf, tree = self.leaf_memory(records), self.tree(records)
        if leaf and tree:
            self.mismatches.add(("representation", record_key[0]))
            # A declared variant may be stale across resume. For conflicting
            # traces the latest explicit search decides segment diagnostics;
            # each search is still decoded according to its own settings.
            modes = []
            for row in records:
                settings = ((row.get("result") or {}).get("settings") or {})
                if row.get("kind") == "search" and settings.get("representation") == "Tree":
                    modes.append("tree")
                elif (row.get("kind") == "search" and settings.get("representation") == "LeafMemory"
                      and settings.get("jev_input_format") == "LeafMemoryV1"):
                    modes.append("leaf_memory")
            return modes[-1] if modes else ("tree" if "tree" in self.variant else "leaf_memory")
        return "tree" if tree else "leaf_memory" if leaf else None

    @staticmethod
    def build_key(index, row):
        seq, event = row.get("seq"), row.get("event_id")
        return (seq, event) if type(seq) is int and isinstance(event, str) else ("invalid", index)

    @staticmethod
    def span(value):
        return [value.get("start"), value.get("end")] if isinstance(value, dict) else value

    @staticmethod
    def jev(outcome):
        return pick(outcome, ("useful", "not_useful", "uncertain")) if isinstance(outcome, dict) and outcome.get("kind") == "reference" else None

    @staticmethod
    def scope_calls(audit):
        calls = audit.get("calls")
        if calls is None:
            return None
        attempts = [a for call in calls for a in call.get("attempts", [])]
        starts = [a["started_at"] for a in attempts if type(a.get("started_at")) is int]
        ends = [a["completed_at"] for a in attempts if type(a.get("completed_at")) is int]
        start, end = min(starts, default=None), max(ends, default=None)
        roles = Counter(c.get("role") for c in calls if c.get("role") is not None)
        return {"n": len(calls), "by_role": {**dict.fromkeys(("validation", "summary", "embedding"), 0), **roles},
                "failed_attempts": sum(a.get("failure") is not None for a in attempts),
                "wall_ms": (end - start) / 1000 if start is not None and end is not None else None,
                "started_at": start, "completed_at": end}

    def memory(self, layers, mapping):
        if layers is None:
            return None
        output = []
        for level, layer in enumerate(layers):
            metadata = layer.get("metadata") or {}
            ref = metadata.get("ref") or {}
            origins = metadata.get("origins")
            output.append({"level": level, **pick(layer, ("current", "relation")),
                           "node": ref.get("node"), "body": ref.get("body"), "ordinal": metadata.get("ordinal"),
                           "info_hashes": [self.text(info) for info in layer.get("info", [])],
                           "origins": None if origins is None else [
                               {**pick(o, ("event_id", "event_sequence", "role")), "source_id": mapping.get(o.get("event_id"))}
                               for o in origins]})
        return output

    def leaf_builds(self, rows, mapping, body_texts):
        """One extraction per snapshot, with event-local L0 build rows.

        Edges/ordinals belong to generations; immutable node content is checked
        separately. Model traces are observational, never inferred from gold.
        """
        scopes, snapshots, output = {}, {}, {}
        self.leaf_history = []

        def event_row(index, row, source_id, nodes):
            key = self.build_key(index, row)
            previous = output.get(key)
            output[key] = {**pick(row, ('seq', 'event_id', 'state', 'operation_id', 'snapshot_id', 'audit_error')),
                           'source_id': source_id, 'nodes': nodes, 'resume_count': row.get('resume_count', 0),
                           'attempts': (previous['attempts'] if previous else []) + [row.get('state')]}
            self.leaf_history.append((index, output[key]))

        for index, row, source_id in sorted(rows, key=lambda entry: entry[0]):
            snapshot = row.get("snapshot") or {}
            audit = row.get("audit") or {}
            scope_id = (snapshot.get("scope") or {}).get("id")
            snapshot_id = row.get("snapshot_id") or snapshot.get("id")
            key = (scope_id, snapshot_id)
            if scope_id is None or snapshot_id is None:
                projected = self.build(row, source_id, body_texts)
                # No origin evidence; do not guess ownership.
                event_row(index, row, source_id, [])
                continue
            if key not in snapshots:
                projected = self.build(row, source_id, body_texts)
                scope = scopes.setdefault(scope_id, {"snapshots": [], "nodes": {}, "node_mismatches": 0})
                members = snapshot.get("members", [])
                memories = snapshot.get("memories") or {}
                by_event = defaultdict(list)
                for raw, node in zip(members, projected['nodes']):
                    ident = node['node']
                    origin = raw.get('origin')
                    if raw.get('level') == 0 and isinstance(origin, dict):
                        by_event[origin.get('event_id')].append(node)
                    previous = scope['nodes'].get(ident)
                    memory = memories.get(ident) or {}
                    value = {**pick(node, ('body', 'level', 'kind', 'text_hash', 'inputs', 'input_summaries', 'underfill')),
                             'leaf_range': self.span(raw.get('leaf_range')),
                             'ordinal_first': previous['ordinal_first'] if previous else raw.get('ordinal'),
                             'origin': None if not isinstance(origin, dict) else {
                                 **pick(origin, ('event_id', 'event_sequence', 'role')),
                                 'source_id': mapping.get(origin.get('event_id')),
                                 'source_span': self.span(origin.get('source_span'))},
                             **pick(memory, ('input_tokens', 'path_revision'))}
                    if previous and any(previous[k] != value[k] for k in ('level', 'leaf_range', 'text_hash', 'inputs')):
                        scope['node_mismatches'] += 1
                    scope['nodes'][ident] = value
                operation = audit.get('operation') or {}
                generation = {'generation': snapshot.get('generation'), 'snapshot_id': snapshot_id,
                              'operation_id': row.get('operation_id'), 'first_seq': row.get('seq'), 'last_seq': row.get('seq'),
                              'events': [{**pick(e, ('event_id', 'event_sequence', 'role')),
                                          'source_id': mapping.get(e.get('event_id')), 'leaf_range': self.span(e.get('leaf_range'))}
                                         for e in snapshot.get('events', [])],
                              'members': [{**pick(n, ('node', 'edge', 'ordinal'))} for n in projected['nodes']],
                              'pending': projected['pending'], 'calls': self.scope_calls(audit),
                              'summaries': projected['summaries'], 'call_details': projected['calls'],
                              'error': pick(operation['error'], ('kind', 'message')) if isinstance(operation.get('error'), dict) else None}
                scope['snapshots'].append(generation)
                snapshots[key] = (projected, by_event, generation)
            projected, by_event, generation = snapshots[key]
            seq = row.get('seq')
            if type(seq) is int:
                generation['first_seq'] = min(generation['first_seq'], seq) if type(generation['first_seq']) is int else seq
                generation['last_seq'] = max(generation['last_seq'], seq) if type(generation['last_seq']) is int else seq
            event_row(index, row, source_id, by_event.get(row.get('event_id'), []))
        # Keep suite-event order while retaining each event's final state and
        # the observed states of earlier waits/resumes, not repeated audits.
        keys = dict.fromkeys(self.build_key(index, row) for index, row, _ in rows)
        return [output[key] for key in keys], scopes

    def build(self, row, source_id, body_texts):
        audit, snapshot = row.get("audit") or {}, row.get("snapshot")
        summaries = [self.summary(a) for a in audit.get("summaries", [])]
        by_body = defaultdict(list)
        for summary in summaries:
            by_body[summary["body"]].append(summary)
        member_summaries, saved_details = [], {}
        for member in (snapshot or {}).get("members", []):
            member_summaries.append([self.input_summary(s) for s in member.get("input_summaries", [])])
        pending = ({level: [self.input_summary(s) for s in group]
                    for level, group in (snapshot.get("pending") or {}).items()} if snapshot is not None else None)
        for group in [*member_summaries, *(pending or {}).values()]:
            for summary in group:
                saved_details[summary["body"]] = pick(summary, ("model", "attempts", "faithful", "reason_hash"))

        def body_hash(body):
            ref = body.get("reference") or {}
            key = self.text(body.get("text"), body.get("body_hash"))
            if key is not None:
                body_texts[(ref.get("node"), ref.get("body"))] = key
            return key

        for body in audit.get("bodies", []):
            body_hash(body)
        members = (snapshot.get("members", []) if snapshot is not None
                   else [{"body": body} for body in audit.get("bodies", [])])
        nodes = []
        for index, member in enumerate(members):
            body = member.get("body") or {}
            ref = body.get("reference") or {}
            attempts = by_body.get(ref.get("body"), [])
            selected = ([a for a in attempts if a["accepted"]] or attempts or [None])[-1]
            verdict = (selected or {}).get("verdict") or {}
            span = member.get("source_span")
            nodes.append({"node": ref.get("node"), "body": ref.get("body"), "kind": body.get("kind"),
                          "input_summaries": member_summaries[index] if snapshot is not None else None,
                          **pick(member, ("level", "ordinal", "edge", "underfill")),
                          "span": [span.get("start"), span.get("end")] if isinstance(span, dict) else span,
                          "text_hash": body_hash(body),
                          "inputs": [r.get("node") for r in member["inputs"]] if "inputs" in member else None,
                          "summary": saved_details.get(ref.get("body")) if not attempts else {
                              **pick(verdict, ("model", "faithful", "reason_hash")), "attempts": len(attempts)}})
        for path in (snapshot or {}).get("paths", {}).values():
            self.text(path.get("text"), path.get("hash"))
        calls = []
        for call in audit.get("calls", []):
            attempts = call.get("attempts")
            error = call.get("terminal_error")
            calls.append({**pick(call, ("role", "model")),
                          "dispatches": len(attempts) if attempts is not None else None,
                          "failed": sum(a.get("failure") is not None for a in attempts) if attempts is not None else None,
                          "terminal_error": (f"{error.get('kind')}: {error.get('message')}" if isinstance(error, dict) else error)})
        return {**pick(row, ("event_id", "seq", "state", "operation_id", "snapshot_id", "audit_error")),
                "source_id": source_id, "nodes": nodes, "pending": pending, "summaries": summaries, "calls": calls}

    @staticmethod
    def build_config(records):
        """First configured build in log order; compare only exported fields."""
        first, mismatches = None, 0
        for row in records:
            if row.get("kind") != "build":
                continue
            config = ((row.get("audit") or {}).get("operation") or {}).get("config")
            if not isinstance(config, dict):
                continue
            selected = {**pick(config, ("chunk_low", "chunk_high", "chunk_target", "build_text_limits", "max_levels",
                                        "leaf_k", "upper_k", "candidate_cap", "memory_tokens", "jev_threshold", "jev_model")),
                        "embedding_model": (config.get("embedding") or {}).get("model"),
                        "generator_model": (config.get("generator") or {}).get("model"),
                        **{key: value for key, value in config.items() if "summary" in key}}
            if first is None:
                first = selected
            else:
                mismatches += selected != first
        return first, mismatches

    def search(self, row, mapping, body_texts, config, record_key, leaf=False, tree=False):
        result = row.get("result")
        error = row.get("error") if isinstance(row.get("error"), dict) else None
        if not isinstance(result, dict):
            if error is None:
                return None
            result = {}  # Failed calls still have a query and native diagnostic.
        mode = self.representation([row], record_key)
        settings = result.get("settings") or {}
        explicit = (settings.get("representation") == "Tree" or
                    (settings.get("representation") == "LeafMemory" and settings.get("jev_input_format") == "LeafMemoryV1"))
        if not explicit and (leaf or tree):
            mode = "tree" if tree else "leaf_memory"  # Failed search inherits this namespace's observed mode.
        leaf, tree = mode == "leaf_memory", mode == "tree"
        sources = row.get("sources") or {}

        def source(value):
            ident = value.get("id") if isinstance(value, dict) else value
            return mapping.get(sources.get(ident))

        def node(value):
            return (value.get("reference") or {}).get("node")

        def text_for(ref):
            return body_texts.get((ref.get("node"), ref.get("body")))

        view = result.get("context_view")
        visible = []
        for body in (view or {}).get("visible", []):
            ref = body.get("reference") or {}
            known_hash = text_for(ref)
            if self.texts.get(known_hash) != body.get("text"):
                known_hash = None  # A visible span can be a slice of the body.
            text_hash = self.text(body.get("text"), body.get("body_hash") or known_hash)
            visible.append({"node": ref.get("node"), "source_id": source(body.get("source")),
                            "role": body.get("role"), "text_hash": text_hash})
            if text_hash is not None:
                body_texts.setdefault((ref.get("node"), ref.get("body")), text_hash)
        items, ranks = [], {}
        for rank, item in enumerate(result.get("items", []), 1):
            path = item.get("path") or {}
            ranks[node(item)] = rank
            self.text(item.get("text"))
            items.append({"rank": rank, "node": node(item), "source_id": source(item.get("source")),
                          **pick(item, ("similarity", "p_use", "rank_utility")),
                          "text_hash": self.text(path.get("text"), path.get("hash")),
                          "path": [{"node": node(part), **pick(part, ("edge", "coverage")),
                                    "text_hash": text_for(part.get("reference") or {})} for part in path.get("parts", [])]})
            if leaf:
                items[-1].update(jev=self.jev(item.get("jev_outcome")), memory=self.memory(item.get("memory"), mapping),
                                 reader_text_hash=self.text(item.get("reader_text")),
                                 reader_sources_n=len(item["reader_sources"]) if item.get("reader_sources") is not None else None)
            elif tree:
                refs = item.get("reader_sources")
                items[-1].update(level=item.get("level"), recorded=self.span(item.get("recorded")),
                                 reader_text_hash=self.text(item.get("reader_text")),
                                 reader_sources_n=len(refs) if refs is not None else None,
                                 sources=[source(ref) for ref in refs] if refs is not None else None,
                                 jev=self.jev(item.get("jev_outcome")))
        omitted = result.get("omitted")
        candidates = []
        for candidate in result.get("candidates", []):
            ident = node(candidate)
            candidates.append({"node": ident, "source_id": source(candidate.get("source")),
                               **pick(candidate, ("level", "similarity", "p_use", "origin", "vector_rank")), "rank": ranks.get(ident),
                               "outcome": "selected" if ident in ranks else (omitted or {}).get(ident, "unselected")})
            if leaf or tree:
                candidates[-1]["jev"] = self.jev(candidate.get("jev_outcome"))
        node_sources = {}
        for candidate in candidates + items:
            ident, source_id = candidate["node"], candidate["source_id"]
            if ident in node_sources and node_sources[ident] != source_id:
                node_sources[ident] = None
            else:
                node_sources[ident] = source_id
        # Current upstream target IDs encode the complete NodeRef (node/body).
        # Match that exact reference; do not infer provenance by splitting an
        # arbitrary ID or by the order of candidates/selected results.
        target_nodes = {}
        for candidate in result.get("candidates", []) + result.get("items", []):
            ref = candidate.get("reference") or {}
            if ref.get("node") is not None and ref.get("body") is not None:
                target_nodes[f"{ref['node']}/{ref['body']}"] = ref["node"]
        jev_trace = result.get("jev_trace")
        batches = None if jev_trace is None else []
        for index, batch in enumerate(jev_trace or []):
            targets, probabilities = batch.get("targets") or [], batch.get("probabilities")
            ids = [target.get("id") for target in targets]
            if isinstance(probabilities, dict):
                # Upstream JevBatchTrace uses BTreeMap<target ID, probability>.
                # Normalize to target order; even equal lengths can have wrong
                # keys. The positional array form in §6.6 remains supported.
                aligned = len(ids) == len(set(ids)) and set(probabilities) == set(ids)
                values = [probabilities.get(ident) for ident in ids]
            else:
                aligned = isinstance(probabilities, list) and len(probabilities) == len(targets)
                values = probabilities
            if probabilities is not None and not aligned:
                self.mismatches.add(("jev", *record_key, index))
            batches.append({"batch": index, **pick(batch, ("model", "error_code")),
                            "attempts": batch.get("attempts", []), "targets": [
                {"node": target_nodes.get(target.get("id"), target.get("id")),
                 "source_id": node_sources.get(target_nodes.get(target.get("id"), target.get("id"))),
                 "probability": values[i] if aligned else None,
                 "target_text_hash": self.text(target.get("target_body")),
                 "path_text_hash": self.text(target.get("full_path"))} for i, target in enumerate(targets)]})
            if leaf or tree:
                answers = batch.get("answers")
                if answers is not None and (not isinstance(answers, dict) or len(ids) != len(set(ids)) or set(answers) != set(ids)):
                    self.mismatches.add(("jev_answers", *record_key, index))
                batches[-1]["output_format"] = batch.get("output_format")
                for raw, target in zip(targets, batches[-1]["targets"]):
                    # Three-class answers are keyed by the full NodeRef, never
                    # by candidate order. path_revision lives on the target.
                    answer = answers.get(raw.get("id")) if isinstance(answers, dict) else None
                    answer = answer if isinstance(answer, dict) else {}
                    provider = answer.get("provider")
                    target.update(jev=self.jev(answer.get("outcome")),
                                  provider=pick(provider, ("choice", "confidence")) if isinstance(provider, dict) else None,
                                  path_revision=raw.get("path_revision"))
        settings = result.get("settings")
        threshold = (settings["jev_threshold"] if isinstance(settings, dict) and "jev_threshold" in settings
                     else row.get("jev_threshold", config.get("jev_threshold")))
        decoded = {"query": row.get("query"), "limit": row.get("limit"), "error": error,
                "jev_threshold": threshold, "settings": settings, "jev_trace": batches,
                **pick(result, ("mode", "degraded", "warnings")), "items": items, "candidates": candidates,
                "context_view": None if view is None else {**pick(view, ("tokens", "tokenizer")),
                    "rendered_hash": self.text(view.get("rendered"), view.get("rendered_hash")), "visible": visible},
                "omitted": omitted}
        if leaf:
            decoded["packed"] = result.get("packed")
            if view is not None:
                memories = view.get("memories")
                decoded["context_view"]["memories"] = None if memories is None else [{
                    **pick(memory.get("reference") or {}, ("node", "body")),
                    "path_revision": memory.get("path_revision"), "source_id": source(memory.get("source")),
                    "layers": [{**pick(layer, ("current", "relation")),
                                **pick(layer.get("reference") or {}, ("node", "body")),
                                "items": [{"child": item.get("child"), "span": self.span(item.get("span"))}
                                          for item in layer.get("items", [])]} for layer in memory.get("layers", [])]
                } for memory in memories]
        if tree:
            if "packed" in result:
                decoded["packed"] = result["packed"]
            nodes = (view or {}).get("nodes")
            if len(nodes or []) != len(items):
                self.mismatches.add(("tree_nodes", *record_key))
            if view is not None:
                decoded["context_view"]["nodes"] = None if nodes is None else [{
                    **pick(node.get("reference") or {}, ("node", "body")),
                    "source_id": source(node.get("source")), "level": node.get("level"),
                    "leaf_range": self.span(node.get("leaf_range")),
                    "sources_n": len(node["sources"]) if node.get("sources") is not None else None,
                    "recorded": self.span(node.get("recorded"))} for node in nodes]
        return decoded

    def case(self, case, written, events, query, report_config):
        namespace = case.get("namespace")
        records = self.logs.get(namespace, [])
        config = dict(report_config)
        for row in records:
            if row.get("kind") == "config":
                config.update(row)
        trace = "full" if any(r.get("kind") in ("build", "search") for r in records) else (
            "off" if config.get("work_trace") == "off" else "missing")
        start = self.cursors.get(namespace, 0)
        match = next((i for i in range(start, len(records))
                      if records[i].get("kind") == "search" and records[i].get("query") == query), None)
        if match is not None:
            self.cursors[namespace] = match + 1
        # Only the builds preceding this query are visible; incremental future
        # writes and later queries must not leak into the current case review.
        stop = match if match is not None else len(records)
        mode = self.representation(records[:stop + (match is not None)], (namespace,))
        scoped = mode is not None
        builds = {}
        for index, row in enumerate(records[:stop]):
            if row.get("kind") != "build":
                continue
            seq = row.get("seq")
            if type(seq) is int and written is not None and seq >= len(written):
                if match is None:
                    continue  # Future events in an incremental episode.
            # v3 retains the history in attempts; legacy keeps its previous
            # last-build-per-seq behavior byte for byte.
            builds[index if scoped else seq if type(seq) is int else ("invalid", index)] = (index, row)
        ordered = sorted(builds.values(), key=lambda pair: (pair[1].get("seq") if type(pair[1].get("seq")) is int else sys.maxsize, pair[0]))
        mapping, output, body_texts, scopes = self.map_builds(namespace, ordered, written, events, scoped)
        result = {"trace": trace, "build": output,
                  "search": self.search(records[match], mapping, body_texts, config, (namespace, match),
                                        mode == "leaf_memory", mode == "tree") if match is not None else None}
        if scoped:
            result.update(representation=mode, scopes=scopes)
        return result

    def map_builds(self, namespace, ordered, written, events, leaf=False):
        """One source-mapping rule shared by case and whole-episode extraction."""
        mapping, output, body_texts = {}, [], {}
        for index, row in ordered:
            seq, ident = row.get("seq"), row.get("event_id")
            source_id = written[seq] if written is not None and type(seq) is int and 0 <= seq < len(written) else None
            content = (events.get(source_id) or {}).get("content")
            if not isinstance(content, str) or row.get("text_sha256") != hashlib.sha256(content.encode()).hexdigest():
                source_id = None
            if source_id is None:
                self.mismatches.add((namespace, index))
            if ident in mapping and mapping[ident] != source_id:
                source_id = None
                self.mismatches.add((namespace, index))
                for previous in output:
                    if previous["event_id"] == ident:
                        previous["source_id"] = None
            mapping[ident] = source_id
            output.append({"event_id": ident, "source_id": source_id} if leaf else self.build(row, source_id, body_texts))
        scopes = None
        if leaf:
            output, scopes = self.leaf_builds([(index, row, decoded["source_id"]) for (index, row), decoded in zip(ordered, output)],
                                             mapping, body_texts)
        return mapping, output, body_texts, scopes

    def episode(self, namespace, events, questions, report_config):
        """Resolve searches per question, retaining majority-build diagnostics.

        Only matched questions vote; ties prefer the newer attempt. With no
        matches, keep the latest log (including missing/off/corrupt evidence).
        Alternate payloads/chronologies stay internal so work_cell can classify
        against the actual query's build history, not the majority snapshot.
        """
        attempts = self.attempts.get(namespace, [])
        if len(attempts) <= 1:
            return self._episode(namespace, events, questions, report_config)
        seen = set()
        for question in questions:
            if question.get("query") in seen:
                raise ValueError(f"duplicate work query in episode: {question['id']}")
            seen.add(question.get("query"))
        latest = {}
        for index, attempt in enumerate(attempts):
            for query in attempt["queries"]:
                latest[query] = index
        selected = {q["id"]: latest[q.get("query")] for q in questions if q.get("query") in latest}
        votes = Counter(selected.values())
        primary = max(votes, key=lambda i: (votes[i], i)) if votes else len(attempts) - 1
        groups = {primary: []}
        for question in questions:
            groups.setdefault(selected.get(question["id"], primary), []).append(question)
        decoded = {i: self._episode(namespace, events, group, report_config, attempts[i])
                   for i, group in groups.items()}
        payload, chronology = decoded[primary]
        payload = dict(payload)
        texts = dict(payload.pop("texts"))
        searches = {}
        alternates = {}
        for question in questions:
            ident = question["id"]
            index = selected.get(ident, primary)
            source, positions = decoded[index]
            if ident in source["searches"]:
                searches[ident] = source["searches"][ident]
            if index != primary:
                alternates[ident] = {"payload": source, "chronology": positions, "attempt": attempts[index]["attempt"]}
                # Only query-referenced alternate texts belong in this file;
                # do not copy another attempt's entire build text table.
                for key, text in self.referenced_texts(source["searches"].get(ident), source["texts"]).items():
                    if key in texts and texts[key] != text:
                        raise ValueError(f"work trace text hash collision: {key}")
                    texts[key] = text
        payload["searches"] = searches
        payload["mapping_mismatches"] = sum(p["mapping_mismatches"] for p, _ in decoded.values())
        payload["texts"] = self.referenced_texts(payload, texts)
        if alternates:
            chronology = {**chronology, "alternate_searches": alternates}
        return payload, chronology

    def _episode(self, namespace, events, questions, report_config, attempt=None):
        """Extract each build once, with an isolated text table for this file.

        Returns (public payload, chronology). Chronology holds original log-row
        positions aligned with builds/searches; it is not serialized. Keeping it
        separate from seq-sorted builds prevents after-query writes being counted
        as evidence available to an earlier search. Questions retain suite order.
        """
        scoped = copy.copy(self)
        scoped.texts, scoped.mismatches = {}, set()
        records = self.logs.get(namespace, [])
        if attempt is not None and attempt is not self.attempts[namespace][-1]:
            records, _, _ = self.read_records(self.directory / attempt["path"])
        config = dict(report_config)
        for row in records:
            if row.get("kind") == "config":
                config.update(row)
        trace = "full" if any(r.get("kind") in ("build", "search") for r in records) else (
            "off" if config.get("work_trace") == "off" else "missing")
        ordered = sorted(((i, row) for i, row in enumerate(records) if row.get("kind") == "build"),
                         key=lambda pair: (pair[1].get("seq") if type(pair[1].get("seq")) is int else sys.maxsize, pair[0]))
        written = [event["source_id"] for event in events]
        mode = scoped.representation(records, (namespace,))
        is_scoped = mode is not None
        mapping, builds, body_texts, scopes = scoped.map_builds(namespace, ordered, written, {e["source_id"]: e for e in events}, is_scoped)
        before, n, first_positions = [], 0, {}
        for index, row in enumerate(records):
            before.append(n)
            if row.get("kind") == "build":
                key = scoped.build_key(index, row)
                if not is_scoped or key not in first_positions:
                    n += 1
                    first_positions.setdefault(key, index)
        searches, positions, seen = {}, {}, set()
        for question in questions:
            query = question.get("query")
            if query in seen:
                raise ValueError(f"duplicate work query in episode: {question['id']}")
            seen.add(query)
            match = next((i for i in range(len(records))
                          if records[i].get("kind") == "search" and records[i].get("query") == query), None)
            if match is None:
                continue
            decoded = scoped.search(records[match], mapping, body_texts, config, (namespace, match),
                                    mode == "leaf_memory", mode == "tree")
            searches[question["id"]] = {**(decoded or {}), "builds_before": before[match]}
            positions[question["id"]] = match
        build_config, config_mismatches = scoped.build_config(records)
        payload = {"namespace": namespace, "trace": trace,
                   "build_config": build_config, "build_config_mismatches": config_mismatches,
                   "log": {"path": attempt["path"] if attempt is not None else self.paths.get(namespace),
                           "rows": {kind: sum(r.get("kind") == kind for r in records) for kind in ("build", "search", "config")},
                           "corrupt_lines": attempt["corrupt_lines"] if attempt is not None else self.corrupt_lines.get(namespace, 0)},
                   "config": pick(config, ("work_trace", "jev_threshold")),
                   "events": [{**pick(event, ("source_id", "role", "timestamp", "turn_group")), "seq": i,
                               "chars": len(event["content"]) if isinstance(event.get("content"), str) else None}
                              for i, event in enumerate(events)],
                   "builds": builds, "searches": searches, "mapping_mismatches": len(scoped.mismatches)}
        if is_scoped:
            # Preserve byte-identical legacy v2 exports (no new null keys).
            payload.update(version=3, representation=mode, scopes=scopes)
        payload["texts"] = self.referenced_texts(payload, scoped.texts)
        build_positions = ([first_positions[key] for key in dict.fromkeys(scoped.build_key(i, row) for i, row in ordered)]
                           if is_scoped else [i for i, _ in ordered])
        chronology = {"builds": build_positions, "searches": positions}
        if is_scoped:
            # Internal-only history lets stage classification use the state at
            # each query even though the public rows contain the final state.
            chronology['build_history'] = scoped.leaf_history
        return payload, chronology

    @staticmethod
    def referenced_texts(payload, texts):
        referenced = set()

        def references(value):
            if isinstance(value, str) and value in texts:
                referenced.add(value)
            elif isinstance(value, dict):
                for child in value.values():
                    references(child)
            elif isinstance(value, list):
                for child in value:
                    references(child)

        references(payload)
        return {key: texts[key] for key in sorted(referenced)}


def event_description(source_id, events):
    attrs = (events.get(source_id) or {}).get("attributes") or {}
    return f"{source_id}（type={attrs.get('type')}, scope={attrs.get('scope')}, superseded_by={attrs.get('superseded_by')}）"


def automatic_analysis(case, events):
    gold = case["gold"].get("evidence_ids")
    returned = case["output"]["retrieved_source_ids"]
    if gold is None or returned is None:
        return []
    notes = []
    # Failed executions are not scored misses, even when a partial output exists.
    if case["status"] != "scored":
        return ["执行未完成；返回结果不能据此判定为检索未命中。"]
    for source in gold:
        if source not in returned:
            notes.append("漏掉 gold：" + event_description(source, events))
        else:
            notes.append(f"gold {source} 的来源排名：{returned.index(source) + 1}。")
    for source in returned:
        if source in gold:
            continue
        attrs = (events.get(source) or {}).get("attributes") or {}
        scopes, query_scopes = attrs.get("scope"), case["input"]["scope"]
        if attrs.get("superseded_by") or (attrs.get("epistemic_status") is not None and attrs["epistemic_status"] != "active"):
            label = "被取代的事实"
        elif scopes and query_scopes and not set(scopes).intersection(query_scopes) and not set(scopes).intersection(("universal", "user:universal")):
            label = "作用域不匹配"
        elif source not in events:
            label = "来源事件不可用，无法判定相关性"
        else:
            label = "无关"
        notes.append(f"返回非 gold {source}：{label}。")
    if gold == [] and case["gold"].get("exhaustive") is True:
        items = case["output"]["items"]
        if items is not None and items:
            notes.append(f"应为空却返回 {len(items)} 条。")
        elif returned:
            notes.append(f"应为空却返回 {len(returned)} 个来源（无 trace 时条目数未知）。")
        elif case["verdict"] == "empty_correct":
            notes.append("空 gold：正确返回空结果。")
    for name, control in sorted(case["controls"].items()):
        if control is None:
            notes.append(f"{name}：无同题同 repeat 的对照结果。")
            continue
        if control["verdict"] == "hit_all":
            notes.append(f"{name} 命中全部。")
        other = control["retrieved_source_ids"]
        if control["status"] != "scored" or other is None:
            continue
        shared_missing = [source for source in gold if source not in returned and source not in other]
        extra = [source for source in other if source not in returned]
        if shared_missing:
            notes.append(f"{name} 同样漏掉：{', '.join(shared_missing)}。")
        if extra:
            notes.append(f"{name} 比本运行多返回：{', '.join(extra)}。")
    return notes


def timestamp_key(value):
    if value is None:
        return float("-inf")
    match = re.fullmatch(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})", value)
    if not match:
        raise ValueError(f"invalid RFC3339 timestamp: {value!r}")
    # Go writes RFC3339Nano with variable precision; older Python versions
    # only accept 3/6 fractional digits. Sort without losing nanoseconds or
    # changing the recorded timestamp in the output.
    whole, fraction, zone = match.groups()
    seconds = int(datetime.fromisoformat(whole + zone.replace("Z", "+00:00")).timestamp())
    return seconds * 1_000_000_000 + int((fraction or "").ljust(9, "0"))


def build_review(report_dir, suite_path, name, controls=(), notes=None, glossary=None):
    report, provenance = load_report(report_dir)
    suite = read_json(suite_path)
    if suite.get("suite_id") != report.get("suite_id"):
        raise ValueError("suite_id differs between report and suite")
    episodes = suite.get("episodes")
    if episodes is None and suite.get("container"):
        episodes = list(jsonl(Path(suite_path).parent / "episodes.jsonl"))
    if not isinstance(episodes, list):
        raise ValueError("suite requires episodes")
    events, suite_cases = {}, {}
    for episode in episodes:
        for event in episode.get("events", []):
            source = event["source_id"]
            value = pick(event, ("content", "role", "session", "owner", "attributes"))
            if source in events and events[source] != value:
                raise ValueError(f"ambiguous source_id across episodes: {source}")
            events[source] = value
        for case in episode.get("cases", []):
            key = (episode["id"], case["id"])
            if key in suite_cases:
                raise ValueError(f"duplicate suite case: {key}")
            suite_cases[key] = (episode, case)
    manual = read_json(notes) if notes else {}
    if not isinstance(manual, dict) or any(not isinstance(v, str) for v in manual.values()):
        raise ValueError("notes must be an object mapping case_id to text")
    control_reports, control_summaries = {}, []
    for directory in sorted(map(Path, controls), key=lambda p: str(p.resolve())):
        other, identity = load_report(directory)
        check_control(report, other, directory.name)
        if directory.name in control_reports:
            raise ValueError(f"duplicate control name: {directory.name}")
        control_reports[directory.name] = (other, case_map(other["cases"]))
        control_summaries.append({"name": directory.name, "sut": pick(other.get("sut"), ("name", "variant")),
                                  **identity, "metrics": other["summary"].get("metrics")})
    histories = []
    for path in sorted((Path(report_dir) / "resume-history").glob("*/report.json")):
        histories.append((path.parent.relative_to(report_dir).as_posix(), case_map(read_json(path)["cases"])))
    histories.append(("report.json", case_map(report["cases"])))
    logs = log_index(report_dir)
    work = WorkTraces(report_dir)
    trace_config = ((report.get("sut") or {}).get("describe") or {}).get("config") or {}
    rows = []
    for case in report["cases"]:
        key = (case.get("episode_id"), case.get("case_id"))
        if key not in suite_cases:
            raise ValueError(f"report case not present in suite: {key}")
        episode, original = suite_cases[key]
        cutoff = case.get("cutoff", original.get("cutoff"))
        if cutoff is not None and (type(cutoff) is not int or not 0 <= cutoff <= len(episode["events"])):
            raise ValueError(f"invalid cutoff for {key}: {cutoff}")
        written = [e["source_id"] for e in episode["events"][:cutoff]] if cutoff is not None else None
        gold = pick(original.get("gold"), ("evidence_ids", "exhaustive", "abstain"))
        row = {**pick(case, ("case_id", "sample_id", "category", "kind", "episode_id", "status")), "cutoff": cutoff,
               "input": {**pick(original, ("query", "user", "scope", "as_of")), "events_written": written},
               "gold": gold, "output": output_of(case), "metrics": metrics_of(case),
               "verdict": verdict(case, gold, report.get("mode")),
               "error": error_of(case, logs.get(case.get("namespace"), []), written),
               "timings": {**dict.fromkeys(("reset_ms", "ready_ms", "ingest_ms", "query_ms")), **(case.get("timings") or {})},
               "attempts": [], "controls": {}}
        for index, (source, previous) in enumerate(histories):
            attempt = previous.get(sample_key(case))
            if attempt is not None:
                row["attempts"].append({"pass": index, "source": source,
                                        **pick(attempt, ("status", "error_type", "error_detail"))})
        for control_name, (other, mapping) in control_reports.items():
            match = mapping.get(sample_key(case))
            row["controls"][control_name] = (None if match is None else {
                **pick(output_of(match), ("retrieved_source_ids", "items")),
                "metrics": metrics_of(match), "status": match.get("status"),
                "verdict": verdict(match, gold, other.get("mode"))})
        row["analysis"] = {"auto": automatic_analysis(row, events), "manual": manual.get(case.get("case_id"))}
        row["work"] = work.case(case, written, events, original.get("query"), trace_config)
        rows.append(row)
    categories = {}
    for category in sorted({r["category"] for r in rows if r["category"] is not None}):
        group = [r for r in rows if r["category"] == category]
        recalls = [r["metrics"]["recall"] for r in group if r["status"] == "scored" and r["metrics"]["recall"] is not None]
        categories[category] = {"total": len(group), "verdicts": dict(Counter(r["verdict"] for r in group if r["verdict"])),
                                "recall_mean": math.fsum(recalls) / len(recalls) if recalls else None, "n_recall": len(recalls)}
    dates = [report.get("started_at_utc")]
    dates.extend(h.get("resumed_at_utc") for h in report.get("resume_history", []))
    generated_at = max(dates, key=timestamp_key)
    metric_ids = set(report["summary"].get("metrics") or {})
    for row in rows:
        metric_ids.update(row["metrics"])
    return {"version": 1, "generated_at": generated_at, "glossary": load_glossary(metric_ids, glossary),
            "run": {"name": name, **provenance,
                    "sut": pick(report.get("sut"), ("name", "variant", "fingerprint")),
                    "suite": {"id": report.get("suite_id"), "sha256": report.get("suite_sha256"),
                              "path": display_path(suite_path), "track": suite.get("track")},
                    **pick(report, ("mode", "run_id", "run_seed", "started_at_utc", "wall_seconds", "resume_count", "memeval_commit", "framework_version")),
                    **{key: protocol_value(report, key) for key in ("reset_policy", "top_k", "max_context_chars", "trace_text")},
                    "sut_usage": native_usage(report_dir)},
            "summary": {"total": len(rows), "by_status": dict(Counter(r["status"] for r in rows if r["status"] is not None)),
                        "by_verdict": {key: sum(r["verdict"] == key for r in rows) for key in VERDICTS},
                        "by_category": categories, "metrics": report["summary"].get("metrics"),
                        "error_stages": {key: sum(r["error"] is not None and r["error"]["stage"] == key for r in rows) for key in STAGES},
                        "work_mapping_mismatches": len(work.mismatches)},
            "controls": control_summaries, "events": events, "texts": work.texts, "cases": rows}


def encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + "\n"


def javascript(global_name, value):
    # Safe both as a script file and when inlined in an offline page.
    body = encode(value).rstrip().replace("</", "<\\/").replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")
    return f"window.{global_name} = {body};\n"


def write_review(review, out):
    name = review["run"]["name"]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", name) or name == "index":
        raise ValueError("name must be a safe filename (not index)")
    out = Path(out)
    index_path = out / "index.js"
    entries = []
    if index_path.exists():
        match = re.fullmatch(r"\s*window\.MEMEVAL_CASE_RUNS\s*=\s*(.*?)\s*;\s*", index_path.read_text(encoding="utf-8"), re.S)
        if not match:
            raise ValueError(f"{index_path}: invalid MEMEVAL_CASE_RUNS assignment")
        entries = json.loads(match[1])
        if not isinstance(entries, list) or any(not isinstance(e, dict) or not isinstance(e.get("name"), str) for e in entries):
            raise ValueError(f"{index_path}: expected run entries with name")
    # Index entries carry the run card plus the script filename for the loader.
    entry = {**review["run"], "generated_at": review["generated_at"], "file": name + ".js"}
    entries = [e for e in entries if e["name"] != name] + [entry]
    entries.sort(key=lambda e: (-timestamp_key(e.get("generated_at")), e["name"]))
    payloads = {name + ".json": encode(review), name + ".js": javascript("MEMEVAL_CASES", review),
                "index.js": javascript("MEMEVAL_CASE_RUNS", entries)}
    out.mkdir(parents=True, exist_ok=True)
    for filename, text in payloads.items():
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=out, delete=False) as stream:
                temporary = Path(stream.name)
                stream.write(text)
            temporary.replace(out / filename)
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
    return {filename: len(text.encode("utf-8")) for filename, text in payloads.items()}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report_dir", type=Path)
    parser.add_argument("--suite", type=Path, required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--control", action="append", type=Path, default=[])
    parser.add_argument("--notes", type=Path)
    parser.add_argument("--glossary", type=Path,
                        help="MetricDef JSON export or results index.json (default: bin/memeval results glossary)")
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        review = build_review(args.report_dir, args.suite, args.name, args.control, args.notes, args.glossary)
        sizes = write_review(review, args.out)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(2, f"build_case_review: {error}\n")
    print(encode({"out": str(args.out.resolve()), "by_verdict": review["summary"]["by_verdict"],
                  "error_stages": review["summary"]["error_stages"], "files_bytes": sizes}), end="")


if __name__ == "__main__":
    main()
