"""Build-time download only; runtime embedding is local_files_only."""
import hashlib
import json
import sys
from pathlib import Path


def main(root):
    from fastembed import TextEmbedding
    root.mkdir(parents=True, exist_ok=True)
    TextEmbedding(model_name="BAAI/bge-small-en-v1.5", cache_dir=str(root))
    files = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
             for p in sorted(root.rglob('*')) if p.is_file() and p.name != 'model-assets.json'}
    (root / 'model-assets.json').write_text(json.dumps({'model': 'BAAI/bge-small-en-v1.5', 'sha256': files}, indent=2) + '\n')


if __name__ == '__main__':
    main(Path(sys.argv[1]).resolve())
