# graphiti adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest.json](manifest.json) | graphiti | falkordblite | gateway | Not separately declared |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

## Graphiti / FalkorDB Lite: initial integration (2026-10-18)

The initial offline design in `design/research/p6-agent-projects-2026-10.md` targeted `graphiti-core[falkordblite]==0.31.1` and `falkordblite==0.10.0`. Wheel hashes belong to `requirements.lock.txt`; runtime, package and model files are recorded in `env.lock.json`.

- The adapter injects an embedded `AsyncFalkorDB` into `FalkorDriver(falkor_db=...)`, then constructs `Graphiti(graph_driver=...)`. At this stage signatures were covered only by stubs; later verification is recorded below.
- `add_episode` receives name=event ID, episode_body=role:content, source_description=session ID, reference_time=explicit UTC datetime, group_id=anonymous namespace. Missing timestamps use 2000-01-01 UTC and are disclosed. No as_of/scope filtering is claimed.
- Search returns facts; their `episodes` map through the UUIDs returned by add_episode to source_ids. Incomplete provenance was classified unsupported. Dump covers group-scoped EpisodicNode, EntityNode and EntityEdge results; reset calls `clear_data(driver, group_ids)`. Actual pagination and return shapes required runtime verification.
- The initial variant used open networking and direct provider calls, with `GRAPHITI_TELEMETRY_ENABLED=false` and episode-local caches. The subsequent gateway variant below supersedes that network/model configuration.
- The historical host design restricted executables to the read-only environment tree and required embedded redis-server there. Synthetic tests covered awaited calls, timestamps, provenance, reset, full-node dump and child cleanup, not actual Redis/provider execution.

### Historical runtime verification (2026-10-18)

- The `falkordblite==0.10.0` wheel exposes the top-level module `redislite`, not `falkordblite`. Use `from redislite import AsyncFalkorDB`; it bundles `redislite/bin/redis-server` and `falkordb.so`.
- Its macOS native module required the OpenMP runtime `libomp`; the missing runtime caused a describe failure hidden behind `rpc_error(-32000)` (F26). Installing the dependency resolved that historical probe. The Linux libomp/libgomp equivalent was not verified in this record.
- The falkordblite extra requires Python ≥3.12; the manifest pins 3.12. `FalkorDriver.__init__(host, port, username, password, falkor_db, database)` accepts the embedded client. Real describe/reset probes passed; reset started redis-server.
- A second check corrected an indirect-dependency assumption: graphiti-core imports `httpx` directly but only declares `openai>=1.91.0`; the resolved `openai==3.33.0` depended on `httpx2`. A clean import failed with `ModuleNotFoundError: httpx`. `requirements.in` explicitly adds `httpx==0.28.1`; this patch dependency must be disclosed.
- Redislite's generated Unix socket path exceeded macOS's 104-byte limit (`Failed opening Unix socket: unix socket path too long (107), must be under 104`). The episode prefix was 77 bytes; a short episode-relative `r.sock` was 84 bytes. OpenMP itself loaded correctly. F28 records the framework short-path correction.
- Passing `unix_socket_path` to AsyncFalkorDB was ineffective: `async_client.py:65` forwards only dbfilename/serverconfig. The verified solution starts synchronous `redislite.FalkorDB(dbfilename=EP/graph.rdb, unix_socket_path=EP/r.sock)` first, then attaches AsyncFalkorDB to the same file through its `.settings` registry. Keep the synchronous handle alive to prevent GC shutdown. `RETURN 1` passed under the previously failing path conditions.

### Container and gateway variant (K7a, 2026-10-19)

- Source verification of graphiti-core 0.31.1 found `LLMConfig` supports model/small_model/base_url/api_key. Native `OpenAIClient` uses `responses.parse` and a string reasoning argument; it does not fit the chat/completions-only gateway contract in this batch.
- Use the SDK's `OpenAIGenericClient(config=..., client=...)`, preserving upstream prompts, schema and response handling. Both models use `GRAPHITI_LLM_MODEL`. Adapter `GatewayCompletions` forwards `GRAPHITI_LLM_REASONING_EFFORT` as `reasoning_effort`, changes max_tokens to max_completion_tokens and removes temperature. This is adapter support, not native generic-client effort support. Actual gateway schema acceptance remained unverified at this stage.
- A custom EmbedderClient uses fastembed 0.9.0, `BAAI/bge-small-en-v1.5`, 384 dimensions and `local_files_only=True`; cache comes from `FASTEMBED_CACHE_PATH={env}/models`. Prepare downloads assets and records file hashes. Runtime does not call OpenAI embeddings.
- The default OpenAIRerankerClient is replaced with a model-free CrossEncoderClient that preserves order and assigns score=1. This changes upstream reranking semantics and is disclosed in `describe.limitations` and `declared.reranker`.
- Missing model, effort, base URL, key or cache configuration fails rather than falling back to SDK defaults. This change added fastembed to requirements.in without rewriting the existing lock. Linux lock regeneration, model build and real acceptance were pending in this dated record.

### Strict schema and describe disclosure (K7d, 2026-10-19)

Version 0.31.1 `_build_response_format` uses raw `model_json_schema()` without strict mode; constructor options provide only json_schema/json_object modes. The injected `GatewayCompletions.create` therefore deep-copies response_format and sets `json_schema.strict=true` without modifying model classes or upstream response handling.

`normalize_strict_schema` recursively visits `$defs/definitions/properties/items/anyOf/oneOf/allOf/prefixItems`. Every object uses `additionalProperties=false`; required contains all property keys in order. Originally optional non-nullable fields become anyOf[original,null]; existing nullable forms remain. The function removes default/format, string length/pattern, numeric bounds/multipleOf, array size/uniqueItems/contains, object counts/patternProperties/propertyNames and dependency/conditional constraints rejected by this gateway subset. It returns the removed keyword set. The wrapper emits one stderr warning on the first removal, containing keyword names only, never schema, prompts or credentials. References and composition remain; this is not a general JSON Schema rewrite.

Describe explicitly states `optional fields coerced to nullable for strict schema` and discloses dropped constraints. It cannot claim to preserve validation rules it removed. The complete llm_notes entry is `reasoning_effort added to chat/completions by adapter wrapper; response_format schema normalised to OpenAI strict for the gateway; embeddings local fastembed; reranker disabled`.

`suts/_bridge/python/testdata/graphiti-0.31.1-schemas.json` was generated from actual upstream BaseModel definitions by AST extraction and Pydantic 2.14.1 `model_json_schema()`, without importing or running Graphiti. It covers ExtractedEntity, ExtractedEntities, Edge, ExtractedEdges, EdgeTimestamps, BatchEdgeTimestamps, NodeDuplicate and NodeResolutions, and records source SHA-256 and Apache-2.0 provenance. Tests check object constraints, optional/null conversion, default removal, references, input immutability, idempotence, the outgoing strict flag and a single warning. Offline fixture success does not establish real gateway success; that acceptance remained pending in this record.
