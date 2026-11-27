# MemoryAgentBench official evaluation code (unaltered references)

Source: https://github.com/HUST-AI-HYZ/MemoryAgentBench, commit `538026089d1a8a8eff05121d0db89b388f360eba` (2026-09-25T06:11:33Z). Retrieved on 2026-10-17; Git blob SHA-1 values matched the tree API. Code: MIT; data: HF ai-hyz/MemoryAgentBench, MIT.

Purpose: `utils/eval_data_utils.py` loads data and chunks context; `utils/eval_other_utils.py` defines substring_exact_match, exact_match, Recall@5 and F1; `utils/templates.py` supplies remember-this ingestion instructions and question templates. `llm_based_eval/longmem_qa_evaluate.py` judges the LongMemEval subset with GPT-4o; `llm_based_eval/summarization_evaluate.py` supplies InfBench summary F1 and fluency. `agent.py`, `main.py`, `initialization.py` and `conversation_creator.py` define ingestion followed by queries. `bash_files/configs/*.txt` records official settings including chunk sizes 512/4096. Referenced files are unmodified.

| File | sha256 | Verification |
|---|---|---|
| `DATASET_CARD.md` | `5a9fd0dc2fbab5beb081eae40f24964ad1e47da75b9a7a01f0007791845b1580` | dataset card (HF ai-hyz/MemoryAgentBench @7ea06698, see data/upstream/memoryagentbench/source.lock.json) |
| `LICENSE` | `94d735aad92355b4880c969fa309e824eb12fdeeb271ae51cf6143866cf78cf7` | verified |
| `README.md` | `a71d5730d1ada9e0471cbb3e2cd2d36e343d751298a69e3d368af878753eab7f` | verified |
| `agent.py` | `41b1ccff3e8676cace8b3d23377e8d8468aab88dd34ebc0b1d3efe4a21282b60` | verified |
| `bash_files/configs/long_context_agents.txt` | `f9ef74a9e915dab61a212b33cad7b20849b154dd2e2fa8880919402338d69b16` | verified |
| `bash_files/configs/rag_agents.txt` | `2b520bdc67a55339f88911ceb860bdf7d03096e0a9bff4ee9423b0b26618b055` | verified |
| `bash_files/configs/rag_agents_chunksize.txt` | `2c407ee375dd5d803318736357ce77a42572ccfb355b87e3f3a23a254ade0b84` | verified |
| `conversation_creator.py` | `9a527e88c9791b37d378b59adac50d9308aac49e714f1d8ddaf3e14bb3f3bec5` | verified |
| `initialization.py` | `86abdbd5442bcff6762a5f90cc45b8cf07bead9415f02aafaca036f2a8302ad7` | verified |
| `llm_based_eval/longmem_qa_evaluate.py` | `ceca59bddea4203a78b417be635e63fe3f685bd957e3df12150978e21972c540` | verified |
| `llm_based_eval/model_utils_openai.py` | `6ea1ecc43d57927e6cbd10e3da620b7dad27bf041c3dfe7ad647791e0e85fda7` | verified |
| `llm_based_eval/summarization_evaluate.py` | `6951d19644cdb1972bcf6cdb272f4757aa995ae07099bf74012925158c376dd4` | verified |
| `main.py` | `f33b87cbdac4cb45f4e3510a08fecc53970bd03de9b8e14578219198c61aa5a7` | verified |
| `utils/eval_data_utils.py` | `f5d564996d63a07454999e94ab01087d4d07939f69936045c700fd57d338cb68` | verified |
| `utils/eval_other_utils.py` | `d77976be409298970614d477a9d8003850caddb0510e56a7e821a037d98493a2` | verified |
| `utils/templates.py` | `148c40d48d19f155ae845482c4417ba59cfa7ae4e194019509e023bd3a8755dd` | verified |
