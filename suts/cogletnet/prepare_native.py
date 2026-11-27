"""Build in the generated Linux image only; never install on the host."""
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.request


class DownloadLengthMismatch(ValueError):
    pass


class DownloadSHA256Mismatch(ValueError):
    pass


def fetch(url, dest=None, *, expected_sha256=None):
    """Return bytes, or the SHA256 when streaming to dest; retry at most 3 times.

    Hash checking belongs inside the retry boundary, so a truncated response
    without Content-Length is still discarded. Log only error types, never
    URLs, headers or exception messages that might contain credentials.
    """
    path = Path(dest) if dest is not None else None
    delays = (5, 15)
    for attempt in range(3):
        try:
            digest, size = hashlib.sha256(), 0
            data = bytearray()
            with urllib.request.urlopen(url, timeout=180) as response:
                length = response.headers.get('Content-Length')
                length = int(length) if length is not None else None
                if length is not None and length < 0:
                    raise DownloadLengthMismatch()
                output = path.open('wb') if path is not None else None
                try:
                    while chunk := response.read(256 * 1024):
                        size += len(chunk)
                        digest.update(chunk)
                        if output is not None:
                            output.write(chunk)
                        else:
                            data.extend(chunk)
                finally:
                    if output is not None:
                        output.close()
            if length is not None and size != length:
                raise DownloadLengthMismatch()
            actual = digest.hexdigest()
            if expected_sha256 is not None and actual != expected_sha256.lower():
                raise DownloadSHA256Mismatch()
            return actual if path is not None else bytes(data)
        except Exception as error:
            if path is not None:
                try:
                    path.unlink(missing_ok=True)
                except OSError as cleanup_error:
                    error = cleanup_error
            if attempt == 2:
                raise SystemExit(f'download failed after 3 attempts: {type(error).__name__}') from None
            print(f'download attempt {attempt + 1}/3 failed: {type(error).__name__}; '
                  f'retrying in {delays[attempt]}s', file=sys.stderr, flush=True)
            time.sleep(delays[attempt])


def main():
    if platform.system() != 'Linux' or os.environ.get('HOME') != '/opt/memeval/env/models-home':
        raise SystemExit('prepare_native must run in the generated Linux image')
    root = Path(sys.argv[1]).resolve()
    assets = root.parent / 'assets'
    work = root / 'native-build'
    shutil.copytree(assets / 'source', work)
    arch = {'aarch64': 'aarch64', 'x86_64': 'x86_64'}[platform.machine()]
    version = '1.97.1'
    name = f'rust-{version}-{arch}-unknown-linux-gnu'
    url = f'https://static.rust-lang.org/dist/{name}.tar.xz'
    archive = root / (name + '.tar.xz')
    expected = fetch(url + '.sha256').decode().split()[0]
    actual = fetch(url, archive, expected_sha256=expected)
    with tarfile.open(archive) as bundle:
        bundle.extractall(root, filter='data')
    toolchain = root / 'rust-toolchain'
    subprocess.run(['sh', str(root / name / 'install.sh'), '--prefix=' + str(toolchain),
                    '--components=rustc,cargo,rust-std-' + arch + '-unknown-linux-gnu', '--disable-ldconfig'], check=True)
    env = dict(os.environ, PATH=str(toolchain / 'bin') + ':' + os.environ['PATH'],
               CARGO_HOME=str(root / 'cargo-home'), CARGO_TARGET_DIR=str(root / 'target'),
               CARGO_BUILD_JOBS='4', CARGO_NET_RETRY='10', CARGO_HTTP_TIMEOUT='180',
               CARGO_HTTP_LOW_SPEED_LIMIT='1')
    # The original lock's existing package versions/checksums are unchanged;
    # staging adds only this host crate's package record.
    subprocess.run(['cargo', 'build', '--locked', '--release', '-p', 'cogletnet-memeval'], cwd=work, env=env, check=True)
    (root / 'bin').mkdir(exist_ok=True)
    shutil.copy2(root / 'target/release/cogletnet-memeval', root / 'bin/cogletnet-memeval')
    facts = {'rust_version': version, 'rust_url': url, 'rust_sha256': actual,
             'cargo_lock_sha256': hashlib.sha256((work / 'Cargo.lock').read_bytes()).hexdigest(),
             'binary_sha256': hashlib.sha256((root / 'bin/cogletnet-memeval').read_bytes()).hexdigest()}
    (root / 'native-build.json').write_text(json.dumps(facts, sort_keys=True, indent=2) + '\n')
    for path in (work, toolchain, root / name, root / 'target', root / 'cargo-home'):
        shutil.rmtree(path)
    archive.unlink()


if __name__ == '__main__':
    main()
