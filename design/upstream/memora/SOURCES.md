# Memora official evaluation code (unaltered references)

Source: https://github.com/geniesinc/Memora, commit `a6493188efc836d6511ed5e4163fe3ba87da30ff` (main, pushed 2026-07-22), Apache-2.0. Files were restored under `data/upstream/memora/` from a tarball and checked against Git tree blob SHA-1 values; see `source.lock.json` and `files.sha256.jsonl`.

Purpose: `evals/agent_eval/memory_to_answer.py` defines yes/no evaluation subquestions, three-judge majority votes, MPA/FAA/lambda/FAMA and the current-date prefix. `base_evaluator.py` and `conversation_to_memory.py` define Track 2 whole-session ingestion, `user_id=<persona>_<period>` and `search_memories(limit=50)`. `evals/model_eval/{model_based_evaluator.py, api_client.py, aggregate_results.py, config.py}` supplies judge prompts, the three judge models, OpenRouter calls and aggregation. These are the references for the Go rubric scorer and protocol declarations. `DATA_README.md` describes data layout. Referenced files are unmodified.

| File | sha256 | Verification |
|---|---|---|
| `DATA_README.md` | `df93166e707dbd9273f5be01f2ec532949d59c55542fe8fb22d71a2e8c82fc0a` | verified (= upstream data/README.md) |
| `LICENSE` | `c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4` | verified (= upstream LICENSE) |
| `README.md` | `e946512d501b75c991859a3e35d69f469266a668082850b734f3fde7d970f6d7` | verified (= upstream README.md) |
| `evals/README.md` | `4dfbf7a0f16e2a9776a27b8ae237c6d5bd38b00a9f1ed007b0d729a613ee20d3` | verified (= upstream evals/README.md) |
| `evals/agent_eval/base_evaluator.py` | `2b0e510a227765a385cc514f29653d9149818f3cc71604c46fdfda101b0069f6` | verified (= upstream evals/agent_eval/base_evaluator.py) |
| `evals/agent_eval/conversation_to_memory.py` | `502b21b4f230bd68cfa550a76c47a8a6e56f93fd2952e8f767c3dd8d7ff20abf` | verified (= upstream evals/agent_eval/conversation_to_memory.py) |
| `evals/agent_eval/memory_to_answer.py` | `119adf7811c2fc92f4922b1a72c6989c0a5f83350e76366b88dff137e47b4c4f` | verified (= upstream evals/agent_eval/memory_to_answer.py) |
| `evals/model_eval/aggregate_results.py` | `64435c6c21f0df6f7629690e0600c94214c55197e3d537c356b59befd541c223` | verified (= upstream evals/model_eval/aggregate_results.py) |
| `evals/model_eval/api_client.py` | `f6571f53e06e601ce1a4fe64dd58f14918fa2b5f5489a0eb8f5170349e4e233f` | verified (= upstream evals/model_eval/api_client.py) |
| `evals/model_eval/config.py` | `9322b21dd89848a93b002b04b8163120a2311d1a21dc5882b06f57305616c1ca` | verified (= upstream evals/model_eval/config.py) |
| `evals/model_eval/model_based_evaluator.py` | `b4e37dd39c2c72b91743c42180f6b07b460e519358f33c6fc1511f575dbb1bfc` | verified (= upstream evals/model_eval/model_based_evaluator.py) |
