# Ten memory benchmarks: raw formats and official scoring (2026-10-16 survey)

This historical field-level survey used raw files, dataset cards and scoring sources to guide importer design. Field names came from inspected files; inferred mappings are identified below. It is not a claim that every surveyed benchmark is implemented. Current registry/protocol declarations take precedence for supported execution. License names are recorded source facts, not legal determinations about a particular use or transformed copy.

## 1. LoCoMo

Source: snap-research/locomo, `data/locomo10.json`, CC BY-NC 4.0, commit cbfbc1d.

- The top-level array contains 10 conversations with sample_id, conversation, qa, event_summary, observation and session_summary.
- conversation contains speaker_a/speaker_b, session_N turn arrays and session_N_date_time (for example `"1:56 pm on 8 May, 2023"`). Its 5,882 turns contain speaker, dia_id (for example D1:3) and text; optional fields include img_url, blip_caption (1,226), query (888) and re-download.
- The 1,986 QA records contain question, evidence[dia_id…], category 1–5 and answer (1,542 present; six integers); one malformed evidence string is `D8:6; D9:17`. adversarial_answer appears in 446 category-5 records, usually without answer. Category counts are 1=282, 2=321, 3=96, 4=841, 5=446. The mapping 1=multi-hop, 2=temporal, 3=open-domain, 4=single-hop, 5=adversarial was inferred from counts and scoring code, not printed in the README.
- Privileged fields: adversarial_answer, evidence, observation, session_summary, event_summary, query and blip_caption when images are supplied as images.
- `task_eval/evaluation.py` normalizes commas, case, punctuation and a/an/the/and, then computes Porter-stem token F1. Categories 2/3/4 use F1 (category 3 gold stops before `;`). Category 1 splits on commas and averages each gold item's maximum F1. Category 5 scores 1 only if the prediction contains `no information available` or `not mentioned`. Retrieval recall is the fraction of evidence dia_ids in prediction_context. Reports order categories [4,1,2,3,5] plus an overall result. Excluding category 5 and adding an LLM judge is not this official script.
- Unit: QA case; cluster: conversation (10). Integration challenges include multimodal turns, adversarial answers and generated summaries used as retrieval units.

## 2. PersonaMem v1

Source: HF bowen-upenn/PersonaMem-v1, revision prefix a8076d56, MIT.

- `questions_{32k,128k,1M}.csv` has 15 columns: persona_id, question_id, question_type, topic, context_length_in_tokens, context_length_in_letters, distance_to_ref_in_blocks, distance_to_ref_in_tokens, num_irrelevant_tokens, distance_to_ref_proportion_in_context, user_question_or_message, correct_answer (for example `(c)`), all_options (Python list literal, four entries prefixed `(a) ` through `(d) `), shared_context_id (SHA-256), end_index_in_shared_context. The 32k set has 589 rows, 20 personas and 37 contexts.
- Recorded type counts: recall_user_shared_facts 129; track_full_preference_evolution 139; recalling_the_reasons_behind_previous_updates 99; suggest_new_ideas 93; generalizing_to_new_scenarios 57; provide_preference_aligned_recommendations 55; recalling_facts_mentioned_by_the_user 17. The original survey incorrectly said eight types while listing these seven (total 589). The seven-type count is confirmed by `data/normalized/personamem-v1-32k@a8076d5608c9/import.json` (`by_question_type`); the recorded card count was also seven.
- Each `shared_contexts_*.jsonl` line is `{"<shared_context_id>":[{role,content},…]}`. Entry zero is a privileged system persona. User/assistant contents include `User: `/`Assistant: ` prefixes; there are no timestamps or session markers. Other personas' blocks appear as distractors.
- Protocol: ask after `context[:end_index_in_shared_context]`; one context may have multiple cut points. The prompt requests (a)–(d) after `<final_answer>`; the set matched by `\(([a-d])\)` must equal correct_answer. Report by question_type.
- Unit: row; cluster: persona_id or shared_context_id. Privileged fields: system persona, distance_*, num_irrelevant_tokens and correct_answer.

## 3. HaluMem

Sources: MemTensor/HaluMem; HF IAAR-Shanghai/HaluMem, revision prefix cb04336a, CC-BY-NC-ND-4.0.

