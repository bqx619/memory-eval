# cogletnet adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest-leaf-memory-abstractive-lv1.json](manifest-leaf-memory-abstractive-lv1.json) | cogletnet | native-leaf-memory-abstractive-lv1-jev3-openai-embedding | open | Not separately declared |
| [manifest-leaf-memory-abstractive.json](manifest-leaf-memory-abstractive.json) | cogletnet | native-leaf-memory-abstractive-jev3-openai-embedding | open | Not separately declared |
| [manifest-leaf-memory-leaf-embed.json](manifest-leaf-memory-leaf-embed.json) | cogletnet | native-leaf-memory-leaf-embed-jev3-openai-embedding | open | Not separately declared |
| [manifest-leaf-memory.json](manifest-leaf-memory.json) | cogletnet | native-leaf-memory-jev3-openai-embedding | open | Not separately declared |
| [manifest-tree.json](manifest-tree.json) | cogletnet | native-tree-jev3-openai-embedding | open | Not separately declared |
| [manifest.json](manifest.json) | cogletnet | native-jev-openai-embedding | open | Not separately declared |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

Original record: CogletNet adapter facts

Read-only sources: `../cogletnet/docs/models.md`, `docs/implementation.md`,
`crates/adapters/src/environment.rs`, `crates/cogletnet/src/types.rs` and the public
host example, checked 2026-10-25. The local source snapshot is hashed during
staging; no upstream revision is invented and no upstream file is edited.

- Rust toolchain: upstream `rust-toolchain.toml` pins 1.97.1. The generated Linux
  image downloads the official archive and verifies its `.sha256`, then builds
  the external host with the upstream Cargo lock. Only the new host package is
  added to the staged workspace/lock; upstream crate source is unchanged.
- `LiveEnvironment::open`, durable `memory.ingest` followed by `build_with_options`, and
  `memory.search(SelectionMode::Jev, Representation::FullPath,
  FailurePolicy::Strict)` are used directly. No vector-only fallback, mock model,
  replacement ranker or feedback settlement is used in the real run.
- One public event maps to one Source. Retrieval uses the returned FullPath text
  and the native source ID mapped back to its public event ID. Empty owner
  allowlists return no items, since native `sources=[]` means all sources.
  `scope` and `as_of` are unsupported and disclosed; metadata is retained without
  claiming native event-time semantics.
- The indexed text is the public event's `content`; attributes are metadata, not
  appended text. The built-in BM25 control additionally indexes `canonical_name`
  and `aliases`. This is a disclosed input-rendering difference, not a claim of
  equal internal representation or equal compute across systems.
- `HOST_GENERATION_MODEL` and `GENERATION_REASONING_EFFORT` come from the same
  `{llm.model}` / `{llm.reasoning_effort}` configuration as other evaluated SUTs.
  PMB retrieval does not call final generation. Internal summaries remain
  Luna/medium and fidelity validation Sol/medium, as specified by CogletNet's
  documentation; they are not mislabeled as the host answer model.
- Embedding is the documented OpenAI `text-embedding-3-large`, 3072 dimensions,
  cl100k_base, maximum 8192 tokens, batch 32. Selection is TypeSafe
  `jev-1.14.0`. Text calls use the documented Responses protocol. These services
  require `network=open`; this variant makes no single-target gateway claim.
- Qdrant 1.20.0 digest was read from the already present local Docker image:
  `sha256:9bfc89f9914fc14876a88c05270685921a55b8877b254a98bcc1cc2f0a424cd2`.
  Each episode starts its own ephemeral Qdrant sidecar and SQLite/blob state.
  Existing CogletNet databases/collections are never used.
- The native UsageMeter emits cumulative `cogletnet_usage` JSON to SUT stderr
  after each operation, including failures. Counters are durable across process
  restarts: group by `ledger_id` and retain the highest `revision`, then sum
  different ledgers. Never sum cumulative log lines or restarted processes. Tokens are service-reported; unknown usage is
  retained. They are not a currency invoice and are not fabricated as governor
  metering in `report.billing.sut` (the run uses direct multiple services).
- Dump is raw input sources only, with a `raw` layer disclosure. Native answer,
  typed context and closed-loop MAM evaluation are outside this first PMB run.

- Native `add_batch` reports each event as Ready, Failed, Unknown or NotDispatched,
  with its source and operation ID when known. It stops launching after failure
  and drains in-flight jobs. `operation_status`, `resume`, `audit_build` expose
  public core recovery APIs; audit contains private source/candidate text.
- A replay within one episode retains the event key/source and persistent retry
  budget. Different events cannot share a source. `reset`, even with the same
  namespace, creates a fresh state directory; no cross-evaluation result cache.
