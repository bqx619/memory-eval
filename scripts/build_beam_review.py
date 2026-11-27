#!/usr/bin/env python3
"""Build offline BEAM review data using only the standard library.

Writes beam-100k.json and window.MEMEVAL_BEAM in beam-100k.js, only under --out.
The contract contains dataset, questions, runs, results and glossary. Questions
are sorted by numeric episode ID then question ID; each run has one result cell
per question. Only repeat_index=0 supplies cells and per-episode mean_score;
reported summary/category metrics and status_counts are preserved, including
repeats. Missing facts are null, including cells for unexecuted questions.
Cells add evidence_hits and evidence_recall from the intersection of direct
source IDs and suite evidence_ids (deduplicated event IDs). Each returned item
contributes only the events it directly represents. LeafMemory rendered items
use L0 metadata.origins.event_sequence, verified by a whitespace-normalised
info[0] substring of the suite event content; malformed/mismatched origins warn
and are excluded, never replaced by their summary dependency closure. Tree
items match response IDs to --work search nodes: only level 0 contributes its
own leaf events; upper nodes are summary closure and contribute no direct IDs.
Missing/unmatched Tree traces warn and conservatively count single-source
items only; missing/invalid ID mappings never fall back to their full closure.
Other items map anonymous source_ids to retrieved_source_ids in first-occurrence
order. Missing response items use retrieved_source_ids; invalid positional
mappings warn and fall back to that list. Suite events are read once.
Empty gold or an unexecuted question yields null for evidence_hits/recall.
This measures direct references before the reader character budget, not
evidence text reaching the reader. Runs, by_category and by_episode add derived
beam.evidence_recall: mean over scored, non-null samples, n their count; both
are null when there are none. Like reported metrics, aggregation includes
repeats, while cells show repeat_index=0. Retrieved ID lists are not exported.
Cells also add retrieved_ids (deduplicated direct count; zero for empty retrieval,
null if unexecuted), evidence_precision and evidence_f1. Precision/F1 are null
without gold or references; defined zero precision/recall yields F1=0.
Metrics additionally include beam.source_ids_per_question and
beam.source_ids_episode_share (count / cutoff, falling back to the episode
event count only when cutoff is absent), beam.evidence_precision and
beam.evidence_f1. All use scored samples with gold, including repeats; precision
and F1 also require nonempty references. A missing/nonpositive share denominator
yields null. Each aggregate is {mean, n}, both null without eligible samples.
The former closure-based measure is preserved as cited_ids (deduplicated
retrieved_source_ids count), cited_hits and citation_coverage in cells, and
beam.citation_coverage in all three metric aggregates with the recall sample
rules. Coverage/hits are null without gold, and all three fields are null for
unexecuted questions. No source ID lists or event text are exported.

generated_at uses recorded start/resume timestamps, never wall time or mtime.
report_sha256 hashes the exact report bytes read. Resume history is read only
from committed resume-history/*/report.json plus the current report metadata;
staged and abandoned attempts are not mixed into results. RPC logs under logs/
(including resume-*/ subdirectories) are matched by namespace and error text.
Native attribution reuses cogletnet_failures, without copying its rules.
resume_history also exposes cases and hash-only sut_change identities;
sut_changes counts those identity-changing resumes. A result's attempt is
root/resume-N only when its selected work search establishes that origin;
otherwise it is null. Counts and status filters cannot identify a question.
runs[].sut.embedding normalises report.sut.declared.embedding to null or
{provider, model, dimensions, local, declared}. Dict fields are type-checked
(dimensions excludes booleans); missing fields are null, invalid fields warn.
Strings retain only their trimmed declaration, without inferring model/local.

glossary is an id-sorted MetricDef list restricted to emitted beam.* metrics.
Definitions come from --glossary (a JSON list or an index's glossary), otherwise
bin/memeval results glossary; unavailable exports produce a warning. The local
derived reference-count, evidence recall, precision, F1 and citation coverage
definitions are always added, with unique IDs.
Shared dataset protocol fields that differ between runs are null with a warning.

--work NAME adds compact per-question evidence diagnostics to the main file,
and work/NAME/EPISODE.{json,js} with an independent, referenced-only text table.
Resumed namespaces are extracted separately: the largest case group keeps the
episode filename; others use -r1, -r2, ... in first-occurrence suite order.
Each question's work.file identifies its namespace; episode counters sum all
namespace files, and episode trace is "mixed" when their trace states differ.
Within a namespace, each question uses the latest attempt with its search.
Segment builds/config use the attempt with most matched questions (ties go to
the newest). Other questions add work.attempt and attempt_mismatch=true with
a warning; their stages use their own build chronology, and only referenced
alternate search texts are merged into the episode file's text table.
searches[qid].reader_prompt reconstructs the items reader messages from the
primary report case (never from SUT trace text), with local template and
request-hash checks. Prompt-only entries do not count as search evidence.
The wire Sample is taken from a reader row in llm_ledger.jsonl indexed by
request_sha, never inferred from the report's public sample_id. reader_prompt.
ledger exposes that row's token counts, latency, timestamp and returned model.
--llm-endpoint / MEMEVAL_OPENAI_BASE_URL is used only for hashing, never exported.
The key follows the evaluator code: Template=beam/reader-v1@SHA/input=items,
Endpoint=base URL with /chat/completions appended, and Go JSON escaping.
Episode payload v2 adds body IDs, input/pending summaries, summary diagnostics
and the first build configuration plus a count of differing later builds.
Tree shares scoped payload v3 with LeafMemory but uses representation="tree"
and context_view.nodes, not memory layers. Returned Tree items carry their
complete coverage sources; any covered evidence event is selected. Unreturned
candidates only diagnose the representative source, not unseen leaf coverage.
Tree work cells add jev_summary and tree.{items_by_level,recorded_items,
direct_sources,covered_sources}; direct_sources counts distinct level 0 sources,
while covered_sources retains the sum of all returned nodes' coverage counts.
The evidence-funnel stage remains based on coverage. After work extraction,
Tree's five derived direct-source metrics and cells are recomputed using each
question's own namespace/attempt search; citation coverage remains unchanged.
Without --work, report variant/retrieval_mode identifies Tree for fallback.
packed is present only if the native trace contains it.
Tree episode summaries add scopes, snapshots, members_by_level and leaf_tokens
from existing LeafMemoryRecord.input_tokens. Body indexing does not record
per-node embedding input tokens: Tree's leaf_tokens are empty, and each observed
node_tokens_by_level entry is {n:0,mean:null,max:null}. Summary tokens, character
counts and batch usage are not substitutes. Tree response UUIDs use the runner's existing
first-occurrence mapping across all items, including multi-source nodes.
Evidence is used only for after-the-fact review, never to alter SUT inputs.
"""

import argparse
from collections import Counter, defaultdict
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sys
import tempfile
from urllib.parse import urlsplit, urlunsplit

import build_case_review as common
import cogletnet_failures as attribution


SCORE = "beam.llm_judge_score"
ORDER = "beam.event_ordering.final"
EVIDENCE = "beam.evidence_recall"
SOURCE_IDS = "beam.source_ids_per_question"
SOURCE_SHARE = "beam.source_ids_episode_share"
PRECISION = "beam.evidence_precision"
F1 = "beam.evidence_f1"
CITATION = "beam.citation_coverage"
EVIDENCE_METRICS = (EVIDENCE, SOURCE_IDS, SOURCE_SHARE, PRECISION, F1, CITATION)
LEAF_MEMORY_HEADER = "认元上下文格式：leaf-memory-context-v1。"
READER_TEMPLATES = Path(__file__).resolve().parents[1] / "internal/scoring/judge/beam/templates.json"
READER_LEDGER_FIELDS = ("prompt_tokens", "completion_tokens", "latency_ms", "ts", "model_returned")


