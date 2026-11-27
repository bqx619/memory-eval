"""Build-time model snapshots only; PostgreSQL runs in a separate image."""
import hashlib
import json
import os
import sys
from pathlib import Path

MODELS = ['BAAI/bge-small-en-v1.5', 'cross-encoder/ms-marco-MiniLM-L-6-v2']


def main(root):
    root.mkdir(parents=True, exist_ok=True)
    os.environ['HF_HOME'] = str(root / 'hf')
    from huggingface_hub import snapshot_download
    snapshots = {model: Path(snapshot_download(model, cache_dir=str(root / 'hf' / 'hub'))).name for model in MODELS}
    files = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
             for p in sorted(root.rglob('*')) if p.is_file() and p.name != 'model-assets.json'}
    (root / 'model-assets.json').write_text(json.dumps({'resolved_snapshots': snapshots, 'sha256': files}, indent=2) + '\n')


if __name__ == '__main__':
    main(Path(sys.argv[1]).resolve())