- The default native batch window is 1740 seconds, within a 1750-second Python
  RPC deadline and the evaluator's 1800-second outer limit. Close bounds its RPC
  and process reaping within 45 seconds. Native/core own individual model retries.
  Idempotent native search in both variants additionally retries the five MODEL_*
  availability/timeout/transport/rate-limit/recovery-exhausted kinds or an explicit
  `model_failure.retryable=true`, using `bridge.config.search_retry_max_attempts`
  (default 3 including the first call) and `search_retry_initial_s` (default 10):
  exponential backoff is raised to `retry_after_ms / 1000 + 2` when present, and
  attempts plus waits share the RPC window (`request_timeout_s - 50` seconds);
  insufficient time preserves the native error, while search trace `retries`
  records kind/backoff_s/retry_after_s (empty when no retry). A partial history
  blocks ready/retrieve.
- Finite admission ceilings are 10000 calls / 100000000 reserved input tokens.
  An explicit dollar ceiling additionally requires complete model prices.
  Unknown/cancelled requests retain reservations. The default new summary recipe
  is source-language-v1; legacy-v4 must be explicit for old build recovery.
- Offline acceptance uses core fault tests, fake native processes and source
  staging/compile checks. No paid calls or real Linux container execution were
  performed for this repair. Previous evaluation results are unchanged; no
  summary quality or production throughput improvement is claimed.

## Installation

2026-10-25 maintenance: new manifests explicitly select `evidence-first-v1`
and `conservative-bytes-v1`. Complete byte equality and pure whitespace may
use auditable local fidelity proofs; every extracted/rewritten candidate still
uses the independent judge. Leaf/parent/candidate byte limits align with the
full HTTP request budget without modifying raw whitespace. Old operation
recovery requires its original `model-only`/`legacy` policy and prompt settings.
`JEV_THRESHOLD=0.5` exposes the existing probability gate without tuning it.
Native `build_progress` returns authorized metadata only; `usage` returns the
namespace's cumulative ledger. Neither enables partial-history scoring or
backfills unknown usage. New real-model quality/cost remains unverified and
The P14 campaign was stopped; its recorded observations do not establish completeness.

Stage into a new scratch directory (including the shared bridge), then give the
printed manifest path to `memeval env install`. The template in this directory
requires staging; it deliberately contains no copy of the upstream code.

```sh
python3 -B scripts/stage_cogletnet.py --source ../cogletnet --out <new-scratch>/staged
memeval env install <new-scratch>/staged/cogletnet/manifest.json
```

Use a temporary private credential file containing only the explicitly allowlisted
`EMBEDDING_API_KEY` and `JEV_API_KEY` with `--env-file`; the framework reads
passthrough from that file, not only the parent environment. Do not import all
upstream settings: unknown private numeric settings can become redaction values
in the evaluator. Supply the evaluator's existing
`MEMEVAL_SUT_LLM_*` gateway configuration in the parent environment (or use a
private combined runtime file). Never print credentials or put them into argv.
The evaluation launcher loads both existing files in memory; neither file is
changed or copied into the build context.

## P15 work traces (2026-10-25)

- The external host additionally forwards `snapshot_manifest(snapshot_id)` and
  `get(source_id, version)` to the public core methods and returns their serde
  JSON unchanged. No core source or search/build policy is modified.
- `COGLETNET_WORK_TRACE=full` is the default and is explicit in the manifest;
  `off` skips all diagnostic reads and build/search records. A content-free
  config marker still records the selected mode. `describe.config.work_trace`
  also exposes it. These diagnostics contain source/candidate text, not just
  counters; treat stderr as evaluation data.
- Each returned batch event, including Failed/Unknown/NotDispatched, emits a
  `sut_trace` build record with stable zero-based episode sequence and SHA256
  of the exact ingested UTF-8 text. Replaying an event retains its sequence.
  Audit and snapshot reads are best effort within the existing ingest deadline;
  unavailable data is null with `audit_error`. Snapshot points retain only `id`
  and `representation`; no vector numbers are logged. Native JSON is not mutated.
- Search diagnostics retain the complete native result before adapter budget
  filtering, its source-to-event map, request limit and configured JEV threshold.
  They make no claim about final answer generation (the retrieval API does not
  generate it). Trace failures never replace write/search results or relax the
  complete-history Ready gate. A broken diagnostic transport is discarded; the
  next business RPC reopens the same durable database without redispatching writes.
- Diagnostic reads and stderr writes have measured overhead; no timings are
  subtracted, no deadline is extended and no model retry is added. An off-mode
  comparison is required to quantify overhead. Long records are not truncated
  by the adapter; the evaluator's per-file log cap still applies.
- The review builder resolves event provenance only after sequence **and** text
  hash checks. Local text keys use `sha256-16:<hex>`; upstream hashes are retained.
  Missing traces stay `missing`; trace-disabled runs stay `off`. Synthetic
  ready/failed builds and search traces in `scripts/testdata/case-review-fixture/`
  are for offline UI development, not evidence of real-model quality.

P15 §6.6 integration: full tracing now sends `SearchOptions.trace=true` and
retains `SearchResult.settings` and `jev_trace` unchanged; off sends false.
The host also forwards `snapshot_manifest_view`. This lightweight response
retains point metadata and omits only vector numbers. If unavailable, the
adapter falls back to the full snapshot and removes only `points[].vector`,
recording the view error without changing the write result.

