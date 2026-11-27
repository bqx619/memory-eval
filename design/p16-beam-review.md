# P16: BEAM review payloads, reader reconstruction, and evidence metrics

<!-- documentation-status: reference -->
> Technical reference. Historical runs are examples, not current operational instructions. See [current evaluation contracts](current-contracts.md); viewer builders, source types, and contract tests define implemented fields.

The BEAM viewer separates dataset facts, run summaries, per-question answers, and optional work traces. It is an offline derivative of immutable reports and permitted suite text. Historical revisions corrected citation-closure comparisons; this reference states the final direct-source rule and retains the prior metric as explicitly labeled coverage.

## Dataset and deterministic export

The pinned `beam-100k@3205395e897e` suite contains 20 episodes, 400 questions (20 per episode), 5732 events, and ten categories of 40: abstention, contradiction_resolution, event_ordering, information_extraction, instruction_following, knowledge_update, multi_session_reasoning, preference_following, summarization, temporal_reasoning. Data attribution is BEAM, Mohammad Tavakoli et al. (2025), HF `Mohammadta/BEAM`, CC-BY-SA-4.0. Revision, suite hash, converter, license, and limitations come from source evidence, not hard-coded guesses about other versions.

Cases are rubric-based; event_ordering also has `beam.event_ordering.final`. Historical displayed runs used Jev 1.14.0 and a fixed reader, but run metadata is authoritative. Gold/rubric is evaluator-only; displayed gold is post-hoc evidence. Character truncation differs from upstream token limits.

`python3 -B scripts/build_beam_review.py --suite <bundle> --run <name>=<report-dir>... [--work <name>]... [--glossary <json>] [--llm-endpoint <base-url>] --out <dir>` writes JSON and `window.MEMEVAL_BEAM = {...};`. Output uses sorted keys, two-space indentation, and Unicode-preserving JSON. `generated_at` comes from the newest recorded start/resume timestamp, never build wall time. Same inputs produce identical bytes. Missing facts remain null. The builder invokes neither models nor containers.

## Main payload v1

This shape is a field specification; placeholders are not measured results.

```text
{ version: 1, generated_at, memeval_commit|null,
  dataset: { suite_id, suite_sha256, source{attribution, revision, url, converter}, license{id, share_alike, redistribute_normalized},
             protocol{benchmark{protocol_id, version, variant, sha256}, judge{provider, model, cutoff}, reader{model, max_tokens, input}},
             limitations[], categories[{id, n}], episodes[{id, cluster_id, events, bytes, cases, sha256}] },
  questions: [ { id, episode_id, cluster_id, category, question_type, kind, difficulty, cutoff, query, query_point,
                 rubric: [{text, polarity, weight}] | null, evidence_ids[], aggregate } ]
  runs: [ { name, report_path, report_sha256, run_id, run_seed, sut{name, variant, network}, framework_version, started_at_utc, wall_seconds,
            resume_count, resume_history[{at, statuses}]|[], interruption{reason, unscheduled}|null,
            status_counts, metrics{ "beam.llm_judge_score"{mean,n}, "beam.event_ordering.final"{mean,n} },
            by_category{ <cat>: {status_counts, metrics{...}} }, by_episode{ <episode_id>: {status_counts, mean_score} },
            billing{ sut{prompt_tokens, calls}|null, evaluator{prompt_tokens, calls}, native{calls, input_tokens, output_tokens}|null },
            isolation{ backend, network_mode, run_clean, containers_remaining }, failures_by_class{<class>: n} } ]
  results: { <run name>: { <question id>: { status, error_type|null, failure{class, native_kinds[], message}|null,
              score|null, judge_label|null, items: [{expectation, confidence, probabilities}] | null,
              event_ordering|null, answer|null, reader_abstain, truncated, finish_reason, completion_tokens, returned_items, returned_chars,
              timings{ingest_ms, query_ms, answer_ms} } } }
  glossary: [MetricDef...]
}
```


