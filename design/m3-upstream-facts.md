# M3: Decision API observations and their limits

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

This reference separates original offline assumptions from later recorded response shapes. It does not claim a current remote API verification. P5 retires the former OpenAI-decision and emulated judge paths.

## Observed TypeSafe shapes

- Noul: `{"type":"noul","noul":0.99}` represents P(true); there is no fabricated `probabilities` or native confidence field.
- Choice criteria are an object mapping option keys to descriptions. A list produced a recorded 422. Responses carry the chosen string, confidence, and probabilities.
- Score criteria are an ordered description list, not an integer level count. Native `score` is a floating-point expectation (for example 3.9); probability keys are zero-based strings and the legend maps those keys to descriptions. Keep native values before any evaluator mapping.
- `jev-latest` historically resolved to `jev-1.13.0`; this is an observation, not a stable alias guarantee. Reproducible runs pin an explicit version.
- Recorded validation failures used FastAPI `detail` arrays with `type`, `loc`, `msg`, and `input`; authentication errors used a detail object with `error_type` and `message`. Error parsers must handle both without exposing credentials.

## Experimental routes that were removed

The OpenAI Decisions schema was never remotely verified. The former codec deliberately returned `OpenAIDecisionsMappingUnverified` without HTTP when no codec existed. Its fixture identifier, `httptest-only-openai-schema-v1`, was a test-only mapping. Emulated decisions used a project-owned prompt, not a verbatim upstream equivalent; strict JSON and probability validation did not establish semantic equivalence. P5 removes both routes.

## Shared client semantics

Decision requests reused the transport/accounting engine while preserving existing chat cassette keys. Identity includes question/option order, codec/template versions, and approved `TypeSafe-Danger-Zone-*` headers; Authorization and Cookie are excluded. Raw JSON means normalized JSON content, not a byte-for-byte wire capture.

The ledger records decision API type, input/output usage, costs, native/calculated confidence, and their difference where defined. Unknown pricing is null; replay adds zero new cost. Retryable 529 follows bounded server-error retry policy, and every attempt is recorded. Invalid responses retain available provider usage.

Repeated samples are identified by suite digest plus case/repeat identity. The `decision-values-v2` interpretation preserves raw native score expectation; it never invents a probability at a noninteger returned score. The repeated-statistics object contains `count`, `flip_rate`, `eligible_cases`, `flipped_cases`, `excluded_cases`, and `by_metric`; legacy numeric summaries remain readable.

## Recorded chat compatibility observations

Some OpenAI-compatible models rejected `max_tokens` in favor of `max_completion_tokens`, or rejected temperature 0. These require structured, bounded fallback and explicit deviations, as defined in [observed wire corrections](observed-wire-corrections.md). Reasoning tokens are a subset of completion tokens. A null system fingerprint is retained rather than synthesized.

Recorded fixtures and later observed shapes have different evidence strength. Neither is a claim that a replacement judge matches ground truth or that an SUT improves.
