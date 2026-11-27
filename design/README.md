# Design and methodology references

Start with [current evaluation contracts](current-contracts.md). Design documents explain fields and tradeoffs; current Go types, schemas, and contract tests determine behavior. Historical task status or execution instructions do not authorize new work.

All SUTs follow one contract. System-specific experiments and traces are case studies, not separate scoring standards.

## Entry points

- [Project overview](../README.md) and [user guide](../docs-site/index.html)
- [Data](../data/README.md), [protocols](../protocols/README.md), and [adapters](../suts/README.md)
- [Current contracts](current-contracts.md), [change history](../CHANGELOG.md), and [historical records](history.md)
- [Publication checklist](publication.md) and [third-party notices](../THIRD_PARTY_NOTICES.md)

## Reference designs

- [dataset-protocol.md](dataset-protocol.md)
- [findings/mab-chunker-deviation-2026-10.md](findings/mab-chunker-deviation-2026-10.md)
- [findings/octop-lexical-longmemeval.md](findings/octop-lexical-longmemeval.md)
- [m0-contract.md](m0-contract.md)
- [m2a-longmemeval.md](m2a-longmemeval.md)
- [m2b-evaluator-llm.md](m2b-evaluator-llm.md)
- [m3-decision-client.md](m3-decision-client.md)
- [m3-upstream-facts.md](m3-upstream-facts.md)
- [m3b-judge-and-env.md](m3b-judge-and-env.md)
- [observed-wire-corrections.md](observed-wire-corrections.md)
- [p1-dataset-protocol-infra.md](p1-dataset-protocol-infra.md)
- [p10-results-site.md](p10-results-site.md)
- [p12-answer-input-contract.md](p12-answer-input-contract.md)
- [p15-case-review.md](p15-case-review.md)
- [p16-beam-review.md](p16-beam-review.md)
- [p18-embedding-baseline.md](p18-embedding-baseline.md)
- [p2-locomo-personamem.md](p2-locomo-personamem.md)
- [p2.5-suite-bundle.md](p2.5-suite-bundle.md)
- [p3-mab-beam-memora-halumem.md](p3-mab-beam-memora-halumem.md)
- [p4-prefeval-convomem.md](p4-prefeval-convomem.md)
- [p5-jev-only-judge.md](p5-jev-only-judge.md)
- [p8-container-platform.md](p8-container-platform.md)
- [p9-throughput-governor.md](p9-throughput-governor.md)

## Source and resource integrity

`design/upstream/*/SOURCES.md` (some use lowercase filenames) pins source revisions and file hashes. Required official templates must be restored and verified rather than removed as historical clutter. Preserve original licenses and fixture notices.

[Report resources](report-ui/README.md) are synchronized with their embedded copies. Do not delete template inputs, pinned image appendices, or prototype regression data during documentation cleanup. Specialized trace formats support explanation without changing the common scoring contract.
