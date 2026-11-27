# P3: Chunked histories, rubric scoring, and memory audits

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

This reference covers MemoryAgentBench, BEAM, Memora, and HaluMem. Imports stream through bundle writers, emit deterministic statistics, bind pinned template hashes, validate semantics/leakage, and register trusted roots. P5 supersedes the original chat/gated judge routes described by this milestone; replacement decision judging remains partial fidelity.

## Pinned inputs

| Dataset | Data revision | Code revision | Recorded license |
|---|---|---|---|
| MemoryAgentBench | `7ea066982b140a19337e17e60d45d4076e042faf` | `538026089d1a8a8eff05121d0db89b388f360eba` | MIT |
| BEAM | `3205395e897e7318c7b094ef4e6047b9b82dbb03` | `b2da22eac88bb0874c64665f13457eb99835774a` | Data CC-BY-SA-4.0; code MIT |
| Memora | `a6493188efc836d6511ed5e4163fe3ba87da30ff` | Same repository snapshot | Apache-2.0 |
| HaluMem | `cb04336aa1b732d4b24f5186c552456b4099806e` | `718f16ff0c83413b1c86fa83fc13cc1a639871f9` | CC-BY-NC-ND-4.0 |

HaluMem normalized derivatives remain local with `redistribute_normalized=false`. Restricted fixtures use synthetic data and gated real-input statistics. Source files and licenses remain under their vendored upstream directories.

## MemoryAgentBench

A parquet row is an episode/cluster `<source>#<row_index>`. Split row counts are 22 accurate-retrieval, 8 conflict-resolution, 110 long-range-understanding, and 6 test-time-learning. Long text is sentence-segmented with the Go Punkt implementation, counted with o200k_base (including the upstream special-token convention), and greedily joined into declared token chunks. Events use `<episode>:c<index>`, observation role, and chunk turn group. The memorize wrapper preserves upstream text; nondeterministic wall-clock time is replaced with an empty timestamp and disclosed. Go sentence boundaries differ from NLTK; see the chunker finding, and do not claim exact chunk parity.

LongMemEval rows instead parse nested Python list/dict literals and preserve session/turn structure and private evidence. Five such rows contain 300 distinct questions overlapping LongMemEval-S; export `mab-longmemeval-overlap` separately, outside MAB independent totals. The final correction is 300, not the initial one-row count of 60.

Questions/answer aliases select checkers: `mab_substring_exact_match` for ruler/fact consolidation; `mab_eventqa_recall` for event QA; `mab_exact_match` plus source-specific postprocessing for detective/ICL; `mab_recsys_recall@5` for recommendation IDs; `mab_infbench_sum` for ROUGE-1/2/L and fluency; `mab_longmemeval_judge` for the overlap subset. Keypoints, previous events, question types, demos, original question IDs/dates, and pair IDs remain private metadata or gold. The SUT gets the original query; reader-specific RAG query wrappers are applied only by the evaluator.

`per_chunk:<tokens>:o200k_base:mab_memorize` means imported events already are chunks: the runner does not split again. `incremental_per_episode` resets/ingests once and runs ordered queries only for an adapter declaring read-only queries. Sources-mode reader uses ranked original chunks; full context uses the upstream long-context wrapper and disclosed whole-block character truncation. P12 later adds actual returned-item input for supported readers.

Scorers mirror pinned normalization, answer-prefix parsing, source-specific cleanup, exact/substring matching, recommendation extraction, and candidate matching. Optional upstream Python oracles compare at least 500 cases; unavailable dependencies skip explicitly. Raw block counts and token lengths expose chunking deviations rather than repairing them silently.

## BEAM

Each conversation is one episode/cluster. Flatten chat batches into original turn order with role, `turn_group=batch<k>`, and optional time anchor parsed at UTC midnight. Privileged profile/plan/narrative/seed/question metadata never enters events. The 1M release repeats some turn IDs across batches: retain all turns with stable occurrence suffixes, expand evidence references to all occurrences, and disclose ambiguity (150 repeated IDs were recorded). Numeric conversation IDs use exact anonymous-identity validation rather than substring scanning inside hashes.

Expand probing questions by ten categories; case IDs are `<conversation>:<category>:<index>`, cutoff is full history, and rubric items preserve `polarity=as_written`, weight 1, aggregate mean. Event ordering stays `kind=rubric` and additionally stores reference sequence plus checker `beam_event_ordering`. Ideal summaries remain private references. Map source chat IDs to non-exhaustive event evidence. Ingest per event, expose available turn dates, and do not inject a current date.

