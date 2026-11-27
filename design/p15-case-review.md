# P15: Case-review data and SUT work traces

<!-- documentation-status: reference -->
> Technical reference. Historical runs are examples, not current operational instructions. See [current evaluation contracts](current-contracts.md); viewer builders, source types, and contract tests define implemented fields.

The case-review viewer combines source reports with permitted suite text, controls, optional notes, and work traces. CogletNet PMB runs motivated the initial trace extension; they remain ordinary historical SUT examples. The common result model must preserve original status and distinguish retrieval quality, execution failure, reader input, and diagnostic interpretation.

## Export and provenance

`python3 -B scripts/build_case_review.py <report-dir> --suite <suite> --name <run-name> [--control <report-dir>]... [--notes <notes.json>] [--glossary <json>] --out <dir>` reads evidence without running an SUT or model. It writes deterministic sorted JSON, a JS assignment `window.MEMEVAL_CASES = {...};`, and a run index `window.MEMEVAL_CASE_RUNS = [...]`. Same-name entries replace prior entries; index ordering follows generated_at. Controls must match suite hash, reset policy, and top-k or export fails. Missing facts are null, never inferred.

Trace text comes from `cases[].response.items` only when recorded. Without `--trace-text`, items are null; showing an event's original content must be labeled as original history, not actual returned text. `events_written` is the original episode prefix before case cutoff. Notes map case IDs to commentary and do not alter scores. Glossary definitions come from explicit exported glossary/index input or `memeval results glossary`; only emitted metrics are retained, sorted by ID; absent source yields an empty list. Legacy `name_zh`/`definition_zh` names remain data keys.

## Base payload v1

The following illustrative shape preserves the field contract; sample values are not a fresh evaluation result. Historical sample paths are repository-relative artifact identifiers.

```jsonc
{
  "version": 1,
  "generated_at": "2026-10-25T08:00:00Z",
  "run": {
    "name": "cogletnet-v3-pmb75", "report_path": "reports/p15-cogletnet/cogletnet-v3-pmb75/report.json", "report_sha256": "...",
    "sut": {"name": "cogletnet", "variant": "native-jev-openai-embedding", "fingerprint": "..."},
    "suite": {"id": "...", "sha256": "...", "path": "data/normalized/pmb-retrieval.json", "track": "retrieval-smoke"},
    "mode": "retrieval", "reset_policy": "fresh_per_case", "top_k": 10, "max_context_chars": 100000,
    "run_id": "...", "run_seed": "...", "started_at_utc": "...", "wall_seconds": 0, "resume_count": 0, "trace_text": true,
    "memeval_commit": "...", "framework_version": "...", "sut_usage": {"calls": 0, "input_tokens": 0, "output_tokens": 0, "unknown_usage_calls": 0, "by_role": {}}
  },
  "summary": {
    "total": 75, "by_status": {"scored": 75}, "by_verdict": {"hit_all": 0, "hit_partial": 0, "miss": 0, "empty_correct": 0, "empty_wrong": 0, "error": 0},
    "by_category": {"Alias resolution": {"total": 23, "verdicts": {"hit_all": 0}, "recall_mean": 0.0, "n_recall": 0}},
    "metrics": {"recall": {"mean": 0.0, "n": 43}, "mrr": {"mean": 0.0, "n": 43}, "ndcg": {"mean": 0.0, "n": 43}, "empty_result_correct": {"mean": 0.0, "n": 32}},
    "error_stages": {"ingest": 0, "retrieve": 0, "bridge": 0, "evaluator": 0, "unclassified": 0}
  },
  "controls": [{"name": "bm25-pmb75-trace", "sut": {"name": "bm25", "variant": "bm25"}, "report_path": "...", "report_sha256": "...", "metrics": {"recall": {"mean": 0.0, "n": 43}}}],
  "glossary": [{"id": "recall", "group": "memory-only retrieval", "name_zh": "Evidence recall", "definition_zh": "...", "formula": "|P∩G|/|G|", "range": "[0,1]", "higher_is_better": true, "aggregation": "...", "caveats": ["..."], "applies_to": ["retrieval"]}],
  "events": {"b-comm-response-length": {"content": "...", "role": "fact", "session": "seed", "owner": "test-user", "attributes": {"type": "preference", "scope": ["user:universal"], "epistemic_status": "active", "superseded_by": null, "aliases": [], "canonical_name": "..."}}},
  "cases": [{
    "case_id": "happy-stack-repository-layer", "sample_id": "happy-stack-repository-layer#0", "category": "...", "kind": "retrieval", "episode_id": "pmb-seed-corpus", "cutoff": 35,
    "input": {"query": "...", "user": "test-user", "scope": ["domain:code"], "as_of": null, "events_written": ["b-comm-response-length", "..."]},
    "gold": {"evidence_ids": [], "exhaustive": true, "abstain": false},
    "output": {"retrieved_source_ids": ["..."], "items": [{"rank": 1, "id": "...", "text": "...", "source_ids": ["..."], "score": 0.91}], "returned_chars": 1434},
    "metrics": {"recall": null, "mrr": null, "ndcg": null, "precision": 0, "empty_result_correct": 0, "hit": null, "all_evidence": null},
    "status": "scored", "verdict": "empty_wrong",
    "error": null,
    "attempts": [{"pass": 0, "source": "resume-history/0001", "status": "error", "error_type": "...", "error_detail": "..."}, {"pass": 1, "source": "report.json", "status": "scored", "error_type": "", "error_detail": ""}],
    "timings": {"reset_ms": 0, "ready_ms": 0, "ingest_ms": 0, "query_ms": 0},
    "controls": {"bm25-pmb75-trace": {"retrieved_source_ids": ["..."], "items": null, "metrics": {}, "verdict": "hit_all", "status": "scored"}},
    "analysis": {"auto": ["..."], "manual": null}
  }]
}
```