def warn(message):
    print(f"build_beam_review: {message}", file=sys.stderr)


def reader_template(reader):
    raw = READER_TEMPLATES.read_bytes()
    sha = hashlib.sha256(raw).hexdigest()
    if sha != reader.get("template_sha256"):
        warn("reader template_sha256 differs from the local templates.json; rebuilding with the local template")
    return json.loads(raw)["reader_note"], sha


def reader_messages(items, query, budget, note):
    """Mirror beam.Build(items): Unicode code points, including separators."""
    texts = [item.get("text", "") for item in items]
    if any(not isinstance(text, str) for text in texts):
        raise ValueError("reader response item text must be a string")
    chars = sum(map(len, texts)) + max(0, len(texts) - 1)
    while texts and chars > budget:
        chars -= len(texts.pop())
        if texts:
            chars -= 1
    messages = [{"role": "user", "content": "\n".join(texts)}] if texts else []
    messages.append({"role": "user", "content": note.replace("{query}", query)})
    return messages, len(texts), chars


def go_json_bytes(value):
    """encoding/json.Marshal escaping; preserve the cassette wire field order."""
    text = json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
    for char, escaped in (("<", r"\u003c"), (">", r"\u003e"), ("&", r"\u0026"),
                          ("\u2028", r"\u2028"), ("\u2029", r"\u2029")):
        text = text.replace(char, escaped)
    return text.encode("utf-8")


def reader_request_sha(prompt, sample, endpoint):
    # Fields and their order match internal/llm/client.go key(). Do not use
    # common.encode (sorted/pretty JSON) or serialize the exported metadata.
    wire = {"Provider": prompt["provider"], "Endpoint": endpoint, "API": "chat/completions",
            "Model": prompt["model"], "Template": prompt["template_id"], "Client": "memeval-llm-v1",
            "Sample": sample, "Purpose": "reader", "Messages": prompt["messages"],
            "Temperature": 0, "MaxTokens": prompt["max_tokens"], "N": 1, "Extra": None}
    return hashlib.sha256(go_json_bytes(wire)).hexdigest()


def reader_template_id(sha):
    # beam.Answer adds @SHA first; runner.readerClient then appends input mode.
    return "beam/reader-v1@" + sha + "/input=items"


def reader_endpoint(base):
    # Engine.endpoint returns the full request URL, not the configured base.
    # Do not include the supplied URL in errors (it may contain credentials).
    parsed = urlsplit(base)
    if (parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username is not None
            or parsed.query or parsed.fragment):
        raise ValueError("invalid reader endpoint")
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path.rstrip("/") + "/chat/completions", "", ""))


def reader_ledger(directory):
    """Index the run ledger once; the last matching reader attempt wins.

    Retries append multiple rows with the same request SHA. Keep the final
    row's metadata, not sums across attempts. Never substitute a judge row.
    """
    path = Path(directory) / "llm_ledger.jsonl"
    try:
        stream = path.open(encoding="utf-8")
    except FileNotFoundError:
        return {}
    index, corrupt = {}, 0
    with stream:
        for line in stream:
            if not line.strip():
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                corrupt += 1
                continue
            if not isinstance(row, dict) or row.get("purpose") != "reader":
                continue
            sha = row.get("request_sha")
            if isinstance(sha, str) and sha:
                index[sha] = common.pick(row, ("sample_id", *READER_LEDGER_FIELDS))
    if corrupt:
        warn(f"{path}: {corrupt} malformed ledger lines skipped")
    return index


def reader_prompt(case, reader, template, endpoint, ledger=None):
    """Reconstruct a reported items reader call, never using SUT trace text."""
    case = case or {}
    qa = case.get("qa") or {}
    if not qa.get("reader_request_sha"):
        return None
    budget = reader.get("context_budget_chars")
    if reader.get("input_mode") != "items" or reader.get("fullcontext"):
        warn(f"reader prompt {case.get('case_id')}: only items input can be reconstructed")
        return None
    if type(budget) is not int or budget < 0 or not isinstance(case.get("query"), str):
        warn(f"reader prompt {case.get('case_id')}: query/context budget unavailable; not reconstructing")
        return None
    note, sha = template
    items = (case.get("response") or {}).get("items") or []
    messages, kept, chars = reader_messages(items, case["query"], budget, note)
    truncated = kept < len(items)
    if "truncated" in qa and qa["truncated"] != truncated:
        warn(f"reader prompt {case.get('case_id')}: rebuilt truncated differs from qa.truncated")
    output = qa.get("reader_output") or {}
    entry = (ledger or {}).get(qa["reader_request_sha"])
    prompt = {"template_id": reader_template_id(sha),
              "provider": reader.get("provider"), "model": reader.get("model"),
              "temperature": 0, "max_tokens": output.get("effective_max_tokens", reader.get("effective_max_tokens")),
              "n": 1, "input_mode": reader["input_mode"], "context_budget_chars": budget,
              "items_returned": len(items), "items_in_prompt": kept, "dropped_items": len(items) - kept,
              "truncated": truncated, "chars": chars, "messages": messages,
              "request_sha": qa["reader_request_sha"], "verified": None, "verify_note": None,
              "reader_output": qa.get("reader_output"),
              "ledger": common.pick(entry, READER_LEDGER_FIELDS) if entry is not None else None}
    if entry is None:
        prompt["verify_note"] = "ledger 无此 request_sha 的 reader 行"
    elif not endpoint:
        prompt["verify_note"] = "未提供 endpoint；未校验 request_sha"
    elif not entry.get("sample_id") or not prompt["provider"] or not prompt["model"]:
        prompt["verify_note"] = "缺少 ledger sample_id/provider/model；未校验 request_sha"
    elif prompt["provider"] != "openai-compat":
        prompt["verify_note"] = "当前仅重建 openai-compat 的 chat/completions 请求哈希；未校验"
    else:
        try:
            wire_endpoint = reader_endpoint(endpoint)
        except ValueError:
            prompt["verify_note"] = "endpoint 无效（须为无凭据、query、fragment 的 HTTP(S) base URL）；未校验"
            return prompt
        computed = reader_request_sha(prompt, entry["sample_id"], wire_endpoint)
        prompt["verified"] = computed == prompt["request_sha"]
        if not prompt["verified"]:
            prompt["verify_note"] = f"request_sha 不一致；重算值 {computed[:12]}（消息保留按规则重建的原文）"
    return prompt


def episode_key(value):
    text = str(value)
    return (0, int(text), text) if text.isdigit() else (1, 0, text)


def rows(path):
    # Suite/index rows are authoritative inputs: unlike capped logs, malformed
    # JSON must not be silently skipped.
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            if line.strip():
                yield json.loads(line)


def unique(mapping, key, value, label):
    if key in mapping and mapping[key] != value:
        raise ValueError(f"conflicting duplicate {label}: {key}")
    mapping[key] = value