The checked upstream `JevBatchTrace.probabilities` is an optional map keyed by
the complete `node/body` target ID, rather than the positional array described
in the initial review contract. The review builder supports both shapes:
maps are checked for exact target keys and reordered to match `targets`, arrays
are checked for equal length. A mismatch nulls the whole batch's probabilities
and increments `work_mapping_mismatches` once per batch; an explicit null from
a failed batch is preserved as unavailable evidence. Target bodies and full
paths enter the deduplicated text pool. A null effective JEV threshold remains
null (e.g. vector fallback), not the configured default.

## P15 v4 JEV decision profile (2026-10-25)

Upstream snapshot `36fda9c6cfbc895be56d227f883d84bc8803bf3d65cf7beb897b02dd8232fedf`
contains 46 source files; the contract is `../cogletnet/docs/jev-decisions.md`.
The manifest explicitly selects `JEV_PROMPT_PROFILE=memory-reference-v1` and
`JEV_SELECTION_POLICY=absolute-threshold`, retaining `JEV_THRESHOLD=0.5` and
all other JEV settings. `describe.config` discloses these values. The intended
v3/v4 comparison changes the upstream question, ancestor representation and
native source-version state, not the selection threshold or strategy.

`SearchResult.settings` now includes `jev_prompt_profile`, `jev_prompt_hash`,
`jev_input_format` and `selection_policy`; the adapter trace and review builder
retain these fields unchanged. New targets retain `target_body` and may have
an empty `full_path`, with `ancestor_context` and `source_state` instead.
Target IDs and the probability map still use complete `node/body` references.

Upstream's three strategy configurations are:
- `JEV_SELECTION_POLICY=absolute-threshold`: `JEV_THRESHOLD` (default 0.5).
- `JEV_SELECTION_POLICY=relative-maximum`: `JEV_RELATIVE_RATIO` (default 0.8)
  and `JEV_MIN_PROBABILITY` (default 0.5), over the entire candidate pool.
- `JEV_SELECTION_POLICY=top-k`: `JEV_TOP_K` (default 5) and
  `JEV_MIN_PROBABILITY` (default 0.5).

Only the first strategy is selected for v4. Alternative strategies are not
tuned on PMB/BEAM; offline mechanism tests do not establish model quality.

## P15 v5 bounded JEV retries (2026-10-25)

Upstream snapshot `c2d0770c3503e470be3b57418e4774a80c28a5c1a1b57bbc4ff9e55891c72e89`
contains 47 source files. See `../cogletnet/docs/jev-decisions.md`, JEV transient
failure recovery. The question, candidates, threshold and selection policy are
unchanged from v4. The manifest adds no retry overrides: upstream defaults are
`MODEL_MAX_ATTEMPTS=3` (including the first attempt),
`MODEL_RETRY_INITIAL_MS=1000`, `MODEL_RETRY_MAX_MS=30000`, and
`MODEL_RETRY_JITTER_MS=250`.

Core JEV retries a complete batch only for retryable failures, with exponential
backoff and Retry-After. A server wait beyond max+jitter returns the original
error rather than retrying early. HTTP remains single-attempt to avoid nested
retries; authentication, protocol and budget failures do not retry. Query
embedding and final generation are outside this change. Strict exhaustion
preserves the last Error/ModelFailure; trace does not turn a failure into a zero
probability or a successful result.

`SearchResult.settings.jev_retry_policy` records the effective policy (null for
pure VectorOnly), and `jev_trace[].attempts` carries each adapter invocation's
`attempt`, `error_code`, `model_failure`, and `retry_delay_ms`. The adapter already
retains native SearchResult JSON; the review builder now also preserves attempts
unchanged under `work.search.jev_trace[].attempts` (absent on old traces becomes
an empty list). Settings pass through unchanged. An adapter invocation can be
rejected before HTTP dispatch, so attempt count is not a billed-call count;
the native usage ledger remains authoritative.

## Leaf-memory container variant (2026-10-28)

Read-only source inspection covered 60 upstream source/workspace files with
staging hash `551903ad8cadeab20af1631a4576c57338266a41ec36124c6647a174b2379c80`.
`manifest-leaf-memory.json` selects the new path through
`bridge.config.retrieval_mode=leaf_memory`; `manifest.json` and its default
ingest/build/FullPath behaviour are unchanged. These are separate runtime
variants, not an in-place conversion of an existing source database.

`manifest-leaf-memory-leaf-embed.json` adds the `leaf-memory-embed-leaf-v1` variant with explicit `LEAF_MEMORY_EMBED_INPUT=leaf`: vector points embed only L0 leaf text; retrieval, Jev input, returned L0–LN layers and the 200000 memory/generation-context limits are unchanged from `manifest-leaf-memory.json`; staging stamps its source snapshot along with every other `manifest*.json`.