- HaluMem-Medium.jsonl is 33.5 MB; Long is 107 MB. Each line is a user with uuid, persona_info, sessions[], total_dialogue_token_length, total_question_count and token_cost. Sessions have start_time/end_time (for example `Sep 04, 2025, 18:42:18`), memory_points[], dialogue[{role,content,timestamp,dialogue_turn}] and questions[].
- A memory point contains index, memory_content, memory_type (Persona/Event/Relationship Memory), memory_source (primary/secondary/interference/system), is_update (string True/False), original_memories[], timestamp and importance. Interference points additionally contain reason and reference_memory_*.
- Questions contain question, answer, evidence[{memory_content,memory_type}] (content references, not IDs), difficulty and question_type: Basic Fact Recall, Multi-hop Inference, Dynamic Update, Memory Boundary, Memory Conflict, Generalization & Application. Boundary questions have empty evidence.
- Protocol: process users/session checkpoints; add every 20 turns with chat_time, dump newly extracted memories after each session, search update points by content, then answer that session's questions using top_k=20. The upstream answer model was GPT-4o.
- Upstream judge: GPT-4o. Completeness uses 0/1/2 (2=fully covered); recall is the score-2 fraction among non-interference points, with a weighted variant using 0.5×score×importance. Accuracy uses 0/1/2 for target precision; combine into F1. FMR is the fraction of interference points with completeness 0. Update labels: Correct/Hallucination/Omission/Other; QA labels: Correct/Hallucination/Omission.
- Units: memory point, update pair or question; clusters: 20 users plus session checkpoints. Privileged fields: persona_info, memory_points, memory_source, importance and question.evidence. Integration requires dump/search(text); interference points are negative gold. Consult upstream license text for use/distribution questions rather than inferring a blanket rule from the ND label.

## 4. MemoryAgentBench

Source: HF ai-hyz/MemoryAgentBench, revision prefix 7ea06698, MIT, 146 parquet rows.

- Rows contain context (string), questions[str], answers[[aliases]] and metadata{demo,haystack_sessions,keypoints,previous_events,qa_pair_ids,question_dates,question_ids,question_types,source}. Sources: AR=ruler_qa1_197K/ruler_qa2_421K/eventqa_*/longmemeval_s*; CR=factconsolidation_mh_6k; LRU=infbench_sum_eng_shots2 (100)/detectiveqa (10); TTL=recsys/icl_*.
- LongMemEval rows encode context as a Python literal list combining `Chat Time: 2022/11/17 (Thu) 12:04` and role/content turns. Questions begin with `Current Date: … Now Answer the Question:`. haystack_sessions contains 60 sessions of content/has_answer/role turns. question_ids reuse LongMemEval IDs, including `_abs`. This overlaps LongMemEval-S and must be registered/deduplicated in combined comparisons.
- Protocol: ingest all chunks before multiple queries. Configurations use chunk_size=4096; the paper's AR/CR settings use 512. Each chunk receives a remember-this wrapper. Scoring: substring_exact_match for AR/CR, exact_match for detectiveQA/ICL, Recall@5 for recsys, GPT-4o judgments for the LongMemEval subset, and InfBench F1 plus fluency for summaries.
- Unit: question; cluster: row/context. Privileged fields: has_answer, keypoints, previous_events unless included in the question, and question_types.

## 5. BEAM

Sources: HF Mohammadta/BEAM revision prefix 32053953 and BEAM-10M 9b209619; data CC BY-SA 4.0, code MIT. These are survey abbreviations; use source.lock.json for the actual pinned revisions.

- Rows contain conversation_id, conversation_seed{category,id,subtopics,theme,title}, narratives, user_profile{user_info,user_relationships}, conversation_plan, user_questions[{messages,time_anchor}] and batched chat. Turns contain content, globally integer id, index, question_type (main_question/followup_question/answer_ai_question/null), role and time_anchor (for example March-15-2024 or null).
- probing_questions is a Python literal string parsed with ast.literal_eval. Categories: abstention, contradiction_resolution, event_ordering, information_extraction, instruction_following, knowledge_update, multi_session_reasoning, preference_following, summarization, temporal_reasoning. Common fields are question, difficulty, rubric[] and source_chat_ids[]; categories add ideal_response/ideal_answer/answer/ideal_summary and others.
- Official answering flattens turns into role/content, trims the history tail to the model limit and appends `NOTE: Only provide the answer without any explanations. Question: {query}`; it injects no current date. `gpt-4.1-mini` with temperature 0 scores rubric items 0/0.5/1, averaged per case. Event ordering additionally computes `(tau_b+1)/2 * f1`; results are grouped by category.
- Unit: probing question; cluster: conversation (100). Privileged fields: user_profile, conversation_plan, narratives, conversation_seed, user_questions, turn question_type/index/time_anchor, and every probing field except question.

