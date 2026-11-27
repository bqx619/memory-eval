# MemoryAgentBench chunker deviation: October 2026

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](../current-contracts.md); source types, schemas, and contract tests define implemented behavior.

The recorded 2026-10-17 comparison used Go `neurosnap/sentences` plus tiktoken against upstream NLTK `punkt_tab` plus o200k_base, at chunk size 4096. The Python comparison mirrored the relevant upstream function because the full module had an unavailable optional dependency. The sample comprises eight conflict-resolution contexts and one context from each of two ruler QA sources; it is not a full-benchmark oracle.

| Source | Upstream blocks | Go blocks |
|---|---:|---:|
| factconsolidation_mh_6k #0 | 2 | 2 |
| factconsolidation_mh_32k #1 | 9 | 8 |
| factconsolidation_mh_64k #2 | 17 | 17 |
| factconsolidation_mh_262k #3 | 67 | 66 |
| factconsolidation_sh_6k #4 | 2 | 2 |
| factconsolidation_sh_32k #5 | 9 | 8 |
| factconsolidation_sh_64k #6 | 17 | 17 |
| factconsolidation_sh_262k #7 | 67 | 66 |
| ruler_qa1_197K #0 | 50 | 50 |
| ruler_qa2_421K #1 | 106 | 106 |
| Total | 346 | 342 |

None of the ten samples had an exact complete block match. All eight conflict-resolution contexts preserved text after whitespace normalization. Numbered-list sentence boundaries and per-sentence trimming changed block boundaries by roughly one to three sentences and block counts by at most one per context. Recorded block token examples were 4395 versus 4319 and 4394 versus 4325; the chunk-size parameter is not proof that every emitted block is bounded by 4096.

The first ruler context differed at an inserted space near a citation marker; the second ruler context's text equivalence was not assessed. This is not evidence of lost content. Different boundaries can change retrieval behavior, so reports disclose partial fidelity and the chunker implementation. A future official-Python path would need independent parity and identity/version handling; it is not claimed here.
