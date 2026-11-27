# hindsight adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest-gateway.json](manifest-gateway.json) | hindsight | llm-gateway | gateway | Not separately declared |
| [manifest-open.json](manifest-open.json) | hindsight | llm-gateway | gateway | Not separately declared |
| [manifest.json](manifest.json) | hindsight | none | none | Not separately declared |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

## Hindsight 1.0.0: initial integration (2026-10-18)

Based on `design/research/p6b-project-facts-2026-10.md` and P6b Appendix C, using `hindsight-all==1.0.0` and Python 3.12. Synthetic fixtures did not establish a successful real installation or smoke run.

- The initial service used the hindsight-api CLI directly, without embed/uvx, in an episode-local working directory to avoid discovering a repository `.env`. API and pg0 TCP endpoints were audited at readiness and around RPCs; non-loopback or unowned listeners rejected scoring. These host-era controls were later replaced by containers.
- The none variant sets `HINDSIGHT_API_LLM_PROVIDER=none`: chunk storage without LLM extraction, merging or reflect; embeddings/reranking are local. The original open variant called an OpenAI-compatible service directly with explicit HINDSIGHT_API_LLM_* variables and unmetered provider costs. Its installation probe temporarily used provider=none, recording only override keys; real runs did not override the provider. The gateway migration below supersedes this configuration.
- `routes.json` maps PUT bank; DELETE bank followed by recreation (only initial 404 tolerated); synchronous POST memories, checking success/items_count/async; document_id=event ID, string-valued metadata and original timestamp. Recall budgets are low for ≤100, mid for ≤300, otherwise high, followed by top_k truncation. These are budget bands, not precise upstream top-k. Scope/as_of filtering is not fabricated. GET memories/list is fully paginated for dump, with stable Dispatcher ordering.
- Prepare initially cached two HF models and their snapshot/file hashes and used the verified CLI to extract pg0 PostgreSQL assets. Runtime mapped installation assets read-only while keeping data in the episode, with `HF_HUB_OFFLINE=1` and `TRANSFORMERS_OFFLINE=1`. It did not guess an unverified Pg0 start/stop API: readiness and shutdown had to close both ports and yield postgres/initdb. Detached-process cleanup still required real verification.
- Offline contracts covered all REST routes, 205-item pagination, initial 404, synchronous counts, timestamps, budget boundaries, truncation, unknown document IDs, reset and prepared cache extraction. Exact model/pg0 bytes belonged to env.lock.json.

### Historical runtime verification (2026-10-18)

These observations explain the retired host-sandbox integration; they are not current host installation instructions.

- The uv console script used a `/bin/sh`/Python polyglot wrapper. The host sandbox rejected shell execution, so the service command invoked Python directly with `from hindsight_api.main import main; sys.exit(main())`, preserving host/port arguments. HOME overrides also failed the environment-control probe; the provided HOME already isolated pg0 and HF caches.
- The dependency resolution contained 213 packages (P6b Appendix C).
- Importing fastmcp triggered `mimetypes.init()` and system MIME-file reads; denied reads crashed startup. F39 introduced general system-data access and removed the manifest's temporary MIME-file exceptions.
- Pg0's PostgreSQL 18.1.0 linked macOS OpenSSL by absolute package-manager paths. Five starts failed with `dyld: Library not loaded`. The historical prepare path copied libssl/libcrypto into the installation, rewrote dependencies to `@loader_path` and re-signed them (F40).
- `initdb` and `pg_ctl start` invoke `/bin/sh`. Denial misleadingly reported that postgres was missing despite its presence. The old manifest allowed shell execution and disclosed it via `report.isolation.allow_shell` (F42).
- PostgreSQL uses a small System V shared-memory segment for data-directory interlocking even with other shared_memory_type settings; denial produced `shmget(size=56)`. The inspected binary also used semget/semop and POSIX `shm_open` with `PostgreSQL.%u`. The historical IPC declaration and `report.isolation.process_ipc` disclosed the same-UID host-global SysV namespace (F43).
- The compiled Unix-socket directory defaulted to `/tmp`, causing denied lock/socket writes. Hindsight `parse_pg0_url` maps URL query settings into Pg0 configuration, allowing `unix_socket_directories={ep}/tmp`.
- PostgreSQL 18 defaults to `io_method=worker`; denied cross-process SIGURG wakeups stalled connections at authentication, and even its timeout could not signal itself. Sync I/O bypassed the symptom, but the correction allowed signals within the sandbox (F44); the manifest did not change io_method.
- `Pg0.start()` returns `pg0 info` output, which uses `/bin/ps` to detect the postmaster. Denied execution returned running=false/uri=null while PostgreSQL was actually running, leading to `Database URL is required for migrations` (F45). macOS's setuid ps could not execute under sandbox-exec even with allow-default, so embedded pg0 was abandoned for this variant.
- The replacement was managed external PostgreSQL: preset `postgres-vector-18.6-0.8.6` from boomship postgres-lite v0.3.1 (PostgreSQL 18.6, pgvector 0.8.6, pg_trgm). Init used `--auth=trust -U hindsight`; only episode Unix sockets were reachable (`listen_addresses=`). Hindsight performed alembic migrations and `CREATE EXTENSION vector`. These facts are superseded by the container database configuration below.
- Real 1.0.0 RetainResponse serializes `is_async` as `async`; observed keys were async, bank_id, items_count, operation_id, operation_ids, success and usage. The adapter accepts either spelling but requires false for synchronous ingestion. The first real single-case probe retained 35 items in 437 ms with items_count=35.
- Blank recall queries returned HTTP 4xx: the first PMB-75 run scored 72 cases, with failures in edge-empty-query, edge-whitespace-query and narrator-voice-pinned-in-own-scope. Blank queries now return empty results without calling recall.