`run.sut_usage` comes from the final native usage close record where available, otherwise null. Report SHA binds the raw source report. Summary means retain their denominators and category/status distributions. A renderer hides undefined metrics instead of converting null into zero.

## Verdicts, failures, and attempts

For scored retrieval with nonempty gold: recall 1 is `hit_all`, between zero and one `hit_partial`, zero `miss`. Empty exhaustive gold uses `empty_correct` or `empty_wrong` from `empty_result_correct`. Non-scored statuses map to viewer `error` while retaining the actual status (`ingest_failed`, `contract_error`, invalid, unsupported, etc.). Answer tracks use a defined pass judgment, or supported benchmark score mapping (BEAM 1/0.5/0, MAB exact match 1/0); never invent a pass for an undefined metric.

Failure stage is ingest/retrieve when the RPC operation establishes that stage; timeout/process/transport errors without native detail are bridge failures; reader/judge/provider/gateway errors are evaluator failures; otherwise unclassified. Native detail is optional and adapter-specific.

```text
error = {stage: ingest|retrieve|bridge|evaluator|unclassified,
         type, detail, rpc_method: ingest|retrieve|null,
         native: {failed_events:[{index,source_id|null,event_id,state,kind,message,operation_id,
                    summary_failure|null,budget_failure|null}], batch_states:{state:count}}|null}
summary_failure = {body,source_hash,max_tokens,max_bytes,
                   attempts:[{ordinal,candidate_kind,stage,output_tokens,output_bytes,reason_hash,error_code}]}
budget_failure = {role,model,phase,unit,input,reserved_output,limit}
```

Map native event index to source ID only when batch order and lengths agree; otherwise retain index and set source_id null. Attempts are ordered `resume-history/*/report.json` followed by final report, starting pass zero. Without resume there is one pass from report.json. Earlier failures remain visible after recovery.

Automatic analysis may describe missed gold, rank, superseded facts, incompatible scopes, empty-gold overreturns, and differences from controls. These are post-hoc diagnostics, not labels fed back to the SUT. A dataset relationship is not proof of an upstream internal operation.

