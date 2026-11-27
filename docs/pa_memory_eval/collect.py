"""Fetch primary raw files, pin upstream revision, verify bytes, and write a source lock.

Not run successfully in the authoring environment: network byte-download was unavailable.
No failures are turned into empty datasets. There is no third-party mirror fallback.
"""
from __future__ import annotations
import hashlib
import json
from pathlib import Path
import time
import urllib.parse
import urllib.request
from .core import ContractError, read_json, write_json

SOURCES = {
    "pmb": {"host": "github", "repo": "tenurehq/precisionMemBench", "revision": "61a712a1754e7d27b6dee42da32a2c214b003ece",
            "files": ["fixtures/beliefs.seed.json", "fixtures/retrieval.cases.json", "fixtures/session-retrieval.cases.json", "LICENSE"],
            "license_note": "MIT repository license; keep Jeffrey Flynt attribution"},
    "locomo": {"host": "github", "repo": "snap-research/locomo", "revision": "main",
               "files": ["data/locomo10.json", "LICENSE.txt"], "license_note": "Read upstream LICENSE.txt before use; do not assume academic data is unrestricted"},
    "longmemeval-s": {"host": "huggingface", "repo": "xiaowu0162/longmemeval-cleaned", "revision": "main",
                      "files": ["longmemeval_s_cleaned.json", "README.md"], "license_note": "Verify dataset card and originating corpora licenses"},
    "longmemeval-m": {"host": "huggingface", "repo": "xiaowu0162/longmemeval-cleaned", "revision": "main",
                      "files": ["longmemeval_m_cleaned.json", "README.md"], "license_note": "Verify dataset card; large file"},
    "longmemeval-oracle": {"host": "huggingface", "repo": "xiaowu0162/longmemeval-cleaned", "revision": "main",
                           "files": ["longmemeval_oracle.json", "README.md"], "license_note": "Separate oracle diagnostic, never mix with full-history scores"},
    "personamem-v1-32k": {"host": "huggingface", "repo": "bowen-upenn/PersonaMem-v1", "revision": "main",
                           "files": ["questions_32k.csv", "shared_contexts_32k.jsonl", "README.md"], "license_note": "Dataset card declares MIT; retain attribution"},
}


def _json_url(url, timeout):
    req = urllib.request.Request(url, headers={"User-Agent": "pa-memory-eval/0.1.0", "Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)


def fetch_dataset(name: str, out: str | Path, *, ack_license=False, timeout=60, max_bytes=2_000_000_000, revision=None):
    if name not in SOURCES: raise ValueError("No native collector for this dataset; see source registry/upstream environment runner")
    if not ack_license: raise ValueError("Review source license and rerun with --ack-license; this is acknowledgement, not a legal clearance")
    cfg = SOURCES[name].copy()
    cfg["revision"] = revision or cfg["revision"]
    root = Path(out) / name
    lockpath = root / "source.lock.json"
    if lockpath.exists():
        lock = read_json(lockpath)
        if revision and lock["revision"] != revision: raise ContractError("Existing lock differs; use a new output directory")
        for file in lock["files"]:
            p = root / file["path"]
            if not p.is_file() or hashlib.sha256(p.read_bytes()).hexdigest() != file["sha256"]:
                raise ContractError("Locked source file missing/modified; refusing silent replacement")
        return lock
    repo = cfg["repo"]
    rev = cfg["revision"]
    if cfg["host"] == "github":
        metadata = _json_url(f"https://api.github.com/repos/{repo}/commits/{urllib.parse.quote(rev, safe='')}", timeout)
        pin = metadata["sha"]
    else:
        metadata = _json_url(f"https://huggingface.co/api/datasets/{repo}/revision/{urllib.parse.quote(rev, safe='')}", timeout)
        pin = metadata["sha"]
    lock = {"dataset": name, "host": cfg["host"], "repo": repo, "revision": pin,
            "requested_revision": rev, "license_note": cfg["license_note"], "fetched_runtime_unix": time.time(), "files": []}
    root.mkdir(parents=True, exist_ok=True)
    try:
        for relative in cfg["files"]:
            encoded = urllib.parse.quote(relative, safe="/")
            url = (f"https://raw.githubusercontent.com/{repo}/{pin}/{encoded}" if cfg["host"] == "github"
                   else f"https://huggingface.co/datasets/{repo}/resolve/{pin}/{encoded}")
            p = root / relative
            p.parent.mkdir(parents=True, exist_ok=True)
            tmp = p.with_suffix(p.suffix + ".part")
            h = hashlib.sha256(); size = 0
            req = urllib.request.Request(url, headers={"User-Agent": "pa-memory-eval/0.1.0"})
            try:
                with urllib.request.urlopen(req, timeout=timeout) as r, open(tmp, "wb") as f:
                    while True:
                        block = r.read(1 << 20)
                        if not block: break
                        size += len(block)
                        if size > max_bytes: raise ContractError("Source exceeds --max-bytes; increase explicitly")
                        h.update(block); f.write(block)
                tmp.replace(p)
            finally:
                if tmp.exists(): tmp.unlink()
            lock["files"].append({"path": relative, "url": url, "bytes": size, "sha256": h.hexdigest()})
    except Exception:
        write_json(root / "incomplete.lock.json", {**lock, "status": "incomplete"})
        raise
    lock["status"] = "complete"
    write_json(lockpath, lock)
    return lock


def verify_projection(projection_path, upstream_seed, upstream_cases):
    """Compare all projected fields against pinned ORIGINAL files after download."""
    p = read_json(projection_path)
    original_seeds, original_cases = read_json(upstream_seed), read_json(upstream_cases)
    seeds = {x["_id"]: x for x in original_seeds}
    cases = {x["caseId"]: x for x in original_cases}
    problems = []
    if {x["_id"] for x in p["beliefs"]} != set(seeds): problems.append("seed id sets differ")
    if {x["caseId"] for x in p["cases"]} != set(cases): problems.append("case id sets differ")
    for item in p["beliefs"]:
        raw = seeds.get(item["_id"], {})
        for k, v in item.items():
            default = [] if k == "participants" else False if k in {"pinned", "user_edited"} else None
            if raw.get(k, default) != v: problems.append(f"seed {item['_id']}:{k}")
    for case in p["cases"]:
        raw = cases.get(case["caseId"], {})
        for k, v in case.items():
            if raw.get(k) != v: problems.append(f"case {case['caseId']}:{k}")
    return {"matches": not problems, "mismatches": problems, "method": "field-for-field, no generated text or fuzzy alignment"}
