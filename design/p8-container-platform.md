# P8: Container execution and verifiable cleanup

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

This design replaced the earlier host-process and microVM backends in October 2026. Third-party SUT installation and execution belong in Linux containers; the evaluator stays on the host. The implementation depends on the standard Docker CLI with an explicit context, not a runtime-specific API. In-process baselines remain separate. Historical host-sandbox designs are superseded.

## Execution boundary

The evaluator and daemon must resolve episode bind-mount paths on the same host. Never mount datasets, gold, evaluator credentials, the repository, or unrelated host configuration into an SUT. The threat model addresses state hygiene and information flow; containerization is not a claim of protection against arbitrary hostile code with daemon access. A user able to inspect Docker can see injected SUT environment values.

The backend implements `Sandbox{Name,Capabilities,Prepare,Spawn,Destroy}` and `CommandBackend.PrepareCommand`. A process exposes stdin/stdout/stderr, wait, and kill. `PrepareEpisode` creates the episode directory; external handles use a teardown hook so timeout cleanup removes containers before killing the attached CLI.

`docker --context <context>` is explicit on every call. Capabilities probe daemon availability, Linux platform/architecture, image availability, nonroot UID, dropped capabilities, no-new-privileges, read-only root, and writable episode mount. Failure is unavailable with a reason; the framework does not start a daemon automatically. UID mapping is observed and disclosed, not assumed portable.

## Lifecycle and launch template

Resources carry `memeval.run`, `memeval.episode`, `memeval.env`, `memeval.role`, `memeval.owner_pid`, and `memeval.owner_start` labels. Roles distinguish SUT, sidecar, init, probe, ready, and governor. Platform is explicit `linux/<arch>`.

Create SUT containers with read-only rootfs, `--cap-drop ALL`, `--security-opt no-new-privileges`, nonroot user (normally `65532:65532`), pids limit, configured memory/swap/CPU/shm limits, `--init`, interactive stdin, SIGTERM stop signal, and bounded stop timeout. Use no TTY and no published ports. Mount the episode at the same absolute path and set cwd/HOME/XDG inside it. `TMPDIR=/tmp` is a tmpfs with `rw,nosuid,nodev,mode=1777` and a size limit. The explicit mode matters for scratch images lacking a preexisting writable directory.

Secrets are expanded into a 0600 `env.list`, never argv, and that file is removed after startup. `Spawn` attaches with `docker start --attach --interactive`. Kill sends TERM, waits at most five seconds, removes forcibly if necessary, and terminates the CLI. Wait records container exit and `OOMKilled`.

Destroy is idempotent: remove SUT, reverse-order sidecars, episode network, and recorded anonymous volumes, then verify all episode-labeled resources and recorded volume IDs are absent before deleting the episode. Failure becomes `Unenforceable` and prevents claiming a clean scored run. Typed `container|network|volume|image inspect` is used; missing-resource handling accepts typed not-found errors and `no such object`. Scoped listing uses full IDs or validated prefixes of at least 12 characters.

`ProfileSHA256` binds the normalized launch template (excluding names, labels, and concrete episode path), image ID, sidecar digests, network mode, platform, and `container-run-v1`. Network mode is run configuration and must not unnecessarily change installation input identity.

## Networks and SUT model access

`none` uses no network, or an internal bridge when sidecars are needed. `open` permits general egress and must be disclosed as such. `gateway` attaches the SUT to an internal episode network and a single-target relay/governor to that network plus an egress network. Its guarantee is `relay_single_target`, not general Internet isolation by URL filtering. P9 replaces the original per-episode TCP relay with a per-run HTTP governor. The original relay supports an `http://` target; HTTPS requires a separately supported path or disclosed open mode. Selftest requires external TCP/UDP/DNS/HTTPS probes to fail while gateway connectivity succeeds, and records the configured single-target allowlist in private run evidence.

Backend configuration uses `MEMEVAL_DOCKER_CONTEXT`, `MEMEVAL_DOCKER_CLI`, `MEMEVAL_LINUX_BINARY`, `MEMEVAL_CONTAINER_TMPFS_SIZE` (original default 1g), `MEMEVAL_CONTAINER_SHM_SIZE` (original default 512m), and `MEMEVAL_EP_ROOT`. Values are environment-specific; the documentation does not prescribe a particular local context.

