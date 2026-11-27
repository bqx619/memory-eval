# BEAM official evaluation code and prompts (unaltered references)

Source: https://github.com/mohammadtavakoli78/BEAM (the repository cited in arXiv 2510.27246), commit `b2da22eac88bb0874c64665f13457eb99835774a` (2026-08-31T02:12:10Z). Retrieved on 2026-10-17; Git blob SHA-1 values matched the tree API. Code license: MIT; data: HF Mohammadta/BEAM, CC BY-SA 4.0.

Purpose: `src/evaluation/compute_metrics.py` defines per-item rubric scores (0/0.5/1), their mean, event-ordering `(tau_b+1)/2 × F1`, and category aggregation. `run_evaluation.py`/`report_results.py` define judge settings and reporting. `src/prompts.py` contains judge and generation prompts. `src/answer_probing_questions/answer_generation.py` flattens history, trims its tail and applies the `NOTE: Only provide the answer…` question template. `long_term_memory_methods.py` documents retrieval/memory baselines; `src/llm.py` supplies model calls. These are the references for the Go rubric/ordering/summary scorers and protocol declarations. The referenced files are unmodified.

| File | sha256 | Verification |
|---|---|---|
| `DATASET_CARD.md` | `30b9ff56f4ca52e4078754e91f5e8dd8cd73b1b1246a42a000f11ce6b940400d` | dataset card (HF Mohammadta/BEAM @3205395e, see data/upstream/beam/source.lock.json) |
| `LICENSE` | `60c7b74ffaacaae383c3f06a48bc79d204b25bc9c1856df9711b3d290febd474` | verified |
| `README.md` | `6eb38ece9322cdd96dba99a50f882187688e55e80b594068ad8827db5bc08020` | verified |
| `src/answer_probing_questions/answer_generation.py` | `b8117f64146ebfe5693b0ce6f3478fed7584bca6023c25059f2def49341a1f42` | verified |
| `src/answer_probing_questions/long_term_memory_methods.py` | `d3fd96786d2d58a4b0b60ff726ebf00d1adf84f7bb8487abfd8768f3519cffd8` | verified |
| `src/evaluation/compute_metrics.py` | `e72bf25d36d521d166862063ec94f8e7875328193142d1edddb9b4677d47780d` | verified |
| `src/evaluation/report_results.py` | `1486a44fac721717c0a9373b32f4487c277d2bf28044cf28f649a6a74e692c96` | verified |
| `src/evaluation/run_evaluation.py` | `518894ab65860043a24259457ddf3b1ab8ae18427b6e01e5aa90cd29c4256a3b` | verified |
| `src/llm.py` | `1ce32f8bf67935fc0fcf169963cce79b5f7ee4e63e25baf72b174c497d364c54` | verified |
| `src/prompts.py` | `81fe51ab2bcd7e8ad218b3a9a969a340c38d31f90ac2d93fa78a3bb890ba157b` | verified |

Official settings (checked 2026-10-17 in `src/llm.py` and `src/evaluation/run_evaluation.py`): `BuildLLm(model_name="gpt-4.1-mini", temperature=0)` uses LangChain ChatOpenAI. Each rubric item fills `<rubric_item>`/`<llm_response>` in `unified_llm_judge_base_prompt` (`src/prompts.py`, starting at line 11547), requesting JSON `{score}` with 0/0.5/1; `llm_judge_score` is the mean. `event_ordering_score(align_type="llm")` splits the answer on newlines, uses `align_with_llm`/`llm_equivalence` for first-match one-to-one alignment, then computes set precision/recall/F1 and `scipy.stats.kendalltau(variant="b")`: `final_score = (tau_b+1)/2 × F1`. Summarization uses the rubric mean too; the BLEU/ROUGE helpers are not called by `evaluate_summarization`.
