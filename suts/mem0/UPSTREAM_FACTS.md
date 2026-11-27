# mem0 adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest-infer.json](manifest-infer.json) | mem0-infer | infer-local | gateway | Not separately declared |
| [manifest-openai-embed.json](manifest-openai-embed.json) | mem0-openai-embed | raw-openai-embed | open | Not separately declared |
| [manifest.json](manifest.json) | mem0 | raw-local | none | Not separately declared |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

## Mem0: initial integration (2026-10-18)

The offline design in `design/research/p6-agent-projects-2026-10.md` initially targeted `mem0ai==2.2.1` and `fastembed==0.9.0`. Synthetic stubs did not prove upstream compatibility. Install resolves transitive dependencies with uv compile, saves hashes in requirements.lock.txt and installs with require-hashes; no wheel hashes were fabricated.

- API mapping: `Memory.from_config`, `add(messages,user_id,run_id,metadata,infer)`, `search(query,top_k,filters)`, `get_all(filters,top_k)` and `delete_all(user_id)`. Local Qdrant and history_db_path are episode-local.
- OSS accepts no timestamp argument. The adapter preserves metadata.created_at/session_id, leaves missing time empty and discloses unsupported as_of filtering.
- infer=False stores original text but still needs embeddings; infer=True uses the project's OpenAI provider. Initial local embeddings were `BAAI/bge-small-en-v1.5`, prepared before runtime; the cache-forwarding correction is recorded below.
- `MEM0_TELEMETRY=false` and episode-local MEM0_DIR are set before import. Bridges do not inherit provider credentials indiscriminately; only declared configuration is passed through.
- Provenance unions search metadata.source_ids with IDs mapped from add results, then validates against ingested public events. The original retrieval path marked untraceable results unsupported; current items-mode handling follows the shared integration contract above. Dump increases top_k until exhausted; completeness depends on actual SDK top_k behavior.
- Initial open verification points were fastembed cache/local-files parameters, v2 search/get_all signatures and shapes, hidden get_all limits, infer-update provenance merging and SQLite/Qdrant write paths. Stub success was not real acceptance.

### Runtime verification (2026-10-18)

- `mem0/embeddings/fastembed.py` calls only `TextEmbedding(model_name=...)`, ignoring model_kwargs.cache_dir. It reads FASTEMBED_CACHE_PATH instead. The adapter sets it to `<env>/models` before creating Memory; prepare uses the same directory, including `models--Qdrant--bge-small-en-v1.5-onnx-Q`. Missing configuration produced `Could not load model BAAI/bge-small-en-v1.5 from any source`.
- Memory.from_config constructs a default OpenAI LLM client even with infer=False. Missing credentials failed construction; raw-local supplies `memeval-placeholder-no-llm`. Its none runtime network prevents external calls, with selftest evidence for that boundary.
- Qdrant defaults to 1536 dimensions but bge-small produces 384. Set `vector_store.config.embedding_model_dims=384`; otherwise it fails with `Vector dimension error: expected dim: 1536, got 384`.
- Missing optional mem0ai[nlp] caused `Failed to load spaCy full model`, without blocking add/search.
- A historical direct host probe ingested three events (accepted=3), returned two scored/provenance-bearing search items and dumped three. This is historical behavior evidence, not a current host execution path.
- Passing scope/as_of originally raised Unsupported and failed entire cases. The adapter now ignores these unsupported filters with describe limitations; namespace user_id provides its scope boundary. Qdrant hybrid search lazily loads `Qdrant/bm25`; absence silently degrades to dense search with a warning. Prepare now also downloads it to preserve default behavior.
- Blank queries failed three PMB cases: edge-empty-query, edge-whitespace-query and narrator-voice-pinned-in-own-scope. The adapter now returns empty results for blank queries, leaving scoring to empty_result_correct.

### Container and gateway variant (K7a, 2026-10-19)