def load_suite(directory):
    directory = Path(directory)
    suite = common.read_json(directory / "suite.json")
    index = {}
    for row in rows(directory / "episodes.index.jsonl"):
        unique(index, row["id"], common.pick(row, ("id", "cluster_id", "events", "bytes", "cases", "sha256")), "episode index")
    questions, event_contents = {}, {}
    for episode in rows(directory / "episodes.jsonl"):
        if episode["id"] not in index:
            raise ValueError(f"episode missing from index: {episode['id']}")
        events = event_contents.setdefault(episode["id"], {})
        for event in episode.get("events", []):
            if event.get("source_id") is not None:
                unique(events, event["source_id"], event.get("content"), "source event")
        for case in episode.get("cases", []):
            gold = case.get("gold") or {}
            rubric = gold.get("rubric") or {}
            items = rubric.get("items")
            question = {**common.pick(case, ("id", "category", "question_type", "kind", "difficulty", "cutoff", "query", "query_point")),
                        "episode_id": episode["id"],
                        "cluster_id": case.get("cluster_id", episode.get("cluster_id")),
                        "rubric": [common.pick(item, ("text", "polarity", "weight")) for item in items] if items is not None else None,
                        "evidence_ids": gold.get("evidence_ids"), "aggregate": rubric.get("aggregate")}
            unique(questions, case["id"], question, "question")
    ordered = sorted(questions.values(), key=lambda q: (episode_key(q["episode_id"]), q["id"]))
    return suite, sorted(index.values(), key=lambda e: episode_key(e["id"])), ordered, event_contents


def history(directory, report):
    reports = [common.read_json(path) for path in sorted((directory / "resume-history").glob("*/report.json"))]
    reports.append(report)
    entries = {}
    dates = []
    for item in reports:
        dates.append(item.get("started_at_utc"))
        for entry in item.get("resume_history") or []:
            change = entry.get("sut_change")
            hashes = ("manifest_sha256", "env_lock_sha256", "source_snapshot_sha256")
            change = {side: common.pick(change.get(side), hashes) for side in ("previous", "current")} if change else None
            cases = entry.get("cases")
            row = {"at": entry.get("resumed_at_utc"), "statuses": entry.get("statuses"),
                   "cases": cases if type(cases) is int else None, "sut_change": change}
            entries[common.encode(row)] = row
            dates.append(row["at"])
    return sorted(entries.values(), key=lambda x: (common.timestamp_key(x["at"]), common.encode(x))), dates


def failure(case, logs, cogletnet):
    if case.get("status") != "error":
        return None
    message = case.get("error_detail") or None
    error_type = case.get("error_type")
    matching = [record for record in logs if message and message in (record.get("error"), record.get("detail"))]
    # Later attempts in the same namespace must not lend native causes to a
    # different failure, especially an evaluator error after successful retrieval.
    selected = max(matching, key=lambda r: common.timestamp_key(r.get("time"))) if matching else None
    kinds, failed_events = {}, []
    if cogletnet and selected and error_type not in attribution.EVALUATOR_TYPES:
        kinds, failed_events, _ = attribution.native_records([selected])
    cls = attribution.classify([error_type], kinds, failed_events)
    if not cogletnet and cls == "unclassified" and (selected or error_type == "*jsonrpc.RPCError"):
        cls = "bridge/transport"
    if cls == "unclassified":
        cls = None
    if message is None and cls is None:
        return None
    return {"class": cls, "native_kinds": sorted(kinds), "message": message}


def tree_report(report):
    sut = report.get("sut") or {}
    config = (sut.get("describe") or {}).get("config") or {}
    return "tree" in (sut.get("variant") or "").lower() or config.get("retrieval_mode") == "tree"


def tree_search(report, search, representation):
    mode = ((search or {}).get("settings") or {}).get("representation")
    return mode == "Tree" if mode is not None else representation == "tree" or tree_report(report)


def tree_source_ids(items, mapping, search, label, warn_missing=True):
    """Use matched native levels; uncertain items contribute at most one source."""
    nodes = defaultdict(list)
    for row in (search or {}).get("items") or []:
        if isinstance(row, dict) and isinstance(row.get("node"), str):
            nodes[row["node"]].append(row)
    direct, unmatched = [], 0
    for item in items:
        refs = list(dict.fromkeys(mapping[ref] for ref in item.get("source_ids") or []))
        matches = nodes.get(item.get("id"), [])
        row = matches[0] if len(matches) == 1 else {}
        sources = row.get("sources")
        if (type(row.get("level")) is int and row["level"] >= 0
                and isinstance(sources, list) and all(isinstance(s, str) for s in sources)
                and set(sources) == set(refs)):
            if row["level"] == 0:
                direct.extend(refs)
        else:
            unmatched += 1
            if len(refs) == 1:
                direct.extend(refs)
    if unmatched and warn_missing:
        warn(f"{label}: Tree trace missing/unmatched for {unmatched} items; single-source fallback (upper coverage excluded)")
    return list(dict.fromkeys(direct))


def direct_source_ids(case, episode_id, events, *, tree=False, search=None, warn_missing=True):
    """Recover direct item provenance without treating ancestor dependencies as hits."""
    if case is None:
        return []
    cited = case.get("retrieved_source_ids") or []
    fallback = list(dict.fromkeys(cited))
    if tree:
        fallback = []  # No item/level evidence can prove a closure is direct.
    items = (case.get("response") or {}).get("items")
    label = case.get("case_id", "unknown case")
    if items is None:
        if tree and cited and warn_missing:
            warn(f"{label}: Tree response items missing; single-source fallback unavailable, excluding direct sources")
        return fallback
    fallback_note = "excluding unprovable Tree direct sources" if tree else "fallback to retrieved_source_ids"
    if not isinstance(items, list) or any(not isinstance(item, dict) for item in items):
        warn(f"{label}: invalid response items for source_ids mapping; {fallback_note}")
        return fallback
    leaf = [isinstance(item.get("text"), str) and item["text"].startswith(LEAF_MEMORY_HEADER)
            for item in items]
    mapping = {}
    if tree or not items or not all(leaf):
        # The runner maps first-seen anonymous IDs to suite IDs in this order.
        anonymous = {}
        for item in items:
            refs = item.get("source_ids")
            if refs is None:
                refs = []
            if not isinstance(refs, list) or any(not isinstance(ref, str) or not ref for ref in refs):
                warn(f"{label}: invalid source_ids mapping; {fallback_note}")
                return fallback
            anonymous.update(dict.fromkeys(refs))
        if len(anonymous) != len(cited) or len(set(cited)) != len(cited):
            warn(f"{label}: source_ids mapping length mismatch; {fallback_note}")
            return fallback
        mapping = dict(zip(anonymous, cited))
    if tree:
        return tree_source_ids(items, mapping, search, label, warn_missing)
    direct = []
    for index, (item, is_leaf) in enumerate(zip(items, leaf)):
        if not is_leaf:
            direct.extend(mapping[ref] for ref in item.get("source_ids") or [])
            continue
        try:
            text = item["text"]
            start = text.index('{"memories"')
            body, _ = json.JSONDecoder().raw_decode(text, start)
            memories = body["memories"]
            if not isinstance(memories, list) or not memories:
                raise ValueError("missing memories")
        except (ValueError, KeyError, TypeError):
            warn(f"{label}: LeafMemory item {index} has invalid memories JSON; excluding its direct sources")
            continue
        for memory in memories:
            try:
                if not isinstance(memory, list) or not memory:
                    raise ValueError("missing L0 layer")
                info = memory[0]["info"]
                origins = memory[0]["metadata"]["origins"]
                if (not isinstance(info, list) or not info or not isinstance(info[0], str) or not info[0].strip()
                        or not isinstance(origins, list) or not origins):
                    raise ValueError("missing L0 provenance")
            except (ValueError, KeyError, TypeError, IndexError):
                warn(f"{label}: LeafMemory item {index} lacks L0 text/origins; excluding its direct sources")
                continue
            fragment = " ".join(info[0].split())
            for origin in origins:
                sequence = origin.get("event_sequence") if isinstance(origin, dict) else None
                source = f"{episode_id}:{sequence}"
                content = events.get(source)
                if (type(sequence) is not int or sequence < 0 or not isinstance(content, str)
                        or fragment not in " ".join(content.split())):
                    warn(f"{label}: LeafMemory item {index} L0 origin/content mismatch; excluding direct source")
                    continue
                direct.append(source)
    return list(dict.fromkeys(direct))