Framework gateway configuration uses `MEMEVAL_SUT_LLM_BASE_URL`, `MEMEVAL_SUT_LLM_API_KEY`, `MEMEVAL_SUT_LLM_MODEL`, and `MEMEVAL_SUT_LLM_REASONING_EFFORT`. CLI model/effort overrides precede manifest pins, then environment defaults. Manifest `llm` declares required/model/effort and variable templates using `{llm.base_url}`, `{llm.api_key}`, `{llm.model}`, `{llm.reasoning_effort}`. Template expansion adapts to each SUT's variable names. Evaluator reader/judge credentials remain separate. Endpoint capabilities such as embeddings must be verified for the configured provider rather than inferred from chat compatibility. Public documentation uses configurable endpoints, never deployment addresses.

Report actual SUT model, effort, configuration source, and data-flow parties. Credentials are redacted from logs, reports, and diagnostics. Gateway host fields in private run evidence must be sanitized before publication.

## Manifest and image locks

Manifest schema v3 uses `sandbox.backend: container|none`; retired host-sandbox tier/shell/IPC/host-executable/microVM controls are removed. Runtime bases are `uv-python`, `uv-python-trixie`, and `node`, pinned in Appendix A. `runtime.cache_links` maps an episode-relative path to an image-internal `/opt/memeval/env/...` path; host-created links may be dangling on the host but resolve in the container. Prefer supported cache environment variables. Prepare steps run as the nonroot runtime user and validate asset readability.

The framework generates Containerfiles. Staging contains project, bridge, assets, and declared locks, never arbitrary workspace files. Without a dependency lock, resolve it in a separate container build stage, export it, compute lock/environment identity, then build the final image. Python installs use hash-locked requirements; Node uses `npm ci`. Prepare downloads model assets at build time. Runtime rootfs protection is `--read-only`; recursive chmod is unnecessary and can duplicate image layers.

`runtime.indexes[{url,strategy}]` and `runtime.constraints` are explicit hashed inputs; platform-specific CPU wheels and constraints avoid accidentally introducing large GPU dependencies or version drift. Locks must match the Linux target. `--offline` for this build contract means no lock re-resolution and `--pull=false`; it does not guarantee that dependency-download steps never need networking. Do not silently claim a network-free build.

Lock v3 records `ImageRoot`, `Image{ID,Ref,Repository,Tag,SizeBytes,BaseImages,ContainerfileSHA256,ContextSHA256,BuiltAt,Platform}`, image-internal executable paths, target platform, and host platform. Host environment records contain the lock, generated Containerfile, context hashes, dependency locks, and build log. `CheckEnv` verifies input/inventory hashes, image presence, labels, and architecture. Reasons include `input_changed`, `image_missing`, `label_mismatch`, `files_changed`, `lock_version`, `lock_platform`, and `retired_host_env`. `env list` reconciles locks and labeled images, marking orphans.

## Sidecars

A sidecar declares name/kind, preset or image, port/user/tmpfs/env/secret-env/exports, readiness (`exec`, TCP, or HTTP), timeout, optional initialization, command, and stop signal. Generic services can reuse the SUT image; independent images start from their own environment instead of inheriting SUT PATH/PYTHONPATH/HOME. Common locale/time/temp defaults may be supplied. `{sidecar.<name>.host}` is its network alias; its port is internal only.

Readiness runs through `docker exec` or a probe on the container network, not host-side Unix sockets. A socket working under one VM filesystem does not establish portable cross-boundary socket behavior. Inspect confirms running state, no port publication, and membership only in the intended network. Logs are captured with bounded diagnostic tails and credential/ANSI sanitization. Evidence records image/digest/network/internal/no-published-ports and readiness timing; legacy PID can be zero with `audit="container-inspect"`.

Every declared image `VOLUME` must be covered by tmpfs or an approved mount. Record any anonymous-volume names after create and prove their deletion. `volumes_none` must pass.

## Selftest and reporting

The Linux evaluator probe is cross-compiled and mounted read-only; it is not baked into every environment image. Selftest uses the same image, network, flags, episode mount, and sidecars as the SUT. It checks gold/host-path absence, writable episode/tmp, unwritable root/environment, nonroot UID, dropped capabilities, no-new-privileges, expected mounts/environment, resource limits, egress expectations, sidecar connectivity, no published ports, and complete destruction. Docker's read-only init binary bind mount is allowed only with the matching launch template.

Selftest record version 3 binds manifest, lock/image, spec, capabilities, host/Linux binary hashes, and episode root; freshness is 24 hours. A changed image requires new evidence. Fake-Docker unit tests exercise these decisions offline; real daemon probes are a separate validation tier.

