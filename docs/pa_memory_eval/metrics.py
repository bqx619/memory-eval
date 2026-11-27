"""Deterministic metrics; no generated labels, semantic-judge claims, or hidden rescoring."""
from __future__ import annotations
from collections import Counter
import math
import re
import string
from statistics import mean
from typing import Any
from .core import ContractError


def ordered_unique(xs):
    return list(dict.fromkeys(xs))


def retrieval_metrics(predicted: list[str], gold: list[str], exhaustive: bool = False, k: int | None = None) -> dict:
    p = ordered_unique(predicted)
    if k is not None:
        if k < 0: raise ValueError("k must be non-negative")
        p = p[:k]
    g = set(gold)
    if not g:
        return {"hit": None, "recall": None, "mrr": None, "all_evidence": None,
                "ndcg": None, "precision": (0.0 if p else None) if exhaustive else None,
                "empty_result_correct": float(not p) if exhaustive else None, "empty_expected": True}
    hits = [i for i, x in enumerate(p) if x in g]
    dcg = sum(1 / math.log2(i + 2) for i in hits)
    ideal = sum(1 / math.log2(i + 2) for i in range(min(len(g), k if k is not None else len(p))))
    return {"hit": float(bool(hits)), "recall": len(hits) / len(g),
            "mrr": 1 / (hits[0] + 1) if hits else 0.0,
            "all_evidence": float(g.issubset(p)), "ndcg": dcg / ideal if ideal else 0.0,
            "precision": len(hits) / len(p) if exhaustive and p else (0.0 if exhaustive else None),
            "empty_result_correct": None, "empty_expected": False}


def score_typed(expect: dict, response: dict, k: int = 10) -> dict:
    tiers = response.get("tiers")
    if not isinstance(tiers, dict) or any(not isinstance(tiers.get(k), list) for k in ("pinnedFacts", "relevantBeliefs", "openQuestions")):
        raise ContractError("typed_context response requires all three list tiers")
    prelude = response.get("personaPrelude")
    if not isinstance(prelude, str):
        raise ContractError("personaPrelude must be a string (empty means no profile)")
    pinned = set(tiers["pinnedFacts"])
    relevant = set(tiers["relevantBeliefs"])
    questions = set(tiers["openQuestions"])
    union = pinned | relevant
    checks: list[tuple[bool, str]] = []
    def check(value, message): checks.append((bool(value), message))
    rb = expect.get("relevantBeliefs", {})
    for field in ("mustInclude", "shouldInclude"):
        for item in rb.get(field, []):
            check(item in union, f"{field}: missing {item}")
    for item in rb.get("mustExclude", []):
        check(item not in union, f"mustExclude: leaked {item}")
    if "shouldOnlyInclude" in rb:
        check(relevant == set(rb["shouldOnlyInclude"]), "relevantBeliefs exact-set mismatch")
    if "maxCount" in rb: check(len(relevant) <= rb["maxCount"], "relevantBeliefs too many")
    if "minCount" in rb: check(len(relevant) >= rb["minCount"], "relevantBeliefs too few")
    for a, b in rb.get("orderedBefore", []):
        order = tiers["relevantBeliefs"]
        check(a in order and b in order and order.index(a) < order.index(b), f"ranking: {a} before {b}")
    for key, predicted in (("pinnedFacts", pinned), ("openQuestions", questions)):
        rule = expect.get(key, {})
        for item in rule.get("mustInclude", []): check(item in predicted, f"{key}: missing {item}")
        for item in rule.get("mustExclude", []): check(item not in predicted, f"{key}: forbidden {item}")
    pp = expect.get("personaPrelude", {})
    if pp.get("nonEmpty"): check(bool(prelude), "personaPrelude empty")
    if pp.get("isNull"): check(prelude == "", "personaPrelude should be empty")
    for s in pp.get("contains", []): check(s in prelude, f"personaPrelude missing substring {s!r}")
    for s in pp.get("mustNotContain", []): check(s not in prelude, f"personaPrelude forbidden substring {s!r}")
    expected = rb.get("shouldOnlyInclude")
    metrics = retrieval_metrics(tiers["relevantBeliefs"], expected, exhaustive=True, k=k) if expected is not None else {
        "precision": None, "recall": None, "hit": None, "mrr": None, "all_evidence": None, "ndcg": None}
    wanted = set(expect.get("pinnedFacts", {}).get("mustInclude", []))
    metrics["pinned_coverage"] = len(wanted & pinned) / len(wanted) if wanted else None
    return {"status": "scored" if checks else "unscored", "passed": all(v for v, _ in checks) if checks else None,
            "assertions": len(checks), "assertions_passed": sum(v for v, _ in checks),
            "failures": [m for v, m in checks if not v], "metrics": metrics,
            "note": None if checks else "No effective source assertions; not a free pass"}


