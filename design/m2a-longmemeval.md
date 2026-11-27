# M2a: LongMemEval import and retrieval protocol

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

This reference records the 2026-10-16 import design. Later suite-container and judge changes are defined in P2.5 and P5.

## Pinned inputs

The vendored MIT evaluation code is pinned to `9e0b455f4ef0e2ab8f2e582289761153549043fc`. The cleaned dataset is `xiaowu0162/longmemeval-cleaned` at `98d7416c24c778c2fee6e6f3006e7a073259d48f`.

| File | Bytes | SHA-256 |
|---|---:|---|
| `longmemeval_s_cleaned.json` | 277383467 | `d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442` |
| `longmemeval_oracle.json` | 15388478 | `821a2034d219ab45846873dd14c14f12cfe7776e73527a483f9dac095d38620c` |

The M variant was outside this milestone. Fetch uses an immutable revision, a staging directory, size/hash verification, and atomic publication. For HF LFS files, the tree API `lfs.oid` is the SHA-256 authority; an Xet ETag or `x-linked-etag` is not a content SHA-256. Non-LFS files retain length and locally computed hash without claiming a remote strong hash. `--verify-only` verifies local content. `source.lock.json` records `dataset_id`, `source_url`, `revision`, `license_ack{by,at}`, `files[{path,bytes,sha256,etag}]`, and `fetched_at`. Conflicting locks require an explicit replacement; failed transfers leave `incomplete.lock.json`. A license acknowledgment is not a commercial-use grant.

## Deterministic conversion

Each question becomes an episode and a cluster identified by `question_id`. This is an episode clustering convention, not proof of independence: questions can share a filler pool. Session order is stable and chronological; ingestion is `per_session`. Event IDs are `<session_id>:<turn_index>`, with user/assistant roles and owner `user`. Dates in `YYYY/MM/DD (www) HH:MM` are interpreted as UTC. Failed parses retain `time_raw` and increment diagnostics.

`has_answer` becomes private `gold.evidence_ids`; `answer_session_ids` becomes private `gold.evidence_session_ids`. Neither label enters public events. Evidence is session-based and non-exhaustive. All references must resolve. The original question and date become `query` and `query_time`; date failures remain private diagnostics. The six upstream question types and answer text are preserved. `_abs` identifies abstention. The cutoff includes the complete history. Provenance includes source revision, local content hash, converter version, and a revision-qualified suite ID.

## Retrieval metrics

Expand item citations, deduplicate first-seen event IDs, then derive unique sessions. Turn metrics are `recall_any`, `recall_all`, and `ndcg_any@5/10/50`; session metrics are `recall_all` and `ndcg_any@5/10`, with effective k measured after session deduplication. The upstream DCG is `rel[0] + sum(rel[i]/log2(i+1), i>=1)`; an ideal gain of zero yields zero. Only scored, non-abstention questions enter retrieval denominators. Report these separately from generic memory-only metrics.

The upstream reference retrieval budget is top-k 50 and 200000 characters; the general framework default is 10 and 100000. Budget changes are protocol changes. Cluster bootstrap requires at least two clusters.

## Recorded import facts and validation limits

The pinned S release contains 500 questions, including 30 abstentions: 133 multi-session, 133 temporal, 78 knowledge-update, 70 single-session-user, 56 single-session-assistant, and 30 preference questions. All 23867 session dates and 500 question dates parsed in the recorded import. These are pinned-input counts, not claims about future releases.

Conformance uses deterministic fixtures and an optional upstream Python oracle over at least 200 rankings, with tolerance `1e-9`. Missing oracle dependencies cause an explicit skip. Fixture parity does not establish a real SUT result.
