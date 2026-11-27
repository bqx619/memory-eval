# P5: A single decision-judge path

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

The October 2026 revision replaced chat and gated judges with the TypeSafe decision judge. Readers still generate text. Deterministic official metrics and aggregation formulas remain independent of the judge provider.

## Mapping and scope

Noul uses `P(true) >= 0.5`, including the boundary, with a fixed cutoff. Record probability, label, request SHA, and returned model. Score questions use the argmax probability level, breaking ties toward the lower level; benchmark score is `level/(Levels-1)`. Preserve normalized native expectation, probability distribution, and confidence separately. Choice questions use the returned choice and retain probabilities/confidence.

This distinction fixes an invalid comparison between continuous expected score and discrete rubric labels. In the recorded 202-response comparison, argmax labels agreed on 0.851 overall (83/91 at zero, 72/73 at one, 17/38 at one-half); it is agreement evidence, not ground-truth accuracy.

The route covers LongMemEval yes/no, BEAM rubric/equivalence, Memora subquestions, PrefEval probes, ConvoMem judgments, HaluMem choices/inclusion, and MAB fluency. LoCoMo token-F1, MCQ accuracy, retrieval metrics, HaluMem aggregate formulas, PrefEval Boolean formulas, FAMA, and BEAM aggregation remain deterministic consumers.

## Request assembly

Keep official instructional wording while removing text-output formatting demands. Data slots belong in structured `state`, never a complete rendered prompt in one field. Examples:

- LongMemEval: question, correct_answer/rubric/explanation, model_response.
- BEAM: rubric_item and llm_response, or first_snippet/second_snippet.
- Memora: evaluation_question and generated_answer.
- ConvoMem: question, reference_answer, response, optional rubric/abstention rules.
- PrefEval: preference, user_query, response, and probe-specific data.
- HaluMem: expected_memory_point, memories, and required context.
- MAB fluency: text.

Choice criteria map options to official descriptions; score criteria are ordered descriptions; noul criteria are nil. Each family records a versioned template ID and instructions SHA. `judge_inputs.jsonl` retains recipe and decision verdict audit data.

PrefEval's former restatement-extraction step cannot be performed by a discriminative model. The `prefeval/noul-hallucinate-v2@sha` recipe directly evaluates any restatement/attribution in the full response, with `{preference,user_query,response}`. It discloses this deviation, and the original formula ignores hallucination when acknowledgment is false.

## Configuration and compatibility

`Router{Decision,DecisionModel}` requires a pinned `jev-x.y.z` and client. Keep `--judge-decision-model` and TypeSafe credentials/configuration. Retired paths include LLM/gated judges, escalation/reference fields, emulated/OpenAI decision codecs, threshold files, judge-agreement, and judge-kind/model/provider/gate/threshold/agreement/cross-model flags. Old reports remain historical evidence; resume rejects incompatible `protocol.judge.kind != decision`.

Report `protocol.judge={kind:"decision",provider:"typesafe",model,cutoff:0.5,multi_valued:"argmax",templates:{family:{id,sha256}}}`. Model-judged official scores have `judge_compatible=false`, explicit replacement deviations, and at most partial fidelity. `judge-decision` is the sole judge billing purpose; readers remain separately accounted.

## Recorded comparison and limits

The recorded rerun reused reader cassettes and changed the judge. Examples were LongMemEval 0.742 to 0.756 with 495 scored and five invalid (label agreement 0.986); BEAM 100-question sample 0.524 to 0.540 (agreement 0.933 on 90 comparable cases); PrefEval generation 50 questions 0.720 to 0.600 (agreement 0.840). PrefEval changed both judge and hallucination procedure, so its difference cannot isolate judge quality. These are historical samples, not fresh validation or general performance claims.

Offline checks cover cutoff boundaries, argmax ties/bimodal distributions, choices, exact state/instructions/criteria identities, removed flags, old-resume rejection, record/replay, 422/401, invalid-response accounting, and environment binding coverage.