def evidence_values(case, evidence_ids, direct_ids):
    gold = set(evidence_ids or ())
    retrieved = set(direct_ids) if case is not None else None
    cited = set(case.get("retrieved_source_ids") or ()) if case is not None else None
    values = {"evidence_hits": None, "evidence_recall": None,
              "retrieved_ids": len(retrieved) if retrieved is not None else None,
              "evidence_precision": None, "evidence_f1": None,
              "cited_ids": len(cited) if cited is not None else None,
              "cited_hits": None, "citation_coverage": None}
    if case is None or not gold:
        return values
    hits = len(gold.intersection(retrieved))
    recall = hits / len(gold)
    cited_hits = len(gold.intersection(cited))
    values.update(evidence_hits=hits, evidence_recall=recall,
                  cited_hits=cited_hits, citation_coverage=cited_hits / len(gold))
    if retrieved:
        precision = hits / len(retrieved)
        values.update(evidence_precision=precision,
                      evidence_f1=2 * precision * recall / (precision + recall) if precision + recall else 0.0)
    return values


def evidence_metrics(cases, questions, episode_sizes, direct_sources):
    samples = {metric: [] for metric in EVIDENCE_METRICS}
    for case in cases:
        question = questions[case["case_id"]]
        if case.get("status") != "scored" or not question.get("evidence_ids"):
            continue
        values = evidence_values(case, question["evidence_ids"], direct_sources[(case["case_id"], case.get("repeat_index", 0))])
        cutoff = question.get("cutoff")
        if cutoff is None:
            cutoff = episode_sizes.get(question["episode_id"])
        share = values["retrieved_ids"] / cutoff if cutoff is not None and cutoff > 0 else None
        for metric, value in ((EVIDENCE, values["evidence_recall"]),
                              (SOURCE_IDS, values["retrieved_ids"]), (SOURCE_SHARE, share),
                              (PRECISION, values["evidence_precision"]), (F1, values["evidence_f1"]),
                              (CITATION, values["citation_coverage"])):
            if value is not None:
                samples[metric].append(value)
    return {metric: {"mean": math.fsum(values) / len(values) if values else None,
                     "n": len(values) if values else None} for metric, values in samples.items()}


def result_cell(case, logs=(), cogletnet=False, evidence_ids=(), direct_ids=()):
    evidence = evidence_values(case, evidence_ids, direct_ids)
    case = case or {}
    qa = case.get("qa") or {}
    output = qa.get("reader_output") or {}
    metrics = case.get("metrics") or {}
    returned = (case.get("response") or {}).get("items")
    details = qa.get("judge_details")
    return {"status": case.get("status"), "error_type": case.get("error_type") or None, "attempt": None,
            "failure": failure(case, logs, cogletnet), "score": metrics.get(SCORE),
            "judge_label": qa.get("judge_label"),
            "items": [common.pick(detail.get("outcome"), ("expectation", "confidence", "probabilities")) for detail in details] if details is not None else None,
            "event_ordering": metrics.get(ORDER), "answer": qa.get("hypothesis"), **evidence,
            **common.pick(qa, ("reader_abstain", "truncated")),
            **common.pick(output, ("finish_reason", "completion_tokens")),
            "returned_items": len(returned) if returned is not None else None,
            "returned_chars": case.get("returned_chars"),
            "timings": common.pick(case.get("timings"), ("ingest_ms", "query_ms", "answer_ms"))}


def beam_metrics(summary):
    metrics = (summary or {}).get("metrics") or {}
    return {key: common.pick(metrics.get(key), ("mean", "n"))
            for key in sorted({SCORE, ORDER} | {key for key in metrics if key.startswith("beam.")})}


def review_glossary(metric_ids, path, questions):
    entries = {entry["id"]: entry for entry in common.load_glossary(
        {key for key in metric_ids if key.startswith("beam.")}, path)}
    score = entries.get(SCORE) or {}
    without_evidence = Counter(q.get("category") or "未分类" for q in questions if not q.get("evidence_ids"))
    categories = "、".join(f"{category} {count}" for category, count in sorted(
        without_evidence.items(), key=lambda item: (-item[1], item[0])))
    excluded = f"无 gold 证据的 {sum(without_evidence.values())} 题"
    if categories:
        excluded += f"（{categories}）"
    entries[EVIDENCE] = {
        "id": EVIDENCE, "group": score.get("group", "fixed_reader"),
        "name_zh": "证据召回",
        "definition_zh": "返回条目直接代表的来源事件覆盖 gold 证据事件的比例（每条条目只计它直接代表的事件；LeafMemory 条目计其 L0 叶子所属事件；Tree 只计 level 0 节点自身的叶子事件）",
        "formula": "|direct_source_ids ∩ evidence_ids| / |evidence_ids|",
        "range": "[0,1]", "higher_is_better": True,
        "aggregation": score.get("aggregation", "scored 且该指标非 null 的样本均值；按类别另报，不跨基准混比"),
        "applies_to": score.get("applies_to", ["fixed_reader"]),
        "caveats": [
            "引用口径：每条条目只计它直接代表的来源事件；LeafMemory 条目只计其 L0 叶子所属事件。",
            "在阅读器字符预算之前统计，被预算丢弃的条目也计入，不等于「证据正文到达阅读器」。",
            excluded + "不进入分母，非 scored 的题也不进入。",
            "引用闭包（摘要层依赖）不计入；闭包口径见 beam.citation_coverage",
        ],
    }
    shared = common.pick(entries[EVIDENCE], ("group", "aggregation", "applies_to"))
    fanout_caveats = [
        "只算有 gold 证据且 scored 的题，与证据召回同一分母",
        "每条条目只计直接代表的事件；LeafMemory 只计 L0 来源，不计摘要层依赖",
    ]
    for metric, definition in {
        SOURCE_IDS: {
            "name_zh": "每题引用来源数", "definition_zh": "返回条目直接代表的来源事件去重后的个数（直接引用扇出）",
            "formula": "|direct_source_ids|", "range": "[0,∞)", "higher_is_better": None,
            "caveats": fanout_caveats,
        },
        SOURCE_SHARE: {
            "name_zh": "引用占可见事件比例", "definition_zh": "返回条目直接代表的去重来源事件数占查询时可见事件数的比例。",
            "formula": "|direct_source_ids| / cutoff", "range": "[0,1]", "higher_is_better": None,
            "caveats": fanout_caveats + ["cutoff 是查询时已写入的事件数（BEAM 100K 全部题为整段，等于 episode 事件数）"],
        },
        PRECISION: {
            "name_zh": "证据精确率", "definition_zh": "返回条目直接代表的去重来源事件中属于 gold 证据的比例。",
            "formula": "|direct_source_ids ∩ evidence_ids| / |direct_source_ids|",
            "range": "[0,1]", "higher_is_better": True,
            "caveats": ["0 个引用的题无定义，不进分母（与召回的 n 可能不同）", "引用口径同证据召回：阅读器字符预算之前"],
        },
        F1: {
            "name_zh": "证据 F1", "definition_zh": "按直接来源计算的证据精确率与证据召回的调和平均；两者均为零时取零。",
            "formula": "2 * |direct_source_ids ∩ evidence_ids| / (|direct_source_ids| + |evidence_ids|)",
            "range": "[0,1]", "higher_is_better": True,
            "caveats": ["样本与精确率相同"],
        },
        CITATION: {
            "name_zh": "引用覆盖", "definition_zh": "返回条目声明的全部来源事件（含摘要层依赖闭包）覆盖 gold 的比例，即此前的证据召回口径",
            "formula": "|retrieved_source_ids ∩ evidence_ids| / |evidence_ids|",
            "range": "[0,1]", "higher_is_better": None,
            "caveats": ["CogletNet LeafMemory 一条记忆声明约 130 个依赖事件，占整段 55–66%，该数值随引用扇出单调不减，不可跨系统比较",
                        "单来源系统与证据召回相同"],
        },
    }.items():
        entries[metric] = {"id": metric, **shared, **definition}
    for metric in (EVIDENCE, SOURCE_IDS, SOURCE_SHARE, PRECISION, F1):
        entries[metric]["caveats"] = entries[metric]["caveats"] + [
            "Tree 条目只计 level 0 节点自身的叶子事件；上层节点为摘要闭包不计，覆盖见引用覆盖"]
    return [entries[key] for key in sorted(entries)]


