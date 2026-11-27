"""Install only: resolve model snapshots once and record their exact file hashes."""
import hashlib
import json
import os
import sys
from pathlib import Path



LBUG_SOURCES = {  # LadybugDB C API shared library matching ladybug==0.19.0 (Claude verified 2026-10-18)
    ('darwin', 'arm64'): ('https://github.com/LadybugDB/ladybug/releases/download/v0.19.0/liblbug-osx-arm64.tar.gz',
                          '625e52ab05051b938e8841798ce57dcee5b6625e9537c6be3f3425e76974e734'),
    ('linux', 'x86_64'): ('https://github.com/LadybugDB/ladybug/releases/download/v0.19.0/liblbug-linux-x86_64.tar.gz',
                          '907ff68f1df703704ed61b618dd9502ba589c5f8983a96de6be2282c6a1c11db'),
    ('linux', 'aarch64'): ('https://github.com/LadybugDB/ladybug/releases/download/v0.19.0/liblbug-linux-aarch64.tar.gz',
                           'b2b4aeb3cb4405ea773b42accecba4c8aa7f6179c198898b962a98058ed94d2a'),
}


def install_lbug_capi(root):
    """ladybug 0.19.0 wheels ship only the Python extension; the C API shared lib (liblbug) is otherwise fetched at
    runtime. Install under the writable models root; LBUG_C_API_LIB_PATH names the
    full shared-library file (ladybug._lbug_capi._resolve_library_path)."""
    import platform, tarfile, urllib.request
    key = (sys.platform if sys.platform in ('darwin',) else 'linux', platform.machine().lower().replace('arm64', 'arm64'))
    if key[0] == 'linux' and key[1] in ('aarch64', 'arm64'):
        key = ('linux', 'aarch64')
    if key not in LBUG_SOURCES:
        raise RuntimeError(f'no pinned liblbug source for {key}')
    url, sha = LBUG_SOURCES[key]
    archive = root / 'lbug' / url.rsplit('/', 1)[1]
    archive.parent.mkdir(parents=True, exist_ok=True)
    if not archive.exists():
        with urllib.request.urlopen(url, timeout=600) as r, open(archive, 'wb') as f:
            f.write(r.read())
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if digest != sha:
        raise RuntimeError(f'liblbug archive sha256 mismatch: {digest} != {sha}')
    libdir = root / 'lbug' / 'lib'
    libdir.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive) as tar:
        for member in tar.getmembers():
            name = Path(member.name).name
            if member.isfile() and name.startswith('liblbug') and (name.endswith('.dylib') or '.so' in name):
                member.name = name
                tar.extract(member, libdir, filter='data')
    # The archive's liblbug.dylib / liblbug.0.dylib (and .so / .so.0) are symlinks, which tar extraction above skips;
    # recreate the generic names that _resolve_library_path probes from the versioned real file.
    import shutil
    real = sorted(p for p in libdir.glob('liblbug*') if not p.is_symlink() and p.is_file())
    if real:
        src = real[-1]
        for generic in (('liblbug.dylib', 'liblbug.0.dylib') if sys.platform == 'darwin' else ('liblbug.so', 'liblbug.so.0')):
            dst = libdir / generic
            if not dst.exists():
                shutil.copyfile(src, dst); dst.chmod(0o755)
    (root / 'lbug' / 'installed.json').write_text(json.dumps({'url': url, 'sha256': sha, 'libdir': str(libdir),
        'files': sorted(p.name for p in libdir.glob('liblbug*'))}, indent=2) + '\n')

def main(root):
    root.mkdir(parents=True, exist_ok=True)
    install_lbug_capi(root)
    os.environ['HF_HOME'] = str(root / 'hf')
    os.environ['FASTEMBED_CACHE_PATH'] = str(root / 'fastembed')
    contract = json.loads(Path(__file__).with_name('model-contract.json').read_text())
    from huggingface_hub import snapshot_download
    snapshot = Path(snapshot_download(contract['gliner_model'], revision=contract['revision'] or 'main',
                                      cache_dir=str(root / 'hf' / 'hub')))
    revision = snapshot.name
    if not revision.startswith(contract['observed_main_sha_prefix']):
        raise RuntimeError('GLiNER main changed from reviewed prefix; review model-contract.json before install')
    # cognee's chunker tokenizes with tiktoken, which downloads encodings at first use; pre-cache them in env.
    os.environ['TIKTOKEN_CACHE_DIR'] = str(root / 'tiktoken')
    (root / 'tiktoken').mkdir(parents=True, exist_ok=True)
    import tiktoken
    for enc in ('cl100k_base', 'o200k_base'):
        tiktoken.get_encoding(enc)
    from fastembed import TextEmbedding
    TextEmbedding(model_name=contract['embedding_model'], cache_dir=str(root / 'fastembed'))
    # cognee's embedding engine also loads the HF *tokenizer* of the embedding model (AutoTokenizer.from_pretrained)
    # and GLiNER's config names a DeBERTa backbone; cache their small config/tokenizer files offline too.
    for repo, patterns in ((contract['embedding_model'], ['*.json', '*.txt']), ('microsoft/deberta-v3-base', ['*.json', 'spm.model', '*.txt'])):
        snapshot_download(repo, cache_dir=str(root / 'hf' / 'hub'), allow_patterns=patterns)
    files = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
             for p in sorted(root.rglob('*')) if p.is_file()}
    (root / 'model-assets.json').write_text(json.dumps({'gliner_model': contract['gliner_model'],
        'resolved_revision': revision, 'embedding_model': contract['embedding_model'], 'sha256': files}, indent=2) + '\n')


if __name__ == '__main__':
    main(Path(sys.argv[1]).resolve())
