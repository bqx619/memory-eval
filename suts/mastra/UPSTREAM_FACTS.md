# mastra adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest-semantic.json](manifest-semantic.json) | mastra-semantic | semantic-local | none | Not separately declared |
| [manifest.json](manifest.json) | mastra | history | none | Not separately declared |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

## Mastra memory: initial integration (2026-10-18)

The offline design in `design/research/p6-agent-projects-2026-10.md` pinned Node 24.22.0, `@mastra/memory` 1.38.0, core 1.78.0, libsql 1.26.2 and fastembed 1.3.3. The initial real npm lock/package set was not yet supplied. Runtime tarball hashes for darwin-arm64/linux-amd64/linux-arm64 came from the recorded official list and were rechecked on installation; offline tests used explicit-hash local fixtures rather than claiming downloaded-tarball validation.

- package.json is the resolution input. The initial installation resolves with npm install --package-lock-only, then npm ci; the complete lock and package hashes are retained in the environment. Repeated installs reuse them rather than resolving anew. No dependency tree was fabricated.
- LibSQLStore/LibSQLVector file URLs stay in the episode. saveMessages receives createdAt/threadId/resourceId. Missing time starts at 2000-01-01 UTC; as_of filtering is unsupported.
- The history variant lists messages without models. The semantic variant uses recall semanticRecall.topK; multiple threads are merged in SDK order, deduplicated and truncated. This does not establish global cross-thread vector ranking.
- Dump paginates listMessagesByResourceId using page/perPage/hasMore. Reset deletes only adapter-created threads through deleteThread. Returned IDs must map to written event IDs; otherwise provenance is unsupported.
- MASTRA_TELEMETRY_DISABLED=1 is set before import. Prepare warms fastembed.doEmbed with HOME in env/models-home; runtime cache links expose the model read-only and avoid downloads.
- Initial real-check requirements included package integration/Node ABI, doEmbed exports, cache location, createThread requirements, pagination/order and LibSQL shutdown. Synthetic contracts covered both variants, counts, provenance, reset, 300-item dump pagination, times and sessions; they did not replace real smoke testing.

### Runtime corrections (2026-10-18)

- saveMessages supports only user/assistant roles; fact raised `BUG: add handling for message role fact`. Other input roles are mapped to user with disclosure.
- String content is not vectorized. The adapter uses `content:{format:2,parts:[{type:'text',text}]}`; a real probe changed memory_messages_384 index count from 0 to 1 and enabled semantic recall. Version 1.38.0 has memory.recall, not memory.query.
- listMessagesByResourceId begins with the earliest messages. History retrieval is an unranked earliest-first list; observed LongMemEval recall near zero was expected. It is a transport/protocol smoke path, not a memory-quality baseline.
- The model cache is `$HOME/.cache/mastra/fastembed-models`. cache_links maps the prepared models-home/.cache/mastra directory into the episode HOME.
- Recall collapses messages with identical createdAt: five same-time messages yielded one result; distinct times yielded topK results. Missing/equal PMB timestamps produced an initial semantic recall of 0.035. The adapter preserves original timestamps but adds namespace-local 1 ms increments to repeated/missing SDK timestamps, disclosing this transformation.