def embedding_declaration(value):
    if value is None:
        return None
    fields = {"provider": str, "model": str, "dimensions": int, "local": bool}
    normalised = dict.fromkeys((*fields, "declared"))
    if isinstance(value, dict):
        for key, kind in fields.items():
            if key not in value:
                continue
            # Exact types prevent bool (an int subclass) becoming dimensions.
            if type(value[key]) is kind:
                normalised[key] = value[key]
            else:
                warn(f"sut.declared.embedding.{key} must be {kind.__name__}; emitting null")
        return normalised
    if isinstance(value, str):
        normalised["declared"] = value.strip()
        return normalised if normalised["declared"] else None
    warn("sut.declared.embedding must be a dict, string or null; emitting null")
    return None


def build_run(name, directory, report, provenance, questions, episodes, event_contents, defer_tree_warning=False):
    known = {q["id"]: q for q in questions}
    episode_sizes = {episode["id"]: episode.get("events") for episode in episodes}
    samples, first = {}, {}
    episode_cases = defaultdict(list)
    metric_ids = set((report["summary"].get("metrics") or {}))
    for case in report["cases"]:
        ident = case["case_id"]
        if ident not in known or case.get("episode_id") != known[ident]["episode_id"]:
            raise ValueError(f"run {name}: unknown question or mismatched episode: {ident}")
        repeat = case.get("repeat_index", 0)
        if not isinstance(repeat, int) or repeat < 0:
            raise ValueError(f"run {name}: invalid repeat_index: {ident}")
        unique(samples, (ident, repeat), case, "report sample")
        metric_ids.update(case.get("metrics") or {})
        if repeat == 0:
            unique(first, ident, case, "primary sample")
    for case in samples.values():
        episode_cases[case["episode_id"]].append(case)
    direct_sources = {key: direct_source_ids(case, case["episode_id"], event_contents[case["episode_id"]],
                                           tree=tree_report(report), warn_missing=not defer_tree_warning)
                      for key, case in samples.items()}
    missing = len(questions) - len(first)
    if missing:
        warn(f"run {name}: {missing} questions have no primary sample; emitting null cells")
    logs = common.log_index(directory)
    sut = report.get("sut") or {}
    cogletnet = sut.get("name") == "cogletnet"
    results = {q["id"]: result_cell(first.get(q["id"]), logs.get((first.get(q["id"]) or {}).get("namespace"), ()), cogletnet,
                                  q["evidence_ids"], direct_sources.get((q["id"], 0), ()))
               for q in questions}
    by_episode = {}
    for episode in episodes:
        cases = episode_cases[episode["id"]]
        values = [(c.get("metrics") or {}).get(SCORE) for c in cases
                  if c.get("status") == "scored" and c.get("repeat_index", 0) == 0]
        values = [v for v in values if isinstance(v, (int, float)) and not isinstance(v, bool)]
        by_episode[episode["id"]] = {"status_counts": dict(Counter(c["status"] for c in cases if c.get("status") is not None)),
                                    "mean_score": math.fsum(values) / len(values) if values else None,
                                    "metrics": evidence_metrics(cases, known, episode_sizes, direct_sources)}
    by_category = {}
    categories = {q["category"] for q in questions if q["category"] is not None} | set(report.get("by_category") or {})
    for category in sorted(categories):
        summary = (report.get("by_category") or {}).get(category) or {}
        by_category[category] = {"status_counts": summary.get("status_counts"), "metrics": beam_metrics(summary)}
        by_category[category]["metrics"].update(evidence_metrics(
            (case for case in samples.values() if known[case["case_id"]]["category"] == category), known, episode_sizes, direct_sources))
        metric_ids.update(summary.get("metrics") or {})
    histories, dates = history(directory, report)
    billing = report.get("billing") or {}
    native = common.native_usage(directory) if cogletnet else None
    isolation = report.get("isolation") or {}
    residue = (isolation.get("container") or {}).get("residue") or {}
    interruption = report.get("interruption")
    classes = Counter((cell.get("failure") or {}).get("class") or "unclassified"
                      for cell in results.values() if cell["status"] == "error")
    run = {"name": name, **provenance,
           **common.pick(report, ("run_id", "run_seed", "framework_version", "started_at_utc", "wall_seconds", "resume_count")),
           "sut": {**common.pick(sut, ("name", "variant")), "network": isolation.get("network_mode"),
                   "embedding": embedding_declaration((sut.get("declared") or {}).get("embedding"))},
           "reader": report.get("reader"),
           "resume_history": histories, "sut_changes": sum(row["sut_change"] is not None for row in histories),
           "interruption": {"reason": interruption.get("reason"), "unscheduled": interruption.get("unscheduled_samples")} if interruption is not None else None,
           "status_counts": report["summary"].get("status_counts"),
           "metrics": {key: common.pick((report["summary"].get("metrics") or {}).get(key), ("mean", "n")) for key in (SCORE, ORDER)},
           "by_category": by_category, "by_episode": by_episode,
           "billing": {"sut": common.pick(billing["sut"], ("prompt_tokens", "calls")) if billing.get("sut") is not None else None,
                       "evaluator": common.pick(billing.get("evaluator"), ("prompt_tokens", "calls")),
                       "native": common.pick(native, ("calls", "input_tokens", "output_tokens")) if native is not None else None},
           "isolation": {**common.pick(isolation, ("backend", "network_mode")), **common.pick(residue, ("run_clean", "containers_remaining"))},
           "failures_by_class": dict(classes)}
    run["metrics"].update(evidence_metrics(samples.values(), known, episode_sizes, direct_sources))
    metric_ids.update(EVIDENCE_METRICS)
    return run, results, dates, metric_ids