`INDEX_REPRESENTATIONS=leaf_memory` is explicitly supported by upstream
`docs/models.md`; Body/FullPath indexes are unnecessary for this variant.
`JEV_PROMPT_PROFILE=leaf-memory-reference-v1` selects native ChoiceThreeClass
(`useful`, `not_useful`, `uncertain`). Admission uses native `useful` with
`JEV_SELECTION_POLICY=absolute-threshold` and `JEV_THRESHOLD=0.5` from the
current upstream `.env.example`. `JEV_CLASS_SUM_TOLERANCE=0.02` is the current
upstream default for rounded provider distributions. No probabilities are
normalised, reconstructed from binary output, or tuned to benchmark answers.
Upstream reports these probabilities as Uncalibrated.

Unlike the initial task background, upstream `docs/implementation.md` now
contains Step 10's small choice probe and one-project development B/C runs.
It also records a three-class distribution summing to 0.99 and the subsequent
tolerance revision. These checks are not a BEAM-scale validation or threshold
calibration; the tolerance revision was not re-evaluated in those runs. We did
not run any real SDK or model call, compile Rust on the host, or launch this
new variant.

### Write and source contract

The adapter uses only public bridge events. The scope key comprises owner
plus `session_id`; without it, `turn_group` (top-level or public attributes)
is used; without either, the event ID gives an independent scope. The scope
kind distinguishes these cases and external_id hashes the owner/group pair.
No category, benchmark name, gold, or future event enters the mapping. Event
sequence is first-seen public order, retained on replacement; role and
timezone-qualified event timestamp are passed as EventInput metadata (UTC
microseconds). An absent timestamp/speaker remains unknown.

Same-scope new events in one ingest batch use `append_events` together, with
sequential scope publication and WaitMode::Ready. A changed, previously Ready
event uses `replace_event` with its exact previous SourceRef.version; changing
its owner or scope is rejected. Operation keys hash the complete scope/events/
expected-version request, so an Unknown batch can be replayed unchanged.
Already Ready unchanged events are not appended again. A failed or Unknown
event still prevents ready/retrieve on incomplete history. Scope batches are
acknowledged together; partial source-lookup failures do not silently create a
different append batch on the next replay. Native retries/budgets remain in
force; no new adapter retry loop is introduced.

Every event is mapped with `event_source`, not by reusing a batch's first
SourceRef. The native host returns per-event build results in input order and
keeps the existing build/search trace envelopes. Multiple events can therefore
reference the same scope operation/snapshot; diagnostic snapshots are not
independent model calls. Scope publication is sequential (`ingest_concurrency`
1), while upstream model concurrency remains unchanged.

### Retrieval, rendering and trace

Search uses Representation::LeafMemory, SelectionMode::Jev, and
FailurePolicy::Strict. Native code restricts candidates to complete L0
memories. Each selected SearchItem.memory is rendered by the **upstream**
`algorithms::render_leaf_memories` with the `leaf-memory-context-v1` header and
`{"memories":[<info/current layer array>]}` body, preserving citation metadata
and ContextFallback semantics. The external host adds `reader_text` and
`reader_sources`; existing native SearchResult fields are retained. Python
uses this exact renderer text, never the source event or legacy path as a
replacement. It applies the existing top_k/Unicode budget to whole items.
Shared ancestors and the format header can repeat across returned items, so
the adapter character budget may admit fewer items than native packed counts.

`reader_sources` is the complete union of ContextView.memories layer references
resolved through VisibleBody.source_closure, including ancestor/background
dependencies. Missing mappings or sources outside the public owner allowlist
fail; Python also rejects missing/unknown/duplicate source references rather
than converting them to an untraceable item. Read-only query `scope`/`as_of`,
native_answer, typed_context, feedback settlement and MAM learning remain
unsupported and disclosed. Dump is still the accepted public event text, not
a full internal graph export.

Raw search trace keeps `packed`, `items[].memory`, `items[].jev_outcome`,
`context_view.memories`, and `jev_trace[].answers/output_format` with native
three-class distributions. Existing case-review extraction remains readable;
its legacy binary-probability field stays null for three-class batches rather
than inventing a binary result. A richer page projection of these new raw
fields is outside this adapter change. Trace reads/logging remain best-effort
and can add overhead within the existing deadline.

### Static API check (no host Rust build)

Paths below are relative to upstream `crates/` at the inspected snapshot.

| Public API/type | Source | Host usage |
|---|---|---|
| LiveEnvironment::open | adapters/src/environment.rs:134 | Existing namespace/config loader; configured index representations at :181 |
| CogletNet | cogletnet/src/engine/mod.rs:24 | Public engine; events/retrieval impl modules at :14/:18 |
| ScopeKey / EventInput / UtcMicros | cogletnet/src/types.rs:69/:127/:216 | Public session identity, stable event order/role/time and MemoryInput |
| append_events / AppendOptions | cogletnet/src/engine/events.rs:186; types.rs:1393 | Scope + Vec<EventInput> + operation key/Ready |
| replace_event / ReplaceEventOptions | cogletnet/src/engine/events.rs:196; types.rs:1398 | Same event ID + exact expected source version/Ready |
| event_source | cogletnet/src/engine/events.rs:168 | Per-event exact SourceRef after scope publication |
| search / SearchOptions | cogletnet/src/engine/retrieval.rs:32; types.rs:1471 | LeafMemory, Jev, Strict, public source allowlist, trace |
| SearchItem / ContextMemory / VisibleBody::source_closure | cogletnet/src/types.rs:1502/:1518/:1549 | Memory layers, complete source dependency closure |
| render_leaf_memories | cogletnet/src/algorithms.rs:532 | One complete selected Memory per reader item; public export in lib.rs:7 |
| hash / OperationKey::new | cogletnet/src/types.rs:55/:201 | Bounded deterministic idempotency keys |

