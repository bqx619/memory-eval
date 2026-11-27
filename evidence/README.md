# Aggregate evaluation evidence

This directory contains a selected, sanitized aggregate snapshot verified on **2026-11-27**: **13 PMB retrieval configurations** and **17 BEAM 100K answer runs**. It supports the numerical comparisons shared with the project. It is not a universal product leaderboard or a complete archive of every experiment.

## Downloads

| File | Contents |
|---|---|
| [score-table.csv](score-table.csv) | One row per recorded configuration/run: exact means, available denominators, status counts, execution/configuration identifiers, and hashes. Blank cells mean unavailable or not applicable, not zero. |
| [verified-results.json](verified-results.json) | The same selected runs with their metric dictionaries, reader/judge settings, declared embeddings, suite/configuration hashes, dataset registration metadata, and known coverage gaps. |

Direct downloads from the current `main` branch:

- [CSV](https://raw.githubusercontent.com/bqx619/memory-eval/main/evidence/score-table.csv)
- [JSON](https://raw.githubusercontent.com/bqx619/memory-eval/main/evidence/verified-results.json)

These branch URLs are mutable. For a durable citation, use the permalink for the **commit containing these evidence files**, or replace `main` in a download URL with that commit's full SHA. Cite the file SHA-256 below as well. The JSON's `source_snapshot` describes the code snapshot used when preparing this evidence; it is not the commit that first publishes these files.

## Interpretation

- **PMB-75:** retrieval diagnostic, `top_k=10`, 100,000-character budget. The full suite has 43 evidence-bearing cases and 32 empty-evidence cases. Metric `n` is the number of scored cases on which that metric is defined; partial runs have smaller denominators. Recall, MRR, nDCG, and correct empty results measure different behaviors.
- **Historical execution:** some PMB records retain their historical `process/…` backend identifiers. These records are historical evidence, not fresh runs through the current container backend. Built-in controls run in-process. Do not silently pool the execution conditions.
- **BEAM 100K:** 400 planned cases per run; `items` input, a `gpt-6-luna` reader, 100,000-character input budget, 2,048 output-token cap, and the recorded `jev-1.14.0` decision judge. The reader and judge settings match across these 17 records; SUT configurations, embeddings, versions, coverage, and resumed identities can differ. `beam.llm_judge_score` is a rubric mean, not percent questions correct or an official leaderboard replication.
- **Coverage:** scored, error, invalid, and unsupported counts are preserved. A mean over 213 scored cases must not be presented as equivalent evidence to a 400-case complete run. No paired significance claim is made for small score differences.
- **Evidence recall:** `review_metrics` (including the CSV's `beam.evidence_recall`) comes from the review builder, identified by `review_metrics_source_sha256`. It uses the builder's direct-source attribution rules and is separate from the evaluator's `metrics`. It is not interchangeable with broad ancestor-citation coverage or proof that text survived the reader's context budget.
- **Selection:** one named PMB reference run per selected configuration and all 17 runs in the checked BEAM review inventory. This is not a selection of each product's best result. SDK/adapter records do not establish current managed-service performance. Graphiti has no complete scored result in this checked inventory; Honcho is deferred; Echo is a protocol fixture.
- **Datasets:** registry integration is not a claim that every SUT completed every benchmark. Dataset licenses remain separate from the project's MIT code license.

## Provenance and disclosure boundary

Every selected source report's SHA-256, summary metrics, and status counts were checked locally against its report bytes. The CSV was checked against the JSON snapshot without rounding its values. `report_sha256`, `suite_sha256`, and available manifest/environment-lock hashes identify the recorded inputs; `resume_count` and `sut_changes` disclose resumed runs and identity changes.

A hash identifies an artifact; it is not independent access to that artifact. Full reports, per-case records, raw prompts, answers, cassettes, logs, private report locators, host paths, credentials, and model endpoints are **not** included here. This aggregate snapshot therefore supports checking the published arithmetic and configuration/coverage disclosures, not independently reconstructing every case. The local reports and generated viewer data remain outside the source distribution.

## File integrity

SHA-256 of the exact published file bytes:

```text
4bc8592e384859e172882a8eaa10048146d63ddb96b219c943fdfe7b20bb6982  score-table.csv
a239740f590bdef4f6847ead3fcb38f8fe190633ee8e2b0ce8c59ac285da834f  verified-results.json
```

Verify downloaded files with `shasum -a 256 score-table.csv verified-results.json` (or an equivalent SHA-256 tool). If this evidence is corrected or refreshed, keep the old commit available and update these hashes and the snapshot date. Existing numerical citations should remain pinned to their original evidence version.
