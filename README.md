# memeval

memeval evaluates memory systems for personal agents. It normalizes public benchmarks into a common suite format, drives systems through the same adapter protocol, and measures retrieval and memory-assisted answers separately. Reports disclose inputs, models, budgets, errors, and execution conditions.

Mem0, Mastra, Graphiti, Cognee, Hindsight, Octop-memory, and CogletNet are evaluation subjects, not reference implementations of the scoring rules. Honcho remains deferred; Echo is a transport fixture. System-specific traces are optional diagnostics.

## Run a baseline without model calls

Use the Go version in [go.mod](go.mod). The first build needs its dependencies; cached dependencies permit an offline build.

```sh
go build -o bin/memeval ./cmd/memeval
bin/memeval run --suite pmb --sut bm25 --out reports/bm25-pmb
```

Open `reports/bm25-pmb/report.html`. This example uses the bundled PMB projection, without Docker or LLM calls. Other built-in baselines are `null`, `recent`, and `fullcontext`. PMB is a small diagnostic collection, not a comprehensive measure of long-term memory.

## Evaluate an external system

External systems are built and run in Linux containers; the evaluator runs on the host. You need Python 3, the Docker CLI, and a Linux Docker daemon. Select the Docker context explicitly.

```sh
scripts/build_linux_memeval.sh arm64   # use the daemon architecture; amd64 is also supported
cp .env.example .env                  # configure the context and any required model services
bin/memeval sandbox caps
bin/memeval env install suts/mem0/manifest.json
bin/memeval run --suite pmb --sut suts/mem0/manifest.json --out reports/mem0-pmb
bin/memeval gc                        # dry-run by default
```

Image builds may download dependencies and models. Network mode `none` restricts the SUT at runtime, not image builds or evaluator-side LLM calls. `gateway` restricts SUT model requests to one configured target; `open` does not enforce a destination allowlist. Check the manifest and the report's `isolation` and `data_flow` fields together.

Some scoring protocols require pinned upstream templates. `python3 -B scripts/fetch_upstream_refs.py` retrieves them using the network; `--verify-only` checks existing copies offline. Dataset acquisition and licensing are described in the [data guide](data/README.md).

## Interpret results

- **Retrieval:** the default `retrieval` mode scores returned source evidence, ranking, and empty-result behavior. There is no universal memory score.
- **Answers:** `fixed_reader` uses a common reader and the benchmark's scoring protocol. `sources` expands source events; LongMemEval, MemoryAgentBench, and BEAM also support explicit `items`, which uses the SUT's actual returned text. These are different experimental conditions.
- **Provenance:** unknown, future, duplicate, or malformed references remain invalid. Genuinely untraceable memories may support answers only in supported items paths; incomplete provenance must not produce apparently complete retrieval scores.
- **Coverage:** report `error`, `invalid`, and `unsupported` alongside scored cases. Different coverage, models, embeddings, budgets, or resumed SUT identities do not establish an overall ranking.
- **Scope:** `native_answer` is reserved in the protocol but not enabled by the runner. Retrieval and question answering do not establish full agent task-execution ability.

Generate metric definitions with `bin/memeval results glossary`. See the [current contracts](design/current-contracts.md) for input boundaries, identity checks, and limitations.

## Parallelism, recovery, and resources

`--parallel 1..16` creates independent workers. `fresh_per_case` schedules independent cases; `incremental_per_episode` preserves order within each episode. Results are assembled in suite order. In gateway mode, one governor serves the run: an explicit flag/environment concurrency is the total cap; otherwise the cap is `(manifest llm.concurrency or 8) × parallel`.

Use `run --resume <out>` to resume a run. A SUT identity change requires `--resume-sut-change` and records both identities; retained results do not become evidence for the new version. `--resume-cases` further selects cases. Consult `run --help` for current options.

Episode and run cleanup verify residues. `env destroy` removes an environment; `gc` previews candidates by default. Neither should prune unrelated resources or shared build caches.

## Documentation and viewers

| Entry | Purpose |
|---|---|
| [User manual](docs-site/index.html) | Commands, configuration, isolation, and reports |
| [Data](data/README.md) / [Protocols](protocols/README.md) | Benchmark identity, sources, templates, deviations, comparability |
| [Adapter guide](suts/README.md) | Manifests, bridges, model configuration, capabilities |
| [Current contracts](design/current-contracts.md) | Common rules and known limitations |
| [Result cards](docs-site/results.html) / [Case review](docs-site/cases.html) | General report viewers; generated data is required |
| [BEAM comparison](docs-site/beam.html) / [Run review](docs-site/beam-run.html) | Answers, scoring, provenance, and optional traces |
| [Design index](design/README.md) / [Changelog](CHANGELOG.md) | Current references and dated history |
| [Contributing](CONTRIBUTING.md) / [Security](SECURITY.md) | Validation, contributions, and publication boundaries |

Page source and evaluation data are separate. `results build`, `scripts/build_case_review.py`, and `scripts/build_beam_review.py` generate viewer data. Local results are not automatically distributed with the source. A selected [aggregate evidence snapshot](evidence/README.md) provides downloadable PMB and BEAM summary tables with denominators, configuration identifiers, and report hashes; full reports, logs, and per-case data remain local.

`cmd/` and `internal/` contain the evaluator, `suts/` contains adapters, and `schemas/` contains generated contracts. `docs/` preserves an early prototype and still-used PMB/regression inputs. `reports/`, `bin/`, private configuration, and bulk data remain local.

## License

The main project uses the [MIT License](LICENSE). Third-party code, data, templates, and fixtures retain their own terms; see [third-party notices](THIRD_PARTY_NOTICES.md).