### Staging and offline evidence

Staging now stamps the same upstream source hash into **all** root
`manifest*.json` variants without modifying the source manifests. Project,
bridge and external-host files use shutil.copy instead of copy2, preserving
permissions but not source file mtime (F93). No upstream .env/data/build output
is staged. Tests cover all variants, unchanged input bytes/mtime, new copied
file mtimes and executable mode.

Offline commands: `python3 -B suts/cogletnet/test_adapter.py` (25 tests),
`python3 -B scripts/test_stage_cogletnet.py` (2 tests),
`python3 -B scripts/test_build_case_review.py` (27 tests), and
`python3 -B scripts/test_build_beam_review.py` (16 tests). All pass; the native
host is signature-reviewed only, so container compilation, full-size scope
rebuild cost, embedding/JEV input bounds, real three-class provider behaviour,
and long trace/frame limits remain real-run verification boundaries.

### Write waiting and recovery (F94–F96, 2026-10-29, LeafMemory only)

The scope host now uses `WaitMode::Scheduled` for `append_events` and
`replace_event`, then polls `operation_status` with 1/2/4/5-second bounded
intervals. `MemoryWriteResult.operation` is retained when a wait window ends:
per-event replies are `Pending` with that operation ID, not an identity-less
`OPERATION_OUTCOME_UNKNOWN`. `wait_operation` continues the same operation and
returns the existing Ready per-event/source/write shape, or the operation's
structured error. Diagnostic trace keeps every existing field and adds
`resume_count` and `wait_windows` (method/window_s/backoff_s/resume_count).

Upstream `environment.rs:363` defaults to `scheduler: None`; Scheduled would
otherwise fail `BACKGROUND_SCHEDULER_REQUIRED` (`engine/events.rs:232–238`).
Only the LeafMemory open path installs an external-host queue implementing
`HostScheduler` (`contracts.rs:371`). A Tokio worker calls `build(operation)`;
window expiry does not cancel that worker. The implementation uses the trait's
boxed-future ABI with std and existing Tokio, without adding async-trait or
another dependency. The legacy open/ingest/build paths and manifest are unchanged.

The bridge permits accepted-but-not-yet-visible history: its ingest contract
checks accepted+filtered+failed and a unique handle, while `ready` is a separate
barrier (`suts/_bridge/python/memeval_bridge/__init__.py:68–85`;
`internal/protocol/types.go:102–117`). Pending inputs remain in the adapter;
following ingest calls first continue earlier operations before submitting new
scope revisions. Requests not yet dispatched are explicitly tracked rather
than claimed Ready. `ready` returns visible only when all accepted inputs are
Ready, bounded by the supplied RFC3339 deadline and the per-RPC time limit
(with response/trace headroom). Retrieval remains blocked on partial history.

Upstream adoption does **not** require the prior revision to be Ready:
`engine/events.rs:483–584` appends a new immutable ScopeRevision. Publication
can mark an older operation Superseded (`engine/build.rs:783–784`). This adapter
therefore serializes publication deliberately, avoiding append/replacement
supersession races. `operation_status` returns `Operation` with state/error
(`engine/mod.rs:201`, `types.rs:1344`); build failures persist the full Error and
release the lease (`engine/build.rs:278–294`).

Adapter-specific settings are in the manifest's existing **bridge.config**,
which the framework passes to the Python Adapter (not new generic schema fields):
`operation_resume_max_attempts` (default 4), `operation_resume_initial_s`
(default 30), and `operation_resume_max_wait_s` (default 300). The default
backoffs are 30 + 60 + 120 + 240 seconds, covering multi-minute gateway bursts
within the existing call window. Resume waits use the larger of exponential
backoff and the maximum Failed-event `model_failure.retry_after_ms` in the same
operation (converted to seconds plus 2 seconds), capped by the configured maximum
wait and remaining window minus 11 seconds; a hint plus headroom that cannot fit
fails immediately with `BATCH_NOT_READY`, and `wait_windows.retry_after_s` records
the unpadded hint in seconds (null when absent). Recovery is per
operation across ingest/ready calls, uses bounded exponential backoff, and calls
upstream `resume` only for recoverable errors; explicit retryable=false and
deterministic failures are not retried. Scheduled resume runs in a background
task, waits within the current window, and retains the operation/snapshot
identity when the wait expires. There are no adapter-level model retries.
Once the recovery count is spent, further ingest calls do not reset it; the
adapter immediately reports `BATCH_NOT_READY` with operation IDs and retained
native errors when the first outstanding operation is Failed and no Pending
or NotDispatched work remains; it does not idle until the deadline. Otherwise
the ready barrier continues enforcing its deadline. Deterministic failures fail
immediately. LeafMemory's episode lifetime is 14400 seconds; legacy stays 3600.
No environment-variable reading point was added.

