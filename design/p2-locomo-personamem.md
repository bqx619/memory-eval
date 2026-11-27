# P2: LoCoMo and PersonaMem conversion and scoring

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

The milestone added LoCoMo text-only and PersonaMem v1 32k, using P1 metadata/conformance and later P2.5 bundle storage. Public inputs preserve upstream text; private labels and parsing details remain evaluator-only.

## Pinned sources

LoCoMo is pinned to `3eb6f2c585f5e1699204e3c3bdf7adc5c28cb376`, with CC-BY-NC-4.0 data and vendored evaluation scripts under `design/upstream/locomo`. PersonaMem data revision is `a8076d5608c93ba2a28983cd78aa99b01a163ae7`; its MIT code is pinned to `d07e6ade22e85e0e5d562247323a9c3e07553226`. The NLTK Porter reference and license remain under `design/upstream/nltk`. Optional oracle execution is configured with `MEMEVAL_ORACLE_PYTHON`, without prescribing a personal environment path.

## LoCoMo import

Each conversation is an episode and cluster. Sessions follow numeric session order; turns retain their original order and `dia_id`. Both speakers are human, so events use owner `conversation`, role `user`, and a whitelisted speaker attribute. Ingestion renders `{speaker}: {content}`. Session dates use `3:04 pm on 2 January, 2006`, interpreted as UTC with the original text retained. Twelve-hour noon/midnight boundaries must parse correctly.

The text-only variant excludes image URLs, captions, download metadata, and image queries, counting and disclosing omissions. Cases use `<sample_id>:<question_index>`, original question, full-history cutoff, original numeric category and canonical category label. Categories 1 through 5 map to multi-hop, temporal, open-domain, single-hop, and adversarial. Ordinary answers are preserved as strings, including numeric answers. Category 5 has `abstain=true` and no answers; `adversarial_answer` is excluded entirely.

Evidence normalization only makes unambiguous structural changes: split semicolon/whitespace-separated references and normalize clear syntax such as `D:11:26` to `D11:26`. Unresolved/nonexistent references are excluded from resolved IDs but retained verbatim with handling in `gold.disputed.note`, statistics, and limitations. Messages, questions, and answers are never repaired. Evidence is event-based and non-exhaustive. Checker type is `locomo_official` with category parameter.

Pinned-input counts: 10 conversations, 272 sessions, 5882 turns, 910 image-bearing turns, 1986 questions; category counts 282/321/96/841/446; six integer answers; 444 absent/null answers; 2815 original evidence references; all 272 session dates parsed. The later full scan found multiple malformed references beyond the initially noticed semicolon case; do not repeat the first-sample count as a complete audit. Synthetic fixtures cover NC data shapes; optional local-data checks use `MEMEVAL_LOCOMO_INPUT`.

## LoCoMo scoring and reader

Normalization removes commas, lowercases, removes Python's 32 ASCII punctuation characters, removes whole Unicode-boundary words `a|an|the|and`, and collapses Unicode whitespace. Token F1 stems with NLTK Porter `NLTK_EXTENSIONS`, uses multiset overlap, and yields zero when no tokens match. Category 1 splits prediction/gold by commas, takes each gold fragment's maximum F1 against predicted fragments, then averages. Categories 2/4 score the whole answer. Category 3 uses the first semicolon-delimited gold fragment. Category 5 accepts the phrases `no information available` or `not mentioned`. Report category-aware `locomo_official.f1`, category F1, abstention accuracy, and upstream overall aggregation without conflating their denominators.

The reader mirrors vendored conversation/date/QA templates. Speaker order follows explicit speaker fields rather than an unstable set iteration. Sources are grouped by session and original turn order. Full-context truncation drops oldest complete sessions under the character budget, disclosed against upstream token/turn truncation. Record the template hash. There is no model judge for deterministic LoCoMo F1.

Oracle checks compare at least 500 prediction/gold/category cases at tolerance `1e-12`, including Unicode boundaries, punctuation, lists, empties, and numbers; Porter checks include at least 2000 words. Missing optional oracle dependencies are explicit skips, not passes.

## PersonaMem import and privileged variants

The 32k release has 589 questions, 37 shared contexts, 20 personas, and seven question types. A context becomes an episode; cluster is persona. Validate that one context belongs to one persona. Events preserve original system/user/assistant role and content, including `User:`/`Assistant:` prefixes. There are 195 system messages; repeated system persona descriptions occur at context boundaries.

The `official` variant includes persona system messages as the upstream long-context protocol does. `memory_only` excludes every system event and recalculates cutoff as the count of retained messages before the original end index; it is a separate suite with `-nopersona` suffix and cannot claim upstream comparability.

Cases use `<persona_id>:<question_id>`, `kind=mcq`, original question, `query_point=at_index`, and exclusive cutoff `end_index_in_shared_context`. Parse `all_options` as JSON or a restricted Python string-list literal supporting quotes and escapes, not executable Python. Keep option prefixes verbatim. Map A-D for the public request; store the original cell as private `attributes.options_raw` because the official reader appends that exact string. Gold choice is normalized from `correct_answer`; `shuffle=false`, parse `personamem_final_answer`. Distance/context-size diagnostics never enter the SUT.

The reader appends the original instruction to return an option after `<final_answer>`. Parsing examines text after that marker, strips its closing tag, lowercases, first finds parenthesized a-d then standalone letters, and accepts only the singleton correct-option set; if necessary it repeats on the complete response. Store `answer_parse{raw_final,options_found}`. Accuracy is split by original question type. No judge call is needed.

Recorded answer distribution is 143/146/158/142. Question types count 129 recall_user_shared_facts, 139 track_full_preference_evolution, 99 recalling_the_reasons_behind_previous_updates, 93 suggest_new_ideas, 57 generalizing_to_new_scenarios, 55 provide_preference_aligned_recommendations, and 17 recalling_facts_mentioned_by_the_user. After removing the message-role prefix, 139 questions match context[end] and 64 match context[end-1]; raw equality is a separate diagnostic. These facts do not justify shifting cutoffs.

## Integration and overlap

Reader selection follows the bound benchmark. `ingest_render` supports only speaker/content placeholders and affects the public-input hash. Compare accepts the deterministic scorer metrics under normal protocol checks. LoCoMo prediction exports retain upstream-readable QA shape. Full fidelity additionally requires complete relevant question sets, matching templates/variant, no truncation, and scorer parity; NC restrictions remain attached.

MemoryAgentBench contains five LongMemEval rows of 60 distinct questions each, totaling 300 IDs from the 500-question S release. The initial 60-question observation covered only one row. Keep this overlap separate from independent benchmark coverage.