## Native trace capture and work payload

The adapter writes `sut_trace <json>` to SUT stderr. `COGLETNET_WORK_TRACE` is full/off; absent historical logs mean missing. Build records contain kind, episode-local zero-based seq, adapter event ID, event-text SHA-256, state, operation/snapshot IDs, raw audit, and snapshot view without vector values. Unavailable audit/snapshot is null with audit_error. Search records contain query, limit, native-source-to-adapter-event map, and raw SearchResult. The evaluator captures logs; adapter trace output is not silently truncated. Log-cap settings and corrupt lines must remain observable.

`build_audit` supplies bodies, candidate attempts, validation verdicts, model calls, and terminal errors. `snapshot_manifest_view` supplies members/levels/inputs/edges/spans/pending without point vectors. Search supplies candidates, selected items/paths, visible context, omitted reasons, effective settings, and optional JEV trace. Do not reconstruct unavailable fields from the result score.

```jsonc
"work": {
  "trace": "full|off|missing",
  "build": [{
    "source_id": "b-...|null", "event_id": "...", "seq": 0, "state": "Ready|Failed|Unknown", "operation_id": "...", "snapshot_id": "...|null",
    "nodes": [{"node": "...", "kind": "Source|Summary|...", "level": 0, "ordinal": 0, "span": [0, 96] | null, "text_hash": "...", "inputs": ["..."], "edge": {"parent": "...", "kind": "SummaryParent|ContextFallback"} | null, "underfill": false,
               "summary": {"model": "...", "attempts": 1, "faithful": true, "reason_hash": "..."} | null}],
    "summaries": [{"body": "...", "ordinal": 0, "candidate_kind": "ExactCopy|Whitespace|ExtractivePrefix|Model", "stage": "...", "accepted": true, "output_hash": "...|null", "output_tokens": null, "error_code": null, "verdict": {"faithful": true, "reason_hash": "...", "model": "..."} | null}],
    "calls": [{"role": "summary|validation|embedding|jev", "model": "...", "dispatches": 1, "failed": 0, "terminal_error": "KIND: message" | null}],
    "audit_error": null
  }],
  "search": {
    "query": "...", "mode": "Jev", "limit": 10, "jev_threshold": 0.5, "degraded": false, "warnings": [],
    "candidates": [{"node": "...", "source_id": "b-...|null", "level": 1, "similarity": 0.71, "p_use": 0.83, "outcome": "selected|jev_below_threshold|result_limit|candidate_cap|evidence_cap|CONTEXT_BUNDLE_TOO_LARGE|unselected", "rank": 1 | null}],
    "items": [{"rank": 1, "node": "...", "source_id": "b-...", "similarity": 0.7, "p_use": 0.9, "rank_utility": 0.8, "text_hash": "...", "path": [{"node": "...", "edge": "SummaryParent|null", "coverage": true, "text_hash": "..."}]}],
    "context_view": {"tokens": 1234, "tokenizer": "...", "rendered_hash": "...", "visible": [{"node": "...", "source_id": "...", "role": "...", "text_hash": "..."}]},
    "omitted": {"<node>": "reason"}
  } | null
}
```


All node/candidate/path/rendered/reason text is interned in run-level `texts`. Prefer upstream body/text hashes; generated SHA-256 prefixes are explicitly marked rather than passed off as upstream IDs. Map seq to events_written and verify text SHA; mismatch sets source_id null and increments `work_mapping_mismatches`. Native search source IDs resolve through the trace mapping. Failed builds can expose audit bodies without a valid level or edge; preserve that uncertainty.

Later additive search fields are `settings`, candidate `origin`/`vector_rank`, and `jev_trace`. Settings include `jev_threshold`, limit, representation/vector_representation, leaf_k/upper_k/candidate_cap, evidence_max_rounds/evidence_candidate_cap, memory_tokens, and later prompt/selection policy fields. Effective trace settings take precedence; legacy records may fall back to manifest values with their source disclosed.

