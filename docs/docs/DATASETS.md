# Historical memory-benchmark source inventory

Reviewed 2026-10-15. Listed does not mean downloaded or executed. Sizes are upstream claims; actual imported counts come from import manifests.

| Dataset | Source type | Purpose | Prototype support |
|---|---|---|---|
| [LongMemEval cleaned](https://github.com/xiaowu0162/LongMemEval) | Curated questions over synthesized/assembled timestamped user-assistant histories; not a released dump of real PA conversations | fact recall, cross-session reasoning, updates, temporal, abstention | collector + JSON importer + schema unit tests; full dataset not acquired/run |
| [LoCoMo](https://github.com/snap-research/locomo) | LLM-generated multi-session dialogue benchmark with curated QA, not real-user logs | episodic recall, multi-hop, temporal, abstention, multimodal optional | collector + strict text-only importer + schema tests; full data not acquired/run |
| [PersonaMem v1](https://github.com/bowen-upenn/PersonaMem) | Synthetic personas and generated multi-session conversations | preference recall, preference updates, preference application, personalized MCQ | 32K collector + CSV/JSONL importer + schema tests; full data not acquired/run |
| [PersonaMem v2](https://github.com/bowen-upenn/PersonaMem-v2) | Synthetic personas and implicit-preference conversations | implicit preferences, evolving preferences, privacy requests | source registry only; use original dataset and runner |
| [PersonaMem v3](https://github.com/bowen-upenn/PersonaMem-v3) | Real-source-derived: anonymized engagement histories from GIST; generated personas and simulated app histories are derivative, not untouched real logs | implicit behavior, personalization, proactiveness, privacy, overpersonalization | source registry only; no data redistributed or evaluated |
| [MemoryAgentBench](https://github.com/HUST-AI-HYZ/MemoryAgentBench) | Heterogeneous existing datasets plus controlled synthetic tasks | accurate retrieval, test-time learning, long-range understanding, conflict resolution | source registry only; incremental replay adapter required |
| [PrecisionMemBench](https://github.com/tenurehq/precisionMemBench) | Upstream-authored synthetic software diagnostic fixture | alias retrieval, scope, supersession, cross-user negative checks, budget, session noise | 77-case source field projection bundled and run; session scenarios not bundled/run |
| [BEAM](https://github.com/mohammadtavakoli78/BEAM) | Automatically generated coherent conversations with validated probing questions | long-history stress, updates, contradictions, temporal, preferences, summarization | source registry only; full-history stress runner not executed |
| [PerLTQA](https://github.com/Elvin-Yiming-Du/PerLTQA) | Constructed personal semantic/episodic memory benchmark; inspect release artifacts for annotation protocol | Chinese-language research, memory classification, retrieval, fusion | source registry only; pin corrected revision before ingest |
| [LifeBench v2.0](https://github.com/1754955896/LifeBench) | Life-event simulator generated histories for virtual users; Chinese and English translations | multi-source, year-long updates, calendar/SMS/notes/photos/notifications, personal history | source registry only; upstream LifeBench_eval recommended |
| [AMA-Bench](https://github.com/AMA-Bench/AMA-Bench) | Agent-trajectory memory QA; source trajectories and task environments must remain identified | causal recall, state updates, state abstraction, agent trajectories | source registry only; trajectory adapter not implemented |
| [MemoryArena](https://github.com/ZexueHe/MemoryArena) | Human-crafted interdependent tasks in executable environments; observed rollouts depend on the agent | skill transfer, shopping, search, travel planning, formal reasoning | source registry only; retain upstream environment runners |
| [LongMemEval-V2](https://github.com/xiaowu0162/LongMemEval-V2) | 451 manually curated questions over multimodal web-agent trajectory histories in web/enterprise environments | static state, dynamic state, workflow knowledge, environment gotchas, premise awareness | source registry only; upstream multimodal/fixed-reader harness recommended |

## LongMemEval cleaned
- Source repository：https://github.com/xiaowu0162/LongMemEval
- Data entry point：https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned
- Upstream size：500 questions; 30 abstention items; oracle/S/M are alternative histories for the same question bank
- Dataset license：Review dataset-card and component-corpus licenses; do not inherit code MIT automatically
- License source：https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Cleaned vs original histories are different revisions
- Exclude _abs questions from positive evidence-retrieval denominators
- Oracle is a separate upper-bound track
- Official semantic judge is external; EM/F1 diagnostic is not official accuracy

## LoCoMo
- Source repository：https://github.com/snap-research/locomo
- Data entry point：https://github.com/snap-research/locomo/blob/main/data/locomo10.json
- Upstream size：10 released conversations in locomo10.json; count actual imported questions rather than copying repack totals
- Dataset license：CC-BY-NC-4.0
- License source：https://github.com/snap-research/locomo/blob/main/LICENSE.txt
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Raw file metadata verified: 2805274 bytes; Git blob d95b872480b413d935821fdc3c84f8a8f5f29e73
- Preserve raw numeric category and upstream evidence ids
- Do not ingest gold event summaries or claim photo reasoning from captions
- Noncommercial terms need review for commercial evaluation

## PersonaMem v1
- Source repository：https://github.com/bowen-upenn/PersonaMem
- Data entry point：https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1
- Upstream size：32K / 128K / 1M context variants; variants/persona-history orders are not independent people
- Dataset license：MIT (dataset card)
- License source：https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Use context[:end_index_in_shared_context], exclusive prefix
- Never provide persona gold labels as observed history
- MCQ requires exact option; parser differs deliberately from upstream permissive extraction

## PersonaMem v2
- Source repository：https://github.com/bowen-upenn/PersonaMem-v2
- Data entry point：https://huggingface.co/datasets/bowen-upenn/PersonaMem-v2
- Upstream size：1000 personas, 20000+ preferences, 300+ scenarios advertised across release; test split is not all generated users
- Dataset license：Not independently verified in this build
- License source：https://huggingface.co/datasets/bowen-upenn/PersonaMem-v2
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Do not count train/dev/test as independent test cases
- Ask-to-forget behavior is not proof of physical storage deletion

## PersonaMem v3
- Source repository：https://github.com/bowen-upenn/PersonaMem-v3
- Data entry point：https://huggingface.co/datasets/bowen-upenn/PersonaMem-v3
- Upstream size：200 real-world source users / ~4M engagements reported; published HF sample is not that entire history
- Dataset license：CC-BY-NC-4.0 inherited from facebook/gistbench
- License source：https://huggingface.co/datasets/bowen-upenn/PersonaMem-v3
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Separate real-source observations from synthetic augmentation
- Noncommercial restriction matters for commercial PA evaluation
- Exclude persona_html/preferences/preference_details if they encode target labels rather than observations

## MemoryAgentBench
- Source repository：https://github.com/HUST-AI-HYZ/MemoryAgentBench
- Data entry point：https://huggingface.co/datasets/ai-hyz/MemoryAgentBench
- Upstream size：146 HF rows, ~76.6 MB; a row can hold many questions, so rows != tests
- Dataset license：MIT on dataset card; inspect component-source terms
- License source：https://huggingface.co/datasets/ai-hyz/MemoryAgentBench
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Incremental ingestion is part of the protocol
- LongMemEval-derived samples overlap; deduplicate across suites
- Conflict resolution/forgetting terminology does not establish privacy-erasure testing

## PrecisionMemBench
- Source repository：https://github.com/tenurehq/precisionMemBench
- Data entry point：https://github.com/tenurehq/precisionMemBench/tree/61a712a1754e7d27b6dee42da32a2c214b003ece/fixtures
- Upstream size：35 seeded beliefs; 77 single-turn cases; 12 additional turn assertions in 2 session scenarios
- Dataset license：MIT, Copyright 2026 Jeffrey Flynt
- License source：https://github.com/tenurehq/precisionMemBench/blob/61a712a1754e7d27b6dee42da32a2c214b003ece/LICENSE
- Pinned revision：61a712a1754e7d27b6dee42da32a2c214b003ece
- All questions/assertions transcribed from pinned upstream; original bytes not fetched
- 1 vacuous case unscored, not free success
- Tiered/pinned/alias expectations contain architecture-specific policy
- This is not official upstream provider scoring or a universal PA ranking

## BEAM
- Source repository：https://github.com/mohammadtavakoli78/BEAM
- Data entry point：https://github.com/mohammadtavakoli78/BEAM/tree/main/chats
- Upstream size：100 conversations / 2000 validated questions; 128K,500K,1M,10M scales
- Dataset license：Repository MIT; check any dataset-specific terms before redistribution
- License source：https://github.com/mohammadtavakoli78/BEAM/blob/main/LICENSE
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Token scale is not number of users
- Subsets/caches need explicit identification
- Do not regenerate new chat histories and call them the released test set

## PerLTQA
- Source repository：https://github.com/Elvin-Yiming-Du/PerLTQA
- Data entry point：https://github.com/Elvin-Yiming-Du/PerLTQA/tree/main/Dataset
- Upstream size：Paper reports 8593 questions over 30 characters; 2025-12-22 revision fixes inconsistencies
- Dataset license：CC-BY-NC-4.0; noncommercial research
- License source：https://github.com/Elvin-Yiming-Du/PerLTQA/blob/main/README.md
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Paper count and revised file count may differ
- Do not silently translate English benchmarks and claim native Chinese coverage

## LifeBench v2.0
- Source repository：https://github.com/1754955896/LifeBench
- Data entry point：https://huggingface.co/datasets/C1754955896/Lifebenchv2.0
- Upstream size：10 virtual users; 3380 questions per language; 47409 phone records; 57486 events; 9 sources
- Dataset license：Not independently verified in this build
- License source：https://huggingface.co/datasets/C1754955896/Lifebenchv2.0
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Default source branch resolved by connector as lifebench2.0
- LoCoMo export is the same 3380 questions, not an extra benchmark
- Do not ingest simulator persona/event trees containing privileged labels
- English/Chinese versions are paired, not independent users

## AMA-Bench
- Source repository：https://github.com/AMA-Bench/AMA-Bench
- Data entry point：https://huggingface.co/datasets/AMA-bench/AMA-bench
- Upstream size：Project reports 2482 evaluation questions; inspect current release counts rather than third-party conversions
- Dataset license：Not independently verified in this build
- License source：https://huggingface.co/datasets/AMA-bench/AMA-bench
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Official test path includes test/open_end_qa_set.jsonl
- Do not flatten entire tool trajectory into a fake chat without disclosure
- Open-answer and MCQ protocols remain separate

## MemoryArena
- Source repository：https://github.com/ZexueHe/MemoryArena
- Data entry point：https://huggingface.co/datasets/ZexueHe/memoryarena
- Upstream size：Several task environments; one source test is not interchangeable with a QA row
- Dataset license：Dataset CC-BY-4.0 on project site; code/dependency licenses separately
- License source：https://memoryarena.github.io/
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Repo marks code preview
- Task→memory→action→environment→observation/reward is an interactive loop
- Native task completion/reward is the primary score
- Do not feed future success trajectories or evaluator gold as past memory

## LongMemEval-V2
- Source repository：https://github.com/xiaowu0162/LongMemEval-V2
- Data entry point：https://huggingface.co/datasets/xiaowu0162/longmemeval-v2
- Upstream size：451 questions; up to500 trajectories/haystack; largest115M tokens; small/medium tiers
- Dataset license：Repository Apache-2.0; verify separate data-card terms
- License source：https://github.com/xiaowu0162/LongMemEval-V2/blob/main/LICENSE
- Pinned revision：Resolved to source.lock.json when acquired; this inventory does not invent a pinned SHA
- Official query receives only question and optional image; metadata and labels private
- Fixed reader and token budget crucial
- Do not claim screenshots are evaluated by text-only importers
- Upstream accuracy/latency frontier metric should not be replaced by our diagnostic pass rate
