# P4: Preference and evidence-conversation benchmarks

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

PrefEval and ConvoMem use deterministic bundle imports, private gold, declared templates, registry hashes, and synthetic conformance fixtures. Recorded data licenses are CC-BY-NC-4.0. P5 replaces the historical chat/gated judges and explicitly changes PrefEval hallucination handling.

## PrefEval pinned release and conversion

The source revision is `50795054b5ff5f418d2b768a331d71e480f93331`. Twenty explicit topics contain 1000 tasks, with 31-62 per topic rather than a fixed 50. The choice/persona forms also contain 1000 tasks. Native JSON dict/list values are the actual release shape; restricted Python-literal parsing is a compatibility fallback, not the primary representation.

MCQ input has an orphan `travel_hotel copy.json` (52 rows) among 21 files/1052 rows; the canonical 20-topic table excludes it. Thirty MCQ preference texts differ from explicit-generation input; preserve MCQ source text rather than replacing it from the other form. Classification options have four entries, with the original first entry correct.

Each task is an episode `<topic>:<index>` and clusters by topic (20 clusters). Explicit history contains the preference user message and omits the upstream dynamically generated acknowledgment, recording that deviation. Implicit-choice history contains query, assistant options, user selection, and acknowledgment; latent preference/explanation/aligned option remain private. Implicit-persona dialogue expands numeric keys in numeric order; persona and latent preference remain private.

Filler is the original ordered concatenation of 24 LMSYS conversations (632 messages). Take the first `2*inter_turns` messages and require an assistant final message; zero means no filler. IDs distinguish preference/filler events and turn groups. Time exposure is none; ingestion is per event and reset fresh per case. The suite ID records form/task/turn count/revision. Initially registered combinations were explicit generation at 10 and 300 inter-turns, explicit MCQ at 10, and implicit-choice generation at 10.

Generation cases use original question, full cutoff, private preference reference, and rubric probes `acknow`, `violate`, `hallucinate`, `helpful` with aggregate `prefeval`. MCQ uses deterministic shuffled A-D options, gold choice, shuffle seed/order, and `prefeval_choice_tag`. A Go shuffle is not Python `random.sample`; disclose its distinct order instead of claiming identical upstream randomization.

## PrefEval reader and formulas

Generation uses the pinned helpful-assistant system message and question suffix requesting a response within 300 words. Sources are original-order retrieved events; full context uses all allowed events. The explicit remind variant appends the pinned reminder and is a distinct protocol; CoT is not implied. MCQ uses the pinned option format and assistant `<choice>` prefill; unsupported prefill is removed only with disclosure. Historical output limits are 300 for generation and 5 for MCQ.

The original chat procedure extracted an acknowledged preference, then evaluated the other probes using pinned tag templates. P5 instead performs four structured noul decisions, directly judging hallucination in the complete response without extraction. Preserve this protocol difference.

Let `ack` be acknowledgment, `hall = ack and hallucinate`, `viol` be violation, and `unhelpful = not helpful`. Then:

- `inconsistent = ack and not hall and viol and not unhelpful`.
- `hallucination_of_preference_violation = ack and hall and viol and not unhelpful`.
- `preference_unaware_violation = not ack and viol and not unhelpful`.
- Accuracy is the negation of the union of those three errors and unhelpful.

Emit the main accuracy and four error rates by topic. Judge failure is invalid and excluded with coverage disclosed. MCQ parsing uses the first A-D inside the first `<choice>` tag; missing tags are incorrect, not guessed from prose. Formula tests cover all 16 Boolean combinations and optional upstream parsing oracles.

## ConvoMem pinned release and scope

Data revision `e3e9b39115b02346824c70d349350de738f8be41` contains 906 evidence-question files, 56273 items: abstention 14910, assistant_facts 12745, changing 18323, implicit_connection 7546, preference 2749. Earlier 880-file observations (54498 items) were incomplete and are not final denominators. Code is pinned to `624f582ecf0d336ae1d4539d19186089800774b1`, Apache-2.0, with vendored Scala templates.

This revision contains evidence conversations only, all marked `containsEvidence=true` in the observed data; it lacks premixed filler/persona resources. The official 1-300-conversation context ladder is therefore not reproduced. Preserve that limitation in every suite/report. `message_evidences` is a native list, with literal-string fallback only for compatibility.

An evidence item becomes episode `<type>:<n>:<personId>:<index>`, clustered by personId. Conversation/message order is preserved; User/Assistant map to user/assistant; unknown speakers reject. Sessions are stable local IDs, source IDs include conversation/message position, and there is no time exposure. Structural labels, scenario/model metadata, checkpoints, and evidence flags remain private. Anonymized identity fields are validated exactly before substring scanning, avoiding accidental short-ID matches in UUIDs without exempting text fields.

Each item has one full-history query. Assistant-facts/changing use QA with default judgment. Implicit-connection uses the rubric template according to the upstream generator (correcting an initial default-template assumption). Abstention has `abstain=true` and no ordinary answer; preference is a rubric with `aggregate=convomem_rubric`. Checkers use `convomem_judge` and default/rubric/abstention template parameters. Resolve evidence by exact speaker+text; multiple matches take the first with a count/disclosure, unresolved evidence stays text. Ingestion is per session, reset fresh per case, and haystack metadata states no filler at this revision.

## ConvoMem reader and judgment

Render original-order messages in `Conversation k:` blocks under pinned MemoryPromptUtils/LongContextMemoryAnswerer templates. Gold evidence is judge-only. The original parser lowercases/strips output: both right and wrong means false, right-only true, wrong-only false, neither triggers bounded upstream retries then invalid. P5 replaces this text response with structured noul under the corresponding instructions; official Gemini provenance is retained and fidelity is partial.

Emit `convomem.correct` by type and evidence-conversation count. Import statistics cover files/items/type-by-n counts, conversation/message distributions, invalid speakers, and exact/multiple/unresolved evidence. Optional local checks use `MEMEVAL_PREFEVAL_INPUT`/`MEMEVAL_CONVOMEM_INPUT`; fixture/oracle skips and fake-service record/replay do not establish live model results.
