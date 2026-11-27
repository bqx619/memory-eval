# PA Memory Eval: historical Python prototype

**Research snapshot: 2026-10-15, v0.1.0, Python 3.11+.** This directory preserves the initial standard-library evaluator. The current project is the Go evaluator described in the [root README](../README.md). This English publication edition updates prose, not benchmark values or source questions.

The prototype connected source registration, adapters, bounded history replay, deterministic scoring, per-case results, and reports. It catalogued 13 benchmark sources, but did not download or run all 13. The bundled PMB projection contains 35 memory records and 77 single-turn cases from a pinned revision; it is not 77 independent conversations or the full official protocol.

## Historical execution

From this directory, the local baselines can be reproduced without model calls:

```sh
python3 -m pa_memory_eval validate data/pmb_projection.json
python3 -m pa_memory_eval run --adapter bm25 --out reports/my-bm25
python3 -m pa_memory_eval run --adapter recent --out reports/my-recent
python3 -m pa_memory_eval run --adapter null --out reports/my-null
python3 -m pa_memory_eval report reports/my-bm25/report.json reports/my-recent/report.json reports/my-null/report.json --out reports/my-comparison.html
```

Do not overwrite the retained reference reports. The prototype wrote JSON, JSONL, CSV, and HTML. Its 40 software tests were synthetic contract tests, not additional benchmark questions. Trusted in-process plugins are not a security sandbox; current third-party SUT execution uses Linux containers.

## Recorded baseline results

One case had no valid assertion and was not scored. Each baseline executed the same 77 cases independently.

| Baseline | Passed / valid | Assertion pass rate | Recall on 43 positive-evidence cases | Empty-result accuracy on 32 cases |
|---|---:|---:|---:|---:|
| BM25 with explicit metadata routing | 48/76 | 63.16% | 91.86% | 87.50% |
| Recent with the same routing | 11/76 | 14.47% | 69.88% | 31.25% |
| Null memory | 15/76 | 19.74% | 0.00% | 100.00% |

These are structured diagnostic results, not general agent scores. Null memory can pass exclusion assertions. BM25 uses available type, scope, pinned-memory, and alias metadata; a fair comparison must align access to those fields. The default context budget is 100,000 Unicode characters, not tokens; source-specific budgets take precedence. The shared seed corpus is one cluster, so paired population confidence intervals are null.

## Scope and provenance

PMB, LoCoMo text, LongMemEval cleaned, and PersonaMem v1 import paths were implemented and schema-tested. Full external imports and real SUT/model runs were not claimed for this snapshot. Raw upstream downloads were unavailable during initial preparation; the projection was transcribed from retrieved pinned content. `verify-projection` exists to check it against original bytes when available. Upstream blob IDs and local projection hashes are distinct.

See [evaluation design](docs/EVALUATION_DESIGN.md), [dataset inventory](docs/DATASETS.md), [adapter v1](docs/ADAPTER_PROTOCOL.md), and [source audit](docs/SOURCE_AUDIT.md). The current Go protocol supersedes v1. Original license notices remain in [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md). `ARTIFACT_MANIFEST.json` retains original identities and explicitly identifies this documentation/display revision.
