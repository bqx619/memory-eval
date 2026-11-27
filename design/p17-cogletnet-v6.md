# Hierarchy and representation experiment

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `f0db347bab9b0ad3593ff738ce71045b4a54345f04135408febed0927bdf1044`.

## Purpose and outcome

This batch compared a legacy retrieval chain with LeafMemory and controlled embedding variants. It exposed token-unit mismatch, repeated trace payloads, operation-window exhaustion, lost pending identities, timeout cleanup races, and source-attribution differences. Generic fixes added scheduled waits/resume, bounded retries, safe traces, and clean interruption publication.

## Interpretation and limits

The work viewer distinguishes event rows, operation snapshots, attempts, and representations. Direct-source evidence excludes ancestor-only provenance. New SUT identities in resume history do not relabel previously scored rows.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
