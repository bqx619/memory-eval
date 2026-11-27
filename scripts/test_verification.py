#!/usr/bin/env python3
"""F12 regression checks; deliberate mutations target synthetic scratch goldens."""
import ast
import json
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile
from textwrap import dedent
import unittest
from unittest import mock

sys.dont_write_bytecode = True
from verification import ROOT, Verification, inventory, output_directory, protected_outputs, verification_main


class ProtectionTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="memeval-verify-tests-")
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name).resolve()
        self.root = self.base / "repo"
        for name in ("reports", "docs"):
            (self.root / name).mkdir(parents=True)
            (self.root / name / "golden.json").write_text('{"score":1}\n')

    def test_default_and_explicit_scratch(self):
        out = output_directory(root=self.root)
        self.addCleanup(__import__('shutil').rmtree, out)
        self.assertTrue(out.name.startswith("memeval-verify-"))
        self.assertFalse(out.is_relative_to(self.root))
        explicit = output_directory(self.base / "explicit", self.root)
        with protected_outputs(explicit, self.root):
            (explicit / "run.json").write_text('{"score":1}')
        proof = json.loads((explicit / "protected-check.json").read_text())
        self.assertTrue(all(x['unchanged'] for x in proof.values()))
        self.assertEqual((explicit / 'protected-before.json').read_bytes(),
                         (explicit / 'protected-after.json').read_bytes())
        with self.assertRaises(ValueError):
            output_directory(explicit, self.root)  # Never clobber a prior run.

    def test_repository_and_symlink_outputs_rejected_before_write(self):
        before = inventory(self.root)
        link = self.base / 'alias'
        link.symlink_to(self.root / 'reports', target_is_directory=True)
        for path in (self.root, self.root / 'reports/new', self.root / 'docs', link / 'new'):
            with self.subTest(path=path), self.assertRaises(ValueError):
                output_directory(path, self.root)
        self.assertEqual(inventory(self.root), before)

    def test_score_change_addition_and_deletion_fail_guard(self):
        for directory in ('reports', 'docs'):
            for kind in ('score', 'add', 'delete'):
                with self.subTest(directory=directory, kind=kind):
                    golden = self.root / directory / 'golden.json'
                    golden.write_text('{"score":1}\n')
                    out = output_directory(self.base / (directory + '-' + kind), self.root)
                    with self.assertRaisesRegex(RuntimeError, 'inventory changed'):
                        with protected_outputs(out, self.root):
                            if kind == 'score':
                                golden.write_text('{"score":0}\n')
                            elif kind == 'add':
                                (golden.parent / 'new.txt').write_text('unexpected')
                            else:
                                golden.unlink()
                    proof = json.loads((out / 'protected-check.json').read_text())
                    self.assertFalse(proof[directory]['unchanged'])

    def test_guard_runs_on_failure(self):
        out = output_directory(self.base / 'failure', self.root)
        with self.assertRaisesRegex(ValueError, 'failed assertion'):
            with protected_outputs(out, self.root):
                raise ValueError('failed assertion')
        self.assertTrue((out / 'protected-after.json').exists())

    def test_setup_locks_and_selftest_reports_are_scratch_copies(self):
        (self.root / 'go.mod').write_text('module fixture\n')
        for name in ('testdata', 'protocols', 'design', 'config', 'data/registry',
                     'data/normalized', 'data/upstream', 'suts/echo'):
            (self.root / name).mkdir(parents=True)
        lock = self.root / 'suts/echo/lock.json'
        lock.write_text('original lock')
        out = output_directory(self.base / 'workspace-check', self.root)
        with mock.patch('verification.ROOT', self.root):
            workspace = Verification(out).workspace()
        (workspace / 'suts/echo/lock.json').write_text('new lock')
        (workspace / 'reports/selftest.json').write_text('new selftest')
        self.assertEqual(lock.read_text(), 'original lock')
        self.assertFalse((self.root / 'reports/selftest.json').exists())
        self.assertFalse((workspace / 'docs').is_symlink())

    def test_failed_verifier_preserves_evidence_from_repo_cwd(self):
        out = self.base / 'failed-script'
        # Exercise the real default entrypoint against synthetic goldens only;
        # a developer may have live evaluation outputs changing concurrently.
        scripts = self.root / 'scripts'
        scripts.mkdir()
        for name in ('verification.py', 'verify_p2_5.py'):
            shutil.copyfile(ROOT / 'scripts' / name, scripts / name)
        result = subprocess.run([sys.executable, '-B', str(scripts / 'verify_p2_5.py'),
                                 str(self.base / 'missing-bundle'), '--out', str(out)],
                                cwd=self.root, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Verification output: ' + str(out), result.stderr)
        proof = json.loads((out / 'protected-check.json').read_text())
        self.assertTrue(all(x['unchanged'] for x in proof.values()))

    def test_programmatic_skip_never_inventories_even_on_failure(self):
        for fail in (False, True):
            out = self.base / ('skip-failure' if fail else 'skip-success')
            def main(args, work):
                (work.out / 'result.json').write_text('{"score":1}')
                if fail:
                    raise ValueError('fixture failure')
                return 17
            with mock.patch.object(sys, 'argv', ['verifier', '--no-protected-check', '--out', str(out)]), \
                    mock.patch('verification.inventory', side_effect=AssertionError('must not read goldens')) as scan:
                if fail:
                    with self.assertRaisesRegex(ValueError, 'fixture failure'):
                        verification_main(main)
                else:
                    self.assertEqual(verification_main(main), 17)
                scan.assert_not_called()
            self.assertTrue((out / 'result.json').exists())
            self.assertFalse(list(out.glob('protected-*.json')))

    def test_manual_entrypoint_still_detects_mutation_by_default(self):
        out = self.base / 'manual-default'
        def mutate(args, work):
            (self.root / 'reports/golden.json').write_text('{"score":0}')
        with mock.patch.object(sys, 'argv', ['verifier', '--out', str(out)]), \
                mock.patch('verification.protected_outputs', lambda dest: protected_outputs(dest, self.root)):
            with self.assertRaisesRegex(RuntimeError, 'inventory changed'):
                verification_main(mutate)
        proof = json.loads((out / 'protected-check.json').read_text())
        self.assertFalse(proof['reports']['unchanged'])

    def test_embedded_ui_probe_still_compiles_after_entrypoint_move(self):
        tree = ast.parse((ROOT / 'scripts/verify_report_ui.py').read_text())
        assignment = next(n for n in ast.walk(tree) if isinstance(n, ast.Assign)
                          and any(isinstance(t, ast.Name) and t.id == 'probe' for t in n.targets))
        probe = eval(compile(ast.Expression(assignment.value), 'probe-literal', 'eval'),
                     {'dedent': dedent, '__builtins__': {}})
        compile(probe, 'chrome-network-probe', 'exec')  # Compile only; no sockets.

    def test_every_verifier_and_parity_caller_has_guard_and_rejects_repo_out(self):
        scripts = sorted((ROOT / 'scripts').glob('verify_*.py'))
        scripts.append(ROOT / 'scripts/test_parity_diff.py')
        for script in scripts:
            with self.subTest(script=script.name):
                tree = ast.parse(script.read_text())
                self.assertTrue(any(isinstance(n, ast.Call) and isinstance(n.func, ast.Name)
                                    and n.func.id == 'verification_main' for n in ast.walk(tree)))
                positional = ['/nonexistent-input'] if script.stem in ('verify_p2_locomo', 'verify_p2_5') else []
                help_run = subprocess.run([sys.executable, '-B', str(script), '--help'], cwd=ROOT,
                                          text=True, capture_output=True)
                self.assertEqual(help_run.returncode, 0, help_run.stderr)
                self.assertIn('--out', help_run.stdout)
                rejected = subprocess.run([sys.executable, '-B', str(script), *positional,
                                           '--out', str(ROOT / 'reports/f12-must-not-exist')],
                                          cwd=ROOT, text=True, capture_output=True)
                self.assertEqual(rejected.returncode, 2, rejected.stdout + rejected.stderr)
                self.assertIn('--out must be outside', rejected.stderr)
                self.assertFalse((ROOT / 'reports/f12-must-not-exist').exists())


if __name__ == '__main__':
    unittest.main(verbosity=2)
