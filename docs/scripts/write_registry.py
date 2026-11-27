"""Research inventory; this is not a claim that catalogued datasets were downloaded."""
import json
from pathlib import Path
R=Path(__file__).resolve().parents[1]
D=[]
def add(id,name,repo,data,nature,coverage,size,license,license_source,priority,status,notes):
    D.append(dict(id=id,name=name,repository=repo,dataset_url=data,data_nature=nature,coverage=coverage,
                  reported_scale=size,data_license=license,license_source=license_source,priority=priority,
                  implementation_status=status,actual_run_in_this_package=id=='precisionmembench',
                  pinned_revision='61a712a1754e7d27b6dee42da32a2c214b003ece' if id=='precisionmembench' else None,
                  notes=notes,verification_date='2026-10-15'))
add('longmemeval','LongMemEval cleaned','https://github.com/xiaowu0162/LongMemEval','https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned',
    'Curated questions over synthesized/assembled timestamped user-assistant histories; not a released dump of real PA conversations',
    ['fact recall','cross-session reasoning','updates','temporal','abstention'],
    '500 questions; 30 abstention items; oracle/S/M are alternative histories for the same question bank',
    'Review dataset-card and component-corpus licenses; do not inherit code MIT automatically','https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned',
    'P0','collector + JSON importer + schema unit tests; full dataset not acquired/run',
    ['Cleaned vs original histories are different revisions','Exclude _abs questions from positive evidence-retrieval denominators','Oracle is a separate upper-bound track','Official semantic judge is external; EM/F1 diagnostic is not official accuracy'])
add('locomo','LoCoMo','https://github.com/snap-research/locomo','https://github.com/snap-research/locomo/blob/main/data/locomo10.json',
    'LLM-generated multi-session dialogue benchmark with curated QA, not real-user logs',['episodic recall','multi-hop','temporal','abstention','multimodal optional'],
    '10 released conversations in locomo10.json; count actual imported questions rather than copying repack totals',
    'CC-BY-NC-4.0','https://github.com/snap-research/locomo/blob/main/LICENSE.txt','P0',
    'collector + strict text-only importer + schema tests; full data not acquired/run',
    ['Raw file metadata verified: 2805274 bytes; Git blob d95b872480b413d935821fdc3c84f8a8f5f29e73','Preserve raw numeric category and upstream evidence ids','Do not ingest gold event summaries or claim photo reasoning from captions','Noncommercial terms need review for commercial evaluation'])
add('personamem-v1','PersonaMem v1','https://github.com/bowen-upenn/PersonaMem','https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1',
    'Synthetic personas and generated multi-session conversations',['preference recall','preference updates','preference application','personalized MCQ'],
    '32K / 128K / 1M context variants; variants/persona-history orders are not independent people',
    'MIT (dataset card)','https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1','P0',
    '32K collector + CSV/JSONL importer + schema tests; full data not acquired/run',
    ['Use context[:end_index_in_shared_context], exclusive prefix','Never provide persona gold labels as observed history','MCQ requires exact option; parser differs deliberately from upstream permissive extraction'])
add('personamem-v2','PersonaMem v2','https://github.com/bowen-upenn/PersonaMem-v2','https://huggingface.co/datasets/bowen-upenn/PersonaMem-v2',
    'Synthetic personas and implicit-preference conversations',['implicit preferences','evolving preferences','privacy requests'],
    '1000 personas, 20000+ preferences, 300+ scenarios advertised across release; test split is not all generated users',
    'Not independently verified in this build','https://huggingface.co/datasets/bowen-upenn/PersonaMem-v2','P1',
    'source registry only; use original dataset and runner',
    ['Do not count train/dev/test as independent test cases','Ask-to-forget behavior is not proof of physical storage deletion'])
add('personamem-v3','PersonaMem v3','https://github.com/bowen-upenn/PersonaMem-v3','https://huggingface.co/datasets/bowen-upenn/PersonaMem-v3',
    'Real-source-derived: anonymized engagement histories from GIST; generated personas and simulated app histories are derivative, not untouched real logs',
    ['implicit behavior','personalization','proactiveness','privacy','overpersonalization'],
    '200 real-world source users / ~4M engagements reported; published HF sample is not that entire history',
    'CC-BY-NC-4.0 inherited from facebook/gistbench','https://huggingface.co/datasets/bowen-upenn/PersonaMem-v3','P1',
    'source registry only; no data redistributed or evaluated',
    ['Separate real-source observations from synthetic augmentation','Noncommercial restriction matters for commercial PA evaluation','Exclude persona_html/preferences/preference_details if they encode target labels rather than observations'])
