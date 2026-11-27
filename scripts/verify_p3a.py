#!/usr/bin/env python3
"""P3a read-only dataset checks and retrieval runs; outputs must stay in scratch."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).replace("\u2028", "\\u2028").replace("\u2029", "\\u2029").encode()


def objects(path):
    """Read an upstream JSON array one object at a time, using only stdlib."""
    decoder = json.JSONDecoder()
    with path.open() as stream:
        buf = stream.read(1)
        assert buf == "[", path
        buf = ""
        eof = False
        while True:
            buf = buf.lstrip(" \t\r\n,")
            if buf.startswith("]"):
                return
            try:
                value, end = decoder.raw_decode(buf)
            except json.JSONDecodeError:
                if eof:
                    raise
                block = stream.read(1024 * 1024)
                eof = not block
                buf += block
                continue
            yield value
            buf = buf[end:]


def main(args, work):
    out = work.out
    empty = work.env_file
    env = work.env
    evidence = {"commands": [], "bundles": [], "retrieval": {}}

    def command(name, *flags):
        cmd = [str(work.binary()), "--env-file", str(empty), *map(str, flags)]
        started = time.monotonic()
        proc = subprocess.run(cmd, cwd=ROOT, env=env, text=True, capture_output=True)
        (out / (name + ".stdout.json")).write_text(proc.stdout)
        (out / (name + ".stderr.txt")).write_text(proc.stderr)
        evidence["commands"].append({"command": cmd, "exit": proc.returncode, "seconds": time.monotonic() - started})
        assert proc.returncode == 0, (cmd, proc.stderr)
        return json.loads(proc.stdout)

    registry = json.loads((ROOT / "data/registry/memoryagentbench.json").read_text())
    assert registry["implementation_status"] == "imported" and len(registry["suites"]) == 5
    expected = {"accurate_retrieval": (17, 1700), "longmemeval-overlap": (5, 300), "conflict_resolution": (8, 800), "long_range_understanding": (110, 171), "test_time_learning": (6, 700)}
    overlap = {}
    for ref in registry["suites"]:
        bundle = ROOT / ref["path"]
        header = json.loads((bundle / "suite.json").read_text())
        index_bytes = (bundle / "episodes.index.jsonl").read_bytes()
        digest = hashlib.sha256(canonical(header) + b"\n" + index_bytes).hexdigest()
        assert digest == ref["suite_sha256"]
        assert hashlib.sha256(index_bytes).hexdigest() == header["container"]["index_sha256"]
        result = command(bundle.name + "-validate", "validate", bundle, "--expected-sha256", digest)
        assert result["valid"] and result["leak_scan"]["problems"] == []
        variant = header["protocol_ref"].split("#")[1]
        stats = json.loads((bundle / "import.json").read_text())
        rows, questions = expected[variant]
        assert stats["counts"]["episodes"] == rows and stats["counts"]["cases"] == questions
        index = [json.loads(line) for line in index_bytes.splitlines()]
        with (bundle / "episodes.jsonl").open("rb") as stream:
            for i, entry in enumerate(index):
                assert stream.tell() == entry["offset"] and entry["line"] == i
                raw = stream.readline()
                assert raw.endswith(b"\n") and len(raw) - 1 == entry["bytes"]
                assert hashlib.sha256(raw[:-1]).hexdigest() == entry["sha256"]
                episode = json.loads(raw)
                assert len(episode["events"]) == entry["events"] and len(episode["cases"]) == entry["cases"]
                assert all(c["cutoff"] == len(episode["events"]) for c in episode["cases"])
                if variant == "longmemeval-overlap":
                    assert len({c["attributes"]["question_id"] for c in episode["cases"]}) == 60
                    for case in episode["cases"]:
                        qid = case["attributes"]["question_id"]
                        pair = (case["query"], case["gold"]["answers"],case["attributes"]["question_date"])
                        if qid in overlap:
                            assert overlap[qid] == pair
                        overlap[qid] = pair
            assert not stream.read()
        chunks = stats["diagnostics"]["chunk_distribution"]
        counts = [c["chunks"] for c in chunks]
        evidence["bundles"].append({"path": ref["path"], "sha256": digest, "counts": stats["counts"], "sources": stats["diagnostics"]["sources"], "chunk_or_turn_distribution": {"min": min(counts), "max": max(counts), "sum": sum(counts), "mean": sum(counts) / len(counts)}, "over_budget_joined_chunks": sum(c["over_budget"] for c in chunks), "greedy_over_budget_chunks": sum(n > 4096 for c in chunks for n in c["sentence_tokens"]), "empty_chunks": sum(c["empty"] for c in chunks)})
    refs = {}
    for row in objects(ROOT / "data/upstream/longmemeval-s/longmemeval_s_cleaned.json"):
        refs[row["question_id"]] = (row["question"], row["answer"])
    assert len(overlap) == 300 and len(set(overlap) & set(refs)) == 300
    for qid, (question, answers, date) in overlap.items():
        prefix = "Current Date: " + date + ",\n\n Now Answer the Question: "
        assert question == prefix + refs[qid][0] and answers == [str(refs[qid][1])], qid
    evidence["overlap"] = {"distinct": 300, "occurrences": 300, "reference_questions": len(refs), "id_intersection": 300, "answer_mismatches": 0, "raw_question_differences": 300, "question_mismatches_after_removing_upstream_date_wrapper": 0}
    print("5 bundles: validation/leak-scan + independent Merkle/line hashes PASS; 300-ID overlap + question after upstream date wrapper / answer exact", flush=True)
    check = command("registry", "registry", "check")
    assert check["ok"] and not check["problems"]
    lme = json.loads((ROOT / "data/registry/longmemeval.json").read_text())
    assert "longmemeval" in registry["overlaps"] and "memoryagentbench" in lme["overlaps"]
    if args.reimport_ar:
        new = command("reimport-ar", "import", "memoryagentbench", "--input", ROOT / "data/upstream/memoryagentbench", "--split", "accurate_retrieval", "--chunk-tokens", "4096", "--out", out / "reimport-ar")
        for item in new:
            old = ROOT / next(r["path"] for r in registry["suites"] if r["suite_id"] == item["suite_id"])
            for name in ("suite.json", "episodes.index.jsonl", "episodes.jsonl"):
                assert (old / name).read_bytes() == (Path(item["path"]) / name).read_bytes()
        overlap_import = next(item for item in new if "overlap" in item["suite_id"])
        assert overlap_import["statistics"]["diagnostics"]["longmemeval_s_overlap"]["intersection"] == 300
        print("AR reimport: both bundles byte-identical; streaming importer overlap intersection=300", flush=True)
    suite = ROOT / "data/normalized/mab-conflict-resolution@7ea066982b14"
    for sut in ("null", "recent", "bm25"):
        destination = out / ("cr-" + sut)
        command("cr-" + sut, "run", "--suite", suite, "--reset-policy", "incremental_per_episode", "--sut", sut, "--mode", "retrieval", "--run-seed", "42" * 32, "--out", destination)
        run = json.loads((destination / "report.json").read_text())
        statuses = dict(Counter(c["status"] for c in run["cases"]))
        assert len(run["cases"]) == 800 and statuses == {"unscored": 800}
        phases = {p: run["summary"]["timings"][p]["n"] for p in ("reset_ms", "ingest_ms", "ready_ms", "query_ms")}
        assert phases == {"reset_ms": 8, "ingest_ms": 8, "ready_ms": 8, "query_ms": 800}
        assert len({c["public_input_sha256"] for c in run["cases"]}) == 8
        evidence["retrieval"][sut] = {"cases": 800, "statuses": statuses, "phase_counts": phases, "execution_errors": run["summary"]["execution_errors"]}
        assert evidence["retrieval"][sut]["execution_errors"] == 0
        print(sut + ": 800 queries, 8 reset/ingest/ready, 0 errors, unscored (no source labels)", flush=True)
    (out / "evidence.json").write_text(json.dumps(evidence, indent=2, ensure_ascii=False) + "\n")
    print("P3a data/retrieval/registry acceptance PASS: " + str(out / "evidence.json"))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--reimport-ar", action="store_true")
    verification_main(main, parser)