## 6. ConvoMem

Source: HF Salesforce/ConvoMem, revision prefix e3e9b391, 27.5 GB, data CC-BY-NC-4.0; code Apache-2.0.

- Layout: `core_benchmark/personas/personas_default.json`; 100 `filler_conversations/<uuid>_<Persona>.json`; `evidence_questions/<type>/<n>_evidence/<personId>_<Persona>.json`; and 826 `pre_mixed_testcases/<type>/<n>_evidence/batched_NNN.json`. Types are user_evidence, assistant_facts_evidence, changing_evidence, abstention_evidence, preference_evidence and implicit_connection_evidence; n=1..6.
- Evidence files contain evidence_items[{question,answer,message_evidences[{speaker,text}],conversations[{messages[{speaker,text}],id,containsEvidence,model_name}],category,scenario_description,personId,…}] and checkpoint. Premixed cases add contextSize. Messages have neither timestamps nor IDs; ordering provides chronology.
- Protocol: render N evidence/filler conversations as `Conversation k:` plus `speaker: text`, with a ladder from 1 to 300. Modes include long context, block extraction and Mem0. The Scala judge defaults to Gemini flash, uses type-specific templates and parses contains(right)/contains(wrong). Criteria cover exact facts, preference rubrics, inverted abstention logic and latest changed facts. Multi-evidence recall is secondary.
- Unit: evidence item × contextSize; cluster: personId (100). Privileged fields: containsEvidence, message_evidences, scenario_description, category and generation model names.

## 7. Memora

Source: geniesinc/Memora, Apache-2.0, no HF dataset in this survey.

- Ten personas under `data/{weekly,monthly,quarterly}/<persona>/conversations/session_NNNN.json`, plus evaluation_questions_<persona>.json. Sessions contain session_id, session_type (observed no_memory/activity/preference, differing from README labels), operation (null/add/update; the paper also discusses delete), operation_details, date (for example 2025-06-01), persona and conversation[{turn,speaker,message,share_memory}], with speakers ai_agent/user_agent.
- Question files contain persona, date_range and questions{remembering[],reasoning[],recommending[]}. Each question has question_id/question/question_date/memory_evidence/forgetting_evidence/evaluation. Evaluation subquestions have id/evaluation_question/expected_answer (yes/no)/evaluation_type (memory_presence/forgetting_absence).
- Track 1 concatenates history as `## Session {id} - {date}`, keeping the most recent content under 200k. Questions start `Today's date is {question_date}`. Track 2 ingests entire sessions sequentially with user_id=<persona>_<period>, then searches with limit=50 and answers using gpt-4o-mini.
- Three judges (GPT-4.1, Claude Haiku 4.5, Gemini 2.5 Flash) vote yes/no per subquestion. Metrics: MPA, FAA, lambda=N_forget/(N_presence+N_forget), FAMA=max(0,MPA−lambda×(1−FAA)).
- Unit: question with evaluation subquestions; cluster: persona×period. Privileged fields: session_type, operation, operation_details, share_memory, memory_evidence, forgetting_evidence and evaluation. Challenges: negative nuggets that must not appear, per-question penalty weights and current-date injection.

## 8. PrefEval

Sources: amazon-science/prefeval, CC BY-NC 4.0; HF siyanzhao/prefeval_*.

- Layout: benchmark_dataset/explicit_preference/<topic>.json (20 topics), implicit_preference/choice-based or persona-driven/<topic>.json, mcq_options/<topic>.json, rag_retrieval/ and filtered_inter_turns.json (LMSYS fillers).
- Explicit records contain preference/question/explanation. Choice-based records add implicit_query/options[4]/conversation{query,assistant_options,user_selection,assistant_acknowledgment}/aligned_op. Persona-driven records add persona and conversation keyed by turn number with user/assistant values. MCQ records contain preference/question/explanation/classification_task_options[4]; the aligned option is stored at index 0 and shuffled at runtime.
- Protocol: expose preference or implicit conversation, insert --inter_turns fillers (5/10/30/70/300 turns), then ask; there are no timestamps. Generation uses Claude 3 Sonnet (Bedrock) yes/no acknowledge/hallucinate/violate/helpful probes, combined into unhelpful, inconsistent, hallucination_of_preference_violation and preference_unaware_violation. preference_following_accuracy requires all four errors to be absent. Classification uses MCQ accuracy.
- Unit: preference/question pair (1,000 × three forms); cluster: topic. Privileged fields: implicit-form preference, explanation, aligned_op and option alignment.

