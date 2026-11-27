# Prototype evaluation design

## Separate the questions being tested

Personal memory evaluation spans retrieval, dialogue facts, implicit preferences, updates/deletion, abstention, and agent behavior. These are not one accuracy measure. The prototype established a common observation replay and reporting layer while keeping benchmark-specific scoring separate.

Three execution modes need distinct interpretation: memory retrieval, fixed-reader answers from retrieved evidence, and native system answers. Oracle and full-history controls measure access to information rather than realistic memory quality. A shared answer model reduces one confounder but does not remove differences in representations, source granularity, or context budgets.

## Prevent evaluation leakage

Keep answers, relevance labels, raw case IDs, and categories on the scoring side. Only ingest events available before each query cutoff. Use pseudonymous identities and independent namespaces. Unknown/future/duplicate references are invalid. The early Python plugin protocol was trusted in-process code and did not itself enforce a sandbox; the current framework uses containers.

## Metrics and denominators

Report hit, recall, all-evidence coverage, MRR, nDCG, and explicitly empty-result correctness according to each benchmark's evidence units. Calculate precision only for exhaustive relevance labels. Positive evidence is often not an exhaustive list of every relevant passage. Lexical EM/F1 are diagnostic or benchmark-prescribed measures, not substitutes for semantic judging.

Keep scored, invalid, unsupported, and execution-error counts. Empty assertion sets are unscored. Null memory can pass exclusion assertions, so a single pass rate is misleading. Report positive-evidence recall and empty-result behavior separately. Returned characters, stage timings, and unmetered costs retain their own meaning; absent cost is not zero.

## Fair comparisons

Align source revision, projection, reset policy, metadata access, model roles, budgets, and protocol. State whether the reader receives source events or system-generated memory text. Compare paired questions and report exclusions. Repeated calls are not independent new questions. A one-cluster corpus cannot yield a trustworthy population confidence interval.

LongMemEval/LoCoMo evidence labels, PersonaMem context variants, and overlapping task collections cannot simply be added into a unique-question count. Controlled ablations can identify effects of retrieval or embeddings; cross-system totals with unequal models/coverage cannot identify a causal winner.

## Evidence and limits

The initial snapshot ran 77 PMB projection cases on BM25, recent, and null baselines, with 76 scorable cases and 35 source memories. It catalogued 13 sources, implemented several import paths, and passed 40 software tests. It did not run every catalogued benchmark, install real SUTs, or invoke paid judges. Keep those boundaries when citing its results.

Current contracts extend the original design with protocol identities, image locks, independent cleanup, usage governance, resume identity, and explicit items-mode semantics. See the [current guide](../../design/current-contracts.md).