def consensus(values, label):
    if all(value == values[0] for value in values):
        return values[0]
    warn(f"{label} differs or is absent in some reports; emitting null")
    return None


def refresh_tree_metrics(run, cells, report, questions, episodes, event_contents, searches):
    """Apply per-sample trace levels after work/attempt resolution, including repeats."""
    if not tree_report(report) and not any(tree for tree, _ in searches.values()):
        return
    known = {q["id"]: q for q in questions}
    sizes = {e["id"]: e.get("events") for e in episodes}
    direct_sources = {}
    for case in report["cases"]:
        tree, search = searches.get((case.get("namespace") or None, case["case_id"]), (tree_report(report), None))
        direct = direct_source_ids(case, case["episode_id"], event_contents[case["episode_id"]], tree=tree, search=search)
        direct_sources[case["case_id"], case.get("repeat_index", 0)] = direct
        if case.get("repeat_index", 0) == 0:
            cell = cells[case["case_id"]]
            cell.update(evidence_values(case, known[case["case_id"]]["evidence_ids"], direct))
            # The per-question evidence count and its work diagnostic must agree,
            # including conservative handling of unmatched or corrupt trace IDs.
            diagnostic = (cell.get("work") or {}).get("tree")
            if diagnostic is not None:
                diagnostic["direct_sources"] = len(direct)
    run["metrics"].update(evidence_metrics(report["cases"], known, sizes, direct_sources))
    for category, row in run["by_category"].items():
        row["metrics"].update(evidence_metrics(
            (c for c in report["cases"] if known[c["case_id"]]["category"] == category), known, sizes, direct_sources))
    for ident, row in run["by_episode"].items():
        row["metrics"].update(evidence_metrics(
            (c for c in report["cases"] if c["episode_id"] == ident), known, sizes, direct_sources))


def build_review(suite_dir, runs, glossary=None, *, work_names=()):
    suite, episodes, questions, event_contents = load_suite(suite_dir)
    loaded = []
    names = set()
    for name, directory in runs:
        if not name or name in names:
            raise ValueError(f"empty or duplicate run name: {name}")
        names.add(name)
        directory = Path(directory)
        if (directory / ".resume.lock").exists():
            warn(f"{directory}: 正在续跑，数据可能过期")
        report, provenance = common.load_report(directory)
        if report.get("suite_id") != suite.get("suite_id"):
            raise ValueError(f"run {name}: suite_id does not match suite")
        loaded.append((name, directory, report, provenance))
    if not loaded:
        raise ValueError("at least one --run is required")
    hashes = {r.get("suite_sha256") for _, _, r, _ in loaded if r.get("suite_sha256") is not None}
    if len(hashes) > 1:
        raise ValueError("runs have different suite_sha256 values")
    run_rows, results, dates, metric_ids = [], {}, [], set()
    for name, directory, report, provenance in loaded:
        run, cells, times, emitted = build_run(name, directory, report, provenance, questions, episodes, event_contents,
                                             defer_tree_warning=name in work_names)
        run_rows.append(run)
        results[name] = cells
        dates.extend(times)
        metric_ids.update(emitted)
    protocol = {}
    for group, fields in (("benchmark", ("protocol_id", "version", "variant", "sha256")),
                          ("judge", ("provider", "model", "cutoff")),
                          ("reader", ("model", "max_tokens", "input"))):
        protocol[group] = {}
        for field in fields:
            values = []
            for _, _, report, _ in loaded:
                p = report.get("protocol") or {}
                source = (report.get("reader") or {}) if group == "reader" else (p.get(group) or {})
                values.append(source.get("input_mode", p.get("reader_input")) if group == "reader" and field == "input" else source.get(field))
            protocol[group][field] = consensus(values, f"protocol.{group}.{field}")
    source = suite.get("provenance") or {}
    dataset = {"suite_id": suite.get("suite_id"),
               "suite_sha256": consensus([r.get("suite_sha256") for _, _, r, _ in loaded], "suite_sha256"),
               "source": {**common.pick(source, ("attribution", "revision", "converter")), "url": source.get("source_url")},
               "license": common.pick(suite.get("license"), ("id", "share_alike", "redistribute_normalized")),
               "protocol": protocol, "limitations": suite.get("limitations"),
               "categories": [{"id": cat, "n": n} for cat, n in sorted(Counter(q["category"] for q in questions if q["category"] is not None).items())],
               "episodes": episodes}
    return {"version": 1, "generated_at": max(dates, key=common.timestamp_key, default=None),
            "memeval_commit": consensus([r.get("memeval_commit") for _, _, r, _ in loaded], "memeval_commit"),
            "dataset": dataset, "questions": questions, "runs": run_rows, "results": results,
            "glossary": review_glossary(metric_ids, glossary, questions)}


STAGES = ("not_written", "not_candidate", "capped", "jev_rejected", "not_packed", "selected")
OUTCOMES = {"selected": "selected", "result_limit": "not_packed", "CONTEXT_BUNDLE_TOO_LARGE": "not_packed",
            "jev_below_threshold": "jev_rejected", "candidate_cap": "capped", "evidence_cap": "capped"}


