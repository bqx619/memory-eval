#!/usr/bin/env python3
"""P15 contract tests; all generated files go to temporary directories."""

import contextlib
import copy
import hashlib
import io
import json
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import build_case_review as review


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(__file__).with_name("build_case_review.py")
REAL = ROOT / "reports/p14-cogletnet/cogletnet-pmb75"
CONTROL = ROOT / "reports/p14-cogletnet/bm25-pmb75-ctrl"
SUITE = ROOT / "data/normalized/pmb-retrieval.json"
WORK_FIXTURE = ROOT / "scripts/testdata/case-review-fixture"


def put(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False), encoding="utf-8")


def parse_js(path, variable):
    text = path.read_text(encoding="utf-8")
    prefix = f"window.{variable} = "
    assert text.startswith(prefix) and text.endswith(";\n"), text[:80]
    return json.loads(text[len(prefix):-2])


def fixture(root):
    events = [
        {"source_id": "a", "content": "原文 a", "role": "fact", "owner": "u", "session": "s",
         "attributes": {"type": "preference", "scope": ["code"], "epistemic_status": "active"}},
        {"source_id": "b", "content": "原文 b", "attributes": {"scope": ["code"], "superseded_by": "a"}},
        {"source_id": "c", "content": "原文 c", "attributes": {"scope": ["food"], "epistemic_status": "active"}},
        {"source_id": "d", "content": "universal", "attributes": {"scope": ["user:universal"], "epistemic_status": "active"}},
        {"source_id": "future", "content": "未来事件"},
    ]
    suite = {"suite_id": "synthetic", "track": "retrieval-smoke", "episodes": [{"id": "e", "events": events, "cases": []}]}
    report = {"run_id": "run", "run_seed": "42" * 32, "started_at_utc": "2026-10-25T08:00:00Z",
              "suite_id": "synthetic", "suite_sha256": "sha", "mode": "retrieval", "sut": {"name": "synthetic"},
              "protocol": {"reset_policy": "fresh_per_case", "budget": {"top_k": 10, "max_context_chars": 10000}},
              "summary": {"metrics": {"recall": {"mean": 0.5, "n": 3}}}, "cases": []}
    definitions = [(["a", "d"], ["a", "d"], 1, None, "scored"),
                   (["a", "d"], ["a", "b", "c"], 0.5, None, "scored"),
                   (["a"], [], 0, None, "scored"),
                   ([], [], None, 1, "scored"),
                   ([], ["b", "c", "d"], None, 0, "scored"),
                   (["a"], [], None, None, "ingest_failed")]
    for i, (gold, returned, recall, empty, status) in enumerate(definitions):
        case_id = f"q{i}"
        suite["episodes"][0]["cases"].append({"id": case_id, "kind": "retrieval", "query": "问题 </script>\u2028", "user": "u",
             "scope": ["code"], "cutoff": 4, "gold": {"evidence_ids": gold, "exhaustive": True}})
        report["cases"].append({"case_id": case_id, "sample_id": case_id + "#0", "repeat_index": 0, "episode_id": "e",
             "category": "category", "kind": "retrieval", "cutoff": 4, "namespace": f"ns{i}", "status": status,
             "metrics": {"recall": recall, "empty_result_correct": empty}, "retrieved_source_ids": returned})
    put(root / "suite.json", suite)
    put(root / "run/report.json", report)
    return suite, report


class ContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="memeval-case-review-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.suite, self.report = fixture(self.root)

    def build(self, **kwargs):
        return review.build_review(self.root / "run", self.root / "suite.json", "test", **kwargs)

    def save(self):
        put(self.root / "run/report.json", self.report)

    def cli(self, *extra):
        return subprocess.run([sys.executable, "-B", str(SCRIPT), str(self.root / "run"),
                               "--suite", str(self.root / "suite.json"), "--name", "test",
                               "--out", str(self.root / "out"), *map(str, extra)], capture_output=True, text=True)

    def test_glossary_file_cli_filter_sort_and_preserve_definitions(self):
        # Synthetic definitions test transport only, not scoring semantics.
        def definition(ident):
            return {"id": ident, "group": "fixture", "name_zh": "测试名称", "definition_zh": "测试定义",
                    "formula": "fixture formula", "range": "[0,1]", "higher_is_better": None,
                    "aggregation": "fixture aggregation", "caveats": ["fixture caveat"], "applies_to": ["fixture"]}
        definitions = [definition(ident) for ident in ("unused", "summary_only", "recall", "case_only", "mrr")]
        self.report['summary']['metrics']['summary_only'] = {'mean': 0.5, 'n': 1}
        self.report['cases'][0]['metrics']['case_only'] = None  # Presence, not a non-null value, selects the definition.
        self.save()
        path = self.root / 'glossary.json'
        expected = sorted(definitions[1:], key=lambda entry: entry['id'])
        for payload in (definitions, {'glossary': definitions, 'results': []}):
            with self.subTest(index=isinstance(payload, dict)):
                put(path, payload)
                result = self.cli('--glossary', path)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, '')
                out = self.root / 'out'
                self.assertEqual(review.read_json(out / 'test.json')['glossary'], expected)
                self.assertEqual(parse_js(out / 'test.js', 'MEMEVAL_CASES')['glossary'], expected)
                before = {p.name: p.read_bytes() for p in out.iterdir()}
                self.assertEqual(self.cli('--glossary', path).returncode, 0)
                self.assertEqual(before, {p.name: p.read_bytes() for p in out.iterdir()})

    def test_glossary_without_binary_is_empty_with_one_warning(self):
        with patch.object(review, 'ROOT', self.root), patch.object(review.subprocess, 'run') as run, contextlib.redirect_stderr(io.StringIO()) as stderr:
            value = self.build()
        self.assertEqual(value['glossary'], [])
        run.assert_not_called()
        self.assertEqual(len(stderr.getvalue().splitlines()), 1)
        self.assertIn('glossary unavailable', stderr.getvalue())

    def test_glossary_binary_fallback_and_explicit_file_precedence(self):
        binary = self.root / 'bin/memeval'
        binary.parent.mkdir()
        binary.touch()  # Execution is mocked; no real binary needed.
        exported = [{'id': 'recall', 'formula': 'from binary'}]
        path = self.root / 'glossary.json'
        put(path, [{'id': 'recall', 'formula': 'from explicit export'}])
        with patch.object(review, 'ROOT', self.root), patch.object(review.subprocess, 'run',
                return_value=subprocess.CompletedProcess([], 0, json.dumps(exported), '')) as run:
            self.assertEqual(self.build()['glossary'], exported)
            run.assert_called_once_with([str(binary), 'results', 'glossary'], cwd=self.root,
                                        check=True, capture_output=True, text=True, timeout=30)
            run.reset_mock()
            self.assertEqual(self.build(glossary=path)['glossary'], review.read_json(path))
            run.assert_not_called()

    def test_unavailable_glossary_export_or_command_is_nonfatal(self):
        path = self.root / 'glossary.json'
        for content in ('not JSON', '{}', '[{"formula":"no id"}]'):
            with self.subTest(content=content):
                path.write_text(content)
                with patch.object(review.subprocess, 'run') as run, contextlib.redirect_stderr(io.StringIO()) as stderr:
                    self.assertEqual(self.build(glossary=path)['glossary'], [])
                run.assert_not_called()
                self.assertEqual(len(stderr.getvalue().splitlines()), 1)
        binary = self.root / 'bin/memeval'
        binary.parent.mkdir()
        binary.touch()
        with patch.object(review, 'ROOT', self.root), patch.object(review.subprocess, 'run',
                side_effect=subprocess.CalledProcessError(2, 'memeval')), contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(self.build()['glossary'], [])
        self.assertEqual(len(stderr.getvalue().splitlines()), 1)

    def test_verdicts_nulls_inputs_categories_and_analysis(self):
        value = self.build()
        self.assertEqual(value["summary"]["by_verdict"], dict.fromkeys(review.VERDICTS, 1))
        self.assertEqual(value["summary"]["error_stages"], {**dict.fromkeys(review.STAGES, 0), "ingest": 1})
        self.assertEqual(value["summary"]["by_category"]["category"]["recall_mean"], 0.5)
        self.assertEqual(value["summary"]["by_category"]["category"]["n_recall"], 3)
        self.assertEqual(value["summary"]["metrics"], self.report["summary"]["metrics"])
        self.assertIsNone(value["run"]["memeval_commit"])
        self.assertIsNone(value["run"]["resume_count"])
        self.assertIsNone(value["run"]["trace_text"])
        self.assertIsNone(value["run"]["sut_usage"])
        self.assertEqual(value["run"]["report_sha256"], hashlib.sha256((self.root / "run/report.json").read_bytes()).hexdigest())
        row = value["cases"][1]
        self.assertEqual(row["input"]["events_written"], ["a", "b", "c", "d"])
        self.assertIsNone(row["input"]["as_of"])
        self.assertIsNone(row["output"]["items"])
        self.assertIsNone(row["metrics"]["mrr"])
        self.assertIsNone(row["timings"]["query_ms"])
        text = "\n".join(row["analysis"]["auto"])
        for expected in ("漏掉 gold：d", "scope=", "superseded_by=", "排名：1", "被取代的事实", "作用域不匹配"):
            self.assertIn(expected, text)
        text = "\n".join(value["cases"][4]["analysis"]["auto"])
        self.assertIn("应为空却返回 3 个来源", text)
        self.assertIn("返回非 gold d：无关", text)

    def test_trace_manual_controls_and_repeat_alignment(self):
        self.report["protocol"]["trace_text"] = True
        self.report["cases"][1]["response"] = {"items": [{"id": "item", "text": "实际返回，非原文", "source_ids": ["a", "b"], "score": 0.91}]}
        self.save()
        control = copy.deepcopy(self.report)
        control["cases"][1]["retrieved_source_ids"] = ["a", "d"]
        control["cases"][1]["metrics"]["recall"] = 1
        control["cases"] = list(reversed(control["cases"]))
        control["cases"][0]["sample_id"] = "q5#1"
        control["cases"][0]["repeat_index"] = 1
        put(self.root / "bm25/report.json", control)
        put(self.root / "notes.json", {"q1": "人工分析"})
        result = self.build(controls=[self.root / "bm25"], notes=self.root / "notes.json")
        row = result["cases"][1]
        self.assertEqual(row["output"]["items"], [{"rank": 1, "id": "item", "text": "实际返回，非原文", "source_ids": ["a", "b"], "score": 0.91}])
        self.assertEqual(row["analysis"]["manual"], "人工分析")
        self.assertIn("bm25 命中全部。", row["analysis"]["auto"])
        self.assertIn("bm25 比本运行多返回：d。", row["analysis"]["auto"])
        self.assertIn("bm25 同样漏掉：a。", result["cases"][2]["analysis"]["auto"])
        self.assertIsNone(result["cases"][5]["controls"]["bm25"])

    def test_fixed_reader_and_unknown_scores_are_not_invented(self):
        for passed, metrics, expected in [(True, {}, "hit_all"), (False, {"beam.llm_judge_score": 1}, "miss"),
             (None, {"beam.llm_judge_score": 0.5}, "hit_partial"), (None, {"beam.llm_judge_score": 0}, "miss"),
             (None, {"mab_exact_match": 1}, "hit_all"), (None, {"mab_exact_match": 0}, "miss"), (None, {}, None)]:
            with self.subTest(passed=passed, metrics=metrics):
                self.assertEqual(review.verdict({"status": "scored", "passed": passed, "metrics": metrics}, {}, "fixed_reader"), expected)
        self.assertIsNone(review.verdict({"status": "scored", "kind": "retrieval", "metrics": {}}, {"evidence_ids": [], "exhaustive": True}, "retrieval"))
        for status in ("invalid", "contract_error", "unsupported", "unscored", "error"):
            self.assertEqual(review.verdict({"status": status}, {}, "retrieval"), "error")

    def test_control_mismatches_rejected_before_writing(self):
        for field in ("suite_sha256", "reset_policy", "top_k"):
            with self.subTest(field=field):
                control = copy.deepcopy(self.report)
                if field == "suite_sha256":
                    control[field] = "different"
                elif field == "reset_policy":
                    control["protocol"][field] = "incremental_per_episode"
                else:
                    control["protocol"]["budget"][field] = 5
                put(self.root / "bad/report.json", control)
                result = self.cli("--control", self.root / "bad")
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn(field, result.stderr)
                self.assertFalse((self.root / "out").exists())

    def test_native_failure_details_mapping_and_unknown_batch(self):
        summary = {"body": "leaf", "attempts": [{"ordinal": 2, "candidate_kind": "rewrite", "stage": "validation", "error_code": "faithfulness", "reason_hash": "abc"}], "future_field": [1, 2]}
        budget = {"role": "summary", "model": "test", "phase": "build", "unit": "bytes", "input": 100, "reserved_output": 3, "limit": 99}
        events = [{"state": "Ready"}, {"state": "Failed", "event_id": "ev", "operation_id": "op",
                    "error": {"kind": "SUMMARY_UNCOMPRESSIBLE", "message": "failed", "summary_failure": summary, "budget_failure": budget}},
                   {"state": "Unknown"}, {"state": "Ready"}]
        record = {"method": "ingest", "detail": "native failed", "rpc_error": {"data": {"details": {"cogletnet": {"events": events}}}}}
        log = self.root / "run/logs/resume-1/0001-ns5"
        put(log / "episode.json", {"namespace": "ns5"})
        (log / "rpc-errors.jsonl").write_text("<omitted>\n" + json.dumps(record) + "\n", encoding="utf-8")
        self.report["cases"][5].update(error_type="*jsonrpc.RPCError", error_detail="native failed")
        self.save()
        error = self.build()["cases"][5]["error"]
        self.assertEqual(error["stage"], "ingest")
        self.assertEqual(error["rpc_method"], "ingest")
        native = error["native"]
        self.assertEqual(native["batch_states"], {"Ready": 2, "Failed": 1, "Unknown": 1})
        self.assertEqual(native["failed_events"][0], {"index": 1, "source_id": "b", "event_id": "ev", "state": "Failed", "kind": "SUMMARY_UNCOMPRESSIBLE", "message": "failed", "operation_id": "op", "summary_failure": summary, "budget_failure": budget})
        self.assertEqual(native["failed_events"][1]["source_id"], "c")
        mapped = review.native_detail(record, ["a", "b"])
        self.assertIsNone(mapped["failed_events"][0]["source_id"])
        self.assertEqual(mapped["failed_events"][0]["index"], 1)

    def test_error_stage_precedence_and_evaluator(self):
        for method, kind, stage in [("ingest", "rpc_timeout", "ingest"), ("retrieve", "*jsonrpc.RPCError", "retrieve"),
              (None, "rpc_timeout", "bridge"), (None, "process_exited", "bridge"), (None, "*jsonrpc.RPCError", "bridge"),
              (None, "other", "unclassified")]:
            self.assertEqual(review.error_of({"status": "error", "error_type": kind}, [{"method": method}], [])["stage"], stage)
        for kind in review.EVALUATOR_ERRORS:
            self.assertEqual(review.error_of({"status": "error", "error_type": kind}, [], [])["stage"], "evaluator")
            error = review.error_of({"status": "error", "error_type": kind, "error_detail": "reader failed"},
                                    [{"method": "ingest", "detail": "old RPC error"}], [])
            self.assertEqual(error["stage"], "evaluator")
            self.assertIsNone(error["rpc_method"])
        self.assertIsNone(review.error_of({"status": "scored"}, [{"method": "ingest"}], []))

    def test_timestamp_precision_and_offsets(self):
        stamp = "2026-10-23T09:13:28.42073Z"
        self.assertEqual(review.timestamp_key(stamp), review.timestamp_key("2026-10-23T17:13:28.420730000+08:00"))
        self.assertLess(review.timestamp_key(stamp), review.timestamp_key("2026-10-23T09:13:28.420730001Z"))
        self.report["resume_history"] = [{"resumed_at_utc": "2026-10-25T09:13:28.42073Z"}]
        self.save()
        self.assertEqual(self.build()["generated_at"], "2026-10-25T09:13:28.42073Z")

    def test_resume_order_last_close_usage_and_deterministic_js(self):
        first = copy.deepcopy(self.report)
        first["cases"][0]["status"] = "error"
        put(self.root / "run/resume-history/0002/report.json", self.report)
        put(self.root / "run/resume-history/0001/report.json", first)
        self.report["resume_history"] = [{"resumed_at_utc": "2026-10-25T09:00:00Z"}]
        self.save()
        usage = {**dict.fromkeys(review.USAGE_FIELDS, 2), "by_role": {"embedding": dict.fromkeys(review.USAGE_FIELDS, 1)}}
        for name in ("0001-ns0", "resume-1/0002-ns1"):
            log = self.root / "run/logs" / name
            log.mkdir(parents=True)
            records = [{"method": "close", "usage": dict.fromkeys(review.USAGE_FIELDS, 999)}, {"method": "close", "usage": usage}, {"method": "retrieve", "usage": dict.fromkeys(review.USAGE_FIELDS, 999)}]
            (log / "sut.stderr.log").write_text("\n".join("cogletnet_usage " + json.dumps(row) for row in records), encoding="utf-8")
        result = self.build()
        self.assertEqual(result["generated_at"], "2026-10-25T09:00:00Z")
        self.assertEqual([a["status"] for a in result["cases"][0]["attempts"]], ["error", "scored", "scored"])
        self.assertEqual([a["pass"] for a in result["cases"][0]["attempts"]], [0, 1, 2])
        self.assertEqual(result["run"]["sut_usage"]["calls"], 4)
        self.assertEqual(result["run"]["sut_usage"]["by_role"]["embedding"]["calls"], 2)
        self.assertEqual(self.cli().returncode, 0)
        before = {p.name: p.read_bytes() for p in (self.root / "out").iterdir()}
        self.assertEqual(self.cli().returncode, 0)
        self.assertEqual(before, {p.name: p.read_bytes() for p in (self.root / "out").iterdir()})
        self.assertNotIn(b"</script>", before["test.js"])
        self.assertIn(b"<\\/script>", before["test.js"])
        self.assertEqual(parse_js(self.root / "out/test.js", "MEMEVAL_CASES"), result)

    def test_index_upsert_sort_and_safe_names(self):
        a = self.build()
        out = self.root / "out"
        review.write_review(a, out)
        b = copy.deepcopy(a)
        b["run"]["name"] = "new"
        b["generated_at"] = "2026-10-26T00:00:00Z"
        review.write_review(b, out)
        review.write_review(a, out)
        index = parse_js(out / "index.js", "MEMEVAL_CASE_RUNS")
        self.assertEqual([entry["name"] for entry in index], ["new", "test"])
        self.assertEqual(index[0]["file"], "new.js")
        for name in ("../escape", "index", "x/y"):
            a["run"]["name"] = name
            with self.assertRaisesRegex(ValueError, "filename"):
                review.write_review(a, out)
        a["run"]["name"] = "test"
        (out / "index.js").write_text("unexpected executable JS", encoding="utf-8")
        before = (out / "test.json").read_bytes()
        with self.assertRaisesRegex(ValueError, "assignment"):
            review.write_review(a, out)
        self.assertEqual((out / "test.json").read_bytes(), before)


class ExistingReportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not all(p.exists() for p in (REAL / "report.json", CONTROL / "report.json", SUITE)):
            raise unittest.SkipTest("local P14 reports/PMB suite absent")

    def test_pmb75_acceptance_and_byte_identity(self):
        value = review.build_review(REAL, SUITE, "cogletnet-pmb75", [CONTROL])
        original = review.read_json(REAL / "report.json")
        self.assertEqual(len(value["cases"]), 75)
        self.assertEqual(value["summary"]["by_status"], original["summary"]["status_counts"])
        self.assertEqual(value["summary"]["by_verdict"], {"hit_all": 36, "hit_partial": 3, "miss": 4, "empty_correct": 7, "empty_wrong": 25, "error": 0})
        self.assertEqual(value["summary"]["error_stages"], dict.fromkeys(review.STAGES, 0))
        self.assertEqual(value["summary"]["metrics"], original["summary"]["metrics"])
        self.assertEqual(value["summary"]["metrics"]["recall"]["mean"], 0.8765227021040974)
        self.assertEqual(value["summary"]["metrics"]["empty_result_correct"]["mean"], 0.21875)
        self.assertEqual(sum(row["attempts"][0]["status"] == "error" for row in value["cases"]), 9)
        self.assertEqual(sum(row["attempts"][1]["status"] == "error" for row in value["cases"]), 1)
        self.assertTrue(all(row["output"]["items"] is None for row in value["cases"]))
        self.assertTrue(all(row["work"] == {"trace": "missing", "build": [], "search": None} for row in value["cases"]))
        self.assertEqual(value["texts"], {})
        self.assertEqual(value["summary"]["work_mapping_mismatches"], 0)
        self.assertEqual(value["run"]["report_path"], "reports/p14-cogletnet/cogletnet-pmb75/report.json")
        with tempfile.TemporaryDirectory(prefix="memeval-case-review-acceptance-") as out:
            command = [sys.executable, "-B", str(SCRIPT), str(REAL), "--suite", str(SUITE), "--name", "cogletnet-pmb75", "--control", str(CONTROL), "--out", out]
            first = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(first.returncode, 0, first.stderr)
            before = {p.name: p.read_bytes() for p in Path(out).iterdir()}
            second = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertEqual(first.stdout, second.stdout)
            self.assertEqual(before, {p.name: p.read_bytes() for p in Path(out).iterdir()})

    def test_real_beam_native_error_shape(self):
        logs = ROOT / "reports/p14-cogletnet/cogletnet-v2-beam100k-400/logs"
        for path in sorted(logs.glob("*/rpc-errors.jsonl")):
            for record in review.jsonl(path):
                native = review.native_detail(record, [])
                if native and native["failed_events"]:
                    self.assertTrue(all(e["source_id"] is None for e in native["failed_events"]))
                    self.assertTrue(any(e["kind"] == "SUMMARY_UNCOMPRESSIBLE" for e in native["failed_events"]))
                    self.assertEqual(review.error_of({"status": "error", "error_type": "*jsonrpc.RPCError"}, [record], [])["stage"], "ingest")
                    return
        self.skipTest("local BEAM native failure logs absent")


class WorkTraceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="memeval-work-review-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.run = self.root / "fixture"
        shutil.copytree(WORK_FIXTURE, self.run)
        self.log = self.run / "logs/0001-fixture/sut.stderr.log"
        self.records = [json.loads(line[len("sut_trace "):]) for line in self.log.read_text().splitlines()
                        if line.startswith("sut_trace ")]

    def save(self):
        self.log.write_text("\n".join("sut_trace " + json.dumps(r) for r in self.records) + "\n", encoding="utf-8")

    def build(self):
        return review.build_review(self.run, self.run / "suite.json", "work-fixture")

    def test_single_attempt_case_work_bytes_unchanged(self):
        value = self.build()
        work = {'cases': [v['work'] for v in value['cases']], 'texts': value['texts']}
        self.assertEqual(hashlib.sha256(review.encode(work).encode()).hexdigest(),
                         '1132282bfb4490b8a778dfe0a838ffda8c33084ae508473329f9b846d928f793')

    def test_lost_redaction_escape_restores_full_build_and_references(self):
        first, later = [r for r in self.records if r['kind'] == 'build']
        first['audit']['note'] = 'url?[REDACTED]" http://fixture.invalid; [REDACTED]"inside'
        first['audit']['closed_string'] = '[REDACTED]'
        for key in ('operation_id', 'snapshot_id'):
            later[key] = first[key]
        later['audit'], later['snapshot'] = {'ref_seq': first['seq']}, {'ref_seq': first['seq']}
        self.save()
        valid = self.log.read_text()
        broken = valid.replace(r'[REDACTED]\"', '[REDACTED]"')
        self.assertNotEqual(valid, broken)
        self.log.write_text(broken)
        trace = review.WorkTraces(self.run)
        self.assertEqual(trace.corrupt_lines['fixture'], 0)
        builds = [r for r in trace.logs['fixture'] if r['kind'] == 'build']
        self.assertEqual(builds[0]['audit'], first['audit'])
        self.assertIs(builds[1]['audit'], builds[0]['audit'])
        self.assertIs(builds[1]['snapshot'], builds[0]['snapshot'])
        # Valid delimiters (including whitespace before them) must stay intact.
        raw = '{"a": "[REDACTED]" , "b": ["[REDACTED]" ], "[REDACTED]" : 1}'
        self.assertEqual(review.parse_work_trace(raw), json.loads(raw))
        with self.log.open('a') as stream:
            stream.write('sut_trace {truncated\n')
        self.assertEqual(review.WorkTraces(self.run).corrupt_lines['fixture'], 1)

    def test_build_references_resolve_to_identical_case_data_and_config(self):
        builds = [r for r in self.records if r['kind'] == 'build']
        first, later = builds
        first['trace_batch'] = later['trace_batch'] = 0
        first['audit']['operation']['config'] = {'memory_tokens': 12000, 'generator': {'model': 'fixture'}}
        for key in ('operation_id', 'snapshot_id', 'audit', 'snapshot'):
            later[key] = copy.deepcopy(first[key])
        self.save()
        full = self.build()
        later['audit'], later['snapshot'] = {'ref_seq': 0}, {'ref_seq': 0}
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            compact = self.build()
            trace = review.WorkTraces(self.run)
        self.assertEqual(stderr.getvalue(), '')
        self.assertEqual(review.encode(full), review.encode(compact))
        decoded = [r for r in trace.logs['fixture'] if r['kind'] == 'build']
        self.assertIs(decoded[1]['audit'], decoded[0]['audit'])
        self.assertIs(decoded[1]['snapshot'], decoded[0]['snapshot'])
        self.assertEqual(trace.corrupt_lines['fixture'], 0)
        config, mismatches = trace.build_config(trace.logs['fixture'])
        self.assertEqual(config['memory_tokens'], 12000)
        self.assertEqual(mismatches, 0)

    def test_broken_build_references_count_once_per_line_and_never_cross_boundaries(self):
        first = copy.deepcopy(next(r for r in self.records if r['kind'] == 'build'))
        first['trace_batch'] = 0
        ref = dict(first, seq=1, event_id='second', audit={'ref_seq': 0}, snapshot={'ref_seq': 0})
        wrong_batch = dict(ref, trace_batch=1)
        wrong_identity = dict(ref, operation_id='different-operation', snapshot_id='different-snapshot')
        forward = dict(ref, audit={'ref_seq': 99}, snapshot={'ref_seq': 99})
        malformed = dict(ref, audit={'ref_seq': []}, snapshot={'ref_seq': True})
        chain = dict(ref, seq=2, audit={'ref_seq': 1}, snapshot={'ref_seq': 1})
        self.records = [first, ref, wrong_batch, wrong_identity, forward, malformed, chain]
        self.save()
        other = self.run / 'logs/0009-other/sut.stderr.log'
        other.parent.mkdir()
        other.write_text('sut_trace {broken\n' + 'sut_trace ' + json.dumps(ref) + '\n')
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            trace = review.WorkTraces(self.run)
        self.assertEqual(trace.corrupt_lines['fixture'], 5)
        self.assertEqual(trace.corrupt_lines['other'], 2)
        self.assertEqual(stderr.getvalue().count('unresolved build trace reference lines'), 2)
        for row in trace.logs['fixture'][2:] + trace.logs['other']:
            self.assertIsNone(row['audit'])
            self.assertIsNone(row['snapshot'])
            self.assertIn('unresolved build trace reference', row['audit_error'])
            rendered = trace.build(row, None, {})
            self.assertEqual(rendered['nodes'], [])
            self.assertIsNone(rendered['pending'])
        self.assertIs(trace.logs['fixture'][1]['audit'], trace.logs['fixture'][0]['audit'])

    def test_nodes_summaries_calls_search_and_deduplicated_texts(self):
        value = self.build()
        good, failed = [c["work"] for c in value["cases"][:2]]
        self.assertEqual(good["trace"], "full")
        self.assertEqual(len(good["build"]), 1)  # No future build from the next case.
        build = good["build"][0]
        self.assertEqual(build["source_id"], "a")
        self.assertEqual([n["level"] for n in build["nodes"]], [0, 1])
        self.assertEqual(build["nodes"][0]["span"], [0, 27])
        self.assertEqual(build["nodes"][1]["inputs"], ["node-anon-a"])
        self.assertTrue(build["nodes"][1]["underfill"])
        self.assertEqual(build["nodes"][1]["summary"]["attempts"], 2)
        self.assertTrue(build["nodes"][1]["summary"]["faithful"])
        self.assertEqual([a["candidate_kind"] for a in build["summaries"]], ["Model", "ExtractivePrefix"])
        self.assertEqual(build["calls"][0], {"role": "validation", "model": "fake-validator", "dispatches": 2, "failed": 1, "terminal_error": None})
        for attempt in build["summaries"]:
            self.assertIn(attempt["output_hash"], value["texts"])
            reason = attempt["verdict"]["reason_hash"]
            self.assertTrue(reason.startswith("sha256-16:"))
            self.assertIn(reason, value["texts"])
        self.assertEqual(build["nodes"][1]["text_hash"], build["summaries"][1]["output_hash"])
        search = good["search"]
        self.assertIsNone(search["error"])
        self.assertEqual((search["limit"], search["jev_threshold"], search["mode"]), (2, 0.5, "Jev"))
        self.assertEqual([c["outcome"] for c in search["candidates"]], ["selected", "jev_below_threshold"])
        self.assertEqual([c["rank"] for c in search["candidates"]], [1, None])
        self.assertEqual([c["source_id"] for c in search["candidates"]], ["a", "a"])
        self.assertEqual([c["origin"] for c in search["candidates"]], ["VectorHit", "AncestorExpansion"])
        self.assertEqual([c["vector_rank"] for c in search["candidates"]], [1, None])
        raw = next(r['result'] for r in self.records if r['kind'] == 'search')
        self.assertEqual(search['settings'], raw['settings'])
        batch = search['jev_trace'][0]
        self.assertEqual((batch['batch'], batch['model'], batch['error_code']), (0, 'fake-jev', None))
        self.assertEqual([t['probability'] for t in batch['targets']], [0.9, 0.3])
        self.assertEqual([t['source_id'] for t in batch['targets']], ['a', 'a'])
        for target, original in zip(batch['targets'], raw['jev_trace'][0]['targets']):
            self.assertEqual(value['texts'][target['target_text_hash']], original['target_body'])
            self.assertEqual(value['texts'][target['path_text_hash']], original['full_path'])
        item = search["items"][0]
        self.assertEqual(value["texts"][item["text_hash"]], 'Please use concise answers.\nUse concise answers.')
        self.assertEqual([part["text_hash"] for part in item["path"]], [n["text_hash"] for n in build["nodes"]])
        self.assertEqual(search["context_view"]["rendered_hash"], item["text_hash"])
        self.assertEqual(search["context_view"]["visible"][0]["source_id"], "a")
        self.assertEqual(failed["build"][1]["source_id"], "b")
        self.assertEqual(failed["build"][1]["state"], "Failed")
        self.assertIsNone(failed["build"][1]["nodes"][0]["level"])
        self.assertIsNone(failed["build"][1]["nodes"][0]["edge"])
        self.assertIn('snapshot_id unavailable', failed["build"][1]["audit_error"])
        self.assertEqual(failed["build"][1]["calls"][0]["terminal_error"], 'SUMMARY_UNCOMPRESSIBLE: synthetic failure')
        self.assertIsNone(failed["search"])
        self.assertEqual(value["summary"]["work_mapping_mismatches"], 0)

    def test_failed_search_preserves_native_error_and_legacy_null(self):
        value = self.build()
        search = value['cases'][2]['work']['search']
        path = self.run / 'logs/0002-fixture-search/sut.stderr.log'
        records = [json.loads(line[len('sut_trace '):]) for line in path.read_text().splitlines()]
        row = records[-1]
        self.assertIsNone(row['result'])
        self.assertEqual(search['error'], row['error'])
        self.assertEqual(search['error']['model_failure']['status'], 503)
        self.assertEqual((search['query'], search['limit']), (row['query'], row['limit']))
        self.assertEqual(search['items'], [])
        self.assertIsNone(search['settings'])
        self.assertEqual(value['summary']['work_mapping_mismatches'], 0)
        work = review.WorkTraces(self.run)
        for missing in (None, 'old unstructured error'):
            legacy = dict(row, error=missing)
            self.assertIsNone(work.search(legacy, {}, {}, {}, ('fixture-search', 2)))

    def test_build_composition_pending_and_summary_diagnostics(self):
        row = next(r for r in self.records if r['kind'] == 'build')
        first = {'body': 'body-anon-a', 'text': 'saved summary', 'output_hash': 'summary-output',
                 'model': 'fake-generator', 'attempts': 2,
                 'validation': {'faithful': False, 'reason': 'not faithful', 'reason_hash': 'summary-reason'}}
        second = {'body': 'other-body', 'text': 'pending summary', 'model': 'other-generator', 'attempts': 1}
        row['snapshot']['members'][1]['input_summaries'] = [first, second]
        row['snapshot']['pending'] = {'1': [second, first], '2': []}
        row['audit']['summaries'][0].update(copied_span={'start': 2, 'end': 7}, input_tokens=42,
                                           failure={'kind': 'synthetic', 'message': 'failed'})
        row['audit']['summaries'][1].update(copied_span=[0, 3], failure='unstructured failure')
        original = copy.deepcopy(row)
        self.save()
        value = self.build()
        build = value['cases'][0]['work']['build'][0]
        self.assertEqual(build['nodes'][0]['body'], row['snapshot']['members'][0]['body']['reference']['body'])
        self.assertEqual(build['nodes'][0]['input_summaries'], [])
        inputs = build['nodes'][1]['input_summaries']
        self.assertEqual(inputs[0], {'body': 'body-anon-a', 'output_hash': 'summary-output',
                                    'model': 'fake-generator', 'attempts': 2,
                                    'faithful': False, 'reason_hash': 'summary-reason'})
        self.assertEqual([x['body'] for x in inputs], ['body-anon-a', 'other-body'])
        self.assertIsNone(inputs[1]['faithful'])
        self.assertIsNone(inputs[1]['reason_hash'])
        self.assertEqual(build['pending'], {'1': list(reversed(inputs)), '2': []})
        self.assertEqual(value['texts'][inputs[0]['output_hash']], 'saved summary')
        self.assertEqual(value['texts'][inputs[0]['reason_hash']], 'not faithful')
        self.assertEqual(value['texts'][inputs[1]['output_hash']], 'pending summary')
        self.assertEqual(build['summaries'][0]['copied_span'], [2, 7])
        self.assertEqual(build['summaries'][0]['input_tokens'], 42)
        self.assertEqual(build['summaries'][0]['failure'], 'synthetic: failed')
        self.assertEqual(build['summaries'][1]['copied_span'], [0, 3])
        self.assertEqual(build['summaries'][1]['failure'], 'unstructured failure')
        self.assertIsNone(build['summaries'][1]['input_tokens'])
        fallback = value['cases'][1]['work']['build'][1]
        self.assertIsNone(fallback['pending'])
        self.assertTrue(all(n['input_summaries'] is None for n in fallback['nodes']))
        self.assertEqual(row, original)  # Projection never rewrites trace data.

    def test_build_missing_summary_diagnostics_are_null(self):
        traces = review.WorkTraces(self.run)
        summary = traces.summary({})
        self.assertIsNone(summary['copied_span'])
        self.assertIsNone(summary['input_tokens'])
        self.assertIsNone(summary['failure'])
        self.assertEqual(traces.build({'snapshot': {'members': []}}, None, {})['pending'], {})

    def test_text_and_sequence_mismatch_never_guess_provenance(self):
        self.records[1]["text_sha256"] = "bad"
        self.save()
        value = self.build()
        self.assertEqual(value["summary"]["work_mapping_mismatches"], 1)  # Reused by 2 cases, counted once.
        work = value["cases"][0]["work"]
        self.assertIsNone(work["build"][0]["source_id"])
        self.assertTrue(all(c["source_id"] is None for c in work["search"]["candidates"]))
        self.records[1]["seq"] = 17
        self.save()
        value = self.build()
        self.assertIsNone(value["cases"][0]["work"]["build"][0]["source_id"])
        self.assertEqual(value["summary"]["work_mapping_mismatches"], 1)

    def test_disabled_missing_truncated_and_resume_streams(self):
        self.log.write_text('sut_trace {"kind":"config","work_trace":"off"}\n', encoding='utf-8')
        self.assertEqual(self.build()["cases"][0]["work"], {"trace": "off", "build": [], "search": None})
        self.log.write_text('sut_trace {truncated\n', encoding='utf-8')
        self.assertEqual(self.build()["cases"][0]["work"]["trace"], "missing")
        self.save()
        resumed = self.run / 'logs/resume-1/0001-fixture/sut.stderr.log'
        resumed.parent.mkdir(parents=True)
        resumed.write_text('sut_trace {"kind":"config","work_trace":"off"}\n', encoding='utf-8')
        self.assertEqual(self.build()["cases"][0]["work"]["trace"], "off")
        self.log.unlink()
        resumed.unlink()
        report = review.read_json(self.run / 'report.json')
        report['sut']['describe']['config']['work_trace'] = 'off'
        put(self.run / 'report.json', report)
        self.assertEqual(self.build()["cases"][0]["work"]["trace"], "off")

    def test_all_omission_reasons_unknown_sources_and_visible_spans(self):
        search = next(r for r in self.records if r['kind'] == 'search')['result']
        reasons = ['candidate_cap', 'evidence_cap', 'result_limit', 'CONTEXT_BUNDLE_TOO_LARGE', 'unselected']
        for reason in reasons:
            search['candidates'].append({'reference': {'node': reason}, 'source': 'unknown-source'})
            if reason != 'unselected':
                search['omitted'][reason] = reason
        search['context_view']['visible'][0]['text'] = 'concise'
        self.save()
        value = self.build()
        search = value['cases'][0]['work']['search']
        self.assertEqual([c['outcome'] for c in search['candidates'][2:]], reasons)
        self.assertTrue(all(c['source_id'] is None for c in search['candidates'][2:]))
        self.assertEqual(value['texts'][search['context_view']['visible'][0]['text_hash']], 'concise')
        self.assertEqual(value['texts'][search['items'][0]['path'][0]['text_hash']], 'Please use concise answers.')

    def test_effective_settings_override_including_null_and_old_records(self):
        row = next(r for r in self.records if r['kind'] == 'search')
        for threshold in (0.8, 0, None):
            with self.subTest(threshold=threshold):
                row['result']['settings']['jev_threshold'] = threshold
                row['result']['settings']['future_field'] = {'preserve': True}
                self.save()
                search = self.build()['cases'][0]['work']['search']
                self.assertEqual(search['jev_threshold'], threshold)
                self.assertEqual(search['settings'], row['result']['settings'])
        for legacy in (None, {}):
            row['result']['settings'] = legacy
            self.save()
            self.assertEqual(self.build()['cases'][0]['work']['search']['jev_threshold'], 0.5)
        del row['result']['settings']
        del row['result']['jev_trace']
        del row['jev_threshold']
        self.records[0].pop('jev_threshold')
        for candidate in row['result']['candidates']:
            candidate.pop('origin')
            candidate.pop('vector_rank')
        self.save()
        search = self.build()['cases'][0]['work']['search']
        self.assertEqual(search['jev_threshold'], 0.5)  # report describe configuration
        self.assertIsNone(search['settings'])
        self.assertIsNone(search['jev_trace'])
        self.assertTrue(all(c['origin'] is None and c['vector_rank'] is None for c in search['candidates']))

    def test_jev_probability_alignment_multiple_batches_and_failure(self):
        raw = next(r['result'] for r in self.records if r['kind'] == 'search')
        original = copy.deepcopy(raw['jev_trace'][0])
        # Target order deliberately differs from candidate order: probabilities
        # follow the target array, never candidate rank or selected item rank.
        raw['jev_trace'][0]['targets'].reverse()
        raw['jev_trace'][0]['probabilities'] = [0.13, 0.91]
        short = copy.deepcopy(original)
        short['probabilities'] = [0.99]
        long = copy.deepcopy(original)
        long['probabilities'] = [0.99, 0.5, 0.1]
        failure = copy.deepcopy(original)
        failure.update(probabilities=None, error_code='MODEL_UNAVAILABLE')
        unknown = copy.deepcopy(original)
        unknown['targets'][0]['id'] = 'unknown-node'
        unknown['probabilities'] = [0.9, 0.3]
        raw['jev_trace'] += [short, long, failure, unknown]
        self.save()
        value = self.build()
        batches = value['cases'][0]['work']['search']['jev_trace']
        self.assertEqual([b['batch'] for b in batches], list(range(5)))
        self.assertEqual([t['node'] for t in batches[0]['targets']], ['summary-anon-a', 'node-anon-a'])
        self.assertEqual([t['probability'] for t in batches[0]['targets']], [0.13, 0.91])
        for batch in batches[1:4]:
            self.assertTrue(all(t['probability'] is None for t in batch['targets']))
        self.assertEqual(batches[3]['error_code'], 'MODEL_UNAVAILABLE')
        self.assertIsNone(batches[4]['targets'][0]['source_id'])
        self.assertEqual(value['summary']['work_mapping_mismatches'], 2)
        self.records[1]['text_sha256'] = 'wrong-source-text'
        self.save()
        value = self.build()
        self.assertEqual(value['summary']['work_mapping_mismatches'], 3)
        self.assertTrue(all(t['source_id'] is None for t in value['cases'][0]['work']['search']['jev_trace'][0]['targets']))
        raw['jev_trace'] = []
        self.save()
        self.assertEqual(self.build()['cases'][0]['work']['search']['jev_trace'], [])

    def test_jev_retry_attempts_pass_through_without_changing_mapping(self):
        raw = next(r['result'] for r in self.records if r['kind'] == 'search')
        raw['settings']['jev_retry_policy'] = {'max_attempts': 3, 'initial_backoff_ms': 1000,
                                             'max_backoff_ms': 30000, 'jitter_ms': 250}
        attempts = [
            {'attempt': 1, 'error_code': 'MODEL_UNAVAILABLE', 'retry_delay_ms': 1034,
             'model_failure': {'role': 'jev', 'model': 'fake-jev', 'service': 'service-hash',
                               'phase': 'status', 'status': 503, 'request_id': 'request-1',
                               'elapsed_ms': 812, 'retryable': True, 'retry_after_ms': 1000,
                               'outcome_unknown': False}},
            {'attempt': 2, 'error_code': None, 'model_failure': None, 'retry_delay_ms': None},
        ]
        raw['jev_trace'][0]['attempts'] = copy.deepcopy(attempts)
        self.save()
        value = self.build()
        search = value['cases'][0]['work']['search']
        self.assertEqual(search['settings']['jev_retry_policy'], raw['settings']['jev_retry_policy'])
        self.assertEqual(search['jev_trace'][0]['attempts'], attempts)
        self.assertEqual([t['source_id'] for t in search['jev_trace'][0]['targets']], ['a', 'a'])
        self.assertEqual([t['probability'] for t in search['jev_trace'][0]['targets']], [0.9, 0.3])
        self.assertEqual(value['summary']['work_mapping_mismatches'], 0)
        self.assertEqual(raw['jev_trace'][0]['attempts'], attempts)
        del raw['jev_trace'][0]['attempts']
        self.save()
        self.assertEqual(self.build()['cases'][0]['work']['search']['jev_trace'][0]['attempts'], [])

    def test_upstream_probability_map_aligns_by_full_target_id(self):
        raw = next(r['result'] for r in self.records if r['kind'] == 'search')
        batch = raw['jev_trace'][0]
        self.assertIsInstance(batch['probabilities'], dict)
        batch['targets'].reverse()
        self.save()
        value = self.build()
        targets = value['cases'][0]['work']['search']['jev_trace'][0]['targets']
        self.assertEqual([t['node'] for t in targets], ['summary-anon-a', 'node-anon-a'])
        self.assertEqual([t['probability'] for t in targets], [0.3, 0.9])
        self.assertEqual([t['source_id'] for t in targets], ['a', 'a'])
        self.assertEqual(value['summary']['work_mapping_mismatches'], 0)
        for probabilities in ({'wrong-1': 0.9, 'wrong-2': 0.3},
                              {'node-anon-a/body-anon-a': 0.9}):
            with self.subTest(probabilities=probabilities):
                batch['probabilities'] = probabilities
                self.save()
                value = self.build()
                targets = value['cases'][0]['work']['search']['jev_trace'][0]['targets']
                self.assertTrue(all(t['probability'] is None for t in targets))
                self.assertEqual(value['summary']['work_mapping_mismatches'], 1)

    def test_fixture_cli_byte_identity_and_local_hash_collisions(self):
        out = self.root / 'output'
        cmd = [sys.executable, '-B', str(SCRIPT), str(self.run), '--suite', str(self.run / 'suite.json'),
               '--name', 'work-fixture', '--out', str(out)]
        subprocess.run(cmd, check=True, capture_output=True)
        before = {p.name: p.read_bytes() for p in out.iterdir()}
        subprocess.run(cmd, check=True, capture_output=True)
        self.assertEqual(before, {p.name: p.read_bytes() for p in out.iterdir()})
        value = parse_js(out / 'work-fixture.js', 'MEMEVAL_CASES')
        self.assertEqual(value, json.loads(before['work-fixture.json']))
        work = review.WorkTraces(self.run)
        key = work.text('hello')
        self.assertEqual(key, 'sha256-16:' + hashlib.sha256(b'hello').hexdigest()[:16])
        with self.assertRaisesRegex(ValueError, 'collision'):
            work.text('different', key)


