# Retired sidecar and HTTP bridge design

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `90f14d871f5591587cf464dccc984d6420cb01e5c5152c4aa5477131b368b357`.

## Purpose and outcome

The design generalized dependencies into sidecars and added an HTTP bridge for service-based SUTs. A virtual-machine backend was evaluated alongside host execution.

## Interpretation and limits

Current sidecars use pinned container images; host binaries, Unix-socket assumptions, and virtual-machine instructions are obsolete.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