- Read-only checks of 2.2.1 `mem0/llms/openai.py`, `configs/llms/openai.py` and `llms/base.py` verified OpenAIConfig model/openai_base_url/api_key/reasoning_effort/is_reasoning_model, base-URL forwarding and chat.completions.create. No real SDK was imported or executed during this check.
- `manifest-infer.json` injects OPENAI_BASE_URL/OPENAI_API_KEY/MEM0_LLM_MODEL/MEM0_LLM_REASONING_EFFORT through llm.env. Missing model/effort fails clearly; the fixed bridge.config.llm_model was removed.
- Explicit effort selects `is_reasoning_model=True`, including effort=none, forwarding effort and omitting temperature/max_tokens. This handles model names beyond the SDK's old heuristic; it does not claim arbitrary automatic model detection. Models rejecting effort require another verified configuration/variant.
- Raw behavior remains unchanged. Gateway infer still uses prepared local fastembed bge-small/384, without gateway embeddings calls. Fake-SDK contracts verify configuration/protocol, not real container inference.

### Describe disclosure (K7d, 2026-10-19)

Infer returns `reasoning_effort forwarded via mem0 OpenAIConfig.reasoning_effort (is_reasoning_model=True); embeddings local fastembed` in llm_notes; raw/none returns an empty array. Tests check both the configured fields and complete statement; no model/parameter mapping changed.

### Direct OpenAI embeddings (P18, 2026-11-01)

- `manifest-openai-embed.json` declares mem0-openai-embed/raw-openai-embed. It preserves infer=False, original text and provenance, while directly using `text-embedding-3-large` (3072 dimensions). Open networking permits the connection but does not enforce a target allowlist. The LLM client uses a fixed placeholder key; the real key is used only by the embedder, with no LLM calls intended.
- Source checks of mem0ai 2.2.1 `mem0/embeddings/openai.py` and `mem0/configs/embeddings/base.py` verified model (upstream default text-embedding-3-small), embedding_dims forwarded as API dimensions, api_key or OPENAI_API_KEY, and openai_base_url or OPENAI_API_BASE/OPENAI_BASE_URL. Both `embed(text,memory_action)` and `embed_batch(texts,memory_action)` exist. Raw add embeds each message with action add; search uses action search.
- bridge.config.embedder accepts provider/model/dims/base_url/api_key_env/max_input_chars. Only verified fastembed/openai mappings are accepted; unknown providers fail. Absent configuration preserves fastembed bge-small/384 and infer settings. Raw mode explicitly configures placeholder LLM credentials while retaining the prior OPENAI_API_KEY setdefault behavior. OpenAI embedding credentials must be provided through the named variable; missing/empty values fail. Describe needs neither credentials nor SDK construction.
- The observed API limit was 8192 tokens. Error forms included `Invalid 'input': maximum context length is 8192 tokens.` and, in a real P18 run, `Invalid 'input[0]': maximum input length is 8192 tokens.` (HTTP 400, invalid_request_error, param/code null). Characters are not tokens: the variant first caps vector input at 32768 Unicode characters, then halves it only after a recognized oversize error, stopping on success or continued failure below 512 characters. Other failures propagate.
- Batch embedding first attempts the normal batch; on an oversize failure it isolates inputs and shortens only failing ones, preserving result order. Shared is_oversize_error matches `maximum\s+(context|input)\s+length` or `too many tokens`, case-insensitively. It must not retry every HTTP 400 or BadRequestError.
- Truncation affects vectors only, not stored text/provenance. The fastembed comparison already silently truncates to 512 tokens. embed_stats.capped/halved count caps/halving retries over the adapter process lifetime, survive namespace reset and appear in dump. Describe/dump limitations disclose the rule; infer llm_notes reports actual local/remote embeddings.
- Offline tests use a fake SDK to check configuration, calls and protocol boundaries. Real SDK/container acceptance is separate. Raw-local/infer-local manifests were unchanged, but the shared adapter change invalidates their image inputs and requires rebuilding.
