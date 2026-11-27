"""Policy for the one-time host environment retirement; stdlib only.

Every root must be supplied explicitly. There are no personal deployment paths
or implicit HOME/TMPDIR defaults. Dry-run is the default; live-reference and
containment checks still apply before deletion.
"""
import argparse
import fnmatch
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
from dataclasses import dataclass


@dataclass(frozen=True)
class Roots:
    cache: Path
    ep: Path
    bootstrap: Path
    temp: tuple
    repo: Path
    vm: Path



PATTERNS = ('memeval-ep-*', 'memeval-seatbelt-*.sbpl', 'memeval-landlock-*.json',
            'memeval-guest-spec-*', 'memeval-msb-*', 'memeval-control-*',
            'memeval-install-*', 'memeval-probe-*')
KEEP_PATTERNS = ('memeval-f*', 'memeval-c1-*', 'memeval-k1*')


def one_line(text):
    return ' '.join(str(text).split())


class Checks:
    @staticmethod
    def memeval_running():
        p = subprocess.run(['pgrep', '-x', 'memeval'], capture_output=True, text=True)
        if p.returncode not in (0, 1):
            raise RuntimeError('pgrep failed: ' + one_line(p.stderr))
        return p.returncode == 0

    @staticmethod
    def live(path):
        # No tool failure is interpreted as proof of absence.
        if not path.exists():
            return 'vanished'
        ps = subprocess.run(['/bin/ps', '-axo', 'pid=,command='], capture_output=True, text=True)
        if not path.exists():
            return 'vanished'
        if ps.returncode:
            return 'ps failed: ' + one_line(ps.stderr)
        for line in ps.stdout.splitlines():
            fields = line.strip().split(None, 1)
            if len(fields) == 2 and fields[0] != str(os.getpid()) and re.search(r'(?<![\w/.-])' + re.escape(str(path)) + r'(?=/|\s|$|[\'"=:])', fields[1]):
                return 'live ps reference pid=' + fields[0]
        args = ['lsof', '-nP'] + (['+D', str(path)] if path.is_dir() else ['--', str(path)])
        p = subprocess.run(args, capture_output=True, text=True)
        if not path.exists():
            return 'vanished'
        # lsof can print usage to either stream. Neither stream is TSV output.
        if p.returncode not in (0, 1) or p.stderr.strip():
            return 'lsof could not prove absence (exit {})'.format(p.returncode)
        if p.stdout.strip():
            return 'live lsof reference'
        return ''


def size(path):
    try:
        if path.is_symlink():
            return path.lstat().st_size
        if path.is_file():
            return path.stat().st_size
        return sum(p.stat().st_size for p in path.rglob('*') if p.is_file() and not p.is_symlink()) if path.exists() else 0
    except FileNotFoundError:
        return 0


def candidates(roots):
    envs = roots.cache / 'envs'
    if envs.is_dir():
        for p in sorted(envs.iterdir()):
            yield p, envs, 'retired-host-env'
    for name in ('env-processes', 'env-vms'):
        p = roots.cache / name
        if p.exists() or p.is_symlink():
            yield p, roots.cache, 'retired-host-metadata'
    if roots.ep.is_dir():
        for p in sorted(roots.ep.iterdir()):
            if re.fullmatch(r'[0-9a-f]{8}', p.name):
                yield p, roots.ep, 'retired-episode'
    if roots.bootstrap.exists() or roots.bootstrap.is_symlink():
        yield roots.bootstrap, roots.bootstrap.parent, 'bootstrap-must-verify'
    for root in roots.temp:
        if root.is_dir():
            for p in sorted(root.iterdir()):
                if any(fnmatch.fnmatchcase(p.name, pattern) for pattern in PATTERNS):
                    yield p, root, 'retired-temp'


def lock_source(env):
    name, _, sha = env.name.partition('@')
    identity = name + '@' + sha[:12]
    if name in ('mem0', 'mem0-infer', 'graphiti'):
        return env / 'requirements.lock.txt', identity, 'requirements.lock.txt'
    if name == 'mastra':
        return env / 'project/package-lock.json', identity, 'package-lock.json'
    return None


def bootstrap_verified(path):
    # Require explicit provenance, never infer it merely from the directory name.
    entries = list(path.rglob('*'))
    if any(p.is_symlink() for p in entries):
        return False
    markers = [p for p in entries if p.is_file() and p.suffix in ('.json', '.log', '.sh') and p.stat().st_size <= 1024 * 1024]
    return bool(entries) and any('msb bootstrap' in p.read_text(errors='replace').lower() for p in markers)


