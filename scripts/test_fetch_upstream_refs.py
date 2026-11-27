"""Offline fetcher contracts: real pins, synthetic downloads, scratch writes."""

from contextlib import redirect_stderr, redirect_stdout
import hashlib
import io
import itertools
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
import fetch_upstream_refs as fetch


COMMIT = "0123456789abcdef0123456789abcdef01234567"
CONTENT = b"upstream fixture\n"
SHA = hashlib.sha256(CONTENT).hexdigest()


def fixture(path="nested/ref.py", notes="verified", digest=SHA):
    return (f"repo: https://github.com/example/project\ncommit: `{COMMIT}`\n\n"
            "| path | sha256 | 核验 |\n|---|---|---|\n"
            f"| `{path}` | `{digest}` | {notes} |\n")


class PinTests(unittest.TestCase):
    def test_all_ten_real_sources(self):
        expected = {"beam": 10, "convomem": 110, "halumem": 9, "locomo": 6,
                    "longmemeval": 7, "memora": 11, "memoryagentbench": 16,
                    "nltk": 2, "personamem": 6, "prefeval": 20}
        paths = fetch.source_files(fetch.UPSTREAM)
        self.assertEqual(set(paths), set(expected))
        sources = {}
        for name, path in paths.items():
            with self.subTest(name=name):
                source = fetch.parse_sources(path.read_text(encoding="utf-8"), name)
                sources[name] = source
                self.assertEqual(len(source.files), expected[name])
                if name != "nltk":
                    self.assertRegex(source.commit, r"^[0-9a-f]{40}$")
                    self.assertIn("/", source.repo)
                else:
                    self.assertIsNone(source.repo)
                    self.assertIsNone(source.commit)
                    self.assertTrue(all("wheel" in e.skip_reason for e in source.files))
        skipped = {(s.name, e.path) for s in sources.values() for e in s.files if e.skip_reason}
        self.assertEqual(skipped, {("nltk", "porter.py"), ("nltk", "LICENSE.txt"),
                                  *((n, "DATASET_CARD.md") for n in
                                    ("beam", "convomem", "halumem", "memoryagentbench", "personamem"))})
        for name, local, remote in (("memora", "DATA_README.md", "data/README.md"),
                                    ("prefeval", "gen_data_prompt.txt", "benchmark_dataset/gen_data_prompt.txt")):
            s = sources[name]
            entry = next(e for e in s.files if e.path == local)
            self.assertEqual(entry.upstream_path, remote)
            self.assertEqual(fetch.raw_url(s, entry),
                             f"https://raw.githubusercontent.com/{s.repo}/{s.commit}/{remote}")

    def test_columns_can_be_reordered_with_or_without_backticks(self):
        for path_header, quoted, columns in itertools.product(
                ("path", "文件"), (False, True), itertools.permutations(("path", "bytes", "sha256", "核验"))):
            values = {"path": "src/file.py", "bytes": "17", "sha256": SHA, "核验": "verified"}
            header = [path_header if c == "path" else c for c in columns]
            row = [f"`{values[c]}`" if quoted else values[c] for c in columns]
            text = (f"来源：https://github.com/org/repo，commit `{COMMIT}`\n"
                    + "| " + " | ".join(header) + " |\n|---|:---:|---:|:---|\n"
                    + "| " + " | ".join(row) + " |\n")
            s = fetch.parse_sources(text, "test")
            self.assertEqual(s.files, (fetch.FileRef("src/file.py", SHA, "src/file.py"),))
            self.assertEqual((s.repo, s.commit), ("org/repo", COMMIT))

    def test_url_encodes_path_and_preserves_case(self):
        s = fetch.parse_sources(fixture(path="a space/问#?.py"), "test")
        self.assertEqual(fetch.raw_url(s, s.files[0]),
                         f"https://raw.githubusercontent.com/example/project/{COMMIT}/a%20space/%E9%97%AE%23%3F.py")

    def test_invalid_pins_rejected(self):
        texts = [fixture(digest="123"), fixture().replace(COMMIT, "main"),
                 fixture().replace("|---|---|---|", "not a separator"),
                 fixture() + f"| nested/ref.py | {SHA} | verified |\n",
                 fixture().replace("commit:", "commit: " + "a" * 40 + " ")]
        for path in ("../escape", "/absolute", "dir/../../escape", "a\\b", "a//b", "SOURCES.md"):
            texts.append(fixture(path=path))
        for text in texts:
            with self.subTest(text=text), self.assertRaises(ValueError):
                fetch.parse_sources(text, "bad")


class FetchTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="memeval-fetch-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.no_network = mock.patch.object(fetch, "urlopen", side_effect=AssertionError("network forbidden"))
        self.no_network.start()
        self.addCleanup(self.no_network.stop)

    def pin(self, name="test", text=None, filename="SOURCES.md"):
        directory = self.root / name
        directory.mkdir()
        (directory / filename).write_text(text or fixture(), encoding="utf-8")
        return directory

    def cli(self, args, downloader=None):
        out, err = io.StringIO(), io.StringIO()
        kwargs = {"downloader": downloader} if downloader is not None else {}
        with redirect_stdout(out), redirect_stderr(err):
            code = fetch.main(args, root=self.root, **kwargs)
        return code, out.getvalue(), err.getvalue()

    def test_fetch_and_verify_matching_file(self):
        directory = self.pin()
        calls = []
        def fake(url, path):
            calls.append(url)
            path.write_bytes(CONTENT)
        code, out, err = self.cli([], fake)
        self.assertEqual((code, err), (0, ""))
        self.assertIn("verified=1 skipped=0 errors=0", out)
        self.assertEqual(calls, [f"https://raw.githubusercontent.com/example/project/{COMMIT}/nested/ref.py"])
        target = directory / "nested/ref.py"
        self.assertEqual(fetch.sha256_file(target), SHA)
        before = target.stat()
        self.assertEqual(self.cli(["--verify-only"])[0], 0)
        self.assertEqual(target.stat().st_mtime_ns, before.st_mtime_ns)

    def test_mismatching_download_is_deleted_and_nonzero(self):
        directory = self.pin()
        code, out, err = self.cli([], lambda url, path: path.write_bytes(b"wrong"))
        self.assertEqual(code, 1)
        self.assertIn("sha256 mismatch", err)
        self.assertIn("errors=1", out)
        self.assertEqual(list((directory / "nested").iterdir()), [])

    def test_failed_download_preserves_verified_copy_and_cleans_temporary_file(self):
        directory = self.pin()
        target = directory / "nested/ref.py"
        target.parent.mkdir()
        target.write_bytes(CONTENT)
        def interrupted(url, path):
            path.write_bytes(b"partial")
            raise OSError("fixture interruption")
        for fake in (interrupted, lambda url, path: path.write_bytes(b"mismatch")):
            self.assertEqual(self.cli([], fake)[0], 1)
            self.assertEqual(target.read_bytes(), CONTENT)
            self.assertEqual(list(target.parent.iterdir()), [target])

    def test_verify_reports_all_missing_and_mismatching_without_writes(self):
        text = fixture() + f"| missing.py | {SHA} | verified |\n"
        directory = self.pin(text=text)
        target = directory / "nested/ref.py"
        target.parent.mkdir()
        target.write_bytes(b"wrong")
        code, out, err = self.cli(["--verify-only"])
        self.assertEqual(code, 1)
        self.assertIn("test/nested/ref.py: sha256 mismatch", err)
        self.assertIn("test/missing.py: missing", err)
        self.assertIn("errors=2", out)
        self.assertEqual(target.read_bytes(), b"wrong")
        self.assertFalse((directory / "missing.py").exists())

    def test_skipped_origins_are_not_downloaded_but_are_verified(self):
        for name, text in (
                ("external", fixture("DATASET_CARD.md (= HF README.md @ 12345678)", "HF dataset card")),
                ("wheel", fixture("porter.py").replace(
                    f"repo: https://github.com/example/project\ncommit: `{COMMIT}`", "来源：PyPI wheel"))):
            self.pin(name, text, filename="sources.md")
        never = mock.Mock(side_effect=AssertionError("skip must not download"))
        code, out, err = self.cli([], never)
        self.assertEqual((code, err), (0, ""))
        self.assertIn("non-GitHub source", out)
        self.assertIn("PyPI wheel", out)
        self.assertIn("skipped=2", out)
        never.assert_not_called()
        code, out, err = self.cli(["--verify-only"], never)
        self.assertEqual(code, 1)
        self.assertEqual(err.count(": missing"), 2)
        for path in (self.root / "external/DATASET_CARD.md", self.root / "wheel/porter.py"):
            path.write_bytes(CONTENT)
        self.assertEqual(self.cli(["--verify-only"], never)[0], 0)
        (self.root / "external/DATASET_CARD.md").write_bytes(b"bad external copy")
        code, out, err = self.cli(["--verify-only"], never)
        self.assertEqual(code, 1)
        self.assertIn("external/DATASET_CARD.md: sha256 mismatch", err)

    def test_list_only_and_unknown_selection_never_download(self):
        self.pin("one")
        self.pin("two")
        never = mock.Mock(side_effect=AssertionError("must not download"))
        code, out, err = self.cli(["--only", "two", "--list"], never)
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(out, f"two: repo=example/project commit={COMMIT} files=1\n")
        self.assertEqual(self.cli(["--only", "absent"], never)[0], 1)
        never.assert_not_called()
        calls = []
        def fake(url, path):
            calls.append(url)
            path.write_bytes(CONTENT)
        self.assertEqual(self.cli(["--only", "two", "one"], fake)[0], 0)
        self.assertEqual(len(calls), 2)

    def test_symlink_destination_rejected_before_download(self):
        directory = self.pin()
        outside = self.root / "outside"
        outside.mkdir()
        (directory / "nested").symlink_to(outside, target_is_directory=True)
        never = mock.Mock(side_effect=AssertionError("unsafe path must not download"))
        code, out, err = self.cli([], never)
        self.assertEqual(code, 1)
        self.assertIn("symlink destination not allowed", err)
        self.assertEqual(list(outside.iterdir()), [])
        never.assert_not_called()


if __name__ == "__main__":
    unittest.main()
