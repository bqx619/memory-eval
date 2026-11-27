"""Missing-source contracts; all subprocess writes stay in a scratch checkout."""
from contextlib import redirect_stderr
import io
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
from upstream import ROOT, require_upstream


class UpstreamTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='memeval-upstream-test-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / 'checkout'
        self.root.mkdir()

    def test_presence_missing_and_other_errors(self):
        path = self.root / 'design/upstream/example/ref.py'
        path.parent.mkdir(parents=True)
        path.write_text('source')
        self.assertEqual(require_upstream('example/ref.py', self.root), path)
        path.unlink()
        error = io.StringIO()
        with redirect_stderr(error), self.assertRaises(SystemExit) as ctx:
            require_upstream('example/ref.py', self.root)
        self.assertEqual(ctx.exception.code, 3)
        self.assertIn('scripts/fetch_upstream_refs.py --only example', error.getvalue())
        with mock.patch.object(Path, 'stat', side_effect=PermissionError('fixture')):
            with self.assertRaises(PermissionError):
                require_upstream('example/ref.py', self.root)

    def test_script_entrypoints_missing_sources_exit_three(self):
        shutil.copytree(ROOT / 'scripts', self.root / 'scripts',
                        ignore=shutil.ignore_patterns('__pycache__'))
        # Mirror a clean checkout: the directories still contain their pins.
        for source in (ROOT / 'design/upstream').iterdir():
            if not source.is_dir():
                continue
            dest = self.root / 'design/upstream' / source.name
            dest.mkdir(parents=True)
            for pin in source.iterdir():
                if pin.name.lower() == 'sources.md':
                    shutil.copyfile(pin, dest / pin.name)
        commands = [(f'extract_{name}_templates.py', ['--check'])
                    for name in ('beam', 'mab', 'memora', 'prefeval')]
        commands += [('make_personamem_fixture.py', []),
                     ('verify_m2a.py', []), ('verify_m2b.py', []),
                     ('verify_dependencies.py', []), ('verify_p2_locomo.py', ['unused-run'])]
        for script, args in commands:
            with self.subTest(script=script):
                if script.startswith('verify_'):
                    args = args + ['--out', str(self.root.parent / 'outputs' / script)]
                p = subprocess.run([sys.executable, '-B', str(self.root / 'scripts' / script), *args],
                                   cwd=self.root, capture_output=True, text=True)
                self.assertEqual(p.returncode, 3, p.stderr)
                self.assertIn('Upstream source unavailable:', p.stderr)
                self.assertIn('scripts/fetch_upstream_refs.py --only', p.stderr)
                self.assertNotIn('Traceback', p.stderr)
                self.assertFalse((self.root / 'internal').exists())


if __name__ == '__main__':
    unittest.main()
