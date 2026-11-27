# Integrating a system under test (SUT)

A SUT or variant consists of a schema-v3 `manifest*.json`, an adapter and dependency declarations under `suts/<name>/`. External SUTs are installed and run only in Linux containers; the evaluator remains on the host. Host installation of a SUT is neither required nor supported.

Current directories: `echo` (transport fixture), `mem0` (raw/infer/openai-embed), `mastra` (history/semantic), `graphiti`, `cognee` (none/gateway), `cogletnet` (multiple manifests), `hindsight` (none/gateway), `octop-memory` (native Go adapter) and `honcho` (deferred). All follow the same integration and acceptance rules. A directory's existence does not establish acceptance of every variant on every benchmark. `_bridge/` contains Python and Node bridges, copied into `/opt/memeval/env/bridge/` in the image.

## 1. Choose a bridge

| Project interface | `adapter.kind` | Integration |
|---|---|---|
| Python SDK | `python-bridge` | `adapter.py` subclasses `memeval_bridge.AdapterBase` and implements `describe / reset / ingest / ready / retrieve`, optionally `answer` and `dump`; declare `bridge.module` and `bridge.class` |
| Node SDK | `generic` with `process.command: ["{node}", "{env}/bridge/node/index.mjs", "--adapter", "{project}/adapter.mjs", "--config", "{config_json}"]` | Export `Adapter` from `adapter.mjs`, using the same methods |
| Project HTTP service | `http-bridge` | Run a generic sidecar on the episode network; declare `bridge.base_url: "http://<sidecar>:<port>"` and version-1 JSON `bridge.routes` for paths, methods, bodies, item mapping and pagination |
| Special integration | `native` (Go adapter in the evaluator) | Currently `octop` only |

