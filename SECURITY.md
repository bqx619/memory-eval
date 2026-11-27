# Security and data boundaries

memeval runs the evaluator on the host and external SUTs in Linux containers. A read-only root filesystem, non-root user, restricted capabilities, network policy, selftests, and residue verification provide an evaluation boundary. They do not establish that arbitrary hostile code is safe on a shared machine.

## Configuration and egress

Credentials belong in private configuration and are injected only into declared processes. Users with access to the same Docker daemon may inspect container environments. Do not run credential-bearing evaluations on an untrusted shared daemon.

`none` restricts SUT runtime egress, not build downloads or evaluator-side reader/judge calls. `gateway` enforces a single target; `open` does not. Check the manifest, selftest, data-flow report, and actual model configuration together.

Redaction reduces accidental disclosure but does not replace review. Do not publish private configuration, env files, credential-bearing request headers, cassettes, complete model responses, or unreviewed traces.

## Reports and viewer data

Questions, source events, answers, prompts, and traces may contain personal information or licensed material. Publishing page source and publishing its data are separate decisions. Prefer synthetic examples or reviewed aggregates. Removing a working-tree file does not remove prior commits or released copies.

## Reporting a problem

For ordinary issues without sensitive details, use the project's public issue channel and include a minimal synthetic reproduction, version, and error category. For credentials or exploit details, first ask the maintainers for a private reporting channel; do not attach raw evaluation logs to a public issue. No dedicated security mailbox or response-time commitment is currently specified.

If credentials were exposed, revoke or rotate them before addressing history and release copies. Deletion or textual masking alone is insufficient.

## Cleanup

Use `env destroy` / `gc` and verify task-owned labels. Do not globally prune a shared Docker daemon or delete other projects' containers, volumes, images, or caches.
