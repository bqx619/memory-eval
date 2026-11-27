# P18: A matched-embedding baseline and citation-granularity correction

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

This reference records the November 2026 Mem0 raw-memory baseline with the same embedding model declared by CogletNet runs. It separates configuration evidence, measured outcomes, and later corrections to retrieval interpretation. Deployment hosts, private endpoints, account events, and execution handoffs are omitted.

## Experimental purpose and baseline

Existing Mem0, Hindsight, and Cognee controls already used embeddings, generally local `BAAI/bge-small-en-v1.5` at 384 dimensions. CogletNet declared OpenAI `text-embedding-3-large` at 3072 dimensions. BM25/null used none. Thus the experiment controls the embedding-model difference, not the presence versus absence of embeddings. Mem0 raw mode was selected because it already supported configurable embedders and `infer=False`, avoiding a new extraction-model confound. Popularity counts are unnecessary to the technical comparison.

## Generic adapter configuration

`bridge.config.embedder` supports fastembed (default) and openai, rejecting unknown providers. Fields are provider, model, dims, base_url, api_key_env, and max_input_chars. The recorded openai variant uses `text-embedding-3-large`, 3072 dimensions, `max_input_chars=32768`, and a configurable credential variable. Missing credentials are errors. Embedding model/dimensions propagate both to the provider and vector store.

For `infer=False`, explicitly construct the unused LLM client with `memeval-placeholder-no-llm` so it never receives the real embedding credential. Embedding truncation changes only the text sent for vectors; stored original memory remains intact. Apply a character ceiling, then halve input on recognized size errors until success or the 512-character floor. Batch failure isolates individual inputs. Recognized size errors include case-insensitive `maximum (context|input) length` and `too many tokens`; other 400 errors are not retried as size errors. Describe/dump expose configuration, limitations, and `embed_stats`.

The initial matcher recognized only context-length wording and missed the provider's `maximum input length` error, causing an entire episode to fail. The bounded retry correction is generic API handling, not a dataset-specific exception. Character length cannot guarantee token length: whitespace-heavy and dense inputs have very different ratios. Recorded 8192-token and local 512-token limits describe those versions, not permanent capabilities.

The manifest uses `variant=raw-openai-embed`, explicit embedding declaration, no required SUT LLM, and open networking for the remote embedding service. Open mode is general egress, not an enforced single-provider allowlist. Adapter and manifest changes affect installation input hashes. Fixtures test provider settings, missing keys, placeholder LLM isolation, truncation/halving/batch isolation, and metadata; they do not call a real service.

## Recorded run protocol and limitations

The BEAM 100K run used 400 questions, incremental episodes, fixed reader with `items`, top-k/context settings matching controls, reader `gpt-6-luna` with 2048 output tokens, and decision judge `jev-1.14.0`. A fixed seed and report hashes identify the comparison. Preflight validates daemon/provider availability and a scored evaluator smoke before installation; it does not imply permanent service health. Resume preserves all earlier attempts.

Final recorded status was 399 scored, one invalid, zero errors, with three resumes. The invalid question `3:preference_following:0` was an empty reader response, also present in the local-embedding control. A size-error fix recovered 20 ingestion errors. The older resume implementation recorded manifest changes but missed an adapter/image change when the manifest stayed constant (F109); this limits source-change audit completeness and must not be concealed.

## Results under the corrected direct-source definition

These are historical report/viewer derivatives, not a new run. A score mean uses scored samples; evidence recall additionally requires nonempty gold.

| Variant | Judge mean (n) | Direct-source evidence recall (n) |
|---|---:|---:|
| Mem0 raw local embedding | 0.5208 (399) | 0.4653 (354) |
| Mem0 raw OpenAI embedding | 0.5148 (399) | 0.4838 (354) |
| CogletNet legacy v6 | 0.5081 (400) | 0.5748 (355) |
| CogletNet leaf-embedding variant | 0.5375 (400) | 0.4007 (355) |
| CogletNet LeafMemory variant | 0.4909 (400) | 0.2435 (355) |
| BM25 | 0.5024 (400) | 0.4066 (355) |

For Mem0, changing embedder increased recall by about 0.019 and changed judge mean by about -0.006. These aggregates alone do not establish a causal or statistically significant general quality claim. Other system mechanisms remain different even with a matched embedder.

## Why the original comparison was corrected

The earlier 0.9112/0.8336 figures for leaf-embedding/LeafMemory counted complete summary citation closure. They are now named `beam.citation_coverage`, not direct-source recall. The corresponding early audit counted about 183.3/154.7 cited events per question, versus 10 for Mem0. Larger closure monotonically raises coverage without proving the original evidence reached the reader. The final viewer uses direct events represented by returned L0 leaves and reports closure separately, as defined in P16.

Do not mix the audit's denominators with final viewer metrics: the early fanout audit averaged all scored questions including no-gold cases and sometimes treated zero-citation precision as zero; the viewer aligns fanout with scored nonempty-gold cases and treats zero-citation precision as undefined. Final leaf-embedding recall includes two empty retrieval cases (n=355), explaining the earlier nonempty-only 0.4030 at n=353 versus 0.4007. LeafMemory similarly changes 0.2470 at n=350 to 0.2435 at n=355.

Configuration evidence includes report `sut.describe.config.embedder`, declared embedding, vector-store dimensions, and adapter wiring for both ingest and search. A dimension check or one provider error alone does not prove model quality. The recorded input audit found only two gold references in unusually long events; truncation cannot by itself explain the full historical coverage gap. This remains a limited diagnostic, not a universal bound on embedding effects.

## Publication and reproducibility

Retain report hashes, protocol/reader/judge settings, case coverage, resume history, and known deviations. Pair comparisons on shared valid cases and compatible protocol, displaying denominator differences. Derived viewer metrics never mutate source reports. Do not publish local launch commands, credential locations, private hostnames, account status, or shared-host cleanup instructions. Container ownership and cleanup follow P8; this experiment does not authorize global shared-cache deletion.
