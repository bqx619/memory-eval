# Container-only platform implementation

<!-- documentation-status: archive -->

This is a condensed English publication record, not the original execution transcript or a current runbook. Private host paths, service addresses, temporary workspaces, credentials, and coordination instructions are excluded. The source record has SHA-256 `dd957298e244c81be1584a9c5687ab8da3b564262791de9a92c796b3eb98159d`.

## Purpose and outcome

P8 removed host and virtual-machine execution backends. It introduced a Docker CLI backend, image builds, manifest/Lock v3, sidecar image presets, capability probes, selftests, gateway networking, and scoped cleanup. Real-daemon checks corrected truncated IDs, inherited labels, missing-object errors, scratch tmpfs permissions, sidecar environment inheritance, model-cache permissions, and build retries.

## Interpretation and limits

Prepared models run as the runtime UID; cache links target image-owned paths. Timeout or clean interruption is not an isolation breach. Actual residue or unknown enforcement still rejects scoring. Full contracts and pinned images are in p8-container-platform.md.

See the [current contracts](current-contracts.md) and [history index](history.md) for context. Historical acceptance evidence is not a claim that new live tests were executed during this documentation revision.
