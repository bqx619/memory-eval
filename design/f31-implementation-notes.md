# Network probes and offline selftests

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `8fe11ab44a4e4e94722df5d133e821358efdda7ee72df48015b963b3a545b4d4`.

## Purpose and outcome

Local DNS and TLS fixtures tested the retired host sandbox's open/none policies, including macOS trust validation. Linux was cross-compiled only; public DNS, public PKI, revocation services, and real provider calls were not validated.

## Interpretation and limits

Network permission, successful DNS, TLS validation, and benchmark correctness are separate observations.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