Questions are sorted by numeric episode ID then question ID. Rubric, evidence IDs, and aggregate come from suite gold. Per-question `answer` is `qa.hypothesis`; judge items are `qa.judge_details[].outcome`; score and ordering come from their named metrics. Returned-item count comes from recorded response items. Failure classification uses native/RPC evidence from the corresponding episode/attempt, with transport/evaluator fallback or null. Episode means use scored cases. Repeats greater than zero contribute to aggregate status/metric accounting but do not create duplicate question cells.

Run readers are additionally preserved as `runs[].reader`; resume history, SUT-change facts, interruption, and attempt provenance remain visible. No run should silently inherit metadata from another run merely because its display name resembles it.

## Work files and namespace/attempt identity

`--work` requires a supported SUT and available stderr traces; invalid requested sources fail explicitly. Reuse case-review WorkTrace logic rather than duplicating mappings. Build seq aligns to episode events and is checked by event-text SHA; mismatch is null with a count. Search queries match in recorded order within an episode; no unrecorded search is fabricated. Corrupt trace lines increment `corrupt_lines`.

Each work file registers under `(window.MEMEVAL_BEAM_WORK = window.MEMEVAL_BEAM_WORK || {})["<run>/<episode-key>"]`. A matching JSON file contains the same payload. Text tables contain exactly referenced hashes, with no missing or unused text.

```text
{ version: 1, run, episode_id, namespace, trace: "full|off|missing",
  log: { path: "logs/<dir>/sut.stderr.log", rows: {build, search, config}, corrupt_lines },
  config: { work_trace, jev_threshold },
  events: [ { source_id, seq, role, timestamp, turn_group, chars } ],
  builds: [ <WorkTrace.build description> ],
  searches: { <question id>: { ...<WorkTrace.search description>, builds_before: n } },
  mapping_mismatches: n,
  texts: { <hash>: <text> } }
```


```text
runs[].work = { config{work_trace, jev_threshold}, episodes: { <episode_id>: { file: "work/<run>/<ep>.js", trace, builds, searches, corrupt_lines,
                 states{Ready, Failed, NotDispatched, ...}, levels{ "0": n, "1": n, ... }, edges{SummaryParent, ContextFallback} } } }
results[run][qid].work = {
  trace: "full|missing|off|corrupt",
  file: "work/<run>/<ep>.js", search: bool, builds_before: n|null,
  evidence: [ { source_id, written: "Ready|Failed|NotDispatched|Unknown|after_query|null", nodes: k,
                best: { node, level, origin, similarity, vector_rank, p_use, outcome, rank } | null,
                stage: "not_written|not_candidate|capped|jev_rejected|not_packed|selected" } ],
  stage: "no_search|no_evidence|not_written|not_candidate|capped|jev_rejected|not_packed|partial|covered",
  covered: [selected, total], candidates: n, items: n, omitted: { <reason>: n }, jev_batches: n,
  context_tokens: n|null, degraded: bool, warnings: [] }
```


Payload version 2 adds node `body`, ordered `input_summaries[{body,output_hash,model,attempts,faithful,reason_hash}]`, build `pending{level:[summary-item]}`, candidate `copied_span`, `input_tokens`, and `failure`; and top-level `build_config` plus `build_config_mismatches`. Build config records chunk_low/high/target, text limits/max levels, leaf/upper/candidate limits, memory tokens, threshold/model/embedding/generator, and summary settings. Episode summaries include pending counts. Existing fields retain their meaning.

Resume can create a new namespace and reingest an entire incremental episode. Group by `(episode,namespace)`; choose the namespace with most questions as the main file, tie-breaking by suite order. Additional namespaces use `-r1`, `-r2`, etc., with `namespace_index` zero for the main file. Each question's work.file points to its own attempt/namespace; loaders must not rebuild the key from episode ID alone. Run episode metadata adds files/namespaces, sums repeated build counts, and uses trace `mixed` when states differ. Tree item matching also uses the current question's namespace/attempt.

## Work-stage diagnostics

