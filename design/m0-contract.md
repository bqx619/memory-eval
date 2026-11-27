# M0: Offline evaluation kernel and original contracts

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

This reference records the 2026-10-15 foundation. Suite v3, bundle storage, container execution, reader inputs, and the decision judge subsequently extend or replace milestone-specific restrictions. The Python prototype under `docs/pa_memory_eval/` and its baseline reports are immutable historical parity evidence.

## Boundaries and dependency direction

The CLI composes the runner, reporting, and statistics. The runner alone owns lifecycle, anonymization, response validation, and scoring dispatch. Adapters receive public protocol DTOs, never a suite or case containing gold. `internal/protocol` has no business logic; scoring is pure; reporting and statistics consume result models. Potentially blocking interfaces take `context.Context` first.

The original M0 kernel used the standard library, in-process `null`, `recent`, and `bm25`, and no provider calls. That milestone restriction does not describe the later external-SUT container backend.

## Original suite v2

The top level contains `schema_version`, `suite_id`, `track`, `provenance`, `limitations`, `protocol.ingestion`, and `episodes`. Tracks include `seeded-state-diagnostic`, `history-qa`, `oracle`, and `personalization-mcq`. Provenance requires a source URL and may include revision, local input hash, and converter.

Episodes contain unique `id`, optional `cluster_id` (default episode ID), `events`, and `cases`. An event has `source_id`, `owner`, `content`, `role`, optional `session`, RFC3339 `timestamp`, preserved `time_raw`, and `attributes`. Original roles are user, assistant, system, observation, and fact. Cases have globally unique `id`, `kind` (`typed_context`, `qa`, `mcq`, `retrieval`), `query`, optional user/scope/category/cluster/query-time/question-type, budget, options, required exclusive-prefix `cutoff`, and private `gold`.

Gold contains `answers`, `abstain`, `evidence_ids`, `evidence_unit`, `exhaustive`, `choice`, and `expect`. MCQ requires nonempty options and a valid gold choice; retrieval requires its evidence field; timestamps and cutoffs must validate. V1 `time` becomes a timestamp when parseable and otherwise `time_raw`; absent role becomes observation. Canonical JSON sorts keys and removes insignificant whitespace before hashing.

## Identity and public projection

`run_seed` is a 32-byte random value encoded as hex, or an explicit supplied seed. Private `sample_id = case_id + "#" + repeat_index`. Anonymization takes the first 16 bytes of HMAC-SHA256 over `sample_id || NUL || kind || NUL || original` with the run seed, then formats as UUIDv4. Kinds include event, owner, session, namespace, and invocation; namespaces use the `paeval_` prefix. Original IDs remain in evaluator results; the mapping is reproducible and need not be stored.

Public events expose `id`, `owner`, `sequence`, `role`, `content`, optional `timestamp`, `time_raw`, `session_id`, and whitelisted attributes: `scope`, `type`, `canonical_name`, `aliases`, `epistemic_status`, `pinned`, `superseded_by`, `resolved_at`, `user_edited`, `participants`, `speaker`. Internal identity references are remapped consistently.

A retrieve request contains `query`, `user`, `scope`, optional `as_of`, `budget`, optional `options`, `query_invocation_id`, and `output_format`. Budget uses `top_k`, `max_context_chars`, and optional `maxBeliefs`. An item contains optional `id`, required text, `source_ids`, and optional score. Responses can expose `items`, typed `tiers` (`pinnedFacts`, `relevantBeliefs`, `openQuestions`), and `personaPrelude`. Answers expose text, abstain, or choice.

The adapter lifecycle is `Describe`, `Reset`, `Ingest`, `Ready`, `Retrieve` or `Answer`, and `Close`. Ingest handles record `batch_seq`, `accepted`, `filtered`, `failed`; readiness records visibility, stages, and warnings. Describe declares name, version, capabilities, limitations, read-only query behavior, and config. `ErrUnsupported` is not success or an incorrect answer; a `ContractError` denotes invalid adapter output.

## Execution and validation

`fresh_per_case` resets a new namespace for each case/repeat, ingests only events before cutoff, waits for readiness, retrieves, validates, then scores. Original per-event batch size was 64; per-session ingestion preserves sessions. P3 introduces incremental execution.

The original retrieval contract requires every item citation to refer to an ingested anonymous event. Unknown, future, duplicate references, missing text, and over-budget responses fail explicitly; the evaluator never silently repairs an invalid result. P12 defines the later narrow empty-citation allowance for supported `fixed_reader`/`items` cases.

Top-k limits items. Retrieval metrics expand source IDs in item order and deduplicate first-seen IDs; session metrics then deduplicate sessions. Report `k_unit="items→events(dedup first-seen)"`. Record cross-user and duplicate-source diagnostics. Unicode code points define character count. Default budgets are top-k 10 and 100000 characters; case `maxBeliefs` takes precedence.

Statuses include `scored`, `unscored`, `unsupported`, `contract_error`, `error`, and `ingest_failed`; `passed` is nullable. In paeval-v2, empty exhaustive retrieval/QA gold can be scored with `empty_result_correct`; non-exhaustive empty gold remains unscored. Record this deviation from the prototype.

## Metrics, statistics, and reports

Generic evidence metrics are hit, recall, MRR, all-evidence, nDCG, and precision only for exhaustive evidence. Typed assertions cover must/should include, exclusions, exact sets, counts, ordering, tier-specific membership, and persona-prelude predicates; zero assertions is unscored. Lexical answer EM/F1 is diagnostic; MCQ accuracy is separate. Aggregate scored metrics as `{mean,n}` and show planned/status counts, coverage, pass denominator, errors, unsupported counts, timing p50/p95/total/n, and returned-character quantiles.

Compare requires compatible suite hash, mode, budget, batching, scorer, case set, coverage, and reader. Compute right-minus-left per case and paired cluster bootstrap over original clusters: 2000 resamples, seed 42, percentile 95% interval. One cluster produces `ci95=null` with explanation. External JSONL resampling indices enable cross-language parity; default indices are recorded.

A case records identities, repeat/category/cluster/kind/cutoff/budget, status, nullable pass/metrics, failures/assertions, original retrieved IDs, returned characters, diagnostics, stage timings, sanitized error type/note, and public-input hash. Reports record framework/run/seed/time, suite hash/provenance/limitations, SUT fingerprint with declared/observed/unavailable facts, protocol, isolation, evaluator billing, environment/code hash, summaries, categories, official/memory-only scores, repeats, cases, and warnings. JSON, JSONL, CSV, and HTML are separate views; sensitive run files use mode 0600.

## Baselines and historical parity

BM25 uses k1=1.5, b=0.75, the prototype stopwords, ASCII alphanumeric tokens or individual CJK characters, and positive scores only. PMB imports beliefs as fact events in session `seed` and cases as typed assertions in one cluster `pmb-seed-corpus`.

The original 77-case typed projection had 76 pass judgments and one unscored case: BM25 48/76, recent 11/76, null 15/76. Null-to-BM25 pass difference was about +0.434, with no cluster CI. The later PMB-75 retrieval projection is a different protocol and denominator. Parity compares IDs, categories, cutoff, statuses, pass/metrics, assertions/failures, ordered citations, and character counts at tolerance `1e-9`, excluding run IDs and timings. Fixed-seed repeated execution must preserve public input and non-timing results. Historical requirements are not a claim of fresh execution during documentation maintenance.