Bridges use line-framed JSON-RPC 2.0 over stdio, defaulting to 16 MiB and 30 seconds, and redirect SDK stdout to stderr. `source_ids` must be a list; nonempty references must identify already-ingested events before the current cutoff. Unknown, future, malformed or duplicate references within an item are contract errors and cannot be silently cleared. Truly untraceable memories retain their text with `source_ids: []`: retrieval and fixed_reader/sources mark them `unsupported`; LongMemEval, MemoryAgentBench and BEAM fixed_reader/items can answer from their text. A case with any untraceable item receives no retrieval metrics based on partial provenance. See the [adapter protocol](../docs-site/index.html#rpc).

HTTP request timeouts derive from `adapter.request_timeout_s`: the framework passes the effective RPC timeout to the bridge, whose default HTTP timeout is `max(1, request_timeout_s - 5)` seconds. The default RPC timeout of 30 seconds therefore yields 25 seconds for HTTP. Explicit `bridge.config.timeout_s` and `ready_timeout_s` override HTTP requests and readiness waits respectively. Long-running services must account for both layers and `sandbox.limits.timeout_s`.

## 2. Write a v3 manifest

Example: Python bridge, no LLM, no runtime SUT egress.

```json
{
  "schema_version": "3",
  "name": "myproj",
  "variant": "raw-local",
  "notes": "what this variant is",
  "adapter": {"kind": "python-bridge", "transport": "jsonrpc-stdio", "max_frame_bytes": 16777216, "request_timeout_s": 120},
  "runtime": {
    "kind": "uv-python", "python": "3.12", "base": "uv-python",
    "requirements": "requirements.in",
    "constraints": "requirements.constraints.txt",
    "indexes": [{"url": "https://download.pytorch.org/whl/cpu", "strategy": "unsafe-best-match"}],
    "apt": ["libgomp1"],
    "prepare": [["{python}", "{project}/prepare_models.py", "{env}/models"]],
    "cache_links": {"home/.cache/some-sdk": "{env}/models-home/.cache/some-sdk"}
  },
  "bridge": {"module": "adapter", "class": "Adapter", "config": {}},
  "process": {"command": [], "cwd": "{ep}/work", "env": {}, "env_passthrough": []},
  "config": {},
  "sandbox": {"backend": "container", "network": "none", "limits": {"timeout_s": 600}},
  "ready": {"strategy": "sync"},
  "telemetry_env": {"SOME_SDK_TELEMETRY": "false", "HF_HUB_OFFLINE": "1"},
  "declared": {"llm": "none", "embedding": {"provider": "fastembed", "model": "BAAI/bge-small-en-v1.5", "local": true}},
  "llm": {"required": false}
}
```

- **Dependencies.** Put top-level packages in `requirements.in`. The first `env install` resolves a hashed Linux `requirements.lock.txt` inside the base image; alternatively supply a Linux-resolved `requirements_lock`. `constraints` pins versions; `indexes` adds sources such as the PyTorch CPU index (Linux torch otherwise pulls CUDA dependencies). `base: uv-python-trixie` supports wheels requiring glibc ≥ 2.39. Node projects supply `package.json`, optionally `package-lock.json`, and `runtime.version`.
- **Models and caches.** Build-time `prepare` runs as UID 65532 and can download models into `{env}/models` or `{env}/models-home`. Local-model variants must point runtime SDKs to these caches through `telemetry_env`/`process.env` (for example `FASTEMBED_CACHE_PATH`, `HF_HOME`). `cache_links` maps hardcoded cache locations inside the episode to the image. The root filesystem is read-only; state goes to `{ep}`, `/tmp` or explicitly declared writable tmpfs paths.
- **Network boundaries.** `none` blocks external runtime SUT access; `gateway` routes allowed LLM traffic through the single-target governor; `open` does not restrict egress destinations. Runtime `none` does not constrain image/dependency/model downloads during builds. Host evaluator reader/judge calls are also separate and are disclosed in `evaluator_llm`, `billing.evaluator` and `data_flow`.
- **Environment.** The prefixes `ANTHROPIC_ MEMEVAL_ HERDR_ CODEX_ CLAUDE` are prohibited; evaluator configuration is not inherited by containers. Secret-like keys such as `*_api_key / token / secret` are redacted.
- **LLM variants.** Set `sandbox.network: "gateway"`, `llm.required: true`, and map configured values to the SDK's variables:

  ```json
  "llm": {"required": true,
          "env": {"OPENAI_BASE_URL": "{llm.base_url}", "OPENAI_API_KEY": "{llm.api_key}",
                  "MYPROJ_LLM_MODEL": "{llm.model}", "MYPROJ_LLM_REASONING_EFFORT": "{llm.reasoning_effort}"}}
  ```

  Adapters read only declared variables and fail clearly when required model configuration is missing. `describe.config.llm_notes[]` explains forwarding or limitations, such as disabled reranking; the report copies it to `sut.llm.notes`. A gateway requiring OpenAI strict `response_format` may require adapter-side schema normalization; see `graphiti/adapter.py`.
- **Embeddings.** Variants may use image-local models or explicitly connect to external embeddings. For example, `mem0/manifest-openai-embed.json` declares `network: open` and credential passthrough. Direct embedding traffic bypasses the SUT LLM governor; its usage must not be assumed included in `billing.sut`, and open networking does not enforce a destination allowlist. Check the manifest, `declared.embedding`, adapter disclosure and actual service configuration. Generic report text is not independent verification of SDK traffic.
- **Fanout.** `llm.concurrency` declares per-episode LLM fanout (for example 32 for Hindsight). Without an explicit override, total gateway concurrency is fanout × `--parallel`, or 8 × parallel when undeclared. Explicit `--sut-llm-concurrency` takes precedence over `MEMEVAL_SUT_LLM_CONCURRENCY` (process environment precedes the configuration file); both are absolute totals and are not multiplied by parallel. Fanout 32 and parallel 4 yields 128; an explicit total of 16 remains 16. Reports record `concurrency`, `concurrency_source`, `concurrency_per_episode` and `parallel`. Queuing beyond SDK timeouts may increase `billing.sut.client_disconnects`; examine queue and server logs to determine the cause.
- **Sidecars.** Use `postgres-pgvector-18` / `qdrant-1.19.2` presets or `kind: generic` with a command in the SUT image. `exports` passes connection settings to subsequent sidecars and the SUT; readiness supports `exec`, `tcp` and `http`. See `hindsight/manifest.json`.
- **Variants.** A directory may contain multiple `manifest-<variant>.json` files. `--network` overrides do not change image identity; neither do `llm.model` or `--sut-llm-model`.

## 3. Acceptance

```sh
bin/memeval env install suts/<name>/manifest.json
bin/memeval env check suts/<name>/manifest.json --json
bin/memeval sandbox selftest --sut suts/<name>/manifest.json --out reports/selftest-<name>
bin/memeval run --suite data/normalized/pmb-retrieval.json --sut suts/<name>/manifest.json --limit 3 --out reports/<name>-smoke
bin/memeval run --suite data/normalized/pmb-retrieval.json --sut suts/<name>/manifest.json --out reports/<name>-pmb
```

Installation builds the image, writes Lock v3 and runs selftest (none/gateway expect all probes to pass). `env check` should report valid; changed project inputs produce `input_changed`. Selftest uses the selected variant's actual network mode; external-service variants should not be forced to none for acceptance.

All supported target cases must be `scored`; `unsupported` must reflect an explained capability gap. Removing failed cases cannot establish a full pass. Verify `isolation.container.residue.run_clean = true` and consistency of the HTML isolation panel and data-flow table. Migration comparisons use the same suite, seed, variant and budget, with `scripts/parity_diff.py <new> <ref>` where required by acceptance criteria. A smoke run cannot replace full acceptance. Each SUT maintains `UPSTREAM_FACTS.md` with verified SDK versions, configuration, behavior and variant decisions; real acceptance evidence belongs to the corresponding batch record (P8: `design/p8-implementation-notes.md`).

## 4. Integration constraints

- Containerfiles are generated from manifests and locks; do not maintain a separate SUT Dockerfile.
- Do not hardcode model names, credentials or gateway addresses in adapters.
- Preserve bridge validation of event IDs, ingestion count conservation and provenance.
- Track manifests, adapters, dependency declarations, locks/constraints and facts; exclude datasets, models and host virtual environments.