For each gold event, the furthest observed stage is selected, then not_packed (result_limit/context-bundle-too-large), jev_rejected, capped (candidate/evidence cap), not_candidate (Ready but no candidate), or not_written (missing/failed/late build). Choose the best node by furthest stage, then probability, then similarity. A question is no_search with no search, no_evidence with empty gold, covered when all are selected, partial when some are selected, otherwise the furthest stage reached by any evidence. Keep omitted-reason counts.

This funnel can count a selected summary associated with an evidence event, unlike the direct-L0 metric below. It describes the SUT's recorded process and may precede evaluator/reader budget filtering. Do not label funnel coverage as gold text delivered to the reader. Evidence overlay is post-hoc only, never tuning input or an SUT constraint.

Build structure follows P15: source/snapshot forest, real SummaryParent versus ContextFallback edges, independent candidate/selection/context dimensions, pending summaries, and no invented global root. Legacy tree and later LeafMemory/Tree representations have distinct trace contracts; an unavailable usage claim is shown as unavailable.

## Exact reader prompt reconstruction

Response-only cassettes do not store a request body. Reconstruct from the report's primary case `response.items[].text`, never from a similar native trace text. For BEAM items input, concatenate item text in rank order with a single newline; Unicode character accounting includes separators. Drop whole trailing items until the context budget fits. With no items, omit the history message. Append the pinned reader_note with query substituted exactly, preserving whitespace.

Use actual reader provider/model/output budget. Template ID is `beam/reader-v1@<template-sha>/input=items` (hash before input suffix), as implemented in `reader_template_id`. Request identity serializes the following ordered wire fields with Go JSON escaping, not sorted pretty JSON:

```text
Provider, Endpoint, API, Model, Template, Client, Sample, Purpose,
Messages, Temperature, MaxTokens, N, Extra
```

`Endpoint` is configured base URL with trailing slash removed and `/chat/completions` appended; `API="chat/completions"`, `Client="memeval-llm-v1"`, Purpose reader, temperature 0, N 1, Extra null. Sample is the evaluator pseudonym from the matching reader ledger row, not raw case sample_id. Use the last matching reader attempt metadata, not a judge row or the sum of retry usage. Reconstructed requested temperature can differ from a disclosed provider fallback.

`--llm-endpoint` or configured evaluator base URL is used only for verification and never exported; missing endpoint/ledger means verified null with reason. Invalid endpoint errors must not echo credential-bearing URLs. A mismatch is verified false, not permission to alter text until it matches.

```text
searches[qid].reader_prompt = {
 template_id,provider,model,temperature,max_tokens,n,input_mode,context_budget_chars,
 items_returned,items_in_prompt,dropped_items,truncated,chars,
 messages:[{role,content}],request_sha,verified:true|false|null,verify_note,
 ledger:{prompt_tokens,completion_tokens,latency_ms,ts,model_returned}|null,
 reader_output:{finish_reason,completion_tokens,reasoning_tokens,effective_max_tokens,...}
}|null
```

Add reader_prompt even when the search entry is otherwise missing. No reader request SHA means null. Compare reconstructed truncation with recorded QA truncation; `dropped_sessions` is not automatically an item count. Preserve full messages without trimming. Historical verification of 1600 prompts is recorded evidence, not a fresh claim from this documentation change.

## Direct-source evidence metrics and citation coverage

For scored questions with nonempty gold G, let D be deduplicated direct sources and C the full declared citation union. `beam.evidence_recall=|D intersect G|/|G|`; `beam.evidence_precision=|D intersect G|/|D|` when D is nonempty; F1 is `2PR/(P+R)`, zero when both defined values are zero. No gold or unexecuted cells yield null. Zero-return retrieval with gold has recall zero and precision/F1 null, not missing recall.

Cells contain `evidence_hits`, `evidence_recall`, `retrieved_ids` (direct count), `evidence_precision`, and `evidence_f1`. Full closure uses `cited_ids`, `cited_hits`, and `citation_coverage`; run metric `beam.citation_coverage` is `|C intersect G|/|G|`. It is secondary diagnostic coverage and cannot establish comparable retrieval quality across different citation fanouts.

