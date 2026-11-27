# LoCoMo official scoring scripts (unaltered references)

Source: https://github.com/snap-research/locomo, commit `3eb6f2c585f5e1699204e3c3bdf7adc5c28cb376` (main, 2024-08-13). Retrieved by pinned raw URL on 2026-10-17; Git blob SHA-1 values matched the tree API (see `data/upstream/locomo/source.lock.json`). `LICENSE.txt`: CC BY-NC 4.0. Data and scoring scripts are byte-identical to the earlier cbfbc1d snapshot.

Purpose: `task_eval/evaluation.py` is the reference for normalized Porter-stem token-F1, category-1 comma-separated maximum-F1 averaging, category-5 abstention phrases and retrieval recall. The Go scorer is checked case by case against this oracle; its top-level `bert_score` import requires torch, so oracle tests inject a `bert_score` stub into `sys.modules` before importing, without changing the reference. `task_eval/gpt_utils.py` contains `CONV_START_PROMPT`, `QA_PROMPT`, `QA_PROMPT_CAT_5`, `QA_PROMPT_BATCH` and `get_input_context` (session dates and `{speaker}: {text}`). `evaluation_stats.py` and `evaluate_qa.py` establish category mappings and reporting. Referenced files are unmodified.

| File | sha256 |
|---|---|
| `LICENSE.txt` | `41003d4a74749c0220e33dd415042164b5a1093ed401f36277234f772d22d3d0` |
| `README.MD` | `9f8e6fd00a3400aa687109f40ed53715f0a2c028ee3f8c465bdfa96475640e8a` |
| `task_eval/evaluate_qa.py` | `dde7c1c6b5501486f96ce31398d6e49de76abbfa656e980b137e54fc69e9f6ee` |
| `task_eval/evaluation.py` | `8e3be5d57ff2ff9ec5cd05939592f468c5f3f1fd95d13e431932bdf6bf0fd6fd` |
| `task_eval/evaluation_stats.py` | `d36bf596de05ea6f1c355e433167a8cd704bea3a3745277c650ac0c464bba139` |
| `task_eval/gpt_utils.py` | `5fc977375878199735acd28fba5ae6f4d657fa0e000c0d2918a90c07b6035793` |