add('memoryagentbench','MemoryAgentBench','https://github.com/HUST-AI-HYZ/MemoryAgentBench','https://huggingface.co/datasets/ai-hyz/MemoryAgentBench',
    'Heterogeneous existing datasets plus controlled synthetic tasks',['accurate retrieval','test-time learning','long-range understanding','conflict resolution'],
    '146 HF rows, ~76.6 MB; a row can hold many questions, so rows != tests',
    'MIT on dataset card; inspect component-source terms','https://huggingface.co/datasets/ai-hyz/MemoryAgentBench','P0',
    'source registry only; incremental replay adapter required',
    ['Incremental ingestion is part of the protocol','LongMemEval-derived samples overlap; deduplicate across suites','Conflict resolution/forgetting terminology does not establish privacy-erasure testing'])
add('precisionmembench','PrecisionMemBench','https://github.com/tenurehq/precisionMemBench','https://github.com/tenurehq/precisionMemBench/tree/61a712a1754e7d27b6dee42da32a2c214b003ece/fixtures',
    'Upstream-authored synthetic software diagnostic fixture',['alias retrieval','scope','supersession','cross-user negative checks','budget','session noise'],
    '35 seeded beliefs; 77 single-turn cases; 12 additional turn assertions in 2 session scenarios',
    'MIT, Copyright 2026 Jeffrey Flynt','https://github.com/tenurehq/precisionMemBench/blob/61a712a1754e7d27b6dee42da32a2c214b003ece/LICENSE','P0-diagnostic',
    '77-case source field projection bundled and run; session scenarios not bundled/run',
    ['All questions/assertions transcribed from pinned upstream; original bytes not fetched','1 vacuous case unscored, not free success','Tiered/pinned/alias expectations contain architecture-specific policy','This is not official upstream provider scoring or a universal PA ranking'])
add('beam','BEAM','https://github.com/mohammadtavakoli78/BEAM','https://github.com/mohammadtavakoli78/BEAM/tree/main/chats',
    'Automatically generated coherent conversations with validated probing questions',['long-history stress','updates','contradictions','temporal','preferences','summarization'],
    '100 conversations / 2000 validated questions; 128K,500K,1M,10M scales',
    'Repository MIT; check any dataset-specific terms before redistribution','https://github.com/mohammadtavakoli78/BEAM/blob/main/LICENSE','P1',
    'source registry only; full-history stress runner not executed',
    ['Token scale is not number of users','Subsets/caches need explicit identification','Do not regenerate new chat histories and call them the released test set'])
add('perltqa','PerLTQA','https://github.com/Elvin-Yiming-Du/PerLTQA','https://github.com/Elvin-Yiming-Du/PerLTQA/tree/main/Dataset',
    'Constructed personal semantic/episodic memory benchmark; inspect release artifacts for annotation protocol',
    ['Chinese-language research','memory classification','retrieval','fusion'],
    'Paper reports 8593 questions over 30 characters; 2025-12-22 revision fixes inconsistencies',
    'CC-BY-NC-4.0; noncommercial research','https://github.com/Elvin-Yiming-Du/PerLTQA/blob/main/README.md','P1-CN',
    'source registry only; pin corrected revision before ingest',
    ['Paper count and revised file count may differ','Do not silently translate English benchmarks and claim native Chinese coverage'])
add('lifebench','LifeBench v2.0','https://github.com/1754955896/LifeBench','https://huggingface.co/datasets/C1754955896/Lifebenchv2.0',
    'Life-event simulator generated histories for virtual users; Chinese and English translations',
    ['multi-source','year-long updates','calendar/SMS/notes/photos/notifications','personal history'],
    '10 virtual users; 3380 questions per language; 47409 phone records; 57486 events; 9 sources',
    'Not independently verified in this build','https://huggingface.co/datasets/C1754955896/Lifebenchv2.0','P1-CN',
    'source registry only; upstream LifeBench_eval recommended',
    ['Default source branch resolved by connector as lifebench2.0','LoCoMo export is the same 3380 questions, not an extra benchmark','Do not ingest simulator persona/event trees containing privileged labels','English/Chinese versions are paired, not independent users'])
