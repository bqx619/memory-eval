# Resuming runs and HTTP backoff

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `c0c46dc17121daf9b38a9574dde1c770c994c1cc1ad19ca8f344615f46c0faf4`.

## Purpose and outcome

Resume preserved completed results and attempt history while retrying selected failures. HTTP 429 handling incorporated Retry-After and bounded backoff. Attempt costs were retained instead of being hidden by the final success.

## Interpretation and limits

Resume is not a license to mix input protocols or silently change SUT identities.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