```text
jev_trace = [{batch,model,error_code,
              targets:[{node,source_id|null,probability|null,target_text_hash,path_text_hash}]}]|null
```

Align targets and probabilities by position only when lengths agree; otherwise probability is null and a mapping mismatch is counted. Trace mode does not imply extra model calls. Search failures retain structured phase/status/elapsed/retry details where recorded. Prompt recipe and selection policy must be displayed when present rather than assumed from a run name.

## Structural interpretation and visualization

A single Source version/snapshot forms an ordered hierarchical forest, not a global-root or multiple-parent DAG. Each node has at most one upward same-source edge to the next level. Do not merge nodes across snapshots by NodeId alone. `SummaryParent` denotes actual consumption in parent inputs. `ContextFallback` links to the last preceding eligible higher-level node when there is no real parent. Path `coverage` becomes false after the first fallback and stays false.

Dataset `participants`, `superseded_by`, alias matches, and semantic relationships are separate from native Member edges. They must be labeled as dataset declarations or viewer deductions, never actual JEV traversal or causal explanation. A query root in a retrieval visualization is navigation, not a stored memory node.

A JEV target is a leaf or formed summary body. FullPath memory-reference input separates target body, ancestor context, and source state. Actual visible content is established by representation, jev_trace, and context_view.visible. `origin` records first entry into candidates: VectorHit, AncestorExpansion, EvidenceExpansion, or Unknown. Expanded nodes have null vector_rank and independent probabilities. Probability is not normalized across candidates; null/unscored/service failure is not zero. A radius derived from `1-p_use` is a probability scale, not semantic distance.

Derived `outcome` comes from selected items/omitted reasons, not a native CandidateTrace field. Selection may use absolute threshold, relative maximum over the whole candidate set, or a floor-plus-top-k policy; display actual settings. Passing a probability threshold does not guarantee packing under result/context budgets. Distinguish selected targets from context-only visible nodes.

The initial recorded build settings used target 512, low 359, high 665 tokens. Accepted summaries aggregate only within one Source version; multiple eligible summaries must reach the aggregation threshold. Independent short events do not form a cross-source tree. A level-zero-only run can still contain accepted pending summaries and is not proof that summary generation was skipped.

The viewer offers a query-oriented retrieval view and a stable storage-structure view. Shared encodings separate type, gold, selected rank, probability, source/version/snapshot, and actual structure edges. Candidate tables, exact JEV target text, summary attempts/verdicts, context rendering, and calls provide inspectable evidence. Keep long text readable and lazy-load where needed; no external resources are required.

## Historical PMB example and limits

The v3 PMB-75 run recorded 75 scored, zero final errors, 43 nonempty-gold and 32 empty-gold cases. Recall/MRR/nDCG/empty-result accuracy were 0.854/0.853/0.819/0.219; verdict counts were 35 full, 4 partial, 4 misses, 7 correct-empty, 25 incorrect-empty. All 2625 event writes formed single leaves; trace had 2625 build, 71 search, and 75 config rows. The four zero-cutoff cases had no search. The data therefore did not validate a multilayer visualization.

The later v5 example recovered errors by resume and ended 75/75 scored: recall 0.940, MRR 0.813, nDCG 0.813, empty-result accuracy 0.125; 37 full/6 partial/0 misses and 4 correct-empty/28 incorrect-empty. The common BM25 control was 0.919/0.857/0.860/0.438. The change broadened background/global-preference acceptance, improving evidence coverage while worsening empty-result behavior. These are dataset/protocol-specific observations, not proof of general semantic improvement or a recommendation to tune on gold. Earlier partial attempts are not interchangeable with the final denominator.

Tests should preserve deterministic exports, missing-trace nulls, control rejection, source mapping, resume attempts, and all trace layers. Existing artifact/test counts are historical; this documentation update does not claim a new SUT execution.
