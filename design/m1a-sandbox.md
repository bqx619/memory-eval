# Retired host-isolation design

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `879a86a730848682aabde17e64187288c072dda50329c825360a01544d48bb22`.

## Purpose and outcome

This design introduced manifest-driven launching, a clean environment, JSON-RPC transport, selftests, and cross-platform isolation experiments. It tested whether external adapters could be evaluated without granting unrestricted host access.

## Interpretation and limits

P8 replaced this model with Linux containers. Old host installation commands and process-isolation profiles are no longer supported.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
