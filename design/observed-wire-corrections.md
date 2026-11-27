# Observed wire-format corrections

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

This reference records compatibility fixes derived from observed response shapes and checked with offline fixtures. P5 supersedes historical gated and emulated judge paths.

## OpenAI-compatible completion fallback

Prefer `max_completion_tokens` and `n=1`. Fall back to `max_tokens` only for a structured HTTP 400 `unsupported_parameter` whose `param` is `max_completion_tokens`. Remove temperature only for a structured HTTP 400 `unsupported_value` identifying `temperature`. Each adjustment occurs at most once and is separate from retry quota; free-form error messages are not sufficient evidence.

Record `temperature unavailable; provider default` on the rejected attempt, retry, response, replay, and applicable QA deviations. Preserve logical chat cassette compatibility while disclosing requested settings and actual adjustments. `usage.completion_tokens_details.reasoning_tokens` is part of completion tokens, not an additional billable count; absent data remains zero/omitted for legacy compatibility.

## TypeSafe mapping

Choice criteria contain the complete option-description object. Score criteria are an ordered list with exactly `Levels` descriptions. Noul sends type and instructions; nonempty criteria are rejected. Its scalar value is P(true), without synthetic probabilities or native confidence.

Calculated noul confidence is `abs(2*p-1)`; calculated choice confidence is `(pmax-1/n)/(1-1/n)`. A native-versus-calculated difference exists only when native confidence exists. No score-confidence formula is assumed.

Preserve score expectation as a float, zero-based probability keys, and legend. `decision-values-v2` records the raw expected index without rounding or normalization. Validate probability range, sum, keys, and legend; finite provider rounding does not require exact equality with a locally recomputed expectation. There is no P(returned noninteger score).

The mapping ID `memeval-typesafe-observed-v2` prevents reuse of cassettes encoded under invalid assumptions. Do not resolve aliases locally. P5 subsequently maps score probabilities by argmax for benchmark grading while retaining the expectation for audit.

## Verification scope

Counted fake services verify request changes, bounded fallback, ledger records, and replay without deleting prior cassettes. Historical test names include `TestChatCompatibilityRecordReplay` and `TestObservedWireGatedReportReplay`; the latter describes a retired route. These are offline compatibility checks, not fresh remote-service validation.
