# HaluMem JSON parsing compatibility

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `5b8778f027c5c41101a75aca2d45cf4a7c31691de3c1e56f24d2cf9f485aa066`.

## Purpose and outcome

The evaluator added a disclosed balanced-object fallback for unfenced JSON, deviating from the pinned HaluMem parser's strict fenced-JSON requirement. Fidelity remains partial; field validation and scoring formulas were unchanged. Parsing fallback and disclosure were tested separately from model accuracy.

## Interpretation and limits

Malformed or semantically invalid output must still remain invalid; parsing tolerance is not a score override.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