Important verified upstream limit: `resume` delegates to build
(`engine/build.rs:19–20`), and model attempts remain persistent across resumes
(`engine/model_calls.rs:20–70`). A logical model call already at max_attempts
can immediately return MODEL_RECOVERY_EXHAUSTED again. This integration cannot
promise recovery of such a call and does not reset its ledger, identity or
policy. The offline fake-native tests exercise both recoverable completion and
bounded failure; no native compilation or live model verification was performed.

The run script classifies OPERATION_OUTCOME_UNKNOWN as retryable. A segment with
`process_exited(143)` in rpc-errors and no native error kind is separately counted
as `host_timeout` and stops automatic replay under the same execution limit.
Historical model-call failures do not mask a later host timeout. Mixed runs
containing a host-timeout segment also stop, avoiding automatic re-execution of
that segment.

### Abstractive LeafMemory variant (2026-11-01)

`manifest-leaf-memory-abstractive.json` adds
`native-leaf-memory-abstractive-jev3-openai-embedding`. It explicitly selects
whole-array embedding with `LEAF_MEMORY_EMBED_INPUT=memory`,
`SUMMARY_PROMPT_PROFILE=abstractive-ratio-v1`,
`SUMMARY_LENGTH_BAND=ratio-v1`, and `SUMMARY_CANDIDATE_POLICY=model-only`.
The requested profile targets 25–30% per level with an accepted band of 20–35%;
`SUMMARY_RATIO`, `SUMMARY_RATIO_MIN`, `SUMMARY_RATIO_MAX`, and
`SUMMARY_MIN_TOKENS` are not pinned here and follow the upstream defaults.
No existing manifest, retrieval policy, budget, or reasoning-effort setting is
changed. The earlier separate-model notes describe the previous variants.

This variant omits `MEMORY_SUMMARY_MODEL` and
`MEMORY_SUMMARY_VALIDATION_MODEL`: the intended upstream resolution is summary
model from `HOST_GENERATION_MODEL` (injected through `{llm.model}`), then
validation model from that effective summary model. Adapter `describe` follows
this resolution while preserving explicit per-role overrides in older variants.
It reports the raw optional `summary_length_band`, `leaf_memory_embed_input`,
and `summary_candidate_policy` values; an absent value is null, not a guessed
upstream default. Embedding and Jev services remain separate from these models.

The upstream work coordinating this variant confirms these knob names but was
still in progress at offline integration time; the inspected environment loader
still required `MEMORY_SUMMARY_MODEL`. This is forward integration, not evidence
of upstream completion or a successful native/model run. No snapshot is invented:
`stage_cogletnet.py` stamps every `manifest*.json` with the actual staged source
hash. The existing run-name rule selects
`cogletnet-v6-leaf-memory-abstractive-beam100k-400`, separate from older variants.

### Abstractive v2 (P19 follow-up)

`abstractive-ratio-v1` above was used in the P19 failed run and remains the
historical recipe for that evidence. The v2 update of
`manifest-leaf-memory-abstractive.json` selected `abstractive-ratio-v2`, delivered
by upstream at revision `7d1c093` (as reported by its maintainer). Only the
profile environment value and the explanatory notes change; embedding,
length-band selection, model inheritance, efforts and budgets stay unchanged.

Read-only source verification: `crates/adapters/src/environment.rs:253–264`
selects `SummaryRecoveryPolicy::SoftV2` from the v2 profile; it is not a separate
`SUMMARY_RECOVERY` environment switch. The v2 defaults are
`SUMMARY_COPY_MAX_TOKENS=64` (`environment.rs:118`) and
`SUMMARY_MIN_TOKENS=32` (`environment.rs:97`). Neither is pinned in this
manifest. `describe.config.summary_copy_max_tokens` exposes the supplied env
string or null; it does not pretend an absent override is an observed runtime
value or introduce a recovery switch absent from upstream.

The v2 recipe retries length rejections in shorten/expand modes. SoftV2 may
accept a faithful candidate outside the soft length band, or fall back to an
exact copy / bounded prefix under the hard limits; the documented final prefix
exception retains an actual negative validation verdict rather than inventing
faithfulness. Transport, identity, protocol and budget errors still fail.
These are upstream semantics described in `docs/models.md`, not a claim of
successful model execution in this offline integration. Staging continues to
stamp the actual source snapshot; real container verification was still pending in this record.

### Abstractive v3 (P19 follow-up)