def work_cell(question, payload, chronology, filename):
    ident = question["id"]
    alternate = chronology.get("alternate_searches", {}).get(ident)
    if alternate is not None:
        cell = work_cell(question, alternate["payload"], alternate["chronology"], filename)
        cell.update(attempt=alternate["attempt"], attempt_mismatch=True)
        cell["warnings"].append("本题 trace 来自另一次 attempt，段级构建计数可能不对应")
        return cell
    position = chronology["searches"].get(ident)
    search = payload["searches"].get(ident) if position is not None else None
    candidates = (search or {}).get("candidates") or []
    items = (search or {}).get("items") or []
    warnings = list((search or {}).get("warnings") or [])
    tree = payload.get("representation") == "tree"
    trace = payload["trace"]
    if search is None and payload["log"]["corrupt_lines"]:
        trace = "corrupt"
        warnings.append("trace 有损坏行；本题未匹配到可解析的 search")
    evidence = []
    for source_id in question.get("evidence_ids") or []:
        history = chronology.get('build_history', zip(chronology["builds"], payload["builds"]))
        builds = [(p, b) for p, b in history if b["source_id"] == source_id]
        available = [(p, b) for p, b in builds if position is None or p < position]
        build = max(available, key=lambda pair: pair[0])[1] if available else None
        written = build.get("state") if build is not None else ("after_query" if builds and position is not None else None)
        row = {"source_id": source_id, "written": written, "nodes": len(build["nodes"]) if build else 0,
               "best": None, "stage": "not_written"}
        if written == "Ready":
            nodes = {n["node"]: n for n in build["nodes"]}
            by_node = {c["node"]: dict(c) for c in candidates if c["source_id"] == source_id
                       and (not tree or c.get("outcome") != "selected")}
            # Selected summaries count exactly like selected leaves. Items may
            # contain a returned node not repeated in the candidate diagnostic.
            for item in items:
                if (source_id in (item.get("sources") or []) if tree else item["source_id"] == source_id):
                    by_node[item["node"]] = {**by_node.get(item["node"], {}), **item, "outcome": "selected"}
            for node in by_node.values():
                if node.get("level") is None:
                    node["level"] = (nodes.get(node["node"]) or {}).get("level")
            reached = [(OUTCOMES.get(c.get("outcome")), c) for c in by_node.values()]
            unknown = [c for stage, c in reached if stage is None]
            reached = [(stage, c) for stage, c in reached if stage is not None]
            if unknown:
                warnings.append(f"{source_id}: 候选结局缺失或未知，未推断其阶段")
            if reached:
                def priority(pair):
                    stage, node = pair
                    def number(value):
                        return value if isinstance(value, (int, float)) else -math.inf
                    return (-STAGES.index(stage), -number(node.get("p_use")), -number(node.get("similarity")), str(node.get("node")))
                stage, best = min(reached, key=priority)
                row.update(stage=stage, best=common.pick(best, ("node", "level", "origin", "similarity", "vector_rank", "p_use", "outcome", "rank")))
            else:
                row["stage"] = None if unknown else "not_candidate"
        evidence.append(row)
    selected = sum(row["stage"] == "selected" for row in evidence)
    if search is None:
        stage = "no_search"
    elif not evidence:
        stage = "no_evidence"
    elif selected == len(evidence):
        stage = "covered"
    elif selected:
        stage = "partial"
    else:
        known = [row["stage"] for row in evidence if row["stage"] is not None]
        stage = max(known, key=STAGES.index, default=None)
    def count(key):
        value = (search or {}).get(key)
        return len(value) if value is not None else None
    cell = {"trace": trace, "file": filename, "search": search is not None,
            "builds_before": (search or {}).get("builds_before"), "evidence": evidence, "stage": stage,
            "covered": [selected, len(evidence)], "candidates": count("candidates"), "items": count("items"),
            "omitted": dict(Counter(c["outcome"] for c in candidates if c.get("outcome") not in (None, "selected", "unselected"))),
            "jev_batches": count("jev_trace"), "context_tokens": ((search or {}).get("context_view") or {}).get("tokens"),
            "degraded": (search or {}).get("degraded"), "warnings": warnings}
    if payload.get("representation") in ("leaf_memory", "tree"):
        scored = [c for c in candidates if c.get("jev") is not None]
        def mean(key):
            values = [c["jev"][key] for c in scored if type(c["jev"].get(key)) in (int, float)]
            return math.fsum(values) / len(values) if values else None
        cell.update(representation=payload["representation"],
                    jev_summary=None if search is None else {
                        "scored": len(scored),
                        # Admission precedes packing. Use observed outcomes,
                        # not a guessed three-class selection/threshold policy.
                        "admitted": sum(c.get("outcome") in ("selected", "CONTEXT_BUNDLE_TOO_LARGE", "result_limit") for c in scored),
                        "useful_mean": mean("useful"), "uncertain_mean": mean("uncertain")})
        if not tree or "packed" in (search or {}):
            cell["packed"] = (search or {}).get("packed")
        if tree:
            refs = [item.get("reader_sources_n") for item in items]
            leaves = [source for item in items if type(item.get("level")) is int and item["level"] == 0
                      for source in item.get("sources") or []]
            cell["tree"] = None if search is None else {
                "items_by_level": dict(Counter(str(item["level"]) for item in items if item.get("level") is not None)),
                "recorded_items": sum(item.get("recorded") is not None for item in items),
                "direct_sources": len(set(leaves)) if all(isinstance(s, str) for s in leaves) else None,
                "covered_sources": sum(refs) if all(type(n) is int for n in refs) else None}
    return cell