The imported 100K release has 20 conversations and 400 questions, ten categories of 40. The 500K and 1M releases each contain 35 conversations. The advertised complete benchmark includes a 10M release not fetched in this milestone; do not substitute its 100-conversation/2000-question scale for the 100K suite.

Rubric scores are means of per-item 0/0.5/1 judgments; P5 maps decision distributions by argmax. Event ordering splits answer lines, greedily aligns each to the first unused equivalent reference, computes set P/R/F1 and tie-corrected Kendall tau-b, then `(tau_b+1)/2 * F1`. A nonfinite upstream tau is reported null with deviation, not serialized NaN. Semantic MiniLM alignment is not implied; this design follows upstream LLM alignment. Optional SciPy oracle checks ties and edge cases.

The reader mirrors role/content history plus the pinned answer-only question note. Sources retain history order; character truncation differs from upstream token limits and is disclosed. Detailed viewer and later direct-source metrics are in P16.

## Memora

Episodes are persona-period pairs (ten personas across weekly/monthly/quarterly); cluster is that pair. Sessions sort by ID and carry date at UTC midnight; `user_agent` maps to user and `ai_agent` to assistant. `share_memory`, operation labels/details, and session type stay private. Questions cover remembering/reasoning/recommending, retain question date, inject current date, and use full-history cutoff. Rubric subquestions encode expected yes/no as must-contain/must-not-contain, weight, and `group=memory_presence|forgetting_absence`; aggregate is `fama`. Text evidence is resolved where possible and otherwise remains an unresolved text reference.

Incremental per-session ingestion mirrors the memory track. Score subquestions against expected labels, then preserve upstream MPA/FAA/FAMA, lambda, zero-presence/forgetting branches, and default FAA=1 where specified. Emit `memora.mpa`, `memora.faa`, `memora.fama`, and all-subquestions `memora.question_correct`, with task breakdowns. The original three-judge majority protocol is not reproduced by a single replacement judge.

Sources-mode reader renders numbered memories with two-decimal relevance; absent score becomes 0.00 with disclosure. No memories returns the fixed upstream insufficient-information sentence without a model call. Full context uses the upstream dated session rendering and recent-session truncation. Historical temperature/output limits are protocol metadata, not guarantees that every provider accepts them.

## HaluMem audit and checkpoints

The public adapter capability `dump` returns stable `{items:[{id,text,timestamp?,layer?}]}` for all current namespace memories. Missing capability marks only memory-audit cases unsupported; QA can still execute. Baseline dump reflects ingested events (null returns none). A raw-only adapter discloses that it performs no memory extraction.

Each of 20 users is an episode/cluster. Events preserve sessions and dialogue turns, parse `Jan 02, 2006, 15:04:05` as UTC, and retain raw time. Persona, memory points/source/importance, and question evidence stay private. Ingest per session, matching the pinned upstream runner rather than assuming a universal 20-turn batch.

Each session creates a `memory_audit` at `after_session_k`, cutoff exactly after that session. Gold points record ID/content/type/importance/update relations and `should_not_exist` for interference. Each question creates QA at the same cutoff with six original types; boundary questions with no evidence abstain. `session_checkpoint` and query time prevent future history from being visible.

At each checkpoint: ingest, ready, dump, and diff by memory ID against the previous dump to identify new memories. Update points retrieve their text at top-k 10; QA retrieves at top-k 20 before the pinned reader. Judge families are integrity, accuracy, update, and question. Integrity uses levels 0/1/2, recall for full inclusion, importance-weighted partial inclusion, and interference accuracy; accuracy evaluates newly extracted memory against observable dialogue and gold. Extraction F1 combines target accuracy and recall. Update labels are Correct/Hallucination/Omission/Other; QA labels are Correct/Hallucination/Omission.

Preserve upstream mutually exclusive routing: an update point with nonempty retrieved memories uses update evaluation; otherwise integrity. Empty dump integrity is zero, not missing. Null is reserved for unsupported/judge failure and excluded from valid denominators. Zero aggregate denominators become null with disclosure. Generated-QA sessions with empty memory points still ingest/ready/dump and update the checkpoint, but are unscored and excluded from official denominators; the recorded Long release has 1030 such sessions.

Audit call volume scales with points, new memories, updates, and QA; report measured usage separately. Synthetic fake-provider fixtures validate formulas and state transitions without claiming full real-data execution or provider quality.
