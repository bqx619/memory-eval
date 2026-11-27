# Throughput governance and resumable evaluation

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `7e3b4a542950bd1dfc7d449bdc6a73d0477fe10032abd706475253ca48022adb`.

## Purpose and outcome

P9 added a run-scoped single-target HTTP governor, per-attempt usage, bounded retries, budget stops, circuit breaking, and episode-level concurrency. Downstream cancellation was separated from upstream failure. Fresh-per-case suites schedule individual independent cases; incremental episodes remain ordered.

## Interpretation and limits

Effective concurrency is an explicit total override or per-episode fan-out multiplied by parallelism. Auto-resume is bounded, budget exhaustion requires user action, and request/input identities remain checked.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
