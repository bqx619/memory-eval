# Historical host-era acceptance recipe, not a current execution entry point.
# Bundled lock.json paths are sanitized fixtures; current SUTs use container Lock v3.
"""Offline, reproducible M1b acceptance; run from the repository root."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import hashlib
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[1]
def main(args, work):
    out = work.out
    workspace = work.workspace()
    env = work.env
    cli = str(work.binary())
    seed = "0123456789abcdef" * 4
    manifest = str(workspace / "suts/octop-memory/manifest.json")
    suite = str(out / "pmb-retrieval.json")
    def run(name, args, extra=None):
        cwd = root
        if args[0] == cli:
            args = work.cli(*args[1:])
            cwd = workspace
        p = subprocess.run(args, capture_output=True, env=extra or env, cwd=cwd)
        (out / name).write_bytes(p.stdout)
        (out / (name + ".stderr")).write_bytes(p.stderr)
        if p.returncode:
            raise RuntimeError(f"{args[0]} failed ({p.returncode}); see {out / name}")

    docs_before = {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in (root / "docs").rglob("*") if p.is_file()}
    run("setup.json", [cli, "sut", "setup", "--manifest", manifest, "--cache-dir", str(out / "venvs")])
    run("check.json", [cli, "sut", "check", "--manifest", manifest])
    lock = json.loads((workspace / "suts/octop-memory/lock.json").read_text())
    env["MEMEVAL_OCTOP_VENV"] = lock["venv_real_path"]
    run("upstream-facts.json", [str(Path(lock["venv_real_path"]) / "bin/python"), "-B", "scripts/verify_octop_upstream.py", "--out", str(out / "upstream")])
    run("vet.log", ["go", "vet", "./..."])
    run("tests.jsonl", ["go", "test", "-count=1", "-json", "./..."])
    no_venv = dict(env)
    no_venv["MEMEVAL_OCTOP_VENV"] = ""
    run("ci-skip.log", ["go", "test", "-count=1", "-v", "./internal/adapter/octop"], no_venv)
    assert "MEMEVAL_OCTOP_VENV unset" in (out / "ci-skip.log").read_text()
    run("selftest-command.json", [cli, "sandbox", "selftest", "--sut", manifest, "--out", str(workspace / "reports")])
    evidence_path = json.loads((out / "selftest-command.json").read_text())["report"]
    evidence = json.loads(Path(evidence_path).read_text())
    assert evidence["passed"]
    run("import.json", [cli, "import", "pmb", "--as", "retrieval", "--format", "single", "--out", suite])
    for variant in ["octop-a", "octop-b", "bm25", "recent", "null"]:
        target = manifest if variant.startswith("octop") else variant
        run(f"run-{variant}.json", [cli, "run", "--suite", suite, "--sut", target, "--run-seed", seed, "--out", str(out / f"m1b-{variant}")])

    reports = {v: json.loads(Path(str(out / f"m1b-{v}/report.json")).read_text()) for v in ["octop-a", "octop-b", "bm25", "recent", "null"]}
    projected = json.loads(Path(suite).read_text())
    known = {e["source_id"] for ep in projected["episodes"] for e in ep["events"]}
    normalized = []
    for name in ["octop-a", "octop-b"]:
        r = reports[name]
        assert r["summary"]["planned"] == 75 and r["summary"]["execution_errors"] == 0
        assert "untraceable_hits" in r["summary"] and "unsupported" in r["summary"]
        assert r["isolation"]["network_enforced"] and r["isolation"]["file_read_enforced"]
        rows = [json.loads(line) for line in Path(str(out / f"m1b-{name}/cases.jsonl")).read_text().splitlines()]
        for row in rows:
            assert set(row["retrieved_source_ids"]) <= known
            assert row["status"] not in ["contract_error", "ingest_failed", "error"]
            row.pop("timings")
        data = "\n".join(json.dumps(row, ensure_ascii=False, separators=(",", ":")) for row in rows) + "\n"
        (out / f"{name}-no-timings.jsonl").write_text(data)
        normalized.append(data.encode())
    assert normalized[0] == normalized[1], "cases differ after removing timings"
    comparisons = {}
    for baseline in ["bm25", "recent", "null"]:
        dest = str(out / f"m1b-octop-vs-{baseline}/delta.json")
        run(f"compare-{baseline}.json", [cli, "compare", str(out / f"m1b-{baseline}/report.json"), str(out / "m1b-octop-a/report.json"), "--metric", "recall", "--out", dest])
        c = json.loads(Path(dest).read_text())
        assert c["clusters"] == 1 and c["ci95"] is None and c["paired_cases"] == 43
        comparisons[baseline] = c
    run("keep.json", [cli, "run", "--suite", suite, "--sut", manifest, "--run-seed", seed, "--limit", "1", "--keep", "--out", str(out / "m1b-octop-keep")])
    kept = json.loads(Path(str(out / "m1b-octop-keep/report.json")).read_text())["isolation"]["effective_config"]
    files = {}
    for episode in kept:
        work = Path(episode["episode_dir"]) / "work"
        files[str(work)] = sorted(p.name for p in work.iterdir())
        assert set(files[str(work)]) <= {"memory.sqlite", "memory.sqlite-wal", "memory.sqlite-shm", "stderr.log"}
        assert {"memory.sqlite", "stderr.log"} <= set(files[str(work)])
    assert docs_before == {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in (root / "docs").rglob("*") if p.is_file()}
    result = {"status": "passed", "selftest": evidence_path, "cases_equal_without_timings": True, "cases_sha256": hashlib.sha256(normalized[0]).hexdigest(), "summary": {k: r["summary"] for k, r in reports.items()}, "comparisons": comparisons, "keep_files": files, "docs_unchanged": True, "linux_runtime_verified": False}
    (out / "acceptance.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(f"M1b §5 验收通过；证据：{out / 'acceptance.json'}")
    print("go vet ./...：通过；go test ./...：通过（真实 venv 契约）；无 venv：明确 skip。")
    print("setup/check/probe：通过；Darwin network none selftest：通过，含对照组。")
    print("octop 两轮：75 例，移除 timings 后逐字节一致；来源校验通过。")
    print(f"octop summary：unsupported={reports['octop-a']['summary']['unsupported']}，untraceable_hits={reports['octop-a']['summary']['untraceable_hits']}，execution_errors=0。")
    for k,c in comparisons.items():
        print(f"compare {k} → octop：recall Δ={c['delta_right_minus_left']:.9f}，paired=43，clusters=1，ci95=null。")
    print("--keep：每个 EP/work 仅有 SQLite 数据/日志及 stderr.log；清单保存在 acceptance.json。")

if __name__ == '__main__':
    verification_main(main)