Run/category/episode aggregates are `{mean,n}` over scored, defined samples, including repeats where aggregates do. `beam.source_ids_per_question` and `beam.source_ids_episode_share` use the same nonempty-gold denominator as recall; share divides by that case's visible-event cutoff, not an unrelated complete-history count. Precision/F1 exclude zero-source cases, so their n may differ. With no samples retain null mean/n per viewer contract. No-gold cells can still display source counts, but not evidence precision. Derive counts of no-gold questions from the suite; for this pinned suite it is 45 (40 abstention, four summarization, one preference).

### Direct-source extraction rules

1. Ordinary single-event items use their source IDs, mapped from anonymous IDs to original `retrieved_source_ids` using first-seen order. Detect mapping length disagreement and warn. Older supported single-source reports without response items use the recorded source list.
2. Text beginning with `leaf-memory-context-v1` is parsed as its declared JSON representation. Only L0 `metadata.origins[*].event_sequence` maps to suite events; verify `info[0]` is a substring of the corresponding original event. Invalid origins/content warn and contribute nothing; never fall back to summary closure.
3. Tree representation aligns each report item ID with native `search.items[].node` for the current namespace/attempt to obtain level. Only level 0 counts its own leaf event. Level 1+ is summary closure and belongs only in citation coverage. Missing/corrupt/mismatched trace warns and conservatively counts only verified single-source items; bad UUID mapping never falls back to closure.

`refresh_tree_metrics` runs after trace extraction and updates cells plus all group aggregates. Work tree metadata distinguishes `tree.direct_sources` from `tree.covered_sources`. An adapter may still declare broad sources for a valid summary item under the evaluator contract; raw report recall/hit/nDCG can therefore remain closure-based. Viewer derivatives do not silently rewrite source reports.

Historical corrections illustrate denominator/meaning changes: leaf-embedding 0.9112 citation coverage became 0.4007 direct recall (n=355), LeafMemory 0.8336 became 0.2435 (n=355). Excluding empty retrievals had incorrectly produced 0.4030 (n=353) and 0.2470 (n=350). Tree v9 closure 0.6386555 became direct recall 0.5432530 (n=354), with precision 0.2443759 (n=353); closure stayed unchanged. These are historical examples, not current rankings.

## Embedding declaration and display

Normalize `report.sut.declared.embedding` into `runs[].sut.embedding=null` or `{provider,model,dimensions,local,declared}`. Objects accept strings for provider/model, integer dimensions (not Boolean), and Boolean local; wrong types warn and become null. An empty object represents a declaration with no details. Nonempty strings are trimmed and stored only as declared, never parsed for a word such as local. Missing/null/empty string becomes null; unrelated types warn.

Overview grouping uses actual declaration: local===false is API embedding; any other declaration is local/other; null is no embedding. Order groups API/local-other/none and sort within each by judge mean. Do not special-case project/run names. A missing embedding key in all older data restores legacy ungrouped display. Overall best-score cards and judge rankings still use all runs, independent of grouping.

## Pages and verification

`beam.html` is the overview; `beam-run.html?run=<name>` shows one run and all 400 question cells; `beam-coglet.html?run=<name>&q=<id>` shows optional work details. Shared script provides metric definitions, links, filters, and panels. URL ref/stage/status/cat/ep/q and `#q=<id>` preserve navigation; a complete case ID is an exact filter, avoiding substring collisions. Unknown runs show a selection list.

Show scored mean beside all-question mean (unscored counted as zero only for that explicitly labeled aggregate), status distribution, count at score one, failure classes, resume/attempt sources, measured usage, and paired comparisons on a disclosed common sample set. Display recall with direct-source count, precision/F1, n, and secondary citation coverage. Old data without new metrics hides those controls. Work pages separate construction from retrieval overlays, show actual input summaries/pending/config, and offer full prompt text in an expandable section without substituting gold.

Contract tests cover deterministic output, nulls, ordering/deduplication, namespace resumes, corrupt traces, exact text references, stage cases, prompt hash vectors, direct-source counterexamples, all aggregation denominators, and embedding types. Browser/theme/mobile rendering requires its own observed checks. Historical test counts and rendered artifacts do not substitute for a fresh UI check.
