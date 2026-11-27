# Evaluation data: registry, upstream copies and normalized suites

`data/` has three layers. The repository tracks registration and reproducibility anchors: registry entries, upstream locks and import statistics. Dataset bodies are excluded by repository policy and can be restored, rebuilt and checked against those anchors. The license names below reproduce registry facts; they do not establish permission for every source file, transformation or use. Consult each entry's `license` fields and upstream license records.

```text
data/
├── registry/<dataset>.json               # Tracked registration
├── upstream/<dataset>/source.lock.json   # Tracked revision and per-file SHA-256; bodies excluded
└── normalized/<suite-id>@<rev12>/         # Generated bundle; only import.json tracked
    ├── suite.json                       # Header and container{...,index_sha256,episodes,events,cases}
    ├── episodes.jsonl                   # One episode (events + cases) per line
    ├── episodes.index.jsonl             # Per-episode line/offset/bytes/sha256
    └── import.json                      # Converter, counts, by_category and diagnostics
```

Four early suites remain single files (schema 2.0): `pmb.json`, `pmb-retrieval.json`, `longmemeval-s.json` and `longmemeval-s-strat70.json`. All other imported suites use bundle directories (schema 3.0).

## Registered datasets

| ID | Name | Recorded license | Case kinds | Protocol | Status | Suites |
|---|---|---|---|---|---|---|
| precisionmembench | PrecisionMemBench (field projection) | MIT | typed_context, retrieval | `protocols/precisionmembench.json` | run | `pmb.json` (77 typed_context), `pmb-retrieval.json` (75 retrieval) |
| longmemeval | LongMemEval-S (cleaned) | MIT | qa | `longmemeval.json#s` | run | `longmemeval-s.json` (500), `-strat70.json` (70, stratified) |
| locomo | LoCoMo | CC BY-NC 4.0 | qa | `locomo.json#text_only` | imported | `locomo10@3eb6f2c585f5`, `…-reader-v2` (1986) |
| personamem | PersonaMem v1 | MIT | mcq | `personamem.json#official` / `#memory_only` | imported | `personamem-v1-32k@a8076d5608c9`, `…-nopersona` (589) |
| memoryagentbench | MemoryAgentBench | MIT | qa, summary, recsys | `memoryagentbench.json#<split>` | imported | `mab-accurate-retrieval`, `-conflict-resolution`, `-long-range-understanding`, `-longmemeval-overlap`, `-test-time-learning` |
| beam | BEAM | CC BY-SA 4.0 | rubric | `beam.json#100K/500K/1M` | imported | `beam-100k/500k/1m@3205395e897e` (400/700/700) |
| memora | Memora | Apache-2.0 | rubric | `memora.json#weekly/monthly/quarterly` | imported | `memora@a6493188efc8/<period>` (150/150/300) |
| halumem | HaluMem | CC BY-NC-ND 4.0 | qa, memory_audit | `halumem.json#medium/long` | imported | `halumem-medium/long@cb04336aa1b7` (4854/5884) |
| prefeval | PrefEval | CC BY-NC 4.0 | rubric, mcq | `prefeval.json#<form-task>` | imported | `prefeval-explicit-generation-t10/t300`, `explicit-mcq-t10`, `implicit-choice-generation-t10` (1000 each) |
| convomem | ConvoMem | CC BY-NC 4.0 | qa, rubric | `convomem.json#<type>` | imported | Five bundles: abstention, assistant_facts, changing, implicit_connection, preference |
| longmemeval-v2, memoryarena | — | Apache-2.0 / CC BY 4.0 | — | No protocol file | registered | Not integrated |

Registry fields (`internal/registry/registry.go`): `id, name, repository, dataset_url, data_nature, task_kinds, protocol_id, variants, reported_scale, license{...}, pinned_revision, implementation_status (registered|importer|imported|run), importer, lock_path, suites[]{suite_id, path, protocol_ref, import_stats_path, suite_sha256}, overlaps, notes, verification_date`.

## Rebuilding a dataset

