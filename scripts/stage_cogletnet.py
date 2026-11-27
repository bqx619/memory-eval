#!/usr/bin/env python3
"""Stage a read-only CogletNet source snapshot and external adapter for install.

No git commands, dependency installation, credential files or upstream writes.
Usage: python3 -B scripts/stage_cogletnet.py --source ../cogletnet --out <scratch>
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil


def stage(source, output):
    source, output = source.resolve(), output.resolve()
    if output == source or source in output.parents:
        raise ValueError('output must be outside the upstream project')
    output.mkdir(parents=True, exist_ok=False)
    root = Path(__file__).resolve().parents[1]
    project = output / 'cogletnet'
    shutil.copytree(root / 'suts/cogletnet', project, copy_function=shutil.copy,
                    ignore=shutil.ignore_patterns('__pycache__', 'test_*'))
    shutil.copytree(root / 'suts/_bridge', output / '_bridge', copy_function=shutil.copy,
                    ignore=shutil.ignore_patterns('__pycache__', 'test_*'))
    destination = project / 'source'
    destination.mkdir()
    originals = {}
    # Include only workspace manifests and Rust source, not data, credentials,
    # target artifacts, local configuration or upstream evaluation results.
    paths = [source / name for name in ('Cargo.toml', 'Cargo.lock', 'rust-toolchain.toml')]
    for directory in ('crates', 'examples', 'evals'):
        paths += [p for p in (source / directory).rglob('*')
                  if p.is_file() and p.suffix in ('.rs', '.toml') and 'target' not in p.parts]
    for path in sorted(paths):
        relative = path.relative_to(source)
        if path.is_symlink():
            raise ValueError('source symlink unsupported: ' + str(relative))
        data = path.read_bytes()
        originals[str(relative)] = hashlib.sha256(data).hexdigest()
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    workspace = destination / 'Cargo.toml'
    text = workspace.read_text()
    if 'members = [' not in text:
        raise ValueError('unrecognized workspace members declaration')
    workspace.write_text(text.replace('members = [', 'members = ["memeval-host", ', 1))
    shutil.copytree(project / 'native', destination / 'memeval-host', copy_function=shutil.copy)
    with (destination / 'Cargo.lock').open('a') as lock:
        lock.write('\n[[package]]\nname = "cogletnet-memeval"\nversion = "0.1.0"\ndependencies = [\n "adapters",\n "cogletnet",\n "serde_json",\n "tokio",\n]\n')
    canonical = json.dumps(originals, sort_keys=True, separators=(',', ':')).encode()
    snapshot = hashlib.sha256(canonical).hexdigest()
    (project / 'source-files.json').write_text(json.dumps(originals, sort_keys=True, indent=2) + '\n')
    for manifest_path in sorted(project.glob('manifest*.json')):
        manifest = json.loads(manifest_path.read_text())
        manifest['bridge']['config']['source_snapshot_sha256'] = snapshot
        manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({'manifest': str(project / 'manifest.json'), 'source_files': len(originals),
                      'source_snapshot_sha256': snapshot}, ensure_ascii=False))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    stage(args.source, args.out)
