# M2b: Evaluator model calls, cassettes, and reader inputs

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

The original 2026-10-16 design separated evaluator calls from SUT calls. P8 subsequently defines the SUT gateway; P5 replaces the original chat-judge route with a TypeSafe decision judge. The reader, accounting, and replay boundaries below remain relevant.

## Client and identity

A completion request carries `Purpose` (`judge`, `reader`, or historical `decision`), `Provider`, `Model`, `Messages`, optional `Temperature` and `MaxTokens`, `N`, `Extra`, `SampleID`, and `TemplateID`. A response records `Text`, `FinishReason`, `Usage`, `SystemFingerprint`, `ModelReturned`, `LatencyMS`, `CassetteHit`, and `RequestSHA`. OpenAI-compatible chat completions and Anthropic messages use separate codecs and evaluator-only credentials.

Modes are `live`, `record`, `replay`, and `record-once`. Replay is strict: a miss never falls through to a paid request. Cassettes are stored beneath `<cassette-dir>/<provider>/<sha-prefix>/<sha>.json`. Identity includes provider endpoint, API type, model, normalized messages and sampling parameters, template/client versions, and anonymized sample identity. Responses retain original usage; a replay incurs zero new cost while preserving recorded cost.

`llm_ledger` records timestamp, run/sample, purpose, provider, requested/returned model, fingerprint, request SHA, hit status, prompt/completion tokens, original/new costs, latency, and errors. Missing prices produce null cost, not zero. Calls, tokens, costs, and hits are aggregated separately. Retryable transport, 429, and server errors have bounded exponential retries; each attempt is accounted for. Credentials and private endpoint details must not be emitted into public artifacts.

## Reader contract

The LongMemEval reader mirrors the pinned `run_generation.py` natural-language template, including History Chats, Current Date, and Question. `sources` resolves returned IDs to original observable events, grouped by session and ordered by time. `items` uses returned item text; session dates come from linked events where available and otherwise remain unknown. Gold and future events never enter the reader.

The historical reader default was 512 output tokens. Character-budget truncation removes complete oldest sessions and is disclosed as a deviation from upstream token truncation. `fullcontext` reads the permitted event prefix under the same budget. Report the actual model, `prompt_id`, `input_mode`, template hashes, truncation, question-set coverage, and deviations.

## Historical judge compatibility

The original chat judge used the five official LongMemEval templates plus abstention, `n=1`, temperature 0, and 10 output tokens; lowercased output containing `yes` counted as correct. Compatibility required the exact official model and template hash. This route is retired by [P5](p5-jev-only-judge.md). Current TypeSafe substitution must remain `judge_compatible=false`, with at most partial fidelity for model-judged official scores.

Model lifecycle information is timestamped configuration, not an evergreen assertion. Recorded responses may remain replayable after live availability changes. Agreement on identical reader outputs, including Cohen's kappa and per-type counts, measures judge agreement rather than semantic ground truth or memory-system quality.

## Validation

Counted fake-service tests exercise record/replay, strict misses, identity changes, accounting, error handling, and reader projections without network access. They do not validate current provider availability or real SUT performance.