1. Restore official scoring/reader templates with `python3 -B scripts/fetch_upstream_refs.py` (`--only <name>` selects a source; `--verify-only` checks offline). Judged benchmarks read exact template bytes and verify their SHA-256 at runtime.
2. Restore data. The built-in downloader currently supports `memeval fetch longmemeval-s --revision <revision-from-source.lock> --ack-license`. For other datasets, use `source_url`/`revision` in `data/upstream/<name>/source.lock.json`, restore into that directory, then run `memeval registry check --verify-files` for per-file SHA-256 checks.
3. Import with `memeval import <dataset> --input … --out data/normalized/<suite-id>@<rev12>`. Consult `memeval import <dataset> --help`; only pmb/longmemeval/locomo/personamem expose `--format bundle|single`.
4. Run `memeval validate <suite>` for structural and semantic checks, including gold leakage scanning. `memeval registry check` checks suite hashes, import statistics and protocol references against registration.

For bundles, `suite_sha256` is `sha256(canonical suite.json + "\n" + raw episodes.index.jsonl bytes)`; single files use the SHA-256 of canonical JSON. Reports record this input identity. Combine it with upstream locks, file checks and import records to establish provenance. A matching hash alone does not prove transformation equivalence, official scoring fidelity or legal permission.

## Normalized suite format

See `design/dataset-protocol.md` (v1.2), `internal/suite/suite.go` and `v3_types.go`. `memeval suite schema` exports JSON Schema.

- Header: `schema_version` (2.0/3.0), `suite_id`, `track`, `provenance{source_url,revision,converter,…}` (`source_url` required), `limitations[]`, `protocol{ingestion,query_time_policy,retrieval_metrics,reset_policy_allowed,…}`, `protocol_ref`, `license`, `data_nature`, `taxonomy`, `episodes[]`.
- Episode: `id`, `cluster_id` (paired-comparison cluster), `events[]`, `cases[]`.
- Event: episode-unique `source_id`, `owner`, `role` (user/assistant/system/observation/fact; schema 3.0 adds tool), `content`, `session`, `timestamp`, `turn_group`, `attachments[]`, `attributes`.
- Case: `id`, `kind`, `query`, `user`, `cutoff` (exclusive event-prefix index; gold evidence must precede it), `category`, `budget{top_k,max_context_chars}`, `options`, `query_point`, `gold`.
- Gold: `answers[]`, `abstain`, `evidence_ids[]` (required for schema-2.0 retrieval), `evidence_session_ids[]`, `evidence_unit` (event/session), `exhaustive`, `choice`, `expect` (PMB typed assertions). Schema 3.0 adds `evidence_refs[]`, `mcq{seed,order,shuffle,parse}`, `rubric{items[],aggregate}`, `checker{type,params}`, `memory_points[]` and related fields.

### Case kinds and scoring

| Kind | Sources | Scoring |
|---|---|---|
| typed_context | PMB | `gold.expect` assertions: pinnedFacts / relevantBeliefs / openQuestions / personaPrelude |
| retrieval, qa (memory-only) | PMB-retrieval, LongMemEval, LoCoMo, MAB and others | Gold-based `hit, recall, mrr, all_evidence, ndcg, precision, empty_result_correct`@top-k; LongMemEval also has `recall_any/recall_all/ndcg_any` |
| qa (fixed_reader) | LongMemEval, LoCoMo, MAB, ConvoMem, HaluMem | Official procedures: LLM judges (LongMemEval/ConvoMem/HaluMem), `token_f1` (LoCoMo), exact-match family (MAB) |
| mcq | PersonaMem, PrefEval | Choice parsing and accuracy |
| rubric | BEAM, Memora, PrefEval, ConvoMem-preference | Item judging and aggregation: mean / fama / prefeval |
| summary, recsys | MAB | ROUGE (infbench_sum), recall@5 |
| memory_audit | HaluMem | Memory extraction F1 |

Schema-3.0 kinds execute only when their benchmark predicates match; otherwise the case is `unsupported` with a report warning.