def normalize_answer(text: str) -> str:
    # Diagnostic SQuAD-style English normalization, NOT any upstream official judge.
    text = text.lower().translate(str.maketrans("", "", string.punctuation))
    text = re.sub(r"\b(a|an|the)\b", " ", text)
    return " ".join(text.split())


def lexical_answer_metrics(prediction: str, references: list[str]) -> dict:
    if not references:
        return {"exact_match_diagnostic": None, "token_f1_diagnostic": None}
    pp = normalize_answer(prediction)
    scores = []
    for answer in references:
        aa = normalize_answer(answer)
        p, a = pp.split(), aa.split()
        if not p or not a:
            f1 = float(p == a)
        else:
            overlap = sum((Counter(p) & Counter(a)).values())
            precision = overlap / len(p); recall = overlap / len(a)
            f1 = 2 * precision * recall / (precision + recall) if overlap else 0.0
        scores.append((float(pp == aa), f1))
    return {"exact_match_diagnostic": max(x[0] for x in scores), "token_f1_diagnostic": max(x[1] for x in scores)}


def percentile(values: list[float], q: float) -> float | None:
    if not values: return None
    a = sorted(values)
    i = (len(a) - 1) * q
    lo, hi = int(i), math.ceil(i)
    return a[lo] + (a[hi] - a[lo]) * (i - lo)


def aggregate(rows: list[dict]) -> dict:
    counts = Counter(r["status"] for r in rows)
    scored = [r for r in rows if r["status"] == "scored"]
    passed = [r["passed"] for r in scored if r.get("passed") is not None]
    keys = sorted({k for r in scored for k in r.get("metrics", {}) if k != "empty_expected"})
    metrics = {}
    for key in keys:
        vals = [r["metrics"][key] for r in scored if isinstance(r.get("metrics", {}).get(key), (int, float))]
        metrics[key] = {"mean": mean(vals) if vals else None, "n": len(vals)}
    timings = {}
    for phase in ("reset_ms", "ingest_ms", "ready_ms", "query_ms", "answer_ms"):
        vals = [r["timings"][phase] for r in rows if isinstance(r.get("timings", {}).get(phase), (int, float))]
        timings[phase] = {"p50": percentile(vals, .5), "p95": percentile(vals, .95), "total": sum(vals), "n": len(vals)}
    return {"planned": len(rows), "status_counts": dict(counts), "n_with_pass_judgment": len(passed),
            "passed": sum(passed), "pass_rate_on_scored": mean(passed) if passed else None,
            "scoring_coverage": len(scored) / len(rows) if rows else None,
            "execution_errors": counts["error"], "unsupported": counts["unsupported"],
            "metrics": metrics, "timings": timings,
            "returned_chars": {"p50": percentile([r["returned_chars"] for r in rows if "returned_chars" in r], .5),
                               "p95": percentile([r["returned_chars"] for r in rows if "returned_chars" in r], .95)},
            "billing": {"api_cost_usd": None, "model_tokens": None,
                        "note": "No billing integration was executed; characters are not tokens"}}