## 9. LongMemEval-V2

Source: HF xiaowu0162/longmemeval-v2, revision prefix f152293e, Apache-2.0, 7.12 GB. Surveyed, not an implemented current importer.

- questions.jsonl has 451 records: id, domain (web/enterprise), environment, question_type (static-environment/static-environment-abs/dynamic-environment/dynamic-environment-abs/procedure/procedure-abs/errors-gotchas), question (requests `\boxed{}`), image, answer and eval_function. Evaluators include norm_phrase_set_match, its ordered form, mc_choice_match, mc_choice_set_match, llm_abstention_checker and llm_gotchas_checker with parameters.
- trajectories.jsonl has 1,870 records: id/domain/environment/goal/outcome/start_url/states[{state_index,step,url,action,thought,accessibility_tree,screenshot}]. Haystacks are small (100/question) or medium (about 500).
- Protocol: memory.insert(trajectory), then memory.query(question,image), returning text/image fragments truncated to token limits. The reader is Qwen3.5-9B; unknown answers use `\boxed{UNKNOWN}`. Timing is measured. String evaluators plus gpt-5.2 judge abstention/gotcha cases; UNKNOWN is forced incorrect. The leaderboard reports LAFS (latency–accuracy frontier).
- Unit: question; cluster: domain/environment haystack. Challenges: trajectories, accessibility trees, screenshots, question images, per-question evaluator specifications and latency as a primary dimension.

## 10. MemoryArena

Source: HF ZexueHe/memoryarena, revision prefix da1a37c8, CC-BY-4.0. Surveyed, not an implemented current importer.

- `<config>/data.jsonl` counts: bundled_shopping 150, progressive_search 221, group_travel_planner 270, formal_reasoning_math 40, formal_reasoning_phys 20.
- Rows contain id/questions[subtask instructions]/answers/category or base_person or paper_name+backgrounds. Shopping answers use target_asin/attributes, travel uses daily_plans, and others use strings.
- Protocol: a memory–agent–environment loop, one session per subtask, storing action/observation under task_id scope. Metrics are SR/PS/sPS, with ASIN exact matching, constraint checks, LLM judgments or numeric matching.
- Unit: task with subtask sessions; cluster: environment.

## Cross-benchmark requirements identified for protocol v1.0

- Case families: qa_free, qa_mcq, abstain, rubric (negative polarity and lambda), set_match, ordering, summary and memory_audit; reserve recsys and agentic.
- Gold shapes: answers[], mcq{options,correct,shuffle,parse}, abstain{reference}, rubric{items[{text,polarity,weight}],aggregate,lambda}, sequence[], phrase_set{items,ordered,norm}, memory_points[], item_ids[], checker{type,params}. Evidence may refer to turn IDs, session IDs, message text or trajectory IDs.
- Protocol dimensions: ingestion turn/turn_batch(n)/session/chunk(tokens,tokenizer,wrapper)/document/trajectory/file; reset per_case/per_cluster/per_user_period/shared_haystack; query end_of_history/at_index/after_session_k/per_subtask_session; time exposure none/session/turn and formatting; current-date injection; role mapping and system-persona visibility; modalities; budgets; reader/judge templates and parsing; denominators/exclusions; original category labels; cluster basis; filler pools and context ladders.
- Register overlap explicitly: MAB includes LongMemEval-S; ConvoMem contains a LoCoMo runner; BEAM-10M contains BEAM.
- Recorded license families: HaluMem CC BY-NC-ND; LoCoMo/ConvoMem/PrefEval CC BY-NC; BEAM CC BY-SA; MemoryArena CC BY; PersonaMem/MAB MIT; Memora/LongMemEval-V2 Apache-2.0. Consult the pinned source records and license texts for scope and conditions; the survey does not infer distribution permission from those labels alone.
