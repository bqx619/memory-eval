# Octop lexical retrieval on LongMemEval: October 2026

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](../current-contracts.md); source types, schemas, and contract tests define implemented behavior.

A recorded 2026-10-16 diagnostic found session recall-all@5 of 0.12 on a 50-question subset, with turn recall zero. On the 500-question run, session recall-all was 0.051 and hit 0.243; the recorded BM25 session reference was 1.00 under its protocol. These are protocol-specific observations, not a general product ranking.

A direct bridge check of release 1.0.1 on case `e47becba` used 53 sessions and 550 events, per-session capture, raw-always storage, and `maxResults=10`. All 550 accepted events retained framework source IDs; the ten returned raw hits had valid provenance and no evaluator truncation. That check narrowed the issue to retrieval/query behavior rather than input loss.

The lexical route searches separately for query tokens with a `limit*2` request and retains first-seen hits. An early common token can fill the candidate list. Its named BM25 rerank in this route was reciprocal rank `1/(1+rank_index)`, not a whole-question BM25 score; raw-memory importance/confidence were constant, old-event recency was negligible, and tokenizer behavior lacked stopword removal/stemming. A keyword-focused query surfaced the relevant turn among the first two results in the inspected example.

The evaluator therefore preserves the original question and discloses the variant's query handling. It must not silently rewrite benchmark queries to fit an upstream expectation of agent-generated keywords. A keyword-extraction or atom-extraction variant would be a separate, explicitly controlled protocol with paired evaluation. One inspected case does not establish that such a variant improves the complete benchmark.
