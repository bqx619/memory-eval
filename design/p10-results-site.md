# P10: Offline result cards and metric glossary

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

A result card is a compact, reproducible derivative of an evaluation report. It supports inspection and compatible comparisons without publishing complete case text or private execution details. The site does not define a cross-dataset weighted score or automatic universal ranking.

## Card v1

A card has `card_version`, `id` (run ID), `subject`, `dataset`, `run`, `results`, `cost`, `warnings`, and `source`.

- `subject`: name, variant, variant_notes, `upstream{project,repo,package,version,license}`, declared model/embedding configuration, optional LLM metadata, manifest hash, environment-lock hash, and image ID. Upstream facts come from `suts/<name>/UPSTREAM.json` and must not be guessed.
- `dataset`: suite ID/hash/schema, protocol ID/variant/ref/hash, planned cases, cases with gold, category count, and license.
- `run`: mode, reset policy, top-k, context budget, seed, limit, repeats, parallelism, resume count, start/wall time, framework/commit, and isolation summary including backend/network/enforcement/cleanliness/selftest.
- `results`: status counts, coverage, errors/invalid/unsupported, `headline` metric IDs, `metrics{id:{mean,n}}`, additional scores, category summaries, and stage p50/p95 timings.
- `cost`: separate evaluator and SUT usage, retaining calls, prompt/cached/completion tokens, retries, and throttling where available.
- `source`: report-relative path and raw report SHA-256.

Keep numerical values unchanged; format rounding only in the UI. Preserve n and nulls. Headline metrics derive from protocol primary/retrieval declarations, not hand-authored rankings. `scores.memory_only` entries identical to summary metrics are deduplicated; genuinely additional official metrics remain. Category summaries retain planned/status/metrics; timings retain stage p50/p95. Private gateway addresses, credentials, and deployment paths require publication sanitization even when a private source report contains them.

## Index and CLI

`memeval results card <report-dir>`, `results build --reports <dir>... --out <dir>`, and `results glossary` create derivatives. Build recursively finds reports, skips rejected documents without a summary with diagnostics, deduplicates a run ID by greatest resume count, and sorts newest first. The index is `{index_version:1,generated_at,memeval_commit,glossary,cards}`; JS assigns the identical JSON to `window.MEMEVAL_RESULTS`. Escape `</script>` safely. Identical inputs must yield identical output.

The upstream schema requires project/repository/package/version/license. Missing facts remain null or trigger the relevant validation error; they are not inferred from a familiar project name.

## Metric definitions

`internal/scoring/glossary.go` is the single metric-definition source. `MetricDef` includes ID, group, legacy `name_zh`/`definition_zh` keys, formula, range, direction, aggregation, caveats, and applicability. Those historical field names are compatibility keys; documentation language does not rename the data contract. Parameterized IDs can match `@k` glossary entries. `TestGlossaryCoversEmittedMetrics` checks emitted metric coverage.

Generic retrieval metrics expand citations and deduplicate first-seen IDs under the documented evidence unit. Hit means any gold evidence; recall is the fraction recovered; MRR is reciprocal first relevant rank; nDCG normalizes discounted gain; all-evidence requires complete gold coverage. Precision is defined only for exhaustive gold under the scorer's empty-result convention. `empty_result_correct` applies only to empty exhaustive gold. Do not mix these with upstream-specific LongMemEval turn/session metrics or the viewer's later direct-source metrics.

Typed-context metrics evaluate declared assertions/tier semantics. Answer metrics follow each protocol's parser and judge recipe; a replacement Jev judge is not the original official model. Status counts and scoring coverage accompany every score. Latency depends on environment/load; provider usage may be incomplete and remains nullable.

## Offline presentation

The site provides filtering, detail panels, side-by-side compatible comparisons with deltas, metric explanations, and optional browser-only import of report JSON. It uses no external resources. Cards omit case texts; case-review views have separate contracts and publication rules. Compare compatibility includes suite/protocol, mode, reader/judge, reset, and budgets; display incompatible runs without pretending a paired delta is valid. Mobile layout and themes must retain counts and caveats.

Golden cards cover baseline, SUT, and fixed-reader cases; tests check deterministic extraction, deduplication, nulls, metrics, and JS escaping. Browser rendering is a separate verification step; editing this reference does not claim a new site validation.
