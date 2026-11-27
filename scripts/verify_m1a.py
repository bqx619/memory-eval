#!/usr/bin/env python3
"""Offline M1a acceptance. Linux binaries are compiled, never executed here."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
LOGS = None
WORK = None
WORKSPACE = None
ENV = dict(os.environ, GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local', GOFLAGS='')


def inventory():
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted((ROOT / 'docs').rglob('*')) if p.is_file()}


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o600)


def invoke(name, *args, env=None):
    cwd = ROOT
    if args[0] == str(WORK.binary()):
        args = WORK.cli(*args[1:])
        cwd = WORKSPACE
    run = subprocess.run(args, cwd=cwd, env=env or ENV, capture_output=True, text=True)
    write(LOGS / (name + '.log'), run.stdout + run.stderr)
    if run.returncode:
        raise RuntimeError(f'{name}: exit {run.returncode}; see {LOGS / (name + ".log")}')
    return run.stdout


def main(args, work):
    global LOGS, ENV, WORK, WORKSPACE
    WORK, ENV, LOGS = work, work.env, work.out
    WORKSPACE = work.workspace()
    before = inventory()
    cli = str(work.binary())
    manifest = str(WORKSPACE / 'suts/echo/manifest.json')
    setup = json.loads(invoke('setup', cli, 'sut', 'setup', '--sut', manifest, '--cache-dir', LOGS / 'venvs'))
    invoke('vet', 'go', 'vet', './...')
    invoke('test', 'go', 'test', '-count=1', './...')
    invoke('race', 'go', 'test', '-race', './...')
    deps = invoke('dependencies', 'go', 'list', '-m', 'all').splitlines()
    invoke('module-verify', 'go', 'mod', 'verify')
    linux = {}
    for arch in ('amd64', 'arm64'):
        with tempfile.TemporaryDirectory(prefix='linux-compile-', dir=LOGS) as temp:
            target_env = dict(ENV, GOOS='linux', GOARCH=arch, CGO_ENABLED='0')
            invoke('linux-' + arch + '-compile', 'go', 'test', '-c', '-o', temp + '/', './...', env=target_env)
            binaries = list(Path(temp).glob('*.test'))
            assert binaries and all(p.read_bytes()[:4] == b'\x7fELF' for p in binaries)
            linux[arch] = {'compiled_test_binaries': len(binaries), 'executed': False}
    caps = json.loads(invoke('caps', cli, 'sandbox', 'caps'))
    check = json.loads(invoke('check', cli, 'sut', 'check', '--sut', manifest))
    assert check['valid'] and caps['caps']['file_read_restrict'] and caps['caps']['network_none']
    selftests, runs = {}, {}
    for mode in ('none', 'open'):
        common = ['--sut', manifest, '--network', mode]
        result = json.loads(invoke('selftest-' + mode, cli, 'sandbox', 'selftest', *common, '--out', WORKSPACE / 'reports'))
        probe = json.loads((ROOT / result['report']).read_text())
        assert result['passed'] and probe['passed'] and len(probe['checks']) == 19
        assert all(c['passed'] and c['control_passed'] for c in probe['checks'].values())
        assert all(c['success'] for c in probe['control']['items'].values())
        selftests[mode] = {'path': result['report'], 'sha256': probe['sha256'], 'checks': 19, 'passed': True}
        dest = str(LOGS / ('m1a-echo-' + mode))
        keep = ['--keep'] if mode == 'none' else []
        invoke('run-' + mode, cli, 'run', *common, '--suite', 'docs/data/pmb_projection.json', '--out', dest, *keep)
        run = json.loads((ROOT / dest / 'report.json').read_text())
        assert len(run['cases']) == 77 and run['summary']['execution_errors'] == 0
        assert run['summary']['status_counts'] == {'scored': 76, 'unscored': 1}
        assert not run['summary'].get('unenforced_isolation', False)
        iso = run['isolation']
        assert iso['backend'] == 'process/seatbelt' and iso['tier'] == 'A'
        assert all(iso[k] for k in ('file_read_enforced', 'file_write_enforced', 'deny_read_enforced', 'process_tree_enforced'))
        assert iso['network_enforced'] == (mode == 'none')
        assert iso['selftest']['sha256'] == probe['sha256']
        assert iso['sandbox_profile_sha256'] == probe['profile_sha256']
        for config in iso['effective_config']:
            ep = Path(config['episode_dir'])
            if mode == 'none':
                assert sorted(str(p.relative_to(ep)) for p in ep.rglob('*') if p.is_file()) == ['stderr.log']
            else:
                assert not ep.exists()
        runs[mode] = {'path': dest + '/report.json', 'cases': 77, 'execution_errors': 0,
                      'episode_directories': len(iso['effective_config']), 'keep_verified': mode == 'none'}
    parity = {}
    for name, passed in (('null', 15), ('recent', 11), ('bm25', 48)):
        dest = str(LOGS / ('m1a-regression-' + name))
        invoke('baseline-' + name, cli, 'run', '--sut', name, '--suite', 'pmb', '--out', dest)
        invoke('parity-' + name, sys.executable, '-B', 'scripts/parity_diff.py', dest, 'docs/reports/' + name)
        r = json.loads((ROOT / dest / 'report.json').read_text())
        assert r['summary']['passed'] == passed and r['summary']['n_with_pass_judgment'] == 76
        parity[name] = {'diff': 'empty', 'passed': passed, 'judged': 76}
    assert inventory() == before, 'docs inventory/content changed'
    result = {'darwin': {'vet': 'passed', 'test': 'passed', 'race': 'passed', 'selftests': selftests, 'runs': runs},
              'linux': linux, 'dependencies': deps, 'docs_unchanged': True, 'parity': parity,
              'runtime_probe': setup['runtime']['probe'],
              'pending': ['Linux unit/integration execution on Linux', 'Offline upstream Seatbelt source/commit provenance check']}
    write(LOGS / 'acceptance.json', json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    lines = [
        'M1a：Darwin 验收通过；Linux 仅交叉编译，未执行。',
        'go vet/test/race、模块缓存校验、echo setup/check：PASS。',
        'selftest none/open：各 19/19（含对照）；echo 各 77 例；--keep 检查通过。',
        'M0 parity：null 15/76、recent 11/76、bm25 48/76，diff 均为空。',
        f'全部产物（含二进制、SUT 锁与 selftest）：{LOGS}',
        f'证据：{LOGS / "acceptance.json"}',
    ]
    assert len(lines) <= 40
    text = '\n'.join(lines) + '\n'
    write(LOGS / 'summary.txt', text)
    print(text, end='')


if __name__ == '__main__':
    verification_main(main)