The v2 recipe was used in the subsequently aborted run; that run's recipe and
evidence remain historical. The current `manifest-leaf-memory-abstractive.json`
selects `abstractive-ratio-v3`. Only `SUMMARY_PROMPT_PROFILE` and notes change;
all other env values remain identical, and `SUMMARY_LANGUAGE_GUARD` is not
pinned. The upstream maintainer reports the v3 series beginning with `9821e1e`,
with final wording still being completed at this integration. Staging records
the actual source snapshot rather than treating that partial revision as a
final immutable v3 hash.

Read-only verification of `crates/adapters/src/environment.rs:96–110` and
`docs/models.md`: v3 defaults to `SUMMARY_LANGUAGE_GUARD=script-v1`, with `off`
available as an override. Script-aware instructions select Chinese or English
prompt text using the source's deterministic Han/Other script classification;
this is not a general natural-language detector. The model language guard
rejects mismatched model-candidate script classes while preserving the existing
SoftV2 behaviour for exact copies and source prefixes. Adapter
`describe.config.summary_language_guard` reports only the env string or null,
including an explicit `off`; it does not fabricate an observed effective
default. COPY/MIN remain unpinned upstream defaults. Offline fake-native tests
verify this wiring; real container/model acceptance was still pending in this record.

### P20: upstream 10f356d, ten-percent defaults and a two-layer variant (2026-11-02)

The upstream maintainer identifies revision `10f356d` as
`feat: target ten-percent summaries and cap hierarchy levels`. Read-only checks
of the current upstream source confirm the following; the earlier P19 ratio,
COPY/MIN and recovery notes above describe those earlier snapshots, not the
defaults of this revision. Staging still records the actual source SHA256.

- `../cogletnet/.env.example:35–43,53` declares `SUMMARY_LENGTH_BAND=ratio-v1`,
  `SUMMARY_RATIO=0.10`, `SUMMARY_RATIO_MIN=0.07`, `SUMMARY_RATIO_MAX=0.13`,
  `SUMMARY_MIN_TOKENS=16`, `SUMMARY_COPY_MAX_TOKENS=32`, and `MAX_LEVEL=63`.
  `crates/adapters/src/environment.rs:90–94,114–125` selects the 0.07/0.13/16
  defaults only for the soft v2/v3 profiles; non-soft defaults remain
  0.20/0.35/24. `environment.rs:136–153` gives soft profiles copy threshold 32;
  `environment.rs:155–163` gives them ratio 0.10 (non-soft: 0.275 with a band,
  otherwise 0.30). Explicit environment overrides still win.
- `environment.rs:286–295` reads `MAX_LEVEL` as the highest layer index and
  computes `config.max_levels = MAX_LEVEL + 1` using checked addition, with at
  least two layers required. Default 63 permits L0–L63; zero, negative and
  overflowing values are configuration errors. `docs/models.md:134–138` and
  `docs/implementation.md:616–617` specify that the highest nonempty layer
  retains all its nodes as parentless roots, makes no summary/validation calls
  for that layer, and creates no higher-layer pending group. Other node/input/
  model budgets can still fail. Nondefault caps change build identity and need
  explicit reindexing, not resumption of an old operation under a new cap.
- `docs/models.md:115–119` and `docs/implementation.md:615` distinguish the
  roughly 10% target from a compression guarantee: short copies, minimum token
  limits and SoftV2 fallbacks can lie outside the regular 7%–13% band. The v3
  prompt still uses the lower bound for its word/character target; the ratio
  remains part of internal accounting and recipe identity.

`manifest-leaf-memory-abstractive.json` changes notes only. It still selects
ratio-v1, abstractive-ratio-v3, model-only and whole-memory embedding, without
pinning RATIO/MIN/MAX/COPY token lengths or MAX_LEVEL. Its effective defaults
therefore come from the staged upstream revision. The new
`manifest-leaf-memory-abstractive-lv1.json` differs only in variant, explanatory
notes and `process.env.MAX_LEVEL=1`: L0 leaves plus L1 summaries, with L1 roots
receiving no further summaries or validation. Models, efforts, retrieval,
embedding, budgets, sidecars and credentials are otherwise identical.

Adapter describe adds raw optional `max_level`, `summary_ratio`,
`summary_ratio_min`, `summary_ratio_max`, and `summary_min_tokens`. Values are
the supplied environment strings or null; null means upstream default, not an
observed effective value. `scripts/stage_cogletnet.py:54–57` stamps every
`manifest*.json`, including the new variant. Offline tests use fake native and
synthetic staging inputs; no upstream build or model execution is claimed.

#### Validation recovery delivered with upstream 4421909

This recovery revision, identified as `4421909` by the upstream maintainer, was
not previously recorded here. The current source and C15 implementation notes
confirm the behaviour (`../cogletnet/docs/implementation.md:594–601`; that
history names `006013b` as the implementation commit):

- After strict JSON failure, a deterministic top-level scanner can recover a
  unique `faithful: boolean` when `reason` has invalid escapes or is truncated.
  Duplicate/ambiguous keys, nested or string-contained lookalikes and malformed
  boolean tokens are rejected. It preserves the actual verdict and sets
  `lenient_parse=true`; it does not guess faithfulness from arbitrary text.
