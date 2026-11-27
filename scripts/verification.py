"""Scratch-only verification outputs and read-only golden-directory evidence."""
import argparse
from contextlib import contextmanager, nullcontext
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def inventory(root=ROOT):
    """Hash file contents in chunks; paths capture additions/deletions as well."""
    result = {}
    for name in ("reports", "docs"):
        entries = {}
        for path in sorted((root / name).rglob("*")):
            if path.is_file():
                digest = hashlib.sha256()
                with path.open("rb") as stream:
                    for block in iter(lambda: stream.read(1 << 20), b""):
                        digest.update(block)
                entries[path.relative_to(root / name).as_posix()] = digest.hexdigest()
            elif path.is_symlink():
                entries[path.relative_to(root / name).as_posix()] = "symlink:" + os.readlink(path)
        result[name] = entries
    return result


def write_json(path, value):
    with path.open("w", encoding="utf-8") as stream:
        json.dump(value, stream, ensure_ascii=False, sort_keys=True, indent=2)
        stream.write("\n")
    path.chmod(0o600)


def output_directory(value=None, root=ROOT):
    if value is None:
        temp_root = Path(tempfile.gettempdir()).resolve()
        if temp_root.is_relative_to(root):
            temp_root = Path("/tmp").resolve()
        path = Path(tempfile.mkdtemp(prefix="memeval-verify-", dir=temp_root)).resolve()
    else:
        path = Path(value).expanduser().resolve()
        if path.is_relative_to(root):
            raise ValueError("--out must be outside the repository (including symlink targets)")
        if path.exists() and (not path.is_dir() or any(path.iterdir())):
            raise ValueError("--out must be a new or empty scratch directory; existing artifacts are read-only")
        path.mkdir(parents=True, exist_ok=True)
    path.chmod(0o700)
    return path


@contextmanager
def protected_outputs(out, root=ROOT):
    before = inventory(root)
    write_json(out / "protected-before.json", before)
    try:
        yield
    finally:
        after = inventory(root)
        write_json(out / "protected-after.json", after)
        evidence = {}
        for name in before:
            old, new = before[name], after[name]
            encode = lambda v: json.dumps(v, sort_keys=True, separators=(",", ":")).encode()
            evidence[name] = {
                "files_before": len(old), "files_after": len(new),
                "sha256_before": hashlib.sha256(encode(old)).hexdigest(),
                "sha256_after": hashlib.sha256(encode(new)).hexdigest(),
                "unchanged": old == new,
                "changed_paths": sorted(k for k in old.keys() | new.keys() if old.get(k) != new.get(k)),
            }
        write_json(out / "protected-check.json", evidence)
        if before != after:
            raise RuntimeError(f"reports/docs inventory changed; see {out / 'protected-check.json'}")
        print("reports/docs file lists + SHA256 unchanged (protected-check.json)", file=sys.stderr, flush=True)


class Verification:
    def __init__(self, out, executable=None):
        self.out = out
        self.executable = Path(executable).resolve() if executable else None
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("MEMEVAL_")}
        self.env.update(GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local", GOFLAGS="",
                        UV_OFFLINE="1", PYTHONDONTWRITEBYTECODE="1")
        # Opt-in Go tests must not persist reports even if enabled in the user's .env.
        self.env.update(MEMEVAL_M2B_ACCEPTANCE="", MEMEVAL_M3_ACCEPTANCE="")
        self.env_file = out / "empty-config"
        self.env_file.write_text("")
        self.env_file.chmod(0o600)

    def binary(self):
        if self.executable is None:
            self.executable = self.out / "memeval"
            result = subprocess.run(["go", "build", "-o", str(self.executable), "./cmd/memeval"],
                                    cwd=ROOT, env=self.env, capture_output=True)
            (self.out / "build.log").write_bytes(result.stdout + result.stderr)
            if result.returncode:
                raise RuntimeError(f"go build failed; see {self.out / 'build.log'}")
        return self.executable

    def cli(self, *args):
        return [str(self.binary()), "--env-file", str(self.env_file), *map(str, args)]

    def workspace(self):
        """External SUT selftest discovers 'reports' via cwd: keep it in scratch.

        Registry and SUT setup locks are writable copies. Large input trees are
        read-only inputs; all importer destinations must still be explicit.
        """
        dest = self.out / "workspace"
        dest.mkdir()
        shutil.copyfile(ROOT / "go.mod", dest / "go.mod")
        # WalkDir in selftest does not descend through a symlink at its root.
        shutil.copytree(ROOT / "docs", dest / "docs")
        for name in ("testdata", "protocols", "design", "config"):
            (dest / name).symlink_to(ROOT / name, target_is_directory=True)
        (dest / "data").mkdir()
        for name in ("normalized", "upstream"):
            (dest / "data" / name).symlink_to(ROOT / "data" / name, target_is_directory=True)
        shutil.copytree(ROOT / "data/registry", dest / "data/registry")
        shutil.copytree(ROOT / "suts", dest / "suts")
        (dest / "reports").mkdir()
        return dest


def verification_main(main, parser=None):
    """Manual verification is guarded by default; embedded callers may opt out."""
    parser = parser or argparse.ArgumentParser(description=main.__doc__)
    parser.add_argument("--out", type=Path, help="new/empty scratch directory (default: tempfile.mkdtemp)")
    parser.add_argument("--memeval", type=Path, help="existing binary (default: build into scratch)")
    # Explicit per-process opt-out for embedded tests. Do not expose it as an
    # environment default that could silently disable manual acceptance checks.
    parser.add_argument("--no-protected-check", action="store_true", help=argparse.SUPPRESS)
    args = parser.parse_args()
    try:
        out = output_directory(args.out)
    except ValueError as exc:
        parser.error(str(exc))
    # stderr preserves JSON / Merkle / empty-diff stdout interfaces.
    print(f"Verification output: {out}", file=sys.stderr, flush=True)
    previous_umask = os.umask(0o077)
    try:
        if args.no_protected_check:
            print("reports/docs protected check skipped (programmatic invocation)",
                  file=sys.stderr, flush=True)
            guard = nullcontext()
        else:
            guard = protected_outputs(out)
        with guard:
            return main(args, Verification(out, args.memeval))
    finally:
        os.umask(previous_umask)
