# Per-run model capability caching and judge-study references

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `ea10e5c70ff8ab21a6df8a02d3a82917bebcdd7465bb67b00ddedcad4b26be95`.

## Purpose and outcome

F9 cached supported temperature/token parameters per provider/model within one Engine, coordinating one in-flight capability probe without serializing all later requests. Logical cassette keys are calculated before negotiation; cold/warm caches and replay preserve identity. A 500-request fixture recorded 501 HTTP attempts (one rejected parameter probe), followed by 500 zero-cost replays. Authentication, throttling, and server failures are not cached as capability limits.

F10 added explicit or automatically discovered judge-agreement references. Selection validates suite SHA, reference provider/model, calibrated decision model, and real holdout evidence before evaluator calls. The study SHA is recorded. Referencing a study does not install its thresholds; threshold selection remains explicit. Missing cross-provider holdout evidence stays null rather than being replaced by whole-sample agreement.

## Interpretation and limits

Historical report values remain tied to their original protocol and framework version.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