### Container variant (P8 K4, 2026-10-19)

- V3 manifests use `postgres-pgvector-18` (P8 Appendix A: PostgreSQL 18.6, pgvector 0.8.7). All database state is container tmpfs, overriding the image's `/var/lib/postgresql` VOLUME. Pg0 PostgreSQL is no longer installed or extracted.
- Generic episode-network addresses are `postgres:5432` and `postgresql://hindsight@postgres:5432/postgres`, with `POSTGRES_USER=hindsight`, `POSTGRES_DB=postgres` and trust confined to the episode network. After `pg_isready -U hindsight -d postgres -h 127.0.0.1`, initialization executes `CREATE EXTENSION IF NOT EXISTS vector` before API startup.
- The API uses the same environment image, listens on `0.0.0.0:8888` without publishing a host port, and is reached at `http://api:8888`. Readiness uses `/health`, timeout 180 seconds. None uses an internal network; the historical open filename already used the K2b gateway network. Real health/model startup timing remained pending in this record; fake SDK/Docker results were not project acceptance.
- Prepare now only downloads the two HF models and records snapshots/file hashes. Build HOME is `/opt/memeval/env/models-home`; runtime reads `{env}/models/hf`; the PyTorch CPU index remains. Old database startup, Homebrew library copies and re-signing branches were removed. REST mappings did not change.

### Gateway variants (K7a, 2026-10-19)

- HTTP bridges accept service DNS names such as `http://api:8888`, validating URL syntax without a DNS/public-address test. HTTPS, userinfo, query and fragment are rejected. `describe.config.bridge_base_url_host` exposes the actual host; matching manifest declarations enter the fingerprint. Limitations state that containers enforce the network boundary. Python and Node bridges follow the same rules.
- `manifest.json` keeps provider=none and local embeddings/reranking. The canonical gateway entry is `manifest-gateway.json`; `manifest-open.json` is a compatibility filename containing the same gateway configuration, not open networking.
- Source checks of 1.0.0 `config.py` and `engine/providers/openai_compatible_llm.py` confirmed `HINDSIGHT_API_LLM_PROVIDER/BASE_URL/API_KEY/MODEL/REASONING_EFFORT`. The OpenAI-compatible provider uses chat/completions and forwards effort for supported models, with possible SDK compatibility fallback. llm.env supplies these values without a fixed model. Missing required gateway model/endpoint/key fails; none installation probes do not require a model.
- K4 PlanServices passes the full SUT environment to generic sidecars without separate images, so the API receives expanded llm.env and database exports. `TestSidecarEnvironmentIsolation` verifies this with fake containers. The separate PostgreSQL image does not inherit SUT credentials.
- Embeddings remain `BAAI/bge-small-en-v1.5`; reranking remains `cross-encoder/ms-marco-MiniLM-L-6-v2`, both from build caches/HF_HOME. Linux CPU locks, constraints and the original manifests' runtime.constraints were preserved. Actual gateway/model acceptance was pending in this dated record.

### Describe disclosure (K7d, 2026-10-19)

Gateway variants with provider other than none return `HINDSIGHT_API_LLM_REASONING_EFFORT set; the SDK may adjust it per model; embeddings and reranker local` in llm_notes; none returns an empty array. Contracts check the complete statement, the none branch and repeated-describe stability, without changing API/environment mappings.
