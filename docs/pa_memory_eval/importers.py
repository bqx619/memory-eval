"""Offline format converters that preserve upstream question texts and hold gold privately.

A loader's implementation/testing on schema fixtures is NOT a claim that the full
upstream dataset was downloaded or benchmarked in this distribution.
"""
from __future__ import annotations
import ast
import csv
import hashlib
import json
import re
from pathlib import Path
from .core import ContractError, read_json, validate_suite, SCHEMA_VERSION


def provenance(path, url, revision=None, nature="upstream dataset; consult registry"):
    p = Path(path)
    return {"source_url": url, "source_revision": revision, "local_input_sha256": hashlib.sha256(p.read_bytes()).hexdigest(),
            "data_nature": nature, "importer_version": "0.1.0", "raw_byte_acquisition": "provided local file"}


def from_pmb(path, cases_path=None, revision=None):
    raw = read_json(path)
    if isinstance(raw, dict) and "beliefs" in raw:
        seeds, cases = raw["beliefs"], raw["cases"]
        prov = raw["provenance"].copy()
        prov["source_url"] = prov.get("source_url", prov.get("repository", "https://github.com/tenurehq/precisionMemBench"))
        prov["projection_sha256"] = hashlib.sha256(Path(path).read_bytes()).hexdigest()
    else:
        if not cases_path: raise ContractError("Original PMB format needs --cases-file")
        seeds, cases = raw, read_json(cases_path)
        prov = provenance(path, "https://github.com/tenurehq/precisionMemBench", revision, "upstream-authored synthetic diagnostic fixture")
        prov["cases_sha256"] = hashlib.sha256(Path(cases_path).read_bytes()).hexdigest()
    events = []
    for s in seeds:
        attrs = {k: s.get(k) for k in ("scope", "type", "canonical_name", "aliases", "epistemic_status", "pinned",
                                     "superseded_by", "resolved_at", "user_edited", "participants") if k in s}
        events.append({"source_id": s["_id"], "owner": s["user_id"], "session": "seed", "role": "fact",
                       "content": s["content"], "attributes": attrs})
    tests = [{"id": c["caseId"], "query": c["query"], "kind": "typed_context", "user": c.get("userId", "test-user"),
              "scope": c["scope"], "category": c["category"], "cutoff": len(events), "budget": c.get("budget", {}),
              "gold": {"expect": c["expect"]}} for c in cases]
    suite = {"schema_version": SCHEMA_VERSION, "suite_id": "precisionmembench-projection-v1" if isinstance(raw, dict) else "precisionmembench-source",
             "track": "seeded-state-diagnostic", "provenance": prov,
             "limitations": ["Source-specific output tiers and expectations are not a universal memory benchmark",
                             "35 structured seeds are not longitudinal raw conversations",
                             "Vacuous assertions are unscored, unlike upstream all([]) success",
                             "Context prelude is produced by the tested adapter, not the upstream fixed persona service"],
             "episodes": [{"id": "pmb-seed-corpus", "events": events, "cases": tests}]}
    validate_suite(suite)
    return suite


def from_longmemeval(path, variant="s", revision=None):
    raw = read_json(path)
    if not isinstance(raw, list): raise ContractError("LongMemEval expects a JSON array")
    eps = []
    for row in raw:
        ids, dates, sessions = row["haystack_session_ids"], row["haystack_dates"], row["haystack_sessions"]
        if not len(ids) == len(dates) == len(sessions): raise ContractError("Mismatched LongMemEval session arrays")
        events = []
        # Preserve file order, including oracle ordering; disclose the upstream variant.
        for sid, dt, turns in zip(ids, dates, sessions):
            for i, turn in enumerate(turns):
                events.append({"source_id": f"{sid}:{i}", "owner": "user", "session": sid,
                               "time": dt, "role": turn["role"], "content": turn["content"], "attributes": {}})
        abstain = str(row["question_id"]).endswith("_abs")
        answer = row["answer"]
        refs = [str(x) for x in answer] if isinstance(answer, list) else [str(answer)]
        test = {"id": str(row["question_id"]), "kind": "qa", "query": row["question"], "user": "user", "scope": [],
                "query_time": row.get("question_date"), "cutoff": len(events), "category": row["question_type"],
                "cluster_id": str(row.get("user_id", "longmemeval-user-group-unverified")),
                "gold": {"answers": refs, "abstain": abstain, "evidence_unit": "session",
                         "evidence_ids": [] if abstain else row["answer_session_ids"], "exhaustive": False},
                "official_metric": "Use upstream LongMemEval evaluate_qa.py; lexical metrics are diagnostic only"}
        eps.append({"id": str(row["question_id"]), "events": events, "cases": [test]})
    suite = {"schema_version": SCHEMA_VERSION, "suite_id": "longmemeval-cleaned-" + variant,
             "track": "oracle" if variant == "oracle" else "history-qa", "provenance": provenance(path, "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned", revision),
             "limitations": ["Oracle and full-history variants must never share a leaderboard", "has_answer flags never leave the evaluator",
                             "Reference sessions are positive labels, not exhaustive negative judgments",
                             "Without source user grouping, cluster bootstrap is conservatively disabled via one shared cluster"], "episodes": eps}
    validate_suite(suite)
    return suite


