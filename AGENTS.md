# Agent collaboration rules

- Follow the current user's authorization. Prefer general mechanisms over dataset-specific or SUT-specific exceptions. All evaluated systems follow the same scoring contract.
- [CONTRIBUTING.md](CONTRIBUTING.md) is the single source for contribution and authorship conventions. Codex does not perform Git operations. Commits, publication, and external messages require explicit authorization.
- Work offline by default: no network, real Docker operations, unapproved dependencies, or changes to private configuration, run reports, or datasets. Authorized documentation work may edit its documentation targets, not underlying experimental evidence.
- Build and execute external SUTs only in Linux containers. Do not install third-party projects on the host. Use the fake Docker CLI for container unit tests; real evaluation requires separate authorization.
- Do not replace binaries used by running jobs, interrupt unrelated evaluations, or start new runs. If an active shell script must change, write a new file and atomically replace it. Keep scripts independent of personal hostnames and temporary paths; never print credentials.
- Limit cleanup to task-owned resources. Prefer `env destroy` and `gc`; do not prune a shared daemon or recursively delete an unresolved path.
- Current behavior is summarized in [design/current-contracts.md](design/current-contracts.md). Historical designs and operational records grant no new execution authority. Distinguish observations, inferences, and unverified claims.
- Preserve existing user edits. Validate the relevant behavior and report unperformed checks accurately. Test counts are not a substitute for acceptance.
- Preserve runtime inputs, test fixtures, source pins, and third-party notices even when they reside under docs or design.
