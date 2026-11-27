# PrefEval official evaluation code (unaltered references)

Source: https://github.com/amazon-science/prefeval, commit `50795054b5ff5f418d2b768a331d71e480f93331` (main, pushed 2025-05-30), CC BY-NC 4.0 (`LICENSE`). Files were restored under `data/upstream/prefeval/` from a tarball and checked against Git tree blob SHA-1 values; see `source.lock.json` and `files.sha256.jsonl`.

Purpose: `generation_task/llm_based_evaluation_errortypes.py` supplies Claude 3 Sonnet yes/no probes for acknowledge, hallucinate, violate and helpful. `generation_task/get_preference_following_accuracy_generation_task.py` combines unhelpful, inconsistent, hallucination_of_preference_violation and preference_unaware_violation into preference_following_accuracy. `utils/{explicit,implicit,mcq_implicit,common}_utils.py` builds preference/filler conversations and reminder/system variants. `classification_task/benchmark_classification.py` shuffles and parses MCQ options; `error_type/check_{acknowledge,violation,hallucination,helpful}.txt` contains exact probe prompts; `config.yaml` contains system_prompt and other settings. Referenced files are unmodified.

| File | sha256 | Verification |
|---|---|---|
| `LICENSE` | `41003d4a74749c0220e33dd415042164b5a1093ed401f36277234f772d22d3d0` | verified (= upstream LICENSE) |
| `README.md` | `87e23a62e58e6b8e04ef3e63b26ba1946bcbdebdd8eea1dc95f595e66af46e2c` | verified (= upstream README.md) |
| `classification_task/baselines_handling_classification.py` | `d72dd06a7f4fdade366f006d6d4f40c384255f0c7b62ad86ffc72053f86de552` | verified (= upstream classification_task/baselines_handling_classification.py) |
| `classification_task/benchmark_classification.py` | `5334ae8dcbb375a26dfd8ab65452ff8606cad127aa085f95df7b1243678c2efa` | verified (= upstream classification_task/benchmark_classification.py) |
| `config.yaml` | `0389ae6b687ffbec525ff9c69a3f2015d6f3563db5c073fea6ae0e29514b50a8` | verified (= upstream config.yaml) |
| `error_type/check_acknowledge.txt` | `82d7a34883a681bf089aa38f02bfd4fcc2a6cca38f82e2ae139a75714a8a7503` | verified (= upstream error_type/check_acknowledge.txt) |
| `error_type/check_hallucination.txt` | `2cff58730a87653365ff8bff275a2b0f0fbfb11525b966f3ffef9d53dd30f251` | verified (= upstream error_type/check_hallucination.txt) |
| `error_type/check_helpful.txt` | `5f09a40ea8f3c9f967a9868527b47daeab540c1b22717b95b10d8f5ee0e27b16` | verified (= upstream error_type/check_helpful.txt) |
| `error_type/check_violation.txt` | `bc5f62437604e875075f861f60c89a912b9562a54e3969c3394af8ab6c6a6b16` | verified (= upstream error_type/check_violation.txt) |
| `gen_data_prompt.txt` | `c4bda4488fadd7623995d9ec8756db6e9a2aa92d31b5c0e4528952619d47f2cb` | verified (= upstream benchmark_dataset/gen_data_prompt.txt) |
| `generation_task/baselines_handling.py` | `c0348c96cad50d2840dc41285db883e8c187db6a538624d9533362c659e94347` | verified (= upstream generation_task/baselines_handling.py) |
| `generation_task/benchmark_generation.py` | `f2d9ad2a56909b8e38e11baecfa4665d65e927f56d2cfb59e78d0cbd9880f223` | verified (= upstream generation_task/benchmark_generation.py) |
| `generation_task/get_preference_following_accuracy_generation_task.py` | `beb50d58f12c3947f62585b81eeb7727428117270b128eff8a86d0334ed72859` | verified (= upstream generation_task/get_preference_following_accuracy_generation_task.py) |
| `generation_task/llm_based_evaluation_errortypes.py` | `2edeb28d8bf347781c9ad62c9f3426c8dfbfa0b64018d6b233a254e7407e7cbe` | verified (= upstream generation_task/llm_based_evaluation_errortypes.py) |
| `utils/common_utils.py` | `31926731576ceb9cce76809682577643d50e7674e4a1b9a9c90cdda0db0dc0b2` | verified (= upstream utils/common_utils.py) |
| `utils/data_loading_utils.py` | `74bf117c88ac0f3f21d7d793728c65c3ea3a4f465ebd2542e71091a4ec8c2996` | verified (= upstream utils/data_loading_utils.py) |
| `utils/explicit_utils.py` | `13d75803589d09ad1c3bc32a130a501728f545626f7b57edaed0b6ef154e6042` | verified (= upstream utils/explicit_utils.py) |
| `utils/implicit_utils.py` | `9b55c413af28165caec853756fef7228eda001d8141d32ea15bbd54d8296ffca` | verified (= upstream utils/implicit_utils.py) |
| `utils/mcq_implicit_utils.py` | `d15846111e0831a4f376d44faa32eab43fc918f15877bbff862765c7c0c56399` | verified (= upstream utils/mcq_implicit_utils.py) |
| `utils/utils_mcq.py` | `b3088007724a5c4e21af80ed0220fa796e9f7473007ce9a7df4612eb7cd8f8ac` | verified (= upstream utils/utils_mcq.py) |
