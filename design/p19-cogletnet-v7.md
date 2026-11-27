# Abstractive-summary experiment

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `ab1392596f9592e8ca417425d80f3397f8fe3a9093905ab0190392547caf2e73`.

## Purpose and outcome

The experiment changed summary generation and validation while retaining common evaluation conditions. The first ratio recipe failed with SUMMARY_UNCOMPRESSIBLE; a later script-aware recipe concluded with 379 scored cases, 20 errors, and one invalid case. Failures and changed snapshots were retained rather than treated as one unchanged condition.

## Interpretation and limits

Compression, semantic faithfulness, evidence recall, answer quality, and usage are separate outcomes. Recovering from build failure does not establish answer-quality improvement.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
