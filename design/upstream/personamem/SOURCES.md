# PersonaMem official inference and scoring code (unaltered references)

Source: https://github.com/bowen-upenn/PersonaMem, commit `d07e6ade22e85e0e5d562247323a9c3e07553226` (2026-09-06T05:21:55Z). Retrieved on 2026-10-17; Git blob SHA-1 values matched the tree API. Code: MIT License. Data v1: HF bowen-upenn/PersonaMem-v1, MIT (see `data/upstream/personamem-v1/source.lock.json`).

Purpose: `prompts.py` and `inference.py` define official `context[:end_index_in_shared_context]` cropping, question templates, `<final_answer>` and `\(([a-d])\)` choice parsing. The Go MCQ reader and parser must match these references and record their hashes. Referenced files are unmodified.

| File | sha256 | Verification |
|---|---|---|
| `LICENSE` | `5cad9808b882c275f91578fbd742e007830123dad1f532b7c48f260d53bb6a20` | verified |
| `README.md` | `62c843f394c7b23af8abde359e9e5380647d8db262e539d27abfc40ddc36d875` | verified |
| `prompts.py` | `a190fa7c9c29f73756300e253703e5c726896b84c34198030d766dd4946ee1cc` | verified |
| `inference.py` | `9b77115a0d92d255c4c6ef6a710f8975e178d7f0c902739a1538411e06b8a71a` | verified |
| `inference_standalone_openai.py` | `7ef9efca3bed32052a014efceb4daffad181ea4c3bad4bbbcb8ffbe386a9a472` | verified |
| `DATASET_CARD.md (= HF README.md @ a8076d56)` | `1b9160ff0719535cbd0fe5952922f69b705362873489d566372318dbc1334193` | see data/upstream/personamem-v1/source.lock.json |
