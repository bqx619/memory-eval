# Current evaluation contracts and limitations

This guide summarizes the common implementation boundary. Go types, generated schemas, and contract tests define the actual suite, manifest, protocol, and report formats. Historical designs explain decisions but do not override later corrections. Every SUT follows the same input, scoring, isolation, and disclosure rules.

## Data and reader inputs

A suite contains `episodes → events + cases`. Each case may see only events before its cutoff. Gold answers, relevance labels, and scoring references stay in the evaluator. Suite SHA, upstream revision, importer identity, and protocol SHA are recorded separately; see the [data guide](../data/README.md).

Support for ten benchmark protocols does not imply every adapter supports every task. `retrieval` evaluates source evidence; `fixed_reader` answers from retrieved material. `native_answer` remains reserved and is not enabled by the runner. These modes do not establish complete agent task-execution ability.

| Reader input | Material consumed | Attribution and disclosure |
|---|---|---|
| `sources` (default) | Original events expanded from valid references | Untraceable memories cannot support this path; protocol deviations remain explicit |
| `items` | Actual returned item text, with no source backfilling | Explicitly supported for LongMemEval, MemoryAgentBench, and BEAM; disclose the input deviation and never claim full fidelity |

Other benchmarks reject unsupported items paths; `fullcontext` accepts sources only. MAB/BEAM items preserve returned order. The budget counts Unicode characters and separators; overflow drops complete trailing items. Unknown, future, duplicate, or non-string references are invalid in both modes. Missing/null HTTP-mapped provenance means genuinely absent provenance, not a way to erase invalid references. Untraceable or mixed-provenance inputs cannot receive apparently complete retrieval scores. BEAM ordering tasks remain unsupported where the required source structure is unavailable.

Input mode participates in request identity and resume checks. Historical sources cassette keys remain stable; items use a distinct identity. Resume and comparison cannot relabel old sources results as items results.

## Execution and models

External systems use manifest v3, Linux images, and Lock v3. Dependencies, preparation, and model assets are installed inside images. Each runtime episode owns its directory, SUT container, and sidecars. Network modes describe runtime SUT traffic, not image downloads or evaluator model calls:

- `none`: no SUT egress; sidecars share an internal network when needed.
- `gateway`: a run-scoped, single-target HTTP governor joins episode networks and meters the configured target.
- `open`: external connectivity is allowed; no destination allowlist is enforced.

LLM configuration precedence is explicit flags, manifest overrides, then environment configuration. Embedding, SUT generation, reader, and judge are different model roles. Matching model names do not imply equal budgets, inputs, or SDK forwarding. Local embeddings and directly hosted embeddings must be disclosed separately.

`--parallel` accepts 1–16. Independent fresh-per-case jobs can run concurrently; cases inside incremental episodes stay ordered. Workers own independent adapters. An explicit governor concurrency flag/environment value is a total limit; otherwise the limit is `(manifest llm.concurrency or 8) × parallel`. These settings do not guarantee third-party clients cannot time out.

Cleanup must establish stopped sidecars and no run residue. Cancellation uses an independent, bounded cleanup context. A clean interrupted run can publish completed results; actual residue or unproven isolation still rejects scoring. `sandbox_timeout` is an episode resource timeout, not automatically an isolation breach.

## Results, evidence, and resume

Reports retain scored, error, invalid, and unsupported statuses with their denominators. Missing costs are null, not zero. `internal/scoring/glossary.go` is the metric-definition source; viewer-derived metrics state their separate formulas.

Resume retains old results and history, reruns selected statuses, and optionally narrows selection with `--resume-cases`. A SUT identity change requires `--resume-sut-change` and records previous/current identities. Retained old rows are not evidence for the new SUT version. Reader mode and protocol compatibility remain mandatory.

Compare suite, reader input, reset policy, models/embeddings, budgets, coverage, and attempt identity. Official rubric scores, declared-reference coverage, direct-source evidence recall, and text actually delivered to the reader are distinct measures. A small shared corpus such as PMB does not justify a population-level ranking.

## Viewers and diagnostics

Cards and case viewers use common report fields. SUT-specific traces are optional explanations of build/retrieval behavior, not scoring prerequisites. Specialized viewers retain the relevant system name to identify their format; this does not confer preferred status.

Traces, source text, answers, full prompts, provider responses, and logs may contain sensitive or licensed material. Source publication and artifact publication are separate decisions; see [SECURITY](../SECURITY.md).

## Known limits

- Pinned templates and large datasets may need to be restored separately. Missing-input skips are not acceptance of the corresponding live behavior.
- Some SDKs lack time filters, dump, or reasoning-effort control. Use actual `describe` output and adapter facts, not an assumed universal capability.
- The data-flow implementation includes a generic local-embedding note. Direct-embedding variants must also be checked against their manifest, `sut.declared.embedding`, and network disclosure.
- Provider versions, concurrency, and retries can affect output and latency. Model-backed experiments are not deterministic oracles.
- Static viewers require generated data. Local artifacts do not automatically belong in a source release.

## References

[Adapter guide](../suts/README.md) · [protocol types](../internal/protocol/types.go) · [manifest schema](../schemas/sut-manifest.v3.json) · [benchmark protocols](../protocols/README.md) · [data contract](dataset-protocol.md) · [bundles](p2.5-suite-bundle.md) · [containers](p8-container-platform.md) · [governance](p9-throughput-governor.md) · [answer inputs](p12-answer-input-contract.md) · [cards](p10-results-site.md) · [case review](p15-case-review.md) · [BEAM review](p16-beam-review.md).
