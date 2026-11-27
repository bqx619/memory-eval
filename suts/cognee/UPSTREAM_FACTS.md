# cognee adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest-gateway.json](manifest-gateway.json) | cognee | llm-gateway | gateway | Not separately declared |
| [manifest.json](manifest.json) | cognee | none | none | Not separately declared |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

## Cognee 1.6.5: initial integration (2026-10-18)

Based on `design/research/p6b-project-facts-2026-10.md` and P6b Appendix C. The initial plan pinned `cognee[gliner]==1.6.5`, Python 3.12 and `GLINER_AUTO_INSTALL=false`; runtime dependency installation is disabled.

- `GRAPH_EXTRACTOR=gliner_demo` uses `fastino/gliner2.5-base-v1`. The initial verified main prefix was `54785d51`, not a complete immutable revision. Prepare resolves the snapshot, rejects a changed prefix, and records the complete revision and every model file SHA in `model-assets.json` and `env.lock`. Fastembed uses `BAAI/bge-small-en-v1.5` and `FASTEMBED_CACHE_PATH`.
- The initial design used one dataset per namespace and `remember("role: content", dataset_name=ns, self_improvement=False, run_in_background=False)`, then `recall(query, query_type=SearchType.CHUNKS, datasets=[ns], top_k=k, auto_route=False)`. Disabling automatic routing avoids an implicit LLM call. The later verification below corrects the ingestion and provenance mapping.
- Reset calls `forget(dataset=ns)`. No complete enumeration API was verified, so dump is unsupported. There is no per-message time/as_of API; query scope/as_of are not fabricated and limitations are disclosed.
- State and logs are episode-local: DATA/SYSTEM/CACHE_ROOT_DIRECTORY, COGNEE_LOGS_DIR and COGNEE_REPOS_DIR. Set `TELEMETRY_DISABLED=1`, `COGNEE_LOG_FILE=false`, `IMPROVE_AUTO_ENABLED=false`; model assets live in the environment and are exposed through read-only cache links with `HF_HUB_OFFLINE=1` and `TRANSFORMERS_OFFLINE=1`.
- Synthetic SDK tests covered 75/50 events, counts, readiness, document/chunk provenance, unknown IDs, reset and dump rejection. They did not establish actual DTO, model-loading or database behavior. Unknown DTOs must not be assigned sources by text similarity.

### Historical runtime verification (2026-10-18)

The following host-era findings explain later integration changes; they are not current host installation instructions.

- Do not override HOME: the environment-control selftest rejected it; the runtime-provided HOME already isolated the SDK's default directories.
- The resolved dependency set contained 181 packages, including gliner2 2.1.0, torch 2.11.0 and fastembed 0.8.0. The environment was about 2.5 GB, including a 774 MB GLiNER model and fastembed assets.
- `ladybug==0.19.0` included the Python extension but also required `liblbug.dylib`/`liblbug.so`. `_lbug_capi._resolve_library_path` searches `LBUG_C_API_LIB_PATH`, site-packages cache/lib locations, the working directory and `ctypes.util.find_library`. A missing C library produced `Relational DB Migrations failed` caused by `Could not find lbug C API shared library`, explaining PMB 75/75 and LME 50/50 errors in that run. Prepare fetched the v0.19.0 release library: recorded SHA-256 prefixes were osx-arm64 `625e52ab…`, linux-x86_64 `907ff68f…`, linux-aarch64 `b2b4aeb3…` (Linux ARM assets use `aarch64`).
- The macOS library depended on Homebrew OpenSSL. Read access alone was insufficient under the retired host sandbox (F40); copying libssl/libcrypto into the environment, rewriting dependencies to `@loader_path` and re-signing made that historical environment self-contained. The current Linux prepare path no longer performs this procedure.
- `forget` on a nonexistent dataset raises `DatasetNotFoundError` (404); reset tolerates it for new namespaces.
- Tiktoken initially attempted a runtime download of `cl100k_base.tiktoken`. Prepare now caches `cl100k_base`/`o200k_base` and maps `TIKTOKEN_CACHE_DIR` to the prepared assets.
- The embedding tokenizer uses the separate `BAAI/bge-small-en-v1.5` repository, not the fastembed ONNX repository `Qdrant/bge-small-en-v1.5-onnx-Q`. GLiNER's config names `microsoft/deberta-v3-base`. Prepare snapshots both sets of tokenizer/config files into HF_HOME for offline use.
- LanceDB defaults to subprocess operation. Its multiprocessing queues needed POSIX semaphores, which failed in the retired host sandbox (F41). `VECTOR_DB_SUBPROCESS_ENABLED=false` selects in-process operation. `GRAPH_DATABASE_SUBPROCESS_ENABLED=false` similarly avoids queues/SemLock in `graph/kuzu/subprocess/proxy.py`; its pydantic field defaults to true.
- Actual 1.6.5 probing showed `cognee.add(list[str]).data_ingestion_info` follows input order; duplicate text reuses a data_id with `PipelineRunAlreadyCompleted`. Recall CHUNKS uses `metadata.data_id` (also `raw.document_id`). `remember().items` is cumulative and unordered, so it cannot map individual events. The adapter therefore changed ingestion to batch `add` followed by synchronous `cognify(datasets=[ns])`; ready is immediately visible afterward. The observed cost was about 2 seconds/event, dominated by GLiNER extraction.
- Empty recall queries raised exceptions: two PMB edge cases failed in the first 75-case run (73 scored). The adapter now returns an empty result for blank queries without calling recall.

### Container and gateway variants (K7a, 2026-10-19)

- Source verification of ladybug 0.19.0 confirmed that `LBUG_C_API_LIB_PATH` must name the full library file, not a directory. Prepare installs under `{env}/models/lbug/lib`; the manifest points to `{env}/models/lbug/lib/liblbug.so`, without writing the venv. Linux uses the image's `libssl3`; no Homebrew copy/re-sign branch was reintroduced.
- `manifest.json` keeps GLiNER/none. `manifest-gateway.json` sets `bridge.config.extractor=llm`, `GRAPH_EXTRACTOR=llm`, and explicit `cognify(..., extractor=...)`. Version 1.6.5 reads `LLM_PROVIDER/LLM_ENDPOINT/LLM_API_KEY/LLM_MODEL`; llm.env supplies them and missing required values fail construction.
- General extraction configuration does not support reasoning_effort: `infrastructure/llm/config.py` has no `llm_reasoning_effort`. Partial support in unrelated image-transcription or low-level calls does not justify an ineffective `LLM_REASONING_EFFORT` variable. The variant does not add it and discloses this limitation. Requested framework effort is not proof of SDK forwarding.
- Both variants retain local fastembed `BAAI/bge-small-en-v1.5`, 384 dimensions, and image HF/fastembed/tiktoken caches. Existing Linux CPU requirements locks, constraints and runtime.constraints were preserved. This change was checked with synthetic SDK/model fixtures, not a real project run.

### Gateway describe disclosure (K7d, 2026-10-19)

For `extractor=llm`, `describe.config.llm_notes` returns `cognee 1.6.5 exposes no reasoning_effort control; model only; embeddings local fastembed`; GLiNER/none returns an empty array. Contract assertions cover both branches. This disclosure adds no unsupported effort setting and changes neither extraction nor embeddings.
