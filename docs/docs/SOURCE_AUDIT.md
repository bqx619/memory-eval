# Prototype source and measurement audit

## Acquired sources

The PMB seed, 77 single-turn cases, scoring implementation, and MIT notice were read at revision `61a712a1754e7d27b6dee42da32a2c214b003ece`.

| Source file | Git blob SHA | Included form |
|---|---|---|
| fixtures/beliefs.seed.json | 2d9c9daf55c075f0f64a9a9f71db61feda7ad4c2 | Field projection of 35 records |
| fixtures/retrieval.cases.json | dd0ee63d91b9b19f4c891463ab104bb2ec0485bd | 77 questions, IDs, categories, assertions, budgets, and user scopes |
| fixtures/session-retrieval.cases.json | 86c0895b252e8c4722c5d1659071af2649a5b6c3 | Metadata only; 12 session assertions not run |

Original bytes were not directly downloadable in the initial build environment. The projection was transcribed field-by-field from pinned retrieved content, not generated questions. Byte-level equivalence was not established; `verify-projection` compares against original files when restored. Upstream blob IDs and local SHA-256 values must not be conflated.

## Transformations

Seed fields retained: id, owner, type, canonical_name, aliases, content, scope, epistemic_status, pinned, superseded_by, resolved_at, user_edited, participants. Missing participants become an empty list; missing flags become false; absent supersession/resolution fields become null. Confidence, reinforcement, why_it_matters, original dates, and some provenance were omitted, so their ranking effects are not evaluated.

Cases retain original questions and assertions, omitting description/notes. Where prose and JSON assertions differ, the actual assertions are used without implicit repair. Original source spellings and phrases remain unchanged.

## Deviations from upstream

1. The 12 additional session assertions are excluded.
2. Local baselines assemble personaPrelude themselves rather than using the fixed upstream persona service.
3. Empty assertion sets are unscored rather than automatically passing.
4. Precision is not calculated without exhaustive relevance labels.
5. Source IDs are pseudonymized for the SUT and restored by the evaluator; raw question IDs/categories are not sent.
6. BM25 is a public baseline, not an implementation of a particular commercial product's full retrieval policy.

## Recorded evidence

Reference JSON/JSONL preserves per-case results and protocol conditions. Initial software verification had 40 tests. Three baselines replayed 77 cases each: 231 executions, not 231 unique questions. There were 76 scorable cases, 43 positive-evidence cases, 32 explicitly empty-relevance cases, one non-exhaustive exclusion case, and one case without valid assertions.

No real agent products or paid model calls were run in this prototype snapshot. The [upstream scoring source](https://github.com/tenurehq/precisionMemBench/blob/61a712a1754e7d27b6dee42da32a2c214b003ece/src/retrieval.external.eval.test.ts) applies mustInclude/mustExclude to the pinned/relevant union and shouldOnlyInclude to the relevant set only; persona substring matching is case-sensitive. Local tests cover these distinctions.
