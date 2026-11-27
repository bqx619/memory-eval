import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import subprocess
import shutil
from retire_host_sandbox import Checks, Roots, retire


class FakeChecks:
    running = False
    paths = set()
    def memeval_running(self): return self.running
    def live(self, path): return 'live reference' if path in self.paths else ''


class RetirementTests(unittest.TestCase):
    def test_cli_requires_explicit_roots_before_inspection(self):
        import sys
        script = Path(__file__).with_name('retire_host_sandbox.py')
        result = subprocess.run([sys.executable, '-B', str(script)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn('--cache-root', result.stderr)
        self.assertEqual(result.stdout, '')

    def test_cli_refuses_filesystem_root_before_inspection(self):
        import sys
        script = Path(__file__).with_name('retire_host_sandbox.py')
        argv = [sys.executable, '-B', str(script)]
        for name in ('cache', 'ep', 'bootstrap', 'repo', 'vm', 'temp'):
            argv.extend(['--' + name + '-root', '/'])
        result = subprocess.run(argv, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn('below the filesystem root', result.stderr)
        self.assertEqual(result.stdout, '')

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.roots = Roots(self.root/'cache', self.root/'ep', self.root/'bootstrap', (self.root/'tmp',), self.root/'repo', self.root/'vm')
        self.checks = FakeChecks()
        for p in (self.roots.cache, self.roots.ep, self.roots.temp[0], self.roots.repo): p.mkdir()
    def put(self, p, data='fixture'):
        p.parent.mkdir(parents=True, exist_ok=True); p.write_text(data); return p
    def snapshot(self):
        return {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in self.root.rglob('*') if p.is_file() and not p.is_symlink()}
    def test_dry_run_and_exact_scope(self):
        self.put(self.roots.cache/'envs/mem0@abc/requirements.lock.txt', 'pinned')
        self.put(self.roots.ep/'abcdef12/work/data')
        for n in ('longmemeval-oracle', 'octop-memory', 'cassettes'): self.put(self.roots.cache/n/'keep')
        for n in ('memeval-f99-proof', 'memeval-c1-proof', 'memeval-k1-proof', 'unrelated'): self.put(self.roots.temp[0]/n/'keep')
        self.put(self.roots.temp[0]/'memeval-probe-fixture')
        before = self.snapshot(); rows = retire(self.roots, checks=self.checks)
        self.assertEqual(before, self.snapshot())
        deletes = [p for a, _, p, _ in rows if a == 'DELETE']
        self.assertEqual(len(deletes), 3)
        self.assertFalse(any('keep' in p or 'cassettes' in p for p in deletes))
        self.assertEqual(sum(a=='LIST-ONLY' for a, *_ in rows), 6)
    def test_escape_and_live_skip(self):
        outside = self.put(self.root/'outside/data').parent
        (self.roots.ep/'01234567').symlink_to(outside, target_is_directory=True)
        live = self.put(self.roots.ep/'abcdef12/data').parent
        self.checks.paths = {live}
        rows = retire(self.roots, True, self.checks)
        self.assertEqual(sum(a=='SKIP' for a, *_ in rows), 2)
        self.assertTrue(outside.exists()); self.assertTrue(live.exists())
    def test_lock_copy_conflict_and_v3(self):
        env=self.roots.cache/'envs/graphiti@abc'
        self.put(env/'requirements.lock.txt','pinned')
        newer=self.roots.cache/'envs/new@abc';self.put(newer/'env.lock.json','{"version":3}')
        retire(self.roots,True,self.checks)
        self.assertFalse(env.exists());self.assertTrue(newer.exists())
        self.assertEqual((self.roots.repo/'reports/p6-reference/host-locks/graphiti@abc/requirements.lock.txt').read_text(),'pinned')
        self.assertFalse((self.roots.repo/'suts').exists())
        self.put(env/'requirements.lock.txt','different')
        rows=retire(self.roots,True,self.checks)
        self.assertTrue(env.exists());self.assertTrue(any('different existing lock' in r for *_,r in rows))
    def test_list_only_root_inside_candidate_is_preserved(self):
        from dataclasses import replace
        candidate = self.roots.cache / 'envs/retired@abc'
        vm = self.put(candidate / 'virtual-machine/keep').parent
        rows = retire(replace(self.roots, vm=vm), True, self.checks)
        self.assertTrue((vm / 'keep').is_file())
        self.assertTrue(any(a == 'KEEP' and p == str(candidate) for a, _, p, _ in rows))

    def test_candidate_ancestor_of_repository_is_preserved(self):
        from dataclasses import replace
        candidate = self.roots.temp[0] / 'memeval-install-fixture'
        repo = self.put(candidate / 'repo/keep').parent
        rows = retire(replace(self.roots, repo=repo), True, self.checks)
        self.assertTrue((repo / 'keep').is_file())
        self.assertTrue(any(a == 'KEEP' and p == str(candidate) for a, _, p, _ in rows))
    def test_host_locks_backup_by_environment_identity(self):
        for name in ('mem0', 'mem0-infer', 'graphiti', 'mastra'):
            env = self.roots.cache / ('envs/' + name + '@' + 'a'*64)
            src = 'project/package-lock.json' if name == 'mastra' else 'requirements.lock.txt'
            self.put(env/src, name + '-host-lock')
        sut_lock = self.put(self.roots.repo/'suts/mem0/requirements.lock.txt', 'linux-lock')
        before = self.snapshot()
        dry = retire(self.roots, checks=self.checks)
        self.assertEqual(before, self.snapshot())
        self.assertEqual(sum(a == 'COPY' for a, *_ in dry), 4)
        retire(self.roots, True, self.checks)
        for name in ('mem0', 'mem0-infer', 'graphiti', 'mastra'):
            filename = 'package-lock.json' if name == 'mastra' else 'requirements.lock.txt'
            dst = self.roots.repo/'reports/p6-reference/host-locks'/(name+'@'+'a'*12)/filename
            self.assertEqual(dst.read_text(), name+'-host-lock')
        self.assertEqual(sut_lock.read_text(), 'linux-lock')
        # An identical existing backup permits retirement without rewriting it.
        env = self.roots.cache/('envs/mem0@'+'a'*64)
        self.put(env/'requirements.lock.txt', 'mem0-host-lock')
        rows = retire(self.roots, True, self.checks)
        self.assertFalse(env.exists())
        self.assertFalse(any(a == 'COPY' for a, *_ in rows))
    def test_backup_symlink_cannot_redirect_to_suts(self):
        self.put(self.roots.cache/'envs/graphiti@abc/requirements.lock.txt', 'host-lock')
        (self.roots.repo/'suts').mkdir()
        (self.roots.repo/'reports/p6-reference').mkdir(parents=True)
        (self.roots.repo/'reports/p6-reference/host-locks').symlink_to(self.roots.repo/'suts')
        rows = retire(self.roots, True, self.checks)
        self.assertTrue(any(a == 'SKIP' and 'backup destination' in reason for a, _, _, reason in rows))
        self.assertEqual(list((self.roots.repo/'suts').iterdir()), [])
    def test_gc_managed_metadata_is_kept(self):
        records = [self.put(self.roots.cache/'selftests/selftest-current.json', '{"version":3}'),
                   self.put(self.roots.cache/'env-gates/current@abc.lock')]
        for name in ('env-processes', 'env-vms'):
            self.put(self.roots.cache/name/'retired')
        rows = retire(self.roots, True, self.checks)
        for p in records:
            self.assertTrue(p.exists())
            self.assertIn(('KEEP', str(p.parent), 'managed by memeval gc'), [(a,p,r) for a,_,p,r in rows])
        self.assertFalse((self.roots.cache/'env-processes').exists())
        self.assertFalse((self.roots.cache/'env-vms').exists())
    def test_failed_install_diagnostics_and_current_lock(self):
        for name in ('cognee', 'hindsight', 'graphiti'):
            self.put(self.roots.cache/('envs/'+name+'@abc/install-failure.log'))
        current = self.roots.cache/'envs/current@abc'
        self.put(current/'env.lock.json', '{"version":3}')
        self.put(current/'install-failure.log')
        rows = retire(self.roots, True, self.checks)
        deleted = [(p,r) for a,_,p,r in rows if a == 'DELETE']
        self.assertEqual(len(deleted), 3)
        self.assertTrue(all(r == 'failed-install-diagnostics; apply' for _,r in deleted))
        self.assertTrue(current.exists())
    def test_lsof_usage_is_captured_and_reason_is_one_line(self):
        path = self.put(self.roots.ep/'abcdef12/data').parent
        def run(args, **kw):
            self.assertEqual(kw, dict(capture_output=True, text=True))
            if args[0] == '/bin/ps':
                return subprocess.CompletedProcess(args, 0, '', '')
            return subprocess.CompletedProcess(args, 1, 'lsof 4.91\nusage: [-?]\n', 'bad path\nusage: ...\n')
        with patch('retire_host_sandbox.subprocess.run', side_effect=run):
            rows = retire(self.roots, checks=Checks())
        skips = [r for r in rows if r[0] == 'SKIP']
        self.assertEqual(skips, [('SKIP', 7, str(path), 'lsof could not prove absence (exit 1)')])
        self.assertTrue(all('\n' not in str(x) and '\t' not in str(x) for r in rows for x in r))
        self.assertNotIn('usage:', '\n'.join('\t'.join(map(str,r)) for r in rows))
    def test_vanished_after_enumeration_or_during_lsof(self):
        path = self.put(self.roots.ep/'abcdef12/data').parent
        def vanished_candidates(roots):
            shutil.rmtree(path)
            yield path, roots.ep, 'retired-episode'
        with patch('retire_host_sandbox.candidates', vanished_candidates):
            rows = retire(self.roots, checks=self.checks)
        self.assertIn(('SKIP', 0, str(path), 'vanished'), rows)
        self.put(path/'data')
        def run(args, **kw):
            if args[0] == '/bin/ps':
                return subprocess.CompletedProcess(args, 0, '', '')
            shutil.rmtree(path)
            return subprocess.CompletedProcess(args, 1, 'usage: ...\n', 'No such file\n')
        with patch('retire_host_sandbox.subprocess.run', side_effect=run):
            rows = retire(self.roots, checks=Checks())
        self.assertIn(('SKIP', 0, str(path), 'vanished'), rows)
    def test_running_and_bootstrap_guard(self):
        self.put(self.roots.bootstrap/'unknown','unrelated')
        rows=retire(self.roots,True,self.checks)
        self.assertTrue(self.roots.bootstrap.exists())
        self.assertTrue(any('unverified' in r for *_,r in rows))
        self.checks.running=True
        with self.assertRaises(RuntimeError):retire(self.roots,True,self.checks)

if __name__ == '__main__': unittest.main()
