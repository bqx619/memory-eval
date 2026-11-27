# HaluMem official evaluation code and prompts (unaltered references)

Source: https://github.com/MemTensor/HaluMem, commit `718f16ff0c83413b1c86fa83fc13cc1a639871f9` (2026-09-03T02:44:49Z). Retrieved on 2026-10-17; Git blob SHA-1 values matched the tree API. The repository `LICENSE.txt` begins with “Attribution-NonCommercial-NoDerivatives 4.0 International”; the recorded GitHub API identifier was NOASSERTION. Data: HF IAAR-Shanghai/HaluMem, CC BY-NC-ND 4.0. These are source license records, not a legal determination for a particular use or transformed copy.

Purpose: `eval/evaluation.py` and `eval/prompts.py` define official GPT-4o prompts and aggregation for extraction (completeness 0/1/2, accuracy and FMR), updates (Correct/Hallucination/Omission/Other) and QA. `eval/eval_tools.py`/`eval/llms.py` document 20-turn ingestion batches, dump and search(top_k=20). Go judge templates and denominators must match these references and record their hashes. Referenced files are unmodified.

| File | sha256 | Verification |
|---|---|---|
| `DATASET_CARD.md` | `f079d020d55c744ec43992d15e48b4e03a75c7f7c9a3544a1f621d39e5fc3b84` | dataset card (HF, see data/upstream/halumem/source.lock.json) |
| `LICENSE.txt` | `cb6303892198afb24723a78e59be37222ffd7494690fca00d9307347df50e0b6` | verified |
| `README.md` | `bdbd5821f7016c996b8dc19624af878e47fa3696814fb62e49b8eb5e9eabd5a7` | verified |
| `eval/README.md` | `975912e49351c51f4512c597e3deffb595f1db074695c8bce39c0b3148ee12cc` | verified |
| `eval/eval_memzero.py` | `7fa34de7e45e1b9730bb63453ee17627925c8a4304645eda7249844eb24ca3c1` | verified |
| `eval/eval_tools.py` | `0c08e5ecb8c93945bafc4bd0336bd6c9756b40d175f442ce44aca4a43169ee3b` | verified |
| `eval/evaluation.py` | `d4c0376a7e59872093564686d77db487ecf8fb5b5ba5f499f8c2ef4e417dba31` | verified |
| `eval/llms.py` | `3944e8133c3777506f2f17971884f18c83ab7379fc3d79d22dfd6a748c0b8547` | verified |
| `eval/prompts.py` | `641a9e89c8a42cedb83feed47543ab35cbd80cb80fb515c0a7afab60fe259a58` | verified |

Official settings (`eval/.env-example` and `eval/README.md`, checked 2026-10-17): answer/judge model `OPENAI_MODEL=gpt-4o`, `OPENAI_TEMPERATURE=0.0`, `OPENAI_MAX_TOKENS=16384`. `eval_memzero.py` processes each user and session, ingests timestamped batches, searches update points (`is_update=="True"`) with top_k=10 and answers questions after search(top_k=20). GPT-4o is retained here as a historical upstream setting; the project's successor-judge and concordance policy is recorded in `design/m3b-judge-and-env.md`.
