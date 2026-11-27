# Contributing

Contributions may improve protocols, importers, adapters, scoring, execution reliability, or result interpretation. Explain the problem, reproducible evidence, and validation scope. Do not add exceptions based on a particular question, category, or expected system score.

## Commits and authorship

Use Conventional Commits: `<type>(<scope>): <description>`. Common types include `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, and `chore`; scopes name a module or `sut/<name>`. Use an English imperative title within 72 characters. Mark incompatible changes with `!` or `BREAKING CHANGE` and explain migration.

Use your own name and email, not the maintainer's identity. Keep one logical change per commit and explain its final behavior and validation. Review tool-generated content; do not invent authorship or co-authorship declarations.

```text
fix(container): preserve cleanup evidence after cancellation

Use a bounded cleanup context so interrupted runs can publish completed
cases when residue verification succeeds.
```

Agents follow [AGENTS.md](AGENTS.md). Real evaluations, publication, and commits require authorization for the actual action.

## Offline validation

Use the Go version in [go.mod](go.mod). Bridge tests also require their Python/Node interpreter. Initial dependency downloads and real container acceptance are separate from offline tests.

```sh
GOPROXY=off GOSUMDB=off go test -count=1 ./...
GOPROXY=off GOSUMDB=off go vet ./...
python3 -B scripts/test_build_case_review.py
python3 -B scripts/test_build_beam_review.py
```

These commands assume dependencies are cached. Optional integration tests should clearly skip unavailable datasets, upstream templates, or container environments. Ordinary tests must not silently use the network or launch real Docker. Container lifecycle tests use a fake CLI; real integration switches stay off unless explicitly authorized.

Choose checks relevant to the change: fake-SDK contracts for adapters, race tests for shared runner/container state, and determinism/missing-value tests for builders. Passing synthetic fixtures does not establish compatibility with a live SDK or model service.

## Adapters and evaluation

External SUTs are installed and executed only inside Linux containers. Do not create host virtual environments, install npm projects, or launch third-party services on the host. See the [adapter guide](suts/README.md). Preserve `describe` limitations and upstream facts, including models, embeddings, network, budgets, and reset policy.

A real workflow may download content, start containers, and incur model charges:

```sh
go build -o bin/memeval ./cmd/memeval
scripts/build_linux_memeval.sh arm64
python3 -B scripts/fetch_upstream_refs.py
bin/memeval sandbox caps
bin/memeval env install suts/<name>/manifest.json
bin/memeval run --suite <suite> --sut suts/<name>/manifest.json --out <out>
```

Choose architecture and Docker context for the actual environment. Acceptance includes coverage, deviations, per-case results, and `residue.run_clean`. Use `env destroy` / `gc`; do not prune shared resources.

## Source and publication boundaries

Keep source code, schemas, protocols, adapters, dependency locks/constraints, generators, user guides, provenance, and license notices. Small source locks, import statistics, and upstream `SOURCES.md` files restore exact inputs; preserve actual path spelling and case.

The following stay local unless independently reviewed for publication:

- Private configuration and credentials; only a reviewed `.env.example` is distributed.
- Reports, bulk datasets, models, cassettes, and generated viewer data, which may contain questions, answers, prompts, or personal information.
- Fetched upstream copies. Keep their restoration process, but retain notices for code or fixtures that are actually distributed or ported.
- Operational handoffs, temporary deployment commands, publication drafts, and one-off authorizations. Public documentation retains technical conclusions, not private transcripts.

Preserve the bundled PMB projection and JSON/JSONL regression references under `docs/`. The P8 image appendix and report UI source/embedded copies have active tests. Update references and validation before moving runtime inputs.

See [third-party notices](THIRD_PARTY_NOTICES.md). Availability for download does not establish permission to redistribute every attachment.

## Documentation

Keep [README](README.md), the [manual](docs-site/index.html), [data](data/README.md), [protocol](protocols/README.md), [adapter](suts/README.md), and [design](design/README.md) guides consistent. English is the primary documentation and UI language. Benchmark text and protocol examples may retain their original language when required to preserve meaning.

Update types, generated schemas, documentation, and contracts together when a format changes. Metric definitions come from `internal/scoring/glossary.go`. Preserve HTML IDs/anchors and generate page data with its builders rather than editing results by hand.

New configuration reads require matching `.env.example` entries and `TestEnvExampleCoverage`. Distinguish offline checks, input downloads, and paid evaluation commands. Review the [publication checklist](design/publication.md) before release.