- `SUMMARY_VALIDATION_RETRIES` defaults to 1, with 0 disabling format re-asks
  (`crates/cogletnet/src/types.rs:1159–1161` and
  `crates/adapters/src/environment.rs:298–299`). Only marked validation-body
  parse failures trigger this bounded re-ask, distinct from transport retries.
  Envelope, model-identity, finish, transport and budget errors still fail.
  Persisted request identities/budgets survive resume and do not reset.
- Exhausted format re-asks produce `ValidationUnavailable` and
  `validation_error=protocol`. Strict recovery fails; SoftV2 may continue to
  other candidates or a hard-limit-checked literal prefix. A prefix accepted
  without a parseable verdict has missing validation, never an invented false
  verdict. `summary_usage`/`cogletnet_usage` expose `validation_lenient`,
  `validation_retried`, and `validation_unavailable` (exhausted candidates,
  including prefixes; it may overlap degraded counts), derived from durable
  records with zero values omitted. These mechanisms do not guarantee model
  availability or successful real evaluation.

### Tree cross-layer retrieval and v4 recording-time summaries (2026-11-03)

The upstream maintainer identifies this contract as HEAD `6056022`
(`10f356d..6056022`). Static checks against the read-only upstream source:

- `Representation::Tree` is query-only; `SearchOptions.representation` selects
  it while `INDEX_REPRESENTATIONS=body` builds every level's Body vectors
  (`crates/cogletnet/src/types.rs:1664–1699,1872–1899`). Tree uses one cross-layer
  top-k (`leaf_k`), no layer quotas, ancestor expansion or evidence descent.
  It requires `node-reference-v1` / NodeV1 / ChoiceThreeClass for Jev
  (`crates/adapters/src/jev.rs:32–77`;
  `crates/cogletnet/src/engine/retrieval.rs:362–395,424–440`). Packing preserves
  vector rank and similarity utility; `settings.vector_representation=Body`
  and `upper_k=0`. Existing LeafMemory-only snapshots must be rebuilt.
- `SearchItem.text` is the selected node's own body, with `memory=None` and a
  one-node path. `ContextView.nodes` is a `Vec<ContextNode>` aligned with items
  (`types.rs:1903–1920,1958–1982`; `engine/retrieval.rs:1098–1104,1177–1220`).
  Each context node carries its reference, representative source, level,
  leaf_range and full direct leaf-source coverage. The external host checks
  alignment/allowlist, deduplicates source IDs preserving first appearance,
  and emits `reader_text=item.text`, `reader_sources`, `level`, `recorded`.
  Raw result/trace retains `context_view.nodes` unchanged. Python maps all
  these direct sources to public event IDs (F113), checking the representative
  source is included and every source belongs to the requesting owner.
  LeafMemory keeps its own-L0 citation rule and complete-memory rendering.
- The typed `ContextNode.recorded` is null or `{start,end}` with integer UTC
  microseconds (`types.rs:214–216,340–345,1965`), **not** a date-string array.
  Only the upstream `node-context-v1` renderer converts it to RFC3339 arrays
  (`algorithms.rs:543` onward). The host serializes the typed field verbatim.
  Coverage and known recording windows come from `ProjectionContext`
  (`projection.rs:283–314`); missing timestamps remain unknown.
- Per t4's confirmed integration decision, Tree reuses LeafMemory's explicit
  owner/session scopes, append_events/replace_event, sequential Scheduled
  writes, bounded wait/resume and per-batch diagnostic cache. It does not use
  legacy per-event writes. Public event `timestamp` maps to `occurred_at` as
  message recording time, never a date inferred from content or gold.
- `SUMMARY_PROMPT_PROFILE=abstractive-ratio-v4` adds temporal preservation to
  the v3 rules and supplies `recorded_between` to the model. With
  `SUMMARY_RECORD_TIME=recorded-v1`, upstream adds `[recorded ...]` summary
  labels from covered leaves' known recording timestamps; these are not event
  occurrence dates. SoftV2, ratio/band defaults and hierarchy limits otherwise
  continue from the staged revision (`crates/adapters/src/environment.rs:85–192`;
  `crates/adapters/src/http/summary_v4.rs:7–11,35,170–173`;
  `docs/models.md:206–212`).

`manifest-tree.json` derives from the abstractive variant with only variant,
notes, declared LLM description, retrieval mode and the four specified
INDEX/JEV/SUMMARY controls changed; `LEAF_MEMORY_EMBED_INPUT` is removed.
Models, efforts, selection threshold/policy, budgets, sidecars and credentials
are unchanged. `stage_cogletnet.py` stamps the new manifest through its existing
`manifest*.json` loop. Both p17 and p19 naming rules already derive `-tree-`
from the filename, with no LeafMemory-only runtime assertions; launcher code
is unchanged. Fake-native, synthetic staging and naming tests are offline;
the Rust host has only been statically checked, not compiled on the host.

TODO(t7): the trace page builder does not yet support Tree's node-context
shape; add that separately before presenting Tree work traces in the viewer.
