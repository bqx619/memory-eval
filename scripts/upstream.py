"""Locate optional pinned upstream copies without fetching or importing them."""

from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]


def require_upstream(rel, root=ROOT):
    """Return a source path, or exit 3 with restore instructions if absent.

    Check only source availability here. Permission errors, invalid source syntax
    and --check template drift must remain failures, never missing-source skips.
    """
    rel = Path(rel)
    if rel.is_absolute() or '..' in rel.parts:
        raise ValueError(f'upstream reference must be a local relative path: {rel}')
    path = root / 'design/upstream' / rel
    try:
        path.stat()
    except FileNotFoundError:
        print(f'Upstream source unavailable: {path}; restore with '
              f'python3 -B scripts/fetch_upstream_refs.py --only {rel.parts[0]}',
              file=sys.stderr)
        raise SystemExit(3)
    return path
