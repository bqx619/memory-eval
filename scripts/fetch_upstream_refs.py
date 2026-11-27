#!/usr/bin/env python3
"""Restore upstream references from SOURCES.md pins, or verify them offline.

Only the standard library is used. Non-GitHub sources remain manual restores;
their pinned hashes are still checked by --verify-only. Downloads are staged
beside the destination and published only after their SHA256 matches.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
from pathlib import Path, PurePosixPath
import re
import shutil
import sys
import tempfile
from urllib.parse import quote
from urllib.request import urlopen


UPSTREAM = Path(__file__).resolve().parents[1] / "design/upstream"


@dataclass(frozen=True)
class FileRef:
    path: str
    sha256: str
    upstream_path: str
    skip_reason: str = ""


@dataclass(frozen=True)
class SourcePin:
    name: str
    repo: str | None
    commit: str | None
    files: tuple[FileRef, ...]


def _cell(text):
    return text.strip().strip("`").strip()


def _row(line):
    return [_cell(cell) for cell in line.strip().strip("|").split("|")]


def _relative_path(value):
    if (not value or PurePosixPath(value).is_absolute() or "\\" in value
            or any(ord(c) < 32 for c in value)
            or any(p in ("", ".", "..") for p in value.split("/"))):
        raise ValueError(f"unsafe relative path: {value!r}")
    return value


def parse_sources(text, name):
    """Parse actual pin-table variants, including annotated local renames."""
    lines = text.splitlines()
    header_at = None
    for i, line in enumerate(lines):
        if not line.lstrip().startswith("|"):
            continue
        columns = [c.lower() for c in _row(line)]
        paths = [j for j, c in enumerate(columns)
                 if c in ("path", "file", "文件", "文件路径")]
        if paths and "sha256" in columns:
            header_at = i
            path_col, hash_col = paths[0], columns.index("sha256")
            break
    if header_at is None:
        raise ValueError(f"{name}: no path/sha256 table")
    if (header_at + 1 >= len(lines)
            or not all(re.fullmatch(r":?-+:?", c)
                       for c in _row(lines[header_at + 1]))
            or len(_row(lines[header_at + 1])) != len(columns)):
        raise ValueError(f"{name}: invalid table separator")

    preamble = "\n".join(lines[:header_at])
    repos = set(re.findall(r"https?://github\.com/([\w.-]+/[\w.-]+)", preamble))
    commits = set(s.lower() for s in re.findall(
        r"(?<![0-9a-fA-F])[0-9a-fA-F]{40}(?![0-9a-fA-F])", preamble))
    if len(repos) > 1 or len(commits) > 1:
        raise ValueError(f"{name}: ambiguous repository or commit")
    repo = next(iter(repos), None)
    if repo and repo.endswith(".git"):
        repo = repo[:-4]
    commit = next(iter(commits), None)
    if repo and not commit:
        raise ValueError(f"{name}: GitHub repository has no pinned 40-hex commit")
    source_note = next((line.strip() for line in lines[:header_at]
                        if re.match(r"\s*(来源|source)\s*[:：]", line, re.I)), "")

    files = []
    seen = set()
    for number, line in enumerate(lines[header_at + 2:], header_at + 3):
        if not line.lstrip().startswith("|"):
            break
        cells = _row(line)
        if len(cells) != len(columns):
            raise ValueError(f"{name}:{number}: invalid table row")
        # PersonaMem annotates an HF origin inside the path cell itself.
        label = cells[path_col]
        path = _relative_path(re.split(r"\s+\(=", label, maxsplit=1)[0].rstrip("`"))
        if PurePosixPath(path).name.lower() == "sources.md":
            raise ValueError(f"{name}:{number}: cannot overwrite source pins")
        if path in seen:
            raise ValueError(f"{name}:{number}: duplicate path {path}")
        seen.add(path)
        digest = cells[hash_col].lower()
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError(f"{name}:{number}: invalid sha256 for {path}")
        notes = " ".join(c for j, c in enumerate(cells) if j != hash_col)
        skip = ""
        if re.search(r"\bHF\b|Hugging\s*Face|huggingface\.co|\bPyPI\b|\bwheel\b", notes, re.I):
            skip = f"non-GitHub source: {notes}"
        elif not repo:
            skip = "no pinned GitHub source" + (f"; {source_note}" if source_note else "")
        # Memora DATA_README.md and PrefEval gen_data_prompt.txt were renamed
        # locally; the verification column supplies their upstream paths.
        renamed = re.search(r"=\s*upstream\s+`?([^\s`)]+)", notes, re.I)
        remote = _relative_path(renamed.group(1) if renamed else path)
        files.append(FileRef(path, digest, remote, skip))
    if not files:
        raise ValueError(f"{name}: empty source table")
    return SourcePin(name, repo, commit, tuple(files))


def source_files(root):
    """Discover one case-insensitive sources.md per immediate directory."""
    pins = {}
    for directory in sorted(root.iterdir()):
        if not directory.is_dir():
            continue
        found = [p for p in directory.iterdir()
                 if p.name.lower() == "sources.md" and p.is_file()]
        if len(found) > 1:
            raise ValueError(f"{directory.name}: multiple sources.md files")
        if found:
            pins[directory.name] = found[0]
    if not pins:
        raise ValueError(f"no sources.md files under {root}")
    return pins


def raw_url(source, entry):
    if entry.skip_reason or not source.repo or not source.commit:
        raise ValueError(entry.skip_reason or "no pinned GitHub source")
    return (f"https://raw.githubusercontent.com/{source.repo}/{source.commit}/"
            + quote(entry.upstream_path, safe="/"))


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def verify_file(path, expected):
    """Read-only verification; never delete a mismatching local copy."""
    if not path.exists():
        raise ValueError("missing")
    actual = sha256_file(path)
    if actual != expected:
        raise ValueError(f"sha256 mismatch: expected {expected}, got {actual}")


def destination(directory, relative):
    path = directory
    for part in _relative_path(relative).split("/"):
        path = path / part
        if path.is_symlink():
            raise ValueError(f"symlink destination not allowed: {path}")
    return path


def download(url, path):
    with urlopen(url, timeout=60) as response, path.open("wb") as target:
        shutil.copyfileobj(response, target)


def fetch_file(source, entry, directory, downloader=download):
    """An injectable downloader writes to a temporary path, never final output."""
    url = raw_url(source, entry)
    target = destination(directory, entry.path)
    target.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(prefix=f".{target.name}-", dir=target.parent,
                                     delete=False) as stream:
        staging = Path(stream.name)
    try:
        downloader(url, staging)
        verify_file(staging, entry.sha256)
        staging.chmod(0o644)
        staging.replace(target)
    finally:
        staging.unlink(missing_ok=True)


def main(argv=None, *, root=UPSTREAM, downloader=download):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--only", nargs="+", metavar="NAME", help="select upstream directories")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--verify-only", action="store_true", help="check local hashes without networking or writes")
    mode.add_argument("--list", action="store_true", help="list parsed repository, commit and file count")
    args = parser.parse_args(argv)
    try:
        paths = source_files(root)
        unknown = sorted(set(args.only or ()) - paths.keys())
        if unknown:
            raise ValueError("unknown upstream name(s): " + ", ".join(unknown))
        # Parse every selected pin before any download, so malformed pins fail
        # without partially updating the selected collection.
        sources = [(p, parse_sources(p.read_text(encoding="utf-8"), name))
                   for name, p in paths.items() if not args.only or name in args.only]
    except (OSError, ValueError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1

    if args.list:
        for _, source in sources:
            print(f"{source.name}: repo={source.repo or 'unavailable (non-GitHub)'} "
                  f"commit={source.commit or 'unavailable'} files={len(source.files)}")
        return 0

    checked = skipped = errors = 0
    for pin, source in sources:
        for entry in source.files:
            label = f"{source.name}/{entry.path}"
            if not args.verify_only and entry.skip_reason:
                print(f"SKIP {label}: {entry.skip_reason}; restore manually, then --verify-only")
                skipped += 1
                continue
            try:
                if args.verify_only:
                    verify_file(destination(pin.parent, entry.path), entry.sha256)
                else:
                    fetch_file(source, entry, pin.parent, downloader)
                print(f"OK {label}")
                checked += 1
            except (OSError, ValueError) as error:
                print(f"ERROR {label}: {error}", file=sys.stderr)
                errors += 1
    print(f"Summary: files={checked + skipped + errors} verified={checked} "
          f"skipped={skipped} errors={errors}")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
