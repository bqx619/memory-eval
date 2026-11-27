# M3: Decision client and repeated-sample statistics

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

This 2026-10-16 design introduced evaluator-side decision requests, replay, and accounting. It did not establish that a memory system improved when using Jev. P5 later removed the experimental OpenAI and emulated decision providers.

## TypeSafe request boundary

TypeSafe is a discriminative decision API, not a text-generating reader. The historical endpoint shape is `/v1/systemone` with bearer authentication and `{model,state,questions}`. Named questions have `type` (`noul`, `choice`, or `score`), `instructions`, and type-specific `criteria`. Responses preserve the returned model, answers, and usage. Documented error handling covers 400, 401, 413, 415, 422, 429, and 529; declared upstream limits must not be confused with locally verified capabilities. There is no asserted seed/temperature determinism. A server alias is not a pinned version.

The original response assumptions were corrected by [observed wire corrections](observed-wire-corrections.md): noul is a scalar probability; native score is a floating-point expected level, not a rounded integer.

## Replay and accounting

Decision identity includes API type, model, state, sorted question names, ordered options/levels, codec/template version, anonymized sample, and approved experimental headers. Only explicitly supported experimental headers belong in identity; credentials and cookies never do. Independent repeats use distinct sample IDs and live requests.

For noul probability p, calculated confidence is `abs(2*p-1)`. For n choices it is `(max_probability-1/n)/(1-1/n)`. Do not invent a score-confidence formula. Preserve native confidence separately and only compute a difference when both quantities exist. P5 defines the current fixed binary cutoff and argmax score mapping.

## Paired comparison

Pair by `(case_id, repeat_index)`, require matching repeat counts and coverage, average repeats within the original case, then cluster-bootstrap original cases. Repeats do not create additional independent clusters. A one-cluster dataset remains one cluster regardless of repeated calls and has no cluster confidence interval.

`flip_rate` describes disagreement among repeated labels at case level, not provider nondeterminism in general. Report `count`, `flip_rate`, `eligible_cases`, `flipped_cases`, `excluded_cases`, and `by_metric`. Decision-only reports are evaluator-only with no official benchmark score. Explicit sample pairing may disclose an allowed top-k difference; unrelated protocol differences still reject comparison.

Offline fixture tests establish codec, ledger, identity, and statistics behavior only.