def leaf_memory_fixture():
    """Synthetic v3 wire fixture, shared with BEAM tests; no dataset text."""
    events = [{"source_id": f"e{i}", "content": f"synthetic event {i}", "role": "user"} for i in range(6)]
    questions = [{"id": "q", "query": "synthetic lookup", "evidence_ids": ["e0"]}]
    refs = [{"node": f"n{i}", "body": f"b{i}"} for i in range(48)]
    rows = [{"kind": "config", "work_trace": "full", "jev_threshold": 0.5}]
    for generation, start, end in [(1, 0, 2), (2, 2, 6)]:
        members, memories = [], {}
        for i in range(end * 8):
            event = i // 8
            members.append({"body": {"reference": refs[i], "text": f"synthetic leaf {i}", "kind": "Leaf"},
                            "level": 0, "ordinal": i + generation, "leaf_range": {"start": i, "end": i + 1},
                            "edge": {"kind": "SummaryParent", "parent": f"upper-{generation}"}, "inputs": [], "input_summaries": [],
                            "underfill": False, "origin": {"event_id": f"event-{event}", "event_sequence": event,
                            "role": "user", "source": {"id": f"native-{event}", "version": 1},
                            "source_span": {"start": 0, "end": 5}}})
            memories[f"n{i}"] = {"input_tokens": 100 + i, "input_hash": f"input-{i}", "path_revision": f"revision-{generation}"}
        if generation == 2:
            members.append(copy.deepcopy(rows[1]['snapshot']['members'][-1]))
        members.append({"body": {"reference": {"node": f"upper-{generation}", "body": f"summary-{generation}"},
                                 "text": f"synthetic upper {generation}", "kind": "Summary"},
                        "level": generation, "ordinal": end * 8, "leaf_range": {"start": 0, "end": end * 8},
                        "edge": None, "inputs": refs[:end * 8], "input_summaries": []})
        snapshot = {"id": f"snapshot-{generation}", "generation": generation, "scope": {"id": "scope-a", "revision": generation},
                    "members": members, "memories": memories, "pending": {},
                    "events": [{"event_id": f"event-{i}", "event_sequence": i, "role": "user", "source": {"id": f"native-{i}"},
                                "leaf_range": {"start": i * 8, "end": (i + 1) * 8}} for i in range(end)]}
        audit = {"operation": {"error": None, "config": {"memory_tokens": 12000}}, "bodies": [], "summaries": [],
                 "calls": [{"role": "summary", "attempts": [{"started_at": 1000000, "completed_at": 2000000, "failure": {"kind": "retry"}},
                                                            {"started_at": 2500000, "completed_at": 5000000, "failure": None}]},
                           {"role": "embedding", "attempts": [{"started_at": 1500000, "completed_at": 4000000, "failure": None}]}]}
        for i in range(start, end):
            rows.append({"kind": "build", "seq": i, "trace_batch": generation, "event_id": f"event-{i}", "state": "Ready",
                         "text_sha256": hashlib.sha256(events[i]['content'].encode()).hexdigest(),
                         "operation_id": f"op-{generation}", "snapshot_id": f"snapshot-{generation}",
                         "audit": audit if i == start else {"ref_seq": start}, "snapshot": snapshot if i == start else {"ref_seq": start}})
    candidates, items, omitted, targets, answers = [], [], {}, [], {}
    for i, ref in enumerate(refs):
        outcome = {"kind": "reference", "useful": 0.8 if i < 47 else 0.1,
                   "not_useful": 0.1 if i < 47 else 0.8, "uncertain": 0.1}
        source = {"id": f"native-{i // 8}", "version": 1}
        candidate = {"reference": ref, "source": source, "level": 0, "origin": "VectorHit", "similarity": 0.9,
                     "vector_rank": i + 1, "jev_outcome": outcome}
        candidates.append(candidate)
        memory = [{"current": 0, "info": [f"synthetic leaf {i}"],
                   "metadata": {"ref": ref, "ordinal": i + 2, "origins": [{"event_id": f"event-{i // 8}", "event_sequence": i // 8, "role": "user"}]}},
                  {"current": 0, "relation": "SummaryParent", "info": ["synthetic upper 2"],
                   "metadata": {"ref": {"node": "upper-2", "body": "summary-2"}, "ordinal": 48}}]
        if i < 2:
            items.append({**candidate, "memory": memory, "reader_text": f"synthetic reader text {i}", "reader_sources": [source],
                          "path": {"text": f"synthetic path {i}", "parts": [{"reference": ref, "coverage": True}]}})
        else:
            omitted[ref['node']] = 'CONTEXT_BUNDLE_TOO_LARGE' if i < 47 else 'jev_below_threshold'
        ident = f"{ref['node']}/{ref['body']}"
        targets.append({"id": ident, "memory": memory, "path_revision": "revision-2", "source_state": "Active",
                        "target_body": f"synthetic target {i}", "full_path": f"synthetic path {i}"})
        answers[ident] = {"outcome": outcome, "provider": {"choice": "useful" if i < 47 else "not_useful", "confidence": 0.7}}
    rows.append({"kind": "search", "query": questions[0]['query'], "limit": 48,
                 "sources": {f"native-{i}": f"event-{i}" for i in range(6)}, "result": {
                     "settings": {"representation": "LeafMemory", "jev_input_format": "LeafMemoryV1", "jev_threshold": 0.5}, "candidates": candidates, "items": items,
                     "omitted": omitted, "packed": {"memories": 2, "visible_ancestors": 1, "visible_bodies": 3, "visible_level0": 2},
                     "context_view": {"memories": [{"reference": refs[0], "source": {"id": "native-0"}, "path_revision": "revision-2",
                                       "layers": [{"reference": refs[0], "current": 0, "items": [{"child": refs[0], "span": {"start": 0, "end": 5}}]}]}],
                                      "visible": [], "tokens": 123, "rendered": "synthetic rendering"},
                     "jev_trace": [{"model": "fake-jev", "output_format": "ChoiceThreeClass", "targets": targets, "answers": answers}]}})
    return events, questions, rows


def tree_fixture():
    """Scoped generations plus independent Body hits; no memory layers."""
    events, questions, rows = leaf_memory_fixture()
    for row in rows:
        snapshot = row.get('snapshot') or {}
        if 'members' in snapshot:
            snapshot['memories'] = {}  # Body points have no per-node token measurement.
    raw = rows[-1]['result']
    raw['settings'].update(representation='Tree', vector_representation='Body', upper_k=0,
                           jev_input_format='NodeV1', jev_prompt_profile='node-reference-v1')
    candidates, items, nodes, targets, answers = [], [], [], [], {}
    for i, (node, body, level, source, refs) in enumerate([
            ('n0', 'b0', 0, 'native-0', ['native-0']),
            ('upper-1', 'summary-1', 1, 'native-1', ['native-1', 'native-2']),
            ('n32', 'b32', 0, 'native-4', [])]):
        ref = {'node': node, 'body': body}
        outcome = {'kind': 'reference', 'useful': .8 if i < 2 else .1,
                   'not_useful': .1 if i < 2 else .8, 'uncertain': .1}
        candidate = {'reference': ref, 'source': {'id': source}, 'level': level,
                     'similarity': .9 - i * .1, 'vector_rank': i + 1,
                     'origin': 'VectorHit', 'jev_outcome': outcome}
        candidates.append(candidate)
        text = f'synthetic node body {i}'
        recorded = {'start': 1793491200000000, 'end': 1793577600000000} if i == 1 else None
        if i < 2:
            items.append({**candidate, 'text': text, 'reader_text': text, 'reader_sources': refs,
                          'memory': None, 'recorded': recorded, 'rank_utility': candidate['similarity'],
                          'path': {'text': text, 'parts': [{'reference': ref, 'coverage': True}]}})
            nodes.append({'reference': ref, 'source': {'id': source}, 'level': level,
                          'recorded': recorded, 'leaf_range': {'start': i, 'end': i + len(refs)},
                          'sources': [{'id': r, 'version': 1} for r in refs]})
        ident = f'{node}/{body}'
        targets.append({'id': ident, 'target_body': text, 'full_path': text})
        answers[ident] = {'outcome': outcome, 'provider': {'choice': 'useful', 'confidence': .8}}
    raw.update(items=items, candidates=candidates, omitted={'n32': 'jev_below_threshold'},
               context_view={'nodes': nodes, 'tokens': 123, 'visible': [], 'rendered': 'synthetic node-context-v1'},
               jev_trace=[{'model': 'fake-jev', 'output_format': 'ChoiceThreeClass', 'targets': targets, 'answers': answers}])
    raw.pop('packed')
    return events, questions, rows


class LeafMemoryWorkTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix='memeval-leaf-review-')
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.log = self.root / 'logs/0001-ns/sut.stderr.log'
        put(self.log.parent / 'episode.json', {'namespace': 'ns'})
        self.events, self.questions, self.rows = leaf_memory_fixture()

    def extract(self):
        self.log.write_text(''.join('sut_trace ' + json.dumps(row) + '\n' for row in self.rows), encoding='utf-8')
        return review.WorkTraces(self.root).episode('ns', self.events, self.questions, {})[0]

    def test_generations_references_node_ownership_calls_and_determinism(self):
        with patch.object(review.WorkTraces, 'build', autospec=True, side_effect=review.WorkTraces.build) as build:
            payload = self.extract()
            self.assertEqual(build.call_count, 2)
        self.assertEqual((payload['version'], payload['representation']), (3, 'leaf_memory'))
        scope = payload['scopes']['scope-a']
        self.assertEqual(len(scope['snapshots']), 2)
        self.assertEqual([len(s['members']) for s in scope['snapshots']], [17, 50])
        self.assertEqual([(s['first_seq'], s['last_seq']) for s in scope['snapshots']], [(0, 1), (2, 5)])
        self.assertEqual(len(scope['nodes']), 50)
        self.assertEqual(scope['node_mismatches'], 0)
        self.assertEqual(scope['nodes']['n0']['ordinal_first'], 1)
        self.assertEqual(scope['nodes']['n0']['path_revision'], 'revision-2')
        self.assertEqual(scope['nodes']['n0']['origin']['source_id'], 'e0')
        self.assertEqual(scope['nodes']['n0']['origin']['source_span'], [0, 5])
        self.assertEqual(scope['snapshots'][1]['events'][-1]['source_id'], 'e5')
        self.assertEqual(scope['snapshots'][1]['members'][0]['edge']['parent'], 'upper-2')
        for i, row in enumerate(payload['builds']):
            self.assertEqual({n['node'] for n in row['nodes']}, {f'n{n}' for n in range(i * 8, (i + 1) * 8)})
            self.assertTrue(all(n['level'] == 0 for n in row['nodes']))
        self.assertEqual(scope['snapshots'][0]['calls'], {'n': 2, 'by_role': {'validation': 0, 'summary': 1, 'embedding': 1},
                         'failed_attempts': 1, 'wall_ms': 4000, 'started_at': 1000000, 'completed_at': 5000000})
        self.assertEqual(review.encode(payload), review.encode(self.extract()))

    def test_three_class_memories_answers_and_text_references(self):
        payload = self.extract()
        search = payload['searches']['q']
        self.assertEqual(search['packed'], self.rows[-1]['result']['packed'])
        self.assertEqual(search['candidates'][0]['jev'], {'useful': 0.8, 'not_useful': 0.1, 'uncertain': 0.1})
        item = search['items'][0]
        self.assertEqual(item['reader_sources_n'], 1)
        self.assertEqual(payload['texts'][item['reader_text_hash']], 'synthetic reader text 0')
        self.assertEqual(item['memory'][0]['origins'][0]['source_id'], 'e0')
        self.assertIsNone(item['memory'][0]['relation'])
        self.assertIsNone(item['memory'][1]['origins'])
        for layer in item['memory']:
            self.assertTrue(all(key in payload['texts'] for key in layer['info_hashes']))
        view = search['context_view']['memories'][0]
        self.assertEqual(view['source_id'], 'e0')
        self.assertEqual(view['layers'][0]['items'], [{'child': {'node': 'n0', 'body': 'b0'}, 'span': [0, 5]}])
        batch = search['jev_trace'][0]
        self.assertEqual(batch['output_format'], 'ChoiceThreeClass')
        target = batch['targets'][0]
        self.assertEqual(target['jev'], item['jev'])
        self.assertEqual(target['provider'], {'choice': 'useful', 'confidence': 0.7})
        self.assertEqual(target['path_revision'], 'revision-2')
        self.assertIsNone(target['probability'])  # Never pretend useful is binary p_use.
        self.assertEqual(payload['mapping_mismatches'], 0)
        references = set()
        def walk(value):
            if isinstance(value, dict):
                for key, child in value.items():
                    if key in ('text_hash', 'output_hash', 'reason_hash', 'reader_text_hash', 'rendered_hash', 'target_text_hash', 'path_text_hash') and child:
                        references.add(child)
                    elif key == 'info_hashes':
                        references.update(child)
                    walk(child)
            elif isinstance(value, list):
                for child in value:
                    walk(child)
        walk({k: v for k, v in payload.items() if k != 'texts'})
        self.assertEqual(references, set(payload['texts']))

    def test_mutated_node_count_keeps_latest_and_exact_answer_keys(self):
        member = self.rows[3]['snapshot']['members'][0]
        member['body']['text'] = 'synthetic changed body'
        batch = self.rows[-1]['result']['jev_trace'][0]
        batch['answers']['n0/wrong-body'] = batch['answers'].pop('n0/b0')
        payload = self.extract()
        scope = payload['scopes']['scope-a']
        self.assertEqual(scope['node_mismatches'], 1)
        self.assertEqual(payload['texts'][scope['nodes']['n0']['text_hash']], 'synthetic changed body')
        target = payload['searches']['q']['jev_trace'][0]['targets'][0]
        self.assertIsNone(target['jev'])
        self.assertIsNone(target['provider'])
        self.assertEqual(payload['mapping_mismatches'], 1)

    def test_missing_diagnostics_and_source_hash_mismatch_remain_unknown(self):
        self.rows[1]['text_sha256'] = 'not-the-event-hash'
        self.rows[3]['audit']['operation']['error'] = {'kind': 'synthetic', 'message': 'failed check'}
        payload = self.extract()
        self.assertEqual(payload['mapping_mismatches'], 1)
        scope = payload['scopes']['scope-a']
        self.assertIsNone(scope['nodes']['n0']['origin']['source_id'])
        self.assertEqual(scope['snapshots'][1]['error'], {'kind': 'synthetic', 'message': 'failed check'})
        self.rows[-1]['result'] = None
        self.rows[-1]['error'] = {'code': 'synthetic-error'}
        put(self.root / 'report.json', {'sut': {'variant': 'native-leaf-memory-test'}})
        search = self.extract()['searches']['q']
        self.assertIsNone(search['packed'])
        self.assertEqual(search['error'], {'code': 'synthetic-error'})

    def test_leaf_requires_explicit_search_pair_or_report_variant(self):
        settings = self.rows[-1]['result']['settings']
        for representation, input_format in [('FullPath', 'AncestorContextV1'),
                                              ('LeafMemory', 'AncestorContextV1'),
                                              ('FullPath', 'LeafMemoryV1')]:
            with self.subTest(representation=representation, input_format=input_format):
                settings.update(representation=representation, jev_input_format=input_format)
                self.rows[1]['wait_windows'] = [{'method': 'add_batch', 'window_s': 10}]
                payload = self.extract()
                self.assertNotIn('version', payload)  # BEAM adds its legacy v2 envelope.
                self.assertNotIn('representation', payload)
                self.assertNotIn('scopes', payload)
                self.assertNotIn('packed', payload['searches']['q'])
                self.assertIn('calls', payload['builds'][0])
        put(self.root / 'report.json', {'sut': {'variant': 'native-leaf-memory-test'}})
        self.rows[-1]['result'] = None
        self.rows[-1]['error'] = {'code': 'synthetic-error'}
        self.assertEqual(self.extract()['version'], 3)

    def test_leaf_builds_compact_repeated_states_and_snapshot_diagnostics(self):
        baseline = self.extract()
        first = copy.deepcopy(self.rows[1])
        first.update(state='Failed', snapshot=None, resume_count=0,
                     audit={'summaries': [], 'calls': []})
        self.rows.insert(1, first)
        self.rows[2]['resume_count'] = 1
        audit = self.rows[2]['audit']
        audit['summaries'] = [{'body': 'summary-1', 'output': {'text': 'diagnostic summary'},
                              'accepted': False, 'verdict': {'faithful': False, 'reason': 'diagnostic reason'}}]
        self.rows[2]['snapshot']['pending'] = {'1': [{'body': 'summary-1', 'text': 'pending text'}]}
        payload = self.extract()
        self.assertEqual(len(payload['builds']), len(self.events))
        expected = {'seq', 'event_id', 'source_id', 'state', 'operation_id', 'snapshot_id',
                    'nodes', 'audit_error', 'resume_count', 'attempts'}
        self.assertTrue(all(set(b) == expected for b in payload['builds']))
        build = payload['builds'][0]
        self.assertEqual(build['state'], 'Ready')
        self.assertEqual(build['attempts'], ['Failed', 'Ready'])
        self.assertEqual(build['resume_count'], 1)
        self.assertEqual(payload['builds'][1]['attempts'], ['Ready'])
        snapshot = payload['scopes']['scope-a']['snapshots'][0]
        self.assertEqual(len(snapshot['summaries']), 1)
        self.assertEqual(len(snapshot['call_details']), 2)
        self.assertEqual(payload['texts'][snapshot['summaries'][0]['output_hash']], 'diagnostic summary')
        self.assertEqual(payload['texts'][snapshot['pending']['1'][0]['output_hash']], 'pending text')
        self.assertEqual(payload['searches'], baseline['searches'])
        self.assertEqual(payload['events'], baseline['events'])
        self.assertEqual(payload['log']['rows']['build'], len(self.events) + 1)

    def test_case_api_sees_only_generations_before_search(self):
        early = copy.deepcopy(self.rows[-1])
        early['query'] = 'early lookup'
        self.rows.insert(3, early)
        self.extract()
        work = review.WorkTraces(self.root).case({'namespace': 'ns'}, ['e0', 'e1'],
                  {e['source_id']: e for e in self.events}, 'early lookup', {})
        self.assertEqual(len(work['scopes']['scope-a']['snapshots']), 1)
        self.assertEqual(len(work['scopes']['scope-a']['nodes']), 17)
        self.assertEqual(len(work['build']), 2)

    def test_leaf_optional_diagnostics_are_null_not_fabricated(self):
        for row in self.rows:
            if isinstance(row.get('audit'), dict) and 'calls' in row['audit']:
                row['audit'].pop('calls')
        raw = self.rows[-1]['result']
        raw.pop('packed')
        raw['items'][0].pop('memory')
        raw['items'][0].pop('reader_text')
        raw['items'][0].pop('reader_sources')
        raw['candidates'][0]['jev_outcome'] = {'kind': 'binary', 'p_use': 0.9}
        raw['context_view'].pop('memories')
        raw['jev_trace'][0].pop('answers')
        payload = self.extract()
        self.assertTrue(all(s['calls'] is None for s in payload['scopes']['scope-a']['snapshots']))
        search = payload['searches']['q']
        self.assertIsNone(search['packed'])
        self.assertIsNone(search['candidates'][0]['jev'])
        self.assertIsNone(search['context_view']['memories'])
        self.assertIsNone(search['jev_trace'][0]['targets'][0]['jev'])
        for key in ('memory', 'reader_text_hash', 'reader_sources_n'):
            self.assertIsNone(search['items'][0][key])


class TreeWorkTests(unittest.TestCase):
    extract = LeafMemoryWorkTests.extract

    def setUp(self):
        LeafMemoryWorkTests.setUp(self)
        self.events, self.questions, self.rows = tree_fixture()

    def test_tree_scoped_builds_items_nodes_and_three_class_answers(self):
        payload = self.extract()
        self.assertEqual((payload['version'], payload['representation']), (3, 'tree'))
        self.assertEqual(len(payload['scopes']['scope-a']['snapshots']), 2)
        self.assertEqual(len(payload['builds']), 6)
        self.assertEqual(payload['mapping_mismatches'], 0)
        search = payload['searches']['q']
        self.assertNotIn('packed', search)
        self.assertNotIn('memories', search['context_view'])
        self.assertEqual([item['sources'] for item in search['items']], [['e0'], ['e1', 'e2']])
        self.assertEqual([item['reader_sources_n'] for item in search['items']], [1, 2])
        for index, item in enumerate(search['items']):
            self.assertNotIn('memory', item)
            self.assertEqual(item['level'], index)
            self.assertEqual(payload['texts'][item['reader_text_hash']], f'synthetic node body {index}')
            self.assertEqual(item['jev'], {'useful': .8, 'not_useful': .1, 'uncertain': .1})
        self.assertIsNone(search['items'][0]['recorded'])
        self.assertEqual(search['items'][1]['recorded'], [1793491200000000, 1793577600000000])
        self.assertEqual(search['context_view']['nodes'][1], {
            'node': 'upper-1', 'body': 'summary-1', 'source_id': 'e1', 'level': 1,
            'leaf_range': [1, 3], 'sources_n': 2, 'recorded': [1793491200000000, 1793577600000000]})
        self.assertEqual(search['candidates'][2]['jev']['useful'], .1)
        self.assertEqual(search['jev_trace'][0]['targets'][1]['jev']['useful'], .8)
        self.assertEqual(review.WorkTraces.referenced_texts(
            {k: v for k, v in payload.items() if k != 'texts'}, payload['texts']), payload['texts'])
        self.assertEqual(review.encode(payload), review.encode(self.extract()))

    def test_tree_alignment_mismatch_and_unknown_source_are_explicit(self):
        raw = self.rows[-1]['result']
        raw['context_view']['nodes'].pop()
        raw['items'][1]['reader_sources'].append('native-unknown')
        payload = self.extract()
        self.assertEqual(payload['mapping_mismatches'], 1)
        self.assertEqual(payload['searches']['q']['items'][1]['sources'], ['e1', 'e2', None])
        self.log.write_text(''.join('sut_trace ' + json.dumps(row) + '\n' for row in self.rows))
        trace = review.WorkTraces(self.root)
        trace.case({'namespace': 'ns'}, [e['source_id'] for e in self.events],
                   {e['source_id']: e for e in self.events}, self.questions[0]['query'], {})
        self.assertIn(('tree_nodes', 'ns', len(self.rows) - 1), trace.mismatches)

    def test_tree_variant_fallback_and_conditional_packed(self):
        raw = self.rows[-1]['result']
        raw['packed'] = {'memories': 0, 'visible_level0': 1, 'visible_ancestors': 1}
        self.assertEqual(self.extract()['searches']['q']['packed'], raw['packed'])
        put(self.root / 'report.json', {'sut': {'variant': 'native-tree-jev3-openai-embedding'}})
        self.rows[-1].update(result=None, error={'kind': 'MODEL_UNAVAILABLE'})
        payload = self.extract()
        self.assertEqual(payload['representation'], 'tree')
        self.assertIn('scopes', payload)
        self.assertEqual(payload['searches']['q']['error'], {'kind': 'MODEL_UNAVAILABLE'})
        self.assertNotIn('packed', payload['searches']['q'])

    def test_tree_search_overrides_stale_variant_and_mixed_modes_count_once(self):
        put(self.root / 'report.json', {'sut': {'variant': 'native-leaf-memory-stale'}})
        payload = self.extract()
        self.assertEqual(payload['representation'], 'tree')
        self.assertEqual(payload['mapping_mismatches'], 1)
        failed = {'kind': 'search', 'query': 'failed tree lookup', 'error': {'kind': 'MODEL_UNAVAILABLE'}}
        self.rows.append(failed)
        self.questions.append({'id': 'failed', 'query': failed['query']})
        payload = self.extract()
        self.assertNotIn('packed', payload['searches']['failed'])
        self.rows.pop()
        self.questions.pop()
        leaf_search = leaf_memory_fixture()[2][-1]
        leaf_search['query'] = 'another synthetic lookup'
        self.rows.append(leaf_search)
        self.questions.append({'id': 'q2', 'query': leaf_search['query']})
        payload = self.extract()
        self.assertEqual(payload['representation'], 'leaf_memory')
        self.assertEqual(payload['mapping_mismatches'], 1)
        self.assertIn('sources', payload['searches']['q']['items'][0])
        self.assertNotIn('memory', payload['searches']['q']['items'][0])
        self.assertIn('memory', payload['searches']['q2']['items'][0])

    def test_tree_case_api_uses_only_available_generation(self):
        early = copy.deepcopy(self.rows[-1])
        early['query'] = 'early tree search'
        self.rows.insert(3, early)
        self.extract()
        work = review.WorkTraces(self.root).case({'namespace': 'ns'}, ['e0', 'e1'],
            {e['source_id']: e for e in self.events}, early['query'], {})
        self.assertEqual(work['representation'], 'tree')
        self.assertEqual(len(work['build']), 2)
        self.assertEqual(len(work['scopes']['scope-a']['snapshots']), 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
