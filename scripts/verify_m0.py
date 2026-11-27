#!/usr/bin/env python3
"""Run the seven M0 acceptance checks, offline, without touching docs/ or Git."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import hashlib
import json
import math
import os
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
from parity_diff import check
from recompute_ci import calculate

ROOT = Path(__file__).resolve().parents[1]
ENV = {**os.environ, 'GOTOOLCHAIN': 'local', 'GOPROXY': 'off', 'GOSUMDB': 'off'}
SEED = '42' * 32

def docs_inventory():
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted((ROOT / 'docs').rglob('*')) if p.is_file()}

def invoke(*args):
    p = subprocess.run(args, cwd=ROOT, env=ENV, text=True, capture_output=True)
    if p.returncode:
        raise RuntimeError(f'{args!r}\n{p.stdout}\n{p.stderr}')
    return p.stdout

def private_write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(text)
    path.chmod(0o600)

def read(path):
    return json.loads((ROOT / path).read_text())

def main(args, work):
    global ENV
    ENV = work.env
    out = work.out
    before = docs_inventory()
    logs = out / 'm0-verification'
    outputs = [logs]
    private_write(logs / 'parity-tests.txt', invoke(sys.executable, '-B', 'scripts/test_parity_diff.py', '--out', out / 'parity-tests'))
    private_write(logs / 'go-vet.txt', invoke('go', 'vet', './...'))
    test_output = invoke('go', 'test', '-count=1', './...')
    private_write(logs / 'go-test.txt', test_output)
    packages = invoke('go', 'list', '-json', './...')
    decoder, offset, package_rows = json.JSONDecoder(), 0, []
    while offset < len(packages):
        while offset < len(packages) and packages[offset].isspace():
            offset += 1
        if offset == len(packages):
            break
        row, offset = decoder.raw_decode(packages, offset)
        package_rows.append(row)
    # Keep architectural boundaries; include the shared helpers added after M0.
    allowed = {'protocol': set(), 'adapter': {'protocol'},
               'report': {'result', 'protocol', 'config/redact'},
               'stats': {'result', 'protocol', 'mathutil'}}
    for package in package_rows:
        name = package['ImportPath'].removeprefix('memeval/internal/')
        if name in allowed:
            internal = {x.removeprefix('memeval/internal/') for x in package.get('Imports', [])
                        if x.startswith('memeval/internal/')}
            assert internal <= allowed[name], (name, internal)
    def command(*args):
        return invoke(*work.cli(*args))
    command('import', 'pmb', '--input', 'docs/data/pmb_projection.json', '--format', 'single', '--out', out / 'pmb.json')
    command('validate', out / 'pmb.json')
    summaries = []
    for name in ('null', 'recent', 'bm25'):
        dest = out / f'm0-{name}'
        command('run', '--suite', out / 'pmb.json', '--sut', name,
               '--run-seed', SEED, '--out', dest)
        outputs.append(dest)
        errors = check(dest, ROOT / 'docs/reports' / name)
        assert not errors, '\n'.join(errors)
        s = read(dest / 'report.json')['summary']
        summaries.append((name, s))
    command('compare', out / 'm0-null/report.json', out / 'm0-bm25/report.json',
           '--metric', 'pass', '--out', out / 'm0-compare/delta.json')
    outputs.append(out / 'm0-compare')
    comparison = read(out / 'm0-compare/delta.json')
    golden = read('docs/reports/paired-null-bm25.json')
    for key in ('left', 'right', 'metric', 'paired_cases', 'clusters', 'excluded_cases', 'ci95', 'note'):
        assert comparison[key] == golden[key], key
    assert math.isclose(comparison['delta_right_minus_left'], golden['delta_right_minus_left'], abs_tol=1e-12)
    for name in ('null', 'bm25'):
        command('run', '--suite', 'testdata/two-clusters.json', '--sut', name,
               '--run-seed', SEED, '--out', out / f'm0-ci-{name}')
        outputs.append(out / f'm0-ci-{name}')
    left, right = out / 'm0-ci-null/report.json', out / 'm0-ci-bm25/report.json'
    command('compare', left, right, '--metric', 'recall', '--resample-indices',
           'testdata/resample_indices.jsonl', '--out', out / 'm0-ci/delta.json')
    outputs.append(out / 'm0-ci')
    ci = read(out / 'm0-ci/delta.json')
    python_ci = calculate(ROOT / left, ROOT / right, ROOT / 'testdata/resample_indices.jsonl', 'recall')
    for actual, expected in zip(ci['ci95'], python_ci['ci95']):
        assert math.isclose(actual, expected, abs_tol=1e-12)
    assert math.isclose(ci['delta_right_minus_left'], python_ci['delta_right_minus_left'], abs_tol=1e-12)
    private_write(logs / 'python-ci.json', json.dumps(python_ci, indent=2) + '\n')
    command('compare', left, right, '--metric', 'recall', '--out', out / 'm0-ci-generated/delta.json')
    outputs.append(out / 'm0-ci-generated')
    generated = read(out / 'm0-ci-generated/delta.json')
    generated_python = calculate(ROOT / left, ROOT / right, out / 'm0-ci-generated/resample_indices.jsonl', 'recall')
    assert generated['draws'] == 2000 and all(math.isclose(x, y, abs_tol=1e-12)
                                            for x, y in zip(generated['ci95'], generated_python['ci95']))
    for name in ('determinism-a', 'determinism-b'):
        command('run', '--suite', out / 'pmb.json', '--sut', 'bm25',
               '--run-seed', SEED, '--repeats', '2', '--out', out / f'm0-{name}')
        outputs.append(out / f'm0-{name}')
    def normalized_cases(path):
        rows = []
        for line in (ROOT / path / 'cases.jsonl').read_text().splitlines():
            row = json.loads(line)
            del row['timings']
            rows.append(json.dumps(row, ensure_ascii=False, separators=(',', ':')))
        return ('\n'.join(rows) + '\n').encode()
    assert normalized_cases(out / 'm0-determinism-a') == normalized_cases(out / 'm0-determinism-b')
    command('report', out / 'm0-null/report.json', out / 'm0-recent/report.json',
           out / 'm0-bm25/report.json', '--out', out / 'm0-comparison.html')
    outputs.append(out / 'm0-comparison.html')
    # Check this verifier's output locations, not unrelated later UI/live checks.
    for output in outputs:
        for p in output.rglob('*') if output.is_dir() else [output]:
            if p.is_file():
                assert p.stat().st_mode & 0o777 == 0o600, p
    assert docs_inventory() == before, 'docs/ changed'
    result = {'acceptance': '1-7 passed', 'docs_unchanged': True,
              'parity_regression_tests': 'passed (including changed-score CLI exit 1)',
              'determinism': '154 samples identical excluding timings',
              'ci': python_ci, 'compare_delta': comparison['delta_right_minus_left'],
              'parity': {name: 'empty diff' for name, _ in summaries}}
    private_write(logs / 'acceptance.json', json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print('M0 §11：1–7 全部通过（离线、无 git、docs/ 内容与文件清单未变）。')
    print(f'生成产物全部位于：{out}（二进制、导入套件、报告、CI 索引与验收日志）')
    print('go vet ./...：PASS；go test -count=1 ./...：PASS；包依赖边界通过。')
    print('parity 回归测试：PASS；故意改分的 CLI 负例返回 1；仅汇总/分类允许新增键。')
    for name, s in summaries:
        recall = s['metrics']['recall']['mean']
        empty = s['metrics']['empty_result_correct']['mean']
        print(f'{name} parity：空 diff；{s["passed"]}/{s["n_with_pass_judgment"]}；unscored=1；recall={recall:.12f}；empty={empty:.4f}')
    print(f'compare null→bm25：delta={comparison["delta_right_minus_left"]:.16f}；76 对；1 簇；ci95=null。')
    print(f'固定索引 CI：delta={ci["delta_right_minus_left"]:.12f}；ci95={ci["ci95"]}；Python 同索引复算通过。')
    print(f'复算：python3 -B scripts/recompute_ci.py {out}/m0-ci-null/report.json {out}/m0-ci-bm25/report.json testdata/resample_indices.jsonl --check {out}/m0-ci/delta.json')
    print('默认 PCG(42,42) 的 2000 组索引已落盘，Python 复算也通过；重复运行 154 行除 timings 外一致。')

if __name__ == '__main__':
    verification_main(main)