def retire(roots, apply=False, checks=None):
    checks = checks or Checks()
    if apply and checks.memeval_running():
        raise RuntimeError('SKIP: pgrep -x memeval found a live evaluator')
    rows = []
    def emit(action, path, reason):
        rows.append((action, size(path) if isinstance(path, Path) else 0, one_line(path), one_line(reason)))
    keeps = [roots.cache / n for n in ('longmemeval-oracle', 'octop-memory', 'cassettes')] + [roots.repo / 'reports', roots.repo / 'bin']
    for root in roots.temp:
        for pattern in KEEP_PATTERNS:
            keeps.extend(sorted(root.glob(pattern)) or [root / pattern])
    for p in keeps:
        emit('KEEP', p, 'protected data or review evidence')
    for name in ('selftests', 'env-gates'):
        p = roots.cache / name
        keeps.append(p)
        emit('KEEP', p, 'managed by memeval gc')
    for p in (roots.vm, roots.cache / 'echo'):
        emit('LIST-ONLY', p, 'D8: requires separate user decision')
    # Explicit roots can overlap. Neither list-only data nor the repository may
    # be deleted indirectly through an ancestor selected as a retirement target.
    protected = keeps + [roots.vm, roots.cache / 'echo', roots.repo]
    for image in ('ghcr.io/astral-sh/uv:python3.12-bookworm-slim',
                  'ghcr.io/astral-sh/uv@sha256:a2e8b64aed3382a20c03fc6f1e03fe95fb312425e071ad27d7936a0f02c3e497',
                  'python:3.12-slim'):
        emit('LIST-ONLY', 'msb image rm ' + image, 'D8: local read-only image_ref inventory 2026-10-19; never executed')
    emit('LIST-ONLY', 'brew uninstall --formula microsandbox && brew untap superradcompany/tap', 'D8: never executed')
    seen = set()
    for path, root, reason in candidates(roots):
        if not path.exists() and not path.is_symlink():
            emit('SKIP', path, 'vanished'); continue
        if str(path) in seen:
            continue
        seen.add(str(path))
        real, boundary = path.resolve(), root.resolve()
        if (root.is_relative_to(roots.cache) and not boundary.is_relative_to(roots.cache.resolve())) or real == boundary or not real.is_relative_to(boundary):
            emit('SKIP', path, 'realpath outside owning root'); continue
        if any(real.is_relative_to(p.resolve()) or p.resolve().is_relative_to(real)
               for p in protected):
            emit('KEEP', path, 'protected'); continue
        live = checks.live(real)
        if live:
            emit('SKIP', path, live); continue
        if not path.exists():
            emit('SKIP', path, 'vanished'); continue
        if reason == 'bootstrap-must-verify' and not bootstrap_verified(real):
            emit('SKIP', path, 'unverified msb bootstrap provenance'); continue
        if reason == 'retired-host-env':
            lock = real / 'env.lock.json'
            if lock.exists():
                try:
                    if json.loads(lock.read_text()).get('version') == 3:
                        emit('KEEP', path, 'current container Lock v3; use memeval env destroy'); continue
                except (ValueError, OSError):
                    emit('SKIP', path, 'unreadable environment lock'); continue
            if not lock.exists() and (real / 'install-failure.log').is_file():
                reason = 'failed-install-diagnostics'
            source = lock_source(real) if reason == 'retired-host-env' else None
            if source:
                src, identity, filename = source
                backup_root = roots.repo / 'reports/p6-reference/host-locks'
                dst = backup_root / identity / filename
                if not src.is_file() or not src.resolve().is_relative_to(real):
                    emit('SKIP', path, 'missing or escaping lock required for migration'); continue
                if (backup_root.resolve() != roots.repo.resolve() / 'reports/p6-reference/host-locks'
                        or not dst.resolve().is_relative_to(backup_root.resolve())):
                    emit('SKIP', path, 'lock backup destination escapes owning root'); continue
                if dst.exists() and dst.read_bytes() != src.read_bytes():
                    emit('SKIP', path, 'different existing lock; preserve both: ' + str(dst)); continue
                if not dst.exists():
                    emit('COPY', dst, 'preserve real lock from ' + str(src))
                    if apply:
                        dst.parent.mkdir(parents=True, exist_ok=True)
                        # Exclusive creation, never overwrite a concurrently written lock.
                        with dst.open('xb') as out:
                            out.write(src.read_bytes())
                else:
                    emit('KEEP', dst, 'identical migration lock already preserved')
        emit('DELETE', path, reason + ('; apply' if apply else '; dry-run'))
        if apply:
            live = checks.live(real)
            if not path.exists():
                rows.pop(); emit('SKIP', path, 'vanished'); continue
            if path.resolve() != real or live:
                rows.pop(); emit('SKIP', path, live or 'path/reference changed before deletion'); continue
            if path.is_symlink() or not path.is_dir():
                path.unlink()
            else:
                shutil.rmtree(path)
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    for name in ('cache', 'ep', 'bootstrap', 'repo', 'vm'):
        parser.add_argument('--' + name + '-root', type=Path, required=True)
    parser.add_argument('--temp-root', type=Path, action='append', required=True)
    args = parser.parse_args()
    supplied = [args.cache_root, args.ep_root, args.bootstrap_root,
                args.repo_root, args.vm_root, *args.temp_root]
    if any(not p.is_absolute() or p.resolve() == Path('/') for p in supplied):
        parser.error('each root must be an explicit absolute path below the filesystem root')
    roots = Roots(args.cache_root, args.ep_root, args.bootstrap_root,
                  tuple(args.temp_root), args.repo_root, args.vm_root)
    print('ACTION\tSIZE\tPATH\tREASON')
    try:
        for row in retire(roots, args.apply):
            print('\t'.join(str(x) for x in row))
    except (RuntimeError, OSError) as exc:
        parser.exit(1, one_line(exc) + '\n')


if __name__ == '__main__':
    main()