add('ama-bench','AMA-Bench','https://github.com/AMA-Bench/AMA-Bench','https://huggingface.co/datasets/AMA-bench/AMA-bench',
    'Agent-trajectory memory QA; source trajectories and task environments must remain identified',
    ['causal recall','state updates','state abstraction','agent trajectories'],
    'Project reports 2482 evaluation questions; inspect current release counts rather than third-party conversions',
    'Not independently verified in this build','https://huggingface.co/datasets/AMA-bench/AMA-bench','P1',
    'source registry only; trajectory adapter not implemented',
    ['Official test path includes test/open_end_qa_set.jsonl','Do not flatten entire tool trajectory into a fake chat without disclosure','Open-answer and MCQ protocols remain separate'])
add('memoryarena','MemoryArena','https://github.com/ZexueHe/MemoryArena','https://huggingface.co/datasets/ZexueHe/memoryarena',
    'Human-crafted interdependent tasks in executable environments; observed rollouts depend on the agent',
    ['skill transfer','shopping','search','travel planning','formal reasoning'],
    'Several task environments; one source test is not interchangeable with a QA row',
    'Dataset CC-BY-4.0 on project site; code/dependency licenses separately','https://memoryarena.github.io/','P1-agentic',
    'source registry only; retain upstream environment runners',
    ['Repo marks code preview','Task→memory→action→environment→observation/reward is an interactive loop','Native task completion/reward is the primary score','Do not feed future success trajectories or evaluator gold as past memory'])
add('longmemeval-v2','LongMemEval-V2','https://github.com/xiaowu0162/LongMemEval-V2','https://huggingface.co/datasets/xiaowu0162/longmemeval-v2',
    '451 manually curated questions over multimodal web-agent trajectory histories in web/enterprise environments',
    ['static state','dynamic state','workflow knowledge','environment gotchas','premise awareness'],
    '451 questions; up to500 trajectories/haystack; largest115M tokens; small/medium tiers',
    'Repository Apache-2.0; verify separate data-card terms','https://github.com/xiaowu0162/LongMemEval-V2/blob/main/LICENSE','P1-agentic',
    'source registry only; upstream multimodal/fixed-reader harness recommended',
    ['Official query receives only question and optional image; metadata and labels private','Fixed reader and token budget crucial','Do not claim screenshots are evaluated by text-only importers','Upstream accuracy/latency frontier metric should not be replaced by our diagnostic pass rate'])
obj={'schema_version':'1.0','research_snapshot':'2026-10-15','policy':{'public_is_not_real':True,'upstream_only':True,'no_new_generated_test_questions':True,
 'catalogued_is_not_downloaded':True,'noncommercial_is_not_commercial_clearance':True,'never_sum_overlapping_tests':True},'datasets':D,
 'framework_references':[{'name':'LongMemEval-V2 official harness','url':'https://github.com/xiaowu0162/LongMemEval-V2','use':'Adopt query-label isolation and fixed reader/token caps; no code copied'},
 {'name':'MemoryArena official runner','url':'https://github.com/ZexueHe/MemoryArena','use':'Delegate interactive environments; do not flatten into QA'},
 {'name':'OmniMemEval','url':'https://github.com/MemTensor/OmniMemEval','use':'Broad suite inventory/runner reference; schemas and revision require independent integration review'},
 {'name':'Agent Memory Benchmark','url':'https://agentmemorybenchmark.ai/','use':'Existing multi-mode framework reference; do not copy its provider results as our measurements'}]}
(R/'data/source_registry.json').write_text(json.dumps(obj,ensure_ascii=False,indent=2)+'\n')
lines=['# PA 记忆测试集来源登记','', '核查日期：2026-10-15；登记 ≠ 已下载 ≠ 已执行；规模为上游报告，实际测试数以导入清单为准。','',
       '| 测试集 | 来源性质 | 主要用途 | 当前实现状态 |','|---|---|---|---|']
for d in D:lines.append(f"| [{d['name']}]({d['repository']}) | {d['data_nature']} | {', '.join(d['coverage'])} | {d['implementation_status']} |")
for d in D:
 lines+=['',f"## {d['name']}",f"- 原始仓库：{d['repository']}",f"- 数据入口：{d['dataset_url']}",f"- 上游规模：{d['reported_scale']}",f"- 数据许可：{d['data_license']}",f"- 许可来源：{d['license_source']}",f"- 固定版本：{d['pinned_revision'] or '本地采集时解析并写入 source.lock.json；本登记未伪造固定 SHA'}",*['- '+n for n in d['notes']]]
(R/'docs/DATASETS.md').write_text('\n'.join(lines)+'\n')
print('registered',len(D),'datasets')
