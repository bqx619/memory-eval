# Changelog

This is a capability-level history, not a count of research progress or a list of promised features. Start with [README](README.md) for current usage and the [historical index](design/history.md) for dated design and experiment summaries.

## Current working tree

Work after the recorded October 2026 commit snapshot includes per-case/BEAM review, attempt attribution, explicit SUT identity changes during resume, reader-prompt reconstruction, and distinct direct-source and citation-coverage diagnostics. Additional SUT variants are experimental subjects, not framework endorsements.

The documentation publication pass adopts MIT, preserves third-party notices, makes English the primary language, separates current contracts from historical summaries, and removes private deployment and coordination transcripts. It does not rewrite Git history or regenerate evaluation results.

## October 24–25, 2026

- Corrected probability-rounding boundaries and distinguished invalid judge output from valid small probabilities.
- Extended structured adapter errors, model usage, and provenance; added systems and cross-benchmark controls.
- Preserved incomplete coverage, failures, and stopped-run outcomes instead of presenting partial results as full evaluations.

Representative commits: `533f1d0c2d83`, `5d932b5cf151`, `6c0399cc6ac8`.

## October 20–21, 2026

- Allowed fixed readers to consume returned memory text while keeping sources/items and valid/missing/invalid references separate.
- Bound input mode to cassette and resume identity; added BEAM items with explicit budgets and unsupported checks.
- Corrected concurrency and HTTP-bridge deadlines; removed redundant metrics and per-episode cost detail from result cards.

Representative commits: `2fb26774f094`, `abf15e5a8e17`, `2248c662bf20`, `801ed2ace09b`.

## October 19, 2026

- Established image builds, Lock v3, manifest v3, sidecars, selftests, destruction, and residue verification.
- Added a per-run single-target governor with metering, backoff, circuit breaking, budgets, and parallel independent cases.
- Added recovery, data-flow disclosure, result cards, and a common metric glossary.

Representative commits: `d729390d84c6`, `68f4f08974d3`, `889323fa09ad`, `eaf538fd3927`, `4c820238f870`, `71418675d976`.

## October 18, 2026

- The initial commits imported accumulated research, the Go evaluator, adapters, benchmark protocols, and the Python prototype; they do not imply this work was created within seconds.
- Pinned upstream restoration and hash checks enabled self-contained tests without bulk data.
- Retired host-process sandboxing in favor of Linux containers and corrected permission, network, and lifecycle assumptions using probes.

Representative commits: `c92f8a13b7ec` through `f1687b9aac95`, `2a79be02e846`, `95c56157f837`, `a58ea2e2f28d`.

Numerical comparisons require the corresponding suite, protocol, identity, and coverage evidence; this changelog does not replace it.
