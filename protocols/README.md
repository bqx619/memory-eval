# Benchmark protocol declarations: `protocols/<benchmark>.json`

Each benchmark file declares its variants, scoring rules, official templates and accepted equivalent deviations. Suites refer to it through `protocol_ref: "protocols/<id>.json#<variant>"`. At runtime, `memeval run` uses `benchmark.ResolveAt` to load and verify the declaration; `protocol.benchmark{protocol_id, version, variant, ref, sha256}` records the protocol actually used.

Available declarations: `beam`, `convomem`, `halumem`, `locomo` (version 2), `longmemeval`, `memora`, `memoryagentbench`, `personamem`, `precisionmembench` and `prefeval`.

## Structure (`internal/benchmark/protocol.go`)

```json
{
  "protocol_id": "longmemeval",
  "version": "1",
  "notes": "...",
  "cluster_basis": "question",
  "overlaps": ["memoryagentbench"],
  "upstream": {
    "repo": "https://github.com/xiaowu0162/LongMemEval",
    "commit": "<40-hex>",
    "files": [{"path": "design/upstream/longmemeval/src/evaluation/evaluate_qa.py", "sha256": "..."}]
  },
  "variants": {
    "s": {"description": "...", "suite_id_patterns": ["longmemeval-s*"], "params": {"reset_policy_default": "fresh_per_case"}}
  },
  "scoring": {
    "primary": "judge",
    "judge": {"file": "design/upstream/longmemeval/src/evaluation/evaluate_qa.py", "sha256": "...", "per_type": true, "model_official": "gpt-4o-2024-08-06"},
    "reader": {"file": "...", "sha256": "..."},
    "answer_parsing": "...",
    "denominators": {"exclude": [], "per_type_report": true},
    "secondary": ["longmemeval_official.turn.recall_any@5"]
  },
  "fidelity_rules": {"full": ["..."], "equivalent": ["..."], "else": "partial"}
}
```

- `scoring.primary` accepts `judge | exact_match | token_f1 | mcq | nugget | assertions | retrieval`.
- `scoring.judge` and `scoring.reader` reference templates. `file` must be a repository-relative existing file with the recorded SHA-256. Otherwise `ResolveAt` reports `template sha256 mismatch`; missing files produce a restoration hint: `python3 -B scripts/fetch_upstream_refs.py --only <name>`.
- Common `variants.<name>.params` keys include `reset_policy_default` (when `run --reset-policy` is absent), `dataset_variants`, `chunk_tokens_allowed`, `tokenizer`, `template` and `context_ladder`.
- `fidelity_rules` determines reported `protocol.fidelity`: `full` for the official procedure, `equivalent` for accepted deviations, otherwise `partial`.

## Reader input and comparability

`fixed_reader` defaults to `--reader-input sources`, which resolves retrieval source IDs to original events. LongMemEval, MemoryAgentBench and BEAM also support explicit `--reader-input items`, using the SUT's returned memory text, including synthesized memories without source IDs. Benchmarks without an items implementation reject that mode. `fullcontext` accepts sources only.

MemoryAgentBench and BEAM concatenate items verbatim in returned rank order. The budget counts Unicode characters, including newline separators; excess context is removed as complete items from the low-ranked tail. BEAM items do not expand sources or reconstruct original roles and chronology. Its sources path retains event roles and chronology and removes the earliest complete events when over budget. Switching input does not change the scorer. Reports disclose input deviations and truncation; items mode does not claim full reproduction of official input construction.

`source_ids` must be a list. Truly untraceable memories may use `[]`; only the supported fixed_reader/items paths continue answering with them. Retrieval and fixed_reader/sources mark them `unsupported`. If any item in a case lacks provenance, that case receives no retrieval metrics based on a partial source set. Nonempty references must identify events already ingested before the current case's cutoff. Unknown, future, malformed or duplicate references within one item remain contract errors; clearing references cannot bypass validation.

Reports record `protocol.reader_input`, `reader.input_mode` and deviations. Sources and items runs cannot resume as one configuration or be directly paired as equivalent inputs. This switch changes reader context, not the SUT's retrieval algorithm or its retrieval mode.

Readers and judges run in the host evaluator. SUT `network: none` constrains runtime SUT traffic only; it does not establish zero egress for an entire fixed_reader evaluation. Evaluator model calls are separately disclosed through `evaluator_llm`, `billing.evaluator` and `data_flow`.

## Restoring reference templates

Official scoring and reader templates under `design/upstream/<name>/` have their own licenses and are excluded from this repository's tracked distribution. Case-insensitive `SOURCES.md` files record repositories, commits and SHA-256 values. After cloning:

```sh
python3 -B scripts/fetch_upstream_refs.py                # Restore and verify pinned references
python3 -B scripts/fetch_upstream_refs.py --verify-only  # Verify existing files offline
```

Tests requiring absent templates skip; benchmark runs requiring them fail with an explicit restoration instruction.
