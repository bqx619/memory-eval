# Dataset normalization and evaluation protocol

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

Version lineage: v1.0 defined the common model in October 2026; v1.1 retained v2 compatibility; P1 made validation and binding explicit; P2.5 added the streaming bundle container. Dataset-specific implementation references refine this document. Current executable types and protocol declarations are authoritative.

## Separation and provenance

Data importers convert formats without repairing answers, generating examples, or silently changing original text. System adapters receive observable history and requests. Evaluator scoring alone receives gold. Record disputed annotations rather than correcting them. Translations, variants, repeated calls, shared histories, and overlapping benchmarks are not independent extra samples.

Every suite binds source URL, immutable revision, raw file hash, converter, license metadata, data nature, original case IDs, cluster basis, and limitations. Explicit protocol fields define ingestion, cutoffs, reset policy, budgets, time/speaker exposure, reader/judge templates, denominators, exclusions, and fidelity. Scores require n and coverage. Official full fidelity requires matching the relevant upstream protocol; current replacement decision judges remain disclosed and partial.

## Common pipeline

`fetch` creates verified upstream files and `source.lock.json`; `import` deterministically creates a suite and `import.json`; `validate` checks schema, semantics, structural leakage, and counts; registry and `protocols/<benchmark>.json` bind metadata; run/compare/report reuse the common engine.

Fetch pins immutable source revisions, records hash authority, stages and verifies before atomic publication, rejects conflicting locks, and leaves `incomplete.lock.json` on failure. A license acknowledgment records acknowledgment only. Import output is byte-deterministic, with sorted canonical JSON and no generation timestamps. Statistics include case/category/session/event counts, time failures, skipped records and reasons, and explained differences from published scale.

## Suite v3 fields

The v3 header extends v2 with `protocol_ref`, `license`, `data_nature`, `taxonomy`, and `splits`. Provenance may include `derived_from`, `subset`, and `cluster_basis`. Data nature includes `real-user-logs`, `real-derived`, `human-authored`, `model-generated`, `structured-diagnostic`, and `agent-trajectory`.

Protocol fields are `ingestion`, `reset_policy_allowed`, `query_time_policy`, `time_exposure`, `inject_current_date`, `speaker_mapping`, `text_only`, and `haystack{filler_source,context_ladder}`. Ingestion supports per-event/session and declared turn-batch/chunk/document/trajectory/file forms. Declaring a format does not establish runtime support; unsupported semantics must reject rather than silently degrade.

Events preserve `source_id`, `owner`, `role`, `content`, session, optional `turn_group`, timestamps/raw time, modality, attachments `{kind,ref,caption}`, and whitelisted attributes. Roles additionally allow tool; modality includes text, image, screenshot, accessibility tree, and tool result. Timestamp assumptions and speaker mappings are disclosed. A persona description is privileged unless the explicit protocol makes it observable.

Cases add `query_point` (`end_of_history`, `at_index`, `after_session_k`), exclusive-prefix `cutoff`, optional `session_checkpoint`, current-date policy, difficulty, category/type/cluster, budget/options, and private gold.

| Kind | Required private evaluation information |
|---|---|
| `qa` | Answers or abstention, optional checker and evidence |
| `retrieval` | Evidence fields, including explicit empty exhaustive gold |
| `mcq` | Public options plus private choice and parsing/shuffle spec |
| `typed_context` | Typed expectations |
| `rubric` | Items with text, polarity, weight and aggregation rule |
| `set_match` | Phrase set and normalization rules |
| `ordering` | Reference sequence |
| `summary` | Reference and optional rubric |
| `memory_audit` | Memory points, updates, polarity, and a session checkpoint |
| `recsys` | Item IDs; support is explicitly declared |
| `agentic` | Checker specification; reserved unless a runner implements it |

Abstention is `gold.abstain`, not a separate kind. `gold.disputed` stores annotation disputes and optional alternatives. `evidence_refs[{kind,ref}]` supports event/session/text/trajectory and is mutually exclusive with flat `evidence_ids`/`evidence_session_ids`. Resolve text evidence where possible; unresolved references remain disclosed, not invented. Gold includes optional `abstain_reference`, `mcq`, `rubric`, `phrase_set`, `sequence`, `reference`, `memory_points`, `item_ids`, and `checker`.

## Validation and compatibility

Require unique event IDs within episodes, globally unique case IDs, valid cutoffs, evidence before cutoff, stable event ordering, compatible kind/gold shape, valid timestamps or disclosed raw time, and disjoint valid split memberships. V3 rejects unknown fields recursively and strips non-whitelisted public attributes. The leak scan checks structural label keys and identity projection, not answer text naturally present in history. See P1 for identity-specific exceptions that prevent substring false positives without weakening field validation.

V2 spelling, hash, and loading behavior remain stable. New v3-only fields are rejected in a v2 file; an explicit upgrade creates a new hash and breaks pairing with old reports/cassettes. Protocol resolution prefers suite binding, then an exact registry suite match, then an explicit warning for no binding. Upstream revision changes create a new revision-qualified suite ID.

License metadata includes `id`, `noncommercial`, `no_derivatives`, `share_alike`, `redistribute_normalized`, and `source`. A false redistribution flag keeps normalized copies out of distributed artifacts; reports can reference hashes. These are source-specific recorded constraints, not a replacement for source licenses.

## Protocol and registry

Protocol declarations record `protocol_id`, version, upstream repository/commit/file hashes, variants, scoring primary/secondary metrics, judge and reader template references, parsing, denominators, fidelity rules, cluster basis, and overlaps. Template file hashes are checked on resolution. A source-file hash and a rendered/template-set hash are distinct. Fidelity-rule text is not automatically an executable policy engine.

Registry entries record ID/name/repository/dataset URL, data nature, task kinds, protocol, variants, reported scale, license, pinned revision, implementation status, importer, lock, suite references, overlaps, notes, and verification date. Status progresses through registered/importer/imported/run without implying more validation than recorded. Registry checks verify references, locks, suite IDs/hashes, import counts, symmetric overlaps, and explained scale discrepancies. Overlapping LongMemEval and MemoryAgentBench questions must not be added as independent benchmark coverage.

## Container and conformance

New large imports use [bundle storage](p2.5-suite-bundle.md): `suite.json`, `episodes.index.jsonl`, `episodes.jsonl`, and `import.json`. Trusted external roots distinguish a changed header from an authorized new bundle.

Each importer needs deterministic fixture/golden comparison, semantic validation, leakage checks, scale accounting, and an upstream scorer oracle where available. License-restricted inputs use synthetic schema fixtures and separately gated local-data checks. Optional Python oracle skips are reported explicitly. Dataset details and corrections are in P2 (LoCoMo/PersonaMem), P3 (MAB/BEAM/Memora/HaluMem), P4 (PrefEval/ConvoMem), and the findings directory; none may overwrite an older report's facts with a later dataset snapshot.
