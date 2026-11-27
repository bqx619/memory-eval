# Durable episode logs

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `0b4d8224acc13375d57848f71feea5e8a99a535dcb8f1395d3876395d4a48911`.

## Purpose and outcome

The implementation stored per-episode process output, RPC failures, resource evidence, bounded tails, and an index. Log capacity and redaction were treated as part of the contract.

## Interpretation and limits

Logs may contain input or model text. They are private artifacts until separately reviewed.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
