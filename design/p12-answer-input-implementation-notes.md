# Answer-input contract implementation

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `86766ab92a81d0b05c6bd4df203f07ba7fe1eba3b89b1a62c3cabf789366f5ef`.

## Purpose and outcome

Sources mode expands legal event references; explicit items mode consumes returned Item.Text without source backfilling. MAB, LongMemEval, and BEAM supported paths share validation. Truly missing provenance is allowed only where specified; unknown, future, duplicate, and malformed references remain errors. Resume preflight and cassette identity separate input modes while retaining historical sources keys.

## Interpretation and limits

Mixed untraceable items cannot claim complete retrieval scores. BEAM ordering tasks are unsupported when the required source structure is absent. Tests covered fake SUT-to-reader requests, truncation, bridges, and record/replay isolation.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
