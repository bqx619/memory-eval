# File-descriptor hygiene

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `ebc57a2b067749bb511bf599937a444c35ea366b383298e9d6dc947dad66674c`.

## Purpose and outcome

The launcher audited open file descriptors before starting a SUT. Diagnostic reporting and tests checked that unintended host descriptors were not inherited.

## Interpretation and limits

Current external execution is container-only. Historical host launch details are superseded.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