Report `isolation.tier="D"`, `backend="container/docker"`, profile hash and existing compatible keys, plus `container{run_id,runtime,image,sidecar_images,network,flags,user,user_mode,tmpfs,shm_size,limits_enforced,mounts,host_ports_published,residue,system_df}`. Effective configurations include container/network/image IDs and create/start/destroy timings. Runtime context/host details are private deployment facts to redact in distributed derivatives. Network enforcement must reflect the actual mode, not merely container existence.

## Cleanup ownership and verification

Per-run cleanup verifies run-labeled containers, networks, and volumes are absent, retries scoped removal once if needed, and reports `run_clean`. Kept episode directories and storage before/after are disclosed separately. `env destroy` removes only the selected environment's labeled resources, image, lock record, related selftests/gates; optional all-environment cleanup remains within framework ownership. Base-image removal is limited to Appendix A references no longer used by environment images.

`gc` defaults to dry-run and considers orphaned/expired framework-labeled resources, known probe images, eligible episode directories, stale selftests, and orphan locks. It must not touch other projects, valid locks, cassettes, or reports. Shared BuildKit cache has no reliable ownership label: no global `docker builder prune`, and no promise to erase unrelated shared cache. A dedicated builder is a separate explicit design choice. Retired host-environment cleanup is historical and must not publish personal paths as reusable commands.

## Verification and limits

`testdata/fake_docker.py` tests lifecycle ordering, label scope, network modes, flags, env-file deletion, secret-free argv, UID mapping, OOM/kill, idempotency, profile sensitivity, and residue failures. Appendix agreement tests bind public image references to source configuration. Real-container parity must use the same suite/seed/protocol as its reference: PMB-75 retrieval is distinct from the 77-case typed projection. Floating-point/model differences, runtime file-sharing behavior, image rebuilds, and provider variability require explicit attribution; documentation editing does not revalidate those runs.

## Appendix A: Pinned base and sidecar images

This table is consumed by `TestBaseImagesAppendixAgreement` and `TestPresetAppendixAgreement`. References and manifest-list digests are exact. The pins were recorded on 2026-10-18; supported target platforms are `linux/amd64` and `linux/arm64`. Use the index digest with an explicit platform, not an architecture-specific image digest. The Node index records `linux/amd64`, `linux/arm64/v8`, and `linux/ppc64le`; the Python and Debian entries record `linux/amd64` and `linux/arm64`.

| Purpose | Reference | Manifest-list digest |
|---|---|---|
| Python 3.12, Bookworm | `ghcr.io/astral-sh/uv:python3.12-bookworm-slim` | `sha256:e5b65587bce7de595f299855d7385fe7fca39b8a74baa261ba1b7147afa78e58` |
| Python 3.12, Trixie | `ghcr.io/astral-sh/uv:python3.12-trixie-slim` | `sha256:e28141cccc682844c7bed0cb7e5b2b4522c1f72f1d5116c7d22710d0d421f7dc` |
| Node 24.22.0 | `node:24.22.0-bookworm-slim` | `sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5` |
| Recorded binary base | `debian:bookworm-slim` | `sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587` |
| PostgreSQL/pgvector | `pgvector/pgvector:pg18` | `sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11` |
| Qdrant | `qdrant/qdrant:v1.19.2` | `sha256:b7b0444c4c351c970b98e90a6f89c2ee4287c65b44e52b4cb503fa5b2aa927ad` |

Trixie supplies glibc 2.41 for wheels requiring at least 2.39; Bookworm supplies 2.36. The recorded first requirement was `falkordblite==0.10.0` on aarch64.

`postgres-pgvector-18` uses port 5432, user `999:999`, 256 MB shm, tmpfs `/var/lib/postgresql` (4 GB, UID/GID 999), `/run/postgresql`, and `/tmp`. Cover the image's `/var/lib/postgresql` VOLUME; PostgreSQL 18 defaults PGDATA beneath `/var/lib/postgresql/18/docker`. Environment includes `POSTGRES_USER`, `POSTGRES_DB`, and `POSTGRES_HOST_AUTH_METHOD=trust` within the isolated network. Readiness is `pg_isready -U <user> -d <db> -h 127.0.0.1`. Recorded versions were PostgreSQL 18.6 and pgvector 0.8.7, differing from the older 0.8.6 preset.

`qdrant-1.19.2` uses port 6333, user `65532:65532`, tmpfs storage (2 GB), snapshots, and tmp; disables telemetry with `QDRANT__TELEMETRY_DISABLED=true`; and probes HTTP `/readyz`. No host ports are published.

The local `memeval/probe:<sha12>` image is generated from scratch plus the Linux binary and a writable tmp mount. Its identity derives from the binary rather than a remote pin.
