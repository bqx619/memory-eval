# NLTK PorterStemmer reference implementation (unaltered reference)

Source: `nltk/stem/porter.py` from the `nltk==3.10.3` PyPI wheel, copied and byte-compared on 2026-10-17. The wheel, rather than a repository URL, was used because the corresponding GitHub 3.10.3 tag/raw path was unavailable. License: Apache-2.0; `LICENSE.txt` comes from the same wheel’s dist-info/licenses directory.

LoCoMo uses the default `nltk.stem.PorterStemmer()` mode, `NLTK_EXTENSIONS`, rather than the original Porter algorithm or Martin Porter extensions. The Go port follows all branches for that mode and is compared word by word against the pinned oracle.

| File | sha256 |
|---|---|
| `porter.py` | `ab16069c9db8f596b7fad2563118812d8b5cf84ccaebc4446a422bb0f3fd2d69` |
| `LICENSE.txt` | `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30` |