def safe_component(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", value):
        raise ValueError(f"unsafe work path component: {value!r}")
    return value


def work_stem(episode_id, namespace_index):
    ident = safe_component(episode_id)
    if type(namespace_index) is not int or namespace_index < 0:
        raise ValueError(f"invalid work namespace_index: {namespace_index!r}")
    return safe_component(ident + (f"-r{namespace_index}" if namespace_index else ""))


def build_work(review, suite_dir, runs, work_names, llm_endpoint=None):
    """Attach compact work metadata; return separate episode file payloads."""
    requested = set(work_names)
    directories = dict(runs)
    if requested - directories.keys():
        raise ValueError(f"--work has no matching --run: {sorted(requested - directories.keys())}")
    if not requested:
        return {}
    if llm_endpoint is None:
        llm_endpoint = os.environ.get("MEMEVAL_OPENAI_BASE_URL")
    suites = {episode["id"]: episode for episode in rows(Path(suite_dir) / "episodes.jsonl")}
    questions = {q["id"]: q for q in review["questions"]}
    files, ledger_cache = {}, {}
    for run in review["runs"]:
        name = run["name"]
        if name not in requested:
            continue
        safe_component(name)
        directory = Path(directories[name])
        if run["sut"]["name"] != "cogletnet":
            raise ValueError(f"--work {name} requires sut.name=cogletnet and logs/**/sut.stderr.log")
        report, provenance = common.load_report(directory)
        if provenance["report_sha256"] != run["report_sha256"]:
            raise ValueError(f"run {name}: report changed during work extraction; rebuild from a stable report")
        if not any((directory / "logs").rglob("sut.stderr.log")) and not tree_report(report):
            raise ValueError(f"--work {name} requires sut.name=cogletnet and logs/**/sut.stderr.log")
        trace = common.WorkTraces(directory)
        report_config = ((report.get("sut") or {}).get("describe") or {}).get("config") or {}
        case_namespaces, primary_cases = {}, {}
        for case in report["cases"]:
            if case.get("repeat_index", 0) == 0:
                case_namespaces[case["episode_id"], case["case_id"]] = case.get("namespace") or None
                primary_cases[case["episode_id"], case["case_id"]] = case
        reader = report.get("reader") or {}
        template = (reader_template(reader) if any((c.get("qa") or {}).get("reader_request_sha")
                                                  for c in primary_cases.values()) else None)
        ledger = {}
        if template is not None:
            cache_key = directory.resolve()
            if cache_key not in ledger_cache:
                ledger_cache[cache_key] = reader_ledger(directory)
            ledger = ledger_cache[cache_key]
        summaries, configs, metric_searches = {}, [], {}
        for episode in review["dataset"]["episodes"]:
            ident = safe_component(episode["id"])
            source = suites[ident]
            # The suite's original case order is the execution order, which may
            # differ from the ID-sorted question list used by the page.
            ordered_questions = list({c["id"]: questions[c["id"]] for c in source.get("cases", [])}.values())
            groups = {}
            for question in ordered_questions:
                namespace = case_namespaces.get((ident, question["id"]))
                groups.setdefault(namespace, []).append(question)
            if not groups:
                groups[None] = []
            # Dict order is first occurrence in suite order; max retains that
            # order for ties. Secondary namespaces retain their original order.
            primary = max(groups, key=lambda ns: len(groups[ns]))
            namespaces = [primary] + [ns for ns in groups if ns != primary]
            summary = {"file": f"work/{name}/{work_stem(ident, 0)}.js", "files": [],
                       "namespaces": len(namespaces), "builds": 0, "searches": 0, "pending": 0, "corrupt_lines": 0}
            counts = {key: Counter() for key in ("states", "levels", "edges")}
            traces = set()
            representations, scope_count, snapshot_count, member_levels, leaf_tokens = set(), 0, 0, Counter(), []
            for index, namespace in enumerate(namespaces):
                payload, chronology = trace.episode(namespace, source.get("events", []), groups[namespace], report_config)
                payload = {"version": 2, "run": name, "episode_id": ident, "namespace_index": index, **payload}
                filename = f"work/{name}/{work_stem(ident, index)}.js"
                if filename in files:
                    raise ValueError(f"duplicate work filename: {filename}")
                files[filename] = payload
                configs.append(payload["config"])
                summary["files"].append(filename)
                summary["builds"] += len(payload["builds"])
                summary["searches"] += len(payload["searches"])
                diagnostics = ([s for scope in payload['scopes'].values() for s in scope['snapshots']]
                               if payload.get('representation') in ('leaf_memory', 'tree') else payload['builds'])
                summary["pending"] += sum(len(group) for b in diagnostics for group in (b["pending"] or {}).values())
                summary["corrupt_lines"] += payload["log"]["corrupt_lines"]
                traces.add(payload["trace"])
                representations.add(payload.get("representation", "legacy"))
                for scope in (payload.get("scopes") or {}).values():
                    scope_count += 1
                    snapshot_count += len(scope["snapshots"])
                    member_levels.update(str(n["level"]) for n in scope["nodes"].values() if n["level"] is not None)
                    leaf_tokens.extend(n["input_tokens"] for n in scope["nodes"].values()
                                       if n["level"] == 0 and type(n["input_tokens"]) in (int, float))
                nodes = [n for build in payload["builds"] for n in build["nodes"]]
                counts["states"].update(b["state"] for b in payload["builds"] if b["state"] is not None)
                counts["levels"].update(str(n["level"]) for n in nodes if n["level"] is not None)
                counts["edges"].update(n["edge"]["kind"] for n in nodes if isinstance(n["edge"], dict) and n["edge"].get("kind") is not None)
                for question in groups[namespace]:
                    cell = review["results"][name][question["id"]]
                    cell["work"] = work_cell(question, payload, chronology, filename)
                    search = payload["searches"].get(question["id"])
                    metric_searches[namespace, question["id"]] = (
                        tree_search(report, search, cell["work"].get("representation")), search)
                    # F104 already selected the question's own search. Reuse
                    # its origin; the segment's log belongs to the majority
                    # attempt and is valid only for non-mismatching questions.
                    cell["attempt"] = None
                    if cell["work"]["search"]:
                        path = payload["log"].get("path")
                        segment_attempt = (next((part for part in Path(path).parts
                                                 if re.fullmatch(r"resume-\d+", part)), "root") if path else None)
                        cell["attempt"] = cell["work"].get("attempt", segment_attempt)
                    # Attach after stage/count/attempt extraction. A placeholder
                    # reader call is not evidence of a SUT search trace.
                    prompt = reader_prompt(primary_cases.get((ident, question["id"])), reader, template, llm_endpoint, ledger)
                    payload["searches"].setdefault(question["id"], {})["reader_prompt"] = prompt
            summaries[ident] = {**summary, "trace": next(iter(traces)) if len(traces) == 1 else "mixed",
                                **{key: dict(value) for key, value in counts.items()}}
            if representations & {"leaf_memory", "tree"}:
                summaries[ident].update(representation="tree" if "tree" in representations else "leaf_memory", scopes=scope_count, snapshots=snapshot_count,
                                        members_by_level=dict(member_levels), leaf_tokens={
                                            "n": len(leaf_tokens), "max": max(leaf_tokens, default=None),
                                            "mean": math.fsum(leaf_tokens) / len(leaf_tokens) if leaf_tokens else None})
            if "tree" in representations:
                # Body points have no persisted per-point embedding token
                # measurement. Keep the agreed unknown-field shape, not an
                # estimate from another representation or tokenizer role.
                summaries[ident]["node_tokens_by_level"] = {
                    level: {"n": 0, "mean": None, "max": None} for level in member_levels}
        if tree_report(report) or any(tree for tree, _ in metric_searches.values()):
            # Work files show repeat=0; metric aggregates include all repeats.
            # Resolve any additional repeat namespaces without exporting files.
            extra = defaultdict(dict)
            for case in report["cases"]:
                namespace = case.get("namespace") or None
                if (namespace, case["case_id"]) not in metric_searches:
                    extra[case["episode_id"], namespace][case["case_id"]] = questions[case["case_id"]]
            for (ident, namespace), group in extra.items():
                payload, _ = trace.episode(namespace, suites[ident].get("events", []), list(group.values()), report_config)
                for qid in group:
                    search = payload["searches"].get(qid)
                    metric_searches[namespace, qid] = (
                        tree_search(report, search, payload.get("representation")), search)
            event_contents = {ident: {e["source_id"]: e.get("content") for e in source.get("events", []) if e.get("source_id")}
                              for ident, source in suites.items()}
            refresh_tree_metrics(run, review["results"][name], report, review["questions"],
                                 review["dataset"]["episodes"], event_contents, metric_searches)
        run["work"] = {"config": {key: consensus([c[key] for c in configs], f"work.{name}.config.{key}") for key in ("work_trace", "jev_threshold")},
                       "episodes": summaries}
    return files


def write_review(review, out, work_files=None):
    out = Path(out)
    payloads = {"beam-100k.json": common.encode(review), "beam-100k.js": common.javascript("MEMEVAL_BEAM", review)}
    for filename, value in (work_files or {}).items():
        key = f"{safe_component(value['run'])}/{work_stem(value['episode_id'], value.get('namespace_index', 0))}"
        expected = f"work/{key}.js"
        if filename != expected:
            raise ValueError(f"invalid work filename: {filename}")
        # Reuse the same JSON/JS escaping as the main offline review files.
        body = common.javascript("MEMEVAL_BEAM_WORK", value).removeprefix("window.MEMEVAL_BEAM_WORK = ")
        payloads[filename] = f'(window.MEMEVAL_BEAM_WORK = window.MEMEVAL_BEAM_WORK || {{}})[{json.dumps(key)}] = {body}'
        payloads[filename[:-3] + ".json"] = common.encode(value)
    out.mkdir(parents=True, exist_ok=True)
    for name, text in payloads.items():
        destination = out / name
        if not destination.parent.resolve().is_relative_to(out.resolve()):
            raise ValueError(f"output directory escapes --out: {name}")
        destination.parent.mkdir(parents=True, exist_ok=True)
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=destination.parent, delete=False) as stream:
                temporary = Path(stream.name)
                stream.write(text)
            temporary.replace(destination)
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
    return {name: len(text.encode("utf-8")) for name, text in payloads.items()}


def run_argument(value):
    name, sep, path = value.partition("=")
    if not sep or not name or not path:
        raise argparse.ArgumentTypeError("--run requires name=report-dir")
    return name, Path(path)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--suite", type=Path, required=True)
    parser.add_argument("--run", type=run_argument, action="append", required=True)
    parser.add_argument("--work", action="append", default=[], help="CogletNet run name whose episode work traces should be extracted (repeatable)")
    parser.add_argument("--glossary", type=Path, help="MetricDef export or results index JSON")
    parser.add_argument("--llm-endpoint", help="Reader base URL for local request hash verification only (defaults to MEMEVAL_OPENAI_BASE_URL)")
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        review = build_review(args.suite, args.run, args.glossary, work_names=args.work)
        work_files = build_work(review, args.suite, args.run, args.work, args.llm_endpoint)
        sizes = write_review(review, args.out, work_files)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(2, f"build_beam_review: {error}\n")
    print(common.encode({"out": str(args.out.resolve()), "questions": len(review["questions"]),
                         "runs": len(review["runs"]), "results": {name: len(cells) for name, cells in review["results"].items()},
                         "files_bytes": sizes}), end="")


if __name__ == "__main__":
    main()