def from_locomo(path, revision=None):
    raw = read_json(path)
    eps = []
    for ei, record in enumerate(raw):
        conv = record["conversation"]
        sessions = sorted((k for k in conv if re.fullmatch(r"session_\d+", k)), key=lambda k: int(k.split("_")[-1]))
        events = []
        for session in sessions:
            for turn in conv[session]:
                content = turn["text"]
                # Deliberately text-only: do not treat captions or remote photos as visual inference.
                events.append({"source_id": turn["dia_id"], "owner": "conversation", "session": session,
                               "time": conv.get(session + "_date_time"), "role": "speaker",
                               "content": content, "attributes": {"speaker": turn["speaker"]}})
        tests = []
        for qi, qa in enumerate(record["qa"]):
            source_id = str(record.get("sample_id", ei))
            answers = qa.get("answer")
            answers = [str(x) for x in answers] if isinstance(answers, list) else ([] if answers is None else [str(answers)])
            cat = str(qa["category"])
            ids = qa.get("evidence", [])
            unknown = [x for x in ids if x not in {e["source_id"] for e in events}]
            if unknown:
                raise ContractError(f"Unresolved LoCoMo evidence ids in {source_id}:{qi}: {unknown}; do not guess or silently drop")
            tests.append({"id": f"{source_id}:{qi}", "kind": "qa", "query": qa["question"], "user": "conversation",
                          "scope": [], "cutoff": len(events), "category": cat,
                          "gold": {"answers": answers, "evidence_ids": ids, "evidence_unit": "event", "exhaustive": False,
                                   "abstain": cat == "5"},
                          "official_metric": "Preserve upstream category-specific evaluation; cat5 requires separate protocol"})
        eps.append({"id": str(record.get("sample_id", ei)), "events": events, "cases": tests})
    suite = {"schema_version": SCHEMA_VERSION, "suite_id": "locomo10-text-only", "track": "history-qa",
             "provenance": provenance(path, "https://github.com/snap-research/locomo", revision, "LLM-generated multi-session dialogue benchmark"),
             "limitations": ["Text-only; images and image-specific evidence require a separate multimodal adapter",
                             "Gold event summaries and generated observation/session-summary files are not ingested",
                             "LoCoMo dialogue between people is not identical to a user-assistant history"], "episodes": eps}
    validate_suite(suite)
    return suite


def literal(value):
    try: return json.loads(value)
    except (ValueError, TypeError): return ast.literal_eval(value)


def from_personamem(path, contexts_path, revision=None):
    contexts = {}
    with open(contexts_path, encoding="utf-8") as f:
        for line in f:
            if not line.strip(): continue
            obj = json.loads(line)
            if not isinstance(obj, dict):
                raise ContractError("Expected context JSONL maps from shared_context_id to message arrays")
            contexts.update(obj)
    grouped = {}
    with open(path, encoding="utf-8-sig", newline="") as f:
        for row in csv.DictReader(f):
            cid = row["shared_context_id"]
            if cid not in contexts: raise ContractError("Unresolved shared_context_id")
            history = contexts[cid]
            if not isinstance(history, list): raise ContractError("Context must be a list of messages")
            end = int(row["end_index_in_shared_context"])
            if not 0 <= end <= len(history): raise ContractError("Invalid exclusive prefix index")
            ep = grouped.setdefault(cid, {"id": cid, "events": [
                {"source_id": f"{cid}:{i}", "owner": "user", "session": cid, "role": m["role"], "content": m["content"], "attributes": {}}
                for i, m in enumerate(history)], "cases": []})
            try:
                values = literal(row["all_options"])
            except (ValueError, SyntaxError):
                matches = list(re.finditer(r"(?:^|\n)\s*\(([a-z])\)\s*", row["all_options"], re.I))
                if [m.group(1).lower() for m in matches] != [chr(97+i) for i in range(len(matches))] or not matches:
                    raise ContractError("Unrecognized formatted options; use explicit sequential (a), (b) labels")
                values = [row["all_options"][m.end():matches[i+1].start() if i+1<len(matches) else None].strip() for i,m in enumerate(matches)]
            if not isinstance(values, list) or any(not isinstance(x, str) for x in values):
                raise ContractError("PersonaMem all_options must be a string list; no guessed schema mapping")
            options = {chr(65 + i): v for i, v in enumerate(values)}
            correct = row["correct_answer"].strip().strip("() ")
            matching = [k for k, v in options.items() if correct == v]
            if len(matching) != 1:
                # Upstream uses option_a style labels; accept only explicit letters/option labels.
                match = re.fullmatch(r"(?:option[_\s-]?)?([A-Za-z])", correct)
                if match and match.group(1).upper() in options: matching = [match.group(1).upper()]
                else: raise ContractError("Unresolved correct_answer; refusing to infer an index convention")
            ep["cases"].append({"id": f"{row['persona_id']}:{row['question_id']}", "kind": "mcq", "query": row["user_question_or_message"],
                                "user": "user", "scope": [], "cutoff": end, "options": options,
                                "category": row["question_type"], "gold": {"choice": matching[0]}, "cluster_id": row["persona_id"]})
    prov = provenance(path, "https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1", revision, "synthetic personas and conversations")
    prov["contexts_sha256"] = hashlib.sha256(Path(contexts_path).read_bytes()).hexdigest()
    suite = {"schema_version": SCHEMA_VERSION, "suite_id": "personamem-v1-" + Path(path).stem, "track": "personalization-mcq",
             "provenance": prov, "limitations": ["Use only history[:end_index_in_shared_context]; future blocks are private",
                                                    "Multiple context lengths/personas are not independent user samples"], "episodes": list(grouped.values())}
    validate_suite(suite)
    return suite
