#!/usr/bin/env python3
"""Offline BEAM contracts: synthetic reports only; all writes are temporary."""

from collections import Counter
import contextlib
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import build_beam_review as beam
import build_case_review as common
import cogletnet_failures as attribution


SCRIPT = Path(__file__).with_name("build_beam_review.py")


def put(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False), encoding="utf-8")


def put_lines(path, records):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in records), encoding="utf-8")


def counts(report):
    report["summary"]["status_counts"] = dict(Counter(c["status"] for c in report["cases"]))


def without_reader_prompts(files):
    """Strip only the additive prompt contract for prior byte baselines."""
    files = copy.deepcopy(files)
    for payload in files.values():
        for ident, search in list(payload['searches'].items()):
            search.pop('reader_prompt', None)
            if not search:
                del payload['searches'][ident]
    return files


def leaf_item(sequence=0, info='leaf fragment', sources=('uuid-0', 'uuid-1')):
    memory = [{'info': [info], 'current': None,
               'metadata': {'origins': [{'event_sequence': sequence}]}},
              {'info': ['ancestor summary'], 'current': 0,
               'metadata': {'origins': [{'event_sequence': 1}]}}]
    return {'text': '认元上下文格式：leaf-memory-context-v1。\n' + json.dumps({'memories': [memory]}, ensure_ascii=False),
            'source_ids': list(sources)}


class DirectSourcesTests(unittest.TestCase):
    def test_non_tree_multi_source_items_follow_runner_first_occurrence_order(self):
        case = {'response': {'items': [
            {'text': 'own upper node text', 'source_ids': ['uuid-c', 'uuid-a', 'uuid-b']},
            {'text': 'own leaf text', 'source_ids': ['uuid-b', 'uuid-d', 'uuid-a']} ]},
            'retrieved_source_ids': ['1:2', '1:0', '1:1', '1:3']}
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            direct = beam.direct_source_ids(case, '1', {})
        self.assertEqual(direct, ['1:2', '1:0', '1:1', '1:3'])
        self.assertEqual(stderr.getvalue(), '')

    def test_tree_sources_match_nodes_not_item_order_and_exclude_upper_closure(self):
        case = {'case_id': '1:q', 'response': {'items': [
            {'id': 'upper', 'source_ids': ['a', 'b']},
            {'id': 'leaf', 'source_ids': ['b']},
            {'id': 'upper-single', 'source_ids': ['c']}]},
            'retrieved_source_ids': ['1:0', '1:1', '1:2']}
        search = {'settings': {'representation': 'Tree'}, 'items': [
            {'node': 'leaf', 'level': 0, 'sources': ['1:1']},
            {'node': 'upper-single', 'level': 2, 'sources': ['1:2']},
            {'node': 'upper', 'level': 1, 'sources': ['1:0', '1:1']}]}
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(beam.direct_source_ids(case, '1', {}, tree=True, search=search), ['1:1'])
        self.assertEqual(stderr.getvalue(), '')
        # Repeated returned leaves still count their one source only once.
        case['response']['items'].append(case['response']['items'][1])
        self.assertEqual(beam.direct_source_ids(case, '1', {}, tree=True, search=search), ['1:1'])

    def test_tree_missing_or_bad_trace_falls_back_to_single_source_items(self):
        case = {'case_id': '1:q', 'response': {'items': [
            {'id': 'leaf', 'source_ids': ['a']}, {'id': 'upper', 'source_ids': ['a', 'b']}]},
            'retrieved_source_ids': ['1:0', '1:1']}
        for search in (None, {}, {'items': []}, {'items': [
                {'node': 'leaf', 'level': 0, 'sources': ['unknown']},
                {'node': 'upper', 'level': True, 'sources': ['1:0', '1:1']}]}):
            with self.subTest(search=search), contextlib.redirect_stderr(io.StringIO()) as stderr:
                self.assertEqual(beam.direct_source_ids(case, '1', {}, tree=True, search=search), ['1:0'])
            self.assertIn('1:q', stderr.getvalue())
            self.assertIn('single-source', stderr.getvalue())
        # Unmappable/missing responses cannot safely fall back to the closure.
        for response in (None, {'items': [{'id': 'leaf', 'source_ids': ['a']}]}, {'items': [None]}):
            with self.subTest(response=response), contextlib.redirect_stderr(io.StringIO()) as stderr:
                self.assertEqual(beam.direct_source_ids({**case, 'response': response}, '1', {}, tree=True), [])
            self.assertTrue(stderr.getvalue())

    def test_leaf_sources_use_only_l0_origins_validated_against_event_text(self):
        item = leaf_item(info='leaf\tfragment')
        case = {'case_id': '1:q', 'response': {'items': [item, item]}, 'retrieved_source_ids': ['1:0', '1:1']}
        self.assertEqual(beam.direct_source_ids(case, '1', {'1:0': 'prefix leaf\n fragment suffix', '1:1': 'ancestor summary'}), ['1:0'])

    def test_multiple_memories_chunks_and_origins_preserve_first_seen_order(self):
        item = leaf_item(2, 'second chunk')
        memory = json.loads(item['text'].split('\n', 1)[1])['memories'][0]
        other = copy.deepcopy(memory)
        other[0]['info'] = ['first chunk']
        other[0]['metadata']['origins'] = [{'event_sequence': 0}, {'event_sequence': 2}]
        item['text'] = beam.LEAF_MEMORY_HEADER + '\n' + json.dumps({'memories': [memory, other, memory]}) + '\nrenderer suffix'
        case = {'response': {'items': [item]}, 'retrieved_source_ids': ['1:0', '1:1', '1:2']}
        self.assertEqual(beam.direct_source_ids(case, '1', {
            '1:0': 'first chunk', '1:2': 'first chunk\nsecond chunk'}), ['1:2', '1:0'])

    def test_leaf_mismatch_and_malformed_body_never_fall_back_to_closure(self):
        cases = [leaf_item(info='wrong fragment'), leaf_item(sequence=8), leaf_item(info=''),
                 leaf_item(sequence=True),
                 {'text': '认元上下文格式：leaf-memory-context-v1。\n{"memories":broken', 'source_ids': ['uuid-0']}]
        for memories in ([], [[]], [[{'info': 'leaf fragment', 'metadata': {'origins': [{'event_sequence': 0}]}}]]):
            cases.append({'text': beam.LEAF_MEMORY_HEADER + json.dumps({'memories': memories}), 'source_ids': ['uuid-0']})
        for item in cases:
            with self.subTest(item=item), contextlib.redirect_stderr(io.StringIO()) as stderr:
                case = {'case_id': '1:q', 'response': {'items': [item]}, 'retrieved_source_ids': ['1:0', '1:1']}
                self.assertEqual(beam.direct_source_ids(case, '1', {'1:0': 'leaf fragment'}), [])
                self.assertIn('1:q', stderr.getvalue())

    def test_uuid_mapping_order_dedup_and_mixed_leaf_items(self):
        case = {'response': {'items': [leaf_item(), {'text': 'ordinary', 'source_ids': ['uuid-2', 'uuid-0']}]},
                'retrieved_source_ids': ['1:0', '1:1', '1:2']}
        self.assertEqual(beam.direct_source_ids(case, '1', {'1:0': 'leaf fragment'}), ['1:0', '1:2'])
        case['response']['items'] = [{'text': 'ordinary', 'source_ids': ['uuid-2', 'uuid-2']},
                                     {'text': 'ordinary', 'source_ids': ['uuid-0', 'uuid-2']}]
        case['retrieved_source_ids'] = ['1:2', '1:0']
        self.assertEqual(beam.direct_source_ids(case, '1', {}), ['1:2', '1:0'])

    def test_missing_response_falls_back_and_bad_uuid_mapping_warns(self):
        for response in (None, {}, {'items': None}):
            with self.subTest(response=response), contextlib.redirect_stderr(io.StringIO()) as stderr:
                case = {'response': response, 'retrieved_source_ids': ['1:1', '1:0', '1:1']}
                self.assertEqual(beam.direct_source_ids(case, '1', {}), ['1:1', '1:0'])
                self.assertEqual(stderr.getvalue(), '')
        for refs in (['uuid-0'], [None, 'uuid-1'], 'uuid-0', 0):
            with self.subTest(refs=refs), contextlib.redirect_stderr(io.StringIO()) as stderr:
                case = {'case_id': '1:q', 'response': {'items': [{'text': 'ordinary', 'source_ids': refs}]},
                        'retrieved_source_ids': ['1:1', '1:0']}
                self.assertEqual(beam.direct_source_ids(case, '1', {}), ['1:1', '1:0'])
                self.assertIn('fallback', stderr.getvalue())
        for items in ({'text': 'wrong container'}, [None]):
            with self.subTest(items=items), contextlib.redirect_stderr(io.StringIO()) as stderr:
                self.assertEqual(beam.direct_source_ids({'response': {'items': items}, 'retrieved_source_ids': ['1:0']}, '1', {}), ['1:0'])
                self.assertIn('fallback', stderr.getvalue())


class BeamReviewTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="memeval-beam-review-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.suite = self.root / "suite"
        self.a, self.b = self.root / "cogletnet", self.root / "baseline"
        suite = {"suite_id": "beam-fixture", "provenance": {"source_url": "https://example.invalid/beam", "revision": "fixture"},
                 "license": {"id": "fixture", "share_alike": True, "redistribute_normalized": True}, "limitations": ["fixture only"]}
        put(self.suite / "suite.json", suite)
        self.episodes, self.index, cases = [], [], []
        for i in reversed(range(1, 21)):
            episode = {"id": str(i), "cluster_id": str(i), "events": [], "cases": []}
            for j in reversed(range(20)):
                ident, category = f"{i}:q{j}", f"category-{j % 10}"
                episode["cases"].append({"id": ident, "category": category, "kind": "rubric", "query": "问 </script>\u2028",
                                         "question_type": category, "difficulty": "medium", "cutoff": 0,
                                         "query_point": "end_of_history", "gold": {"evidence_ids": [],
                                         "rubric": {"aggregate": "mean", "items": [{"text": "判断", "polarity": "as_written", "weight": 1}]}}})
                cases.append({"case_id": ident, "episode_id": str(i), "repeat_index": 0, "category": category,
                              "namespace": f"ns-{i}", "status": "scored", "metrics": {beam.SCORE: 0.5},
                              "qa": {"hypothesis": "回答", "judge_label": False, "reader_abstain": False, "truncated": False,
                                     "reader_output": {"finish_reason": "stop", "completion_tokens": 3},
                                     "judge_details": [{"outcome": {"expectation": 0.7, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.4, "2": 0.5}, "score": 1}}]},
                              "response": {"items": [{"text": "原文"}]}, "returned_chars": 2,
                              "timings": {"ingest_ms": 1.25, "query_ms": 2.5, "answer_ms": 3.75}})
            self.episodes.append(episode)
            self.index.append({"id": str(i), "cluster_id": str(i), "events": 0, "cases": 20, "bytes": 123, "sha256": f"sha-{i}"})
        self.save_suite()
        self.report = {"suite_id": "beam-fixture", "suite_sha256": "fixture-sha", "run_id": "r1", "run_seed": "seed",
                       "framework_version": "fixture", "started_at_utc": "2026-10-24T00:00:00Z", "wall_seconds": 1.25,
                       "sut": {"name": "cogletnet", "variant": "fixture"}, "resume_count": 0,
                       "protocol": {"benchmark": {"protocol_id": "beam", "version": "1", "variant": "100K", "sha256": "protocol-sha"},
                                    "judge": {"provider": "typesafe", "model": "fixture", "cutoff": 0.5}, "reader_input": "items"},
                       "reader": {"model": "fixture-reader", "max_tokens": 2048, "input_mode": "items"},
                       "summary": {"metrics": {beam.SCORE: {"mean": 0.5, "n": 400}, beam.ORDER: {"mean": None, "n": 0}}},
                       "by_category": {f"category-{j}": {"status_counts": {"scored": 40}, "metrics": {beam.SCORE: {"mean": 0.5, "n": 40}}} for j in range(10)},
                       "billing": {"sut": None, "evaluator": {"calls": 400, "prompt_tokens": 123}},
                       "isolation": {"backend": "fixture", "network_mode": "none", "container": {"residue": {"run_clean": True, "containers_remaining": 0}}},
                       "cases": cases}
        counts(self.report)
        self.other = copy.deepcopy(self.report)
        self.other.update(run_id="r2", sut={"name": "bm25"}, started_at_utc="2026-10-25T00:00:00Z")
        self.save()
        self.glossary = self.root / "glossary.json"
        put(self.glossary, {"glossary": [{"id": beam.SCORE, "formula": "fixture"}, {"id": "recall"}, {"id": beam.ORDER}, {"id": "beam.unused"}]})

    def save_suite(self):
        put_lines(self.suite / "episodes.jsonl", self.episodes)
        put_lines(self.suite / "episodes.index.jsonl", self.index)

    def save(self):
        put(self.a / "report.json", self.report)
        put(self.b / "report.json", self.other)

    def build(self, **kwargs):
        return beam.build_review(self.suite, [("cog", self.a), ("bm25", self.b)], kwargs.get("glossary", self.glossary))

    def cli(self, *extra):
        return subprocess.run([sys.executable, "-B", str(SCRIPT), "--suite", str(self.suite),
                               "--run", f"cog={self.a}", "--run", f"bm25={self.b}", "--glossary", str(self.glossary),
                               "--out", str(self.root / "out"), *map(str, extra)], capture_output=True, text=True)

    def test_embedding_declaration_complete_dict(self):
        embedding = {'provider': 'OpenAI direct', 'model': 'text-embedding-3-large',
                     'dimensions': 3072, 'local': False}
        self.report['sut']['declared'] = {'embedding': {**embedding, 'extra': 'ignored', 'declared': 'ignored'}}
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            value = self.build()
        self.assertEqual(value['version'], 1)
        self.assertEqual(value['runs'][0]['sut'], {'name': 'cogletnet', 'variant': 'fixture', 'network': 'none',
                                                  'embedding': {**embedding, 'declared': None}})
        self.assertEqual(stderr.getvalue(), '')

    def test_embedding_declaration_invalid_field_types(self):
        for dimensions in (True, False, '3072', 3072.0, None):
            with self.subTest(dimensions=dimensions):
                self.report['sut']['declared'] = {'embedding': {
                    'provider': 3, 'model': ['model'], 'dimensions': dimensions, 'local': 'false'}}
                self.save()
                with contextlib.redirect_stderr(io.StringIO()) as stderr:
                    value = self.build()
                self.assertEqual(value['runs'][0]['sut']['embedding'],
                                 dict.fromkeys(('provider', 'model', 'dimensions', 'local', 'declared')))
                self.assertEqual(len(stderr.getvalue().splitlines()), 4)
                for field in ('provider', 'model', 'dimensions', 'local'):
                    self.assertIn(f'sut.declared.embedding.{field}', stderr.getvalue())
        # Valid fields survive, while absent optional keys remain unknown.
        self.report['sut']['declared'] = {'embedding': {'provider': 'fastembed', 'model': 'BAAI/bge-small-en-v1.5', 'local': True}}
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            value = self.build()
        self.assertEqual(value['runs'][0]['sut']['embedding'], {
            'provider': 'fastembed', 'model': 'BAAI/bge-small-en-v1.5', 'dimensions': None, 'local': True, 'declared': None})
        self.assertEqual(stderr.getvalue(), '')

    def test_embedding_declaration_string_is_not_parsed(self):
        self.report['sut']['declared'] = {'embedding': ' \nBAAI/bge-small-en-v1.5; local\t '}
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            value = self.build()
        self.assertEqual(value['runs'][0]['sut']['embedding'], {
            'provider': None, 'model': None, 'dimensions': None, 'local': None,
            'declared': 'BAAI/bge-small-en-v1.5; local'})
        self.assertEqual(stderr.getvalue(), '')

    def test_embedding_declaration_missing_empty_and_other_shapes(self):
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertTrue(all(run['sut']['embedding'] is None for run in self.build()['runs']))
        self.assertEqual(stderr.getvalue(), '')
        for declared in (None, {}, {'embedding': None}, {'embedding': ''}, {'embedding': ' \n\t'}):
            with self.subTest(declared=declared):
                self.report['sut']['declared'] = declared
                self.save()
                with contextlib.redirect_stderr(io.StringIO()) as stderr:
                    self.assertIsNone(self.build()['runs'][0]['sut']['embedding'])
                self.assertEqual(stderr.getvalue(), '')
        for embedding in ({}, [], 0, False, 1.5):
            with self.subTest(embedding=embedding):
                self.report['sut']['declared'] = {'embedding': embedding}
                self.save()
                with contextlib.redirect_stderr(io.StringIO()) as stderr:
                    value = self.build()['runs'][0]['sut']['embedding']
                if isinstance(embedding, dict):
                    self.assertEqual(value, dict.fromkeys(('provider', 'model', 'dimensions', 'local', 'declared')))
                    self.assertEqual(stderr.getvalue(), '')
                else:
                    self.assertIsNone(value)
                    self.assertEqual(len(stderr.getvalue().splitlines()), 1)
                    self.assertIn('sut.declared.embedding', stderr.getvalue())

    def test_400_questions_order_dedup_repeats_and_report_totals(self):
        self.episodes[0]["cases"].append(copy.deepcopy(self.episodes[0]["cases"][0]))
        self.save_suite()
        repeat = copy.deepcopy(self.report["cases"][0])
        repeat.update(repeat_index=1, status="error", error_type="rpc_timeout")
        repeat["metrics"][beam.SCORE] = 1
        self.report["cases"].append(repeat)
        counts(self.report)
        self.save()
        value = self.build()
        questions = value["questions"]
        self.assertEqual(len(questions), 400)
        self.assertEqual([q["episode_id"] for q in questions], [str(i) for i in range(1, 21) for _ in range(20)])
        for start in range(0, 400, 20):
            ids = [q["id"] for q in questions[start:start+20]]
            self.assertEqual(ids, sorted(ids))
        self.assertEqual(len({q["id"] for q in questions}), 400)
        for run in value["runs"]:
            self.assertEqual(len(value["results"][run["name"]]), 400)
        cog = value["runs"][0]
        self.assertEqual(cog["status_counts"], {"scored": 400, "error": 1})
        self.assertEqual(cog["metrics"][beam.SCORE], self.report["summary"]["metrics"][beam.SCORE])
        self.assertEqual(cog["by_episode"]["20"], {"status_counts": {"scored": 20, "error": 1}, "mean_score": 0.5,
                                                "metrics": {key: {"mean": None, "n": None} for key in beam.EVIDENCE_METRICS}})
        self.assertEqual(value["results"]["cog"][repeat["case_id"]]["status"], "scored")
        self.assertEqual(value["dataset"]["categories"], [{"id": f"category-{i}", "n": 40} for i in range(10)])
        self.assertEqual([x["id"] for x in value["glossary"]], sorted([*beam.EVIDENCE_METRICS, beam.ORDER, beam.SCORE]))

    def test_evidence_recall_cells_and_scored_aggregates(self):
        questions = {q['id']: q for ep in self.episodes for q in ep['cases']}
        cases = {c['case_id']: c for c in self.report['cases']}
        for j in (0, 10, 1, 3, 4, 5, 6):
            questions[f'20:q{j}']['gold']['evidence_ids'] = ['20:e0', '20:e1']
        for j, cutoff in ((0, 6), (10, 4), (1, 2), (6, 4)):
            questions[f'20:q{j}']['cutoff'] = cutoff
        cases['20:q0']['retrieved_source_ids'] = ['20:e0', '20:e0', '20:e1', '20:other']
        cases['20:q10']['retrieved_source_ids'] = ['20:e1', '20:e1']
        cases['20:q1']['retrieved_source_ids'] = ['20:other']
        cases['20:q2']['retrieved_source_ids'] = ['20:e0']  # No gold, never a denominator.
        cases['20:q6']['retrieved_source_ids'] = None  # Nil retrieval list is empty.
        for j, status in ((3, 'error'), (4, 'invalid')):
            cases[f'20:q{j}'].update(status=status, retrieved_source_ids=['20:e0', '20:e1'])
        self.report['cases'].remove(cases['20:q5'])  # Unexecuted is unknown, not zero.
        # Derived data must ignore report metric copies, item text and reader truncation.
        for metric in beam.EVIDENCE_METRICS:
            cases['20:q0']['metrics'][metric] = 0
            self.report['summary']['metrics'][metric] = {'mean': 999, 'n': 999}
            self.report['by_category']['category-0']['metrics'][metric] = {'mean': 999, 'n': 999}
        cases['20:q0']['qa']['truncated'] = True
        cases['20:q0']['response']['items'] = []
        self.save_suite()
        counts(self.report)
        self.save()
        with contextlib.redirect_stderr(io.StringIO()):
            value = self.build()
        cells = value['results']['cog']
        expected = {0: (2, 1.0), 10: (1, 0.5), 1: (0, 0.0), 2: (None, None),
                    3: (2, 1.0), 4: (2, 1.0), 5: (None, None), 6: (0, 0.0)}
        for j, pair in expected.items():
            cell = cells[f'20:q{j}']
            self.assertEqual((cell['evidence_hits'], cell['evidence_recall']), pair, j)
            self.assertEqual((cell['cited_hits'], cell['citation_coverage']), pair, j)
            self.assertNotIn('retrieved_source_ids', cell)
        for j, (retrieved, precision, f1) in {
                0: (3, 2/3, 0.8), 10: (1, 1.0, 2/3), 1: (1, 0.0, 0.0),
                2: (1, None, None), 3: (2, 1.0, 1.0), 4: (2, 1.0, 1.0),
                5: (None, None, None), 6: (0, None, None)}.items():
            cell = cells[f'20:q{j}']
            self.assertEqual(cell['retrieved_ids'], retrieved, j)
            self.assertEqual(cell['cited_ids'], retrieved, j)
            for field, expected_value in (('evidence_precision', precision), ('evidence_f1', f1)):
                if expected_value is None:
                    self.assertIsNone(cell[field], (j, field))
                else:
                    self.assertAlmostEqual(cell[field], expected_value, msg=(j, field))
        run = value['runs'][0]
        self.assertEqual(run['metrics'][beam.EVIDENCE], {'mean': 0.375, 'n': 4})
        self.assertEqual(run['by_episode']['20']['metrics'][beam.EVIDENCE], {'mean': 0.375, 'n': 4})
        self.assertEqual(run['by_episode']['1']['metrics'][beam.EVIDENCE], {'mean': None, 'n': None})
        self.assertEqual(run['by_category']['category-0']['metrics'][beam.EVIDENCE], {'mean': 0.75, 'n': 2})
        self.assertEqual(run['by_category']['category-1']['metrics'][beam.EVIDENCE], {'mean': 0.0, 'n': 1})
        self.assertEqual(run['by_category']['category-3']['metrics'][beam.EVIDENCE], {'mean': None, 'n': None})
        aggregates = {
            'beam.source_ids_per_question': ((1.25, 4), (2.0, 2), (1.0, 1)),
            'beam.source_ids_episode_share': ((0.3125, 4), (0.375, 2), (0.5, 1)),
            'beam.evidence_precision': ((5/9, 3), (5/6, 2), (0.0, 1)),
            'beam.evidence_f1': ((22/45, 3), (11/15, 2), (0.0, 1)),
            'beam.citation_coverage': ((0.375, 4), (0.75, 2), (0.0, 1)),
        }
        for metric, (total, category0, category1) in aggregates.items():
            for metrics, (mean, n) in (
                    (run['metrics'], total), (run['by_episode']['20']['metrics'], total),
                    (run['by_category']['category-0']['metrics'], category0),
                    (run['by_category']['category-1']['metrics'], category1)):
                self.assertAlmostEqual(metrics[metric]['mean'], mean, msg=metric)
                self.assertEqual(metrics[metric]['n'], n, metric)
            for metrics in (run['by_episode']['1']['metrics'], run['by_category']['category-3']['metrics']):
                self.assertEqual(metrics[metric], {'mean': None, 'n': None}, metric)

    def test_evidence_recall_no_scored_evidence_is_null(self):
        # Even when error/invalid cases have evidence and perfect references,
        # all aggregates remain undefined if scored questions have no gold.
        for j, status in ((0, 'error'), (1, 'invalid')):
            self.episodes[0]['cases'][j]['gold']['evidence_ids'] = ['20:e0']
            self.report['cases'][j].update(status=status, retrieved_source_ids=['20:e0'])
        self.save_suite()
        self.save()
        for run in self.build()['runs']:
            metrics = [run['metrics']] + [row['metrics'] for row in run['by_episode'].values()] + [
                row['metrics'] for row in run['by_category'].values()]
            # The untouched baseline has scored evidence rows with empty retrieval.
            if run['name'] == 'cog':
                self.assertTrue(all(row[key] == {'mean': None, 'n': None}
                                    for row in metrics for key in beam.EVIDENCE_METRICS))

    def test_evidence_recall_repeats_follow_metric_aggregation(self):
        self.episodes[0]['cases'][0]['gold']['evidence_ids'] = ['20:e0', '20:e1']
        self.episodes[0]['cases'][0]['cutoff'] = 2
        primary = self.report['cases'][0]
        primary['retrieved_source_ids'] = ['20:e0', '20:e1']
        repeat = copy.deepcopy(primary)
        repeat.update(repeat_index=1, retrieved_source_ids=[])
        self.report['cases'].append(repeat)
        self.save_suite()
        self.save()
        value = self.build()
        self.assertEqual(value['results']['cog'][primary['case_id']]['evidence_recall'], 1.0)
        run = value['runs'][0]
        expected = {beam.EVIDENCE: {'mean': 0.5, 'n': 2}, beam.SOURCE_IDS: {'mean': 1.0, 'n': 2},
                    beam.SOURCE_SHARE: {'mean': 0.5, 'n': 2}, beam.PRECISION: {'mean': 1.0, 'n': 1},
                    beam.F1: {'mean': 1.0, 'n': 1}, beam.CITATION: {'mean': 0.5, 'n': 2}}
        for metrics in (run['metrics'], run['by_episode']['20']['metrics'],
                        run['by_category'][primary['category']]['metrics']):
            for key, aggregate in expected.items():
                self.assertEqual(metrics[key], aggregate, key)

    def test_leaf_direct_metrics_and_citation_coverage_use_separate_provenance(self):
        episode = self.episodes[0]
        episode['events'] = [{'source_id': f'20:{i}', 'content': f'event {i} leaf fragment'} for i in range(4)]
        self.index[0]['events'] = 4
        for question, case in zip(episode['cases'][:6], self.report['cases'][:6]):
            question.update(cutoff=4, gold={'evidence_ids': ['20:0']})
            case.update(retrieved_source_ids=['20:0', '20:1'], response={'items': [leaf_item()]})
        episode['cases'][0]['gold']['evidence_ids'] = ['20:0', '20:1']
        self.report['cases'][0]['retrieved_source_ids'] = ['20:0', '20:1', '20:2']
        episode['cases'][1]['gold']['evidence_ids'] = ['20:1']
        self.report['cases'][1].update(retrieved_source_ids=['20:1', '20:2'],
                                       response={'items': [leaf_item(1, 'not in source content')]})
        episode['cases'][2]['gold']['evidence_ids'] = ['20:3']
        self.report['cases'][2].update(retrieved_source_ids=['20:3'],
                                       response={'items': [{'text': 'ordinary', 'source_ids': ['anonymous-3']}]})
        episode['cases'][3]['gold']['evidence_ids'] = ['20:2']
        self.report['cases'][3].update(retrieved_source_ids=['20:2'], response=None)
        self.report['cases'][4]['status'] = 'error'
        self.report['cases'][5]['status'] = 'invalid'
        self.save_suite()
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr, patch.object(beam, 'rows', wraps=beam.rows) as read_rows:
            value = self.build()
        self.assertEqual(sum(call.args[0].name == 'episodes.jsonl' for call in read_rows.call_args_list), 1)
        self.assertEqual(stderr.getvalue().count('L0 origin/content mismatch'), 1)
        run = value['runs'][0]
        expected = {beam.EVIDENCE: (0.625, 4), beam.SOURCE_IDS: (0.75, 4), beam.SOURCE_SHARE: (0.1875, 4),
                    beam.PRECISION: (1.0, 3), beam.F1: (8/9, 3), beam.CITATION: (1.0, 4)}
        for metrics in (run['metrics'], run['by_episode']['20']['metrics']):
            for key, (mean, n) in expected.items():
                self.assertAlmostEqual(metrics[key]['mean'], mean, msg=key)
                self.assertEqual(metrics[key]['n'], n, key)
        category = run['by_category']['category-9']['metrics']
        for key, mean in ((beam.EVIDENCE, 0.5), (beam.SOURCE_IDS, 1.0), (beam.SOURCE_SHARE, 0.25),
                          (beam.PRECISION, 1.0), (beam.F1, 2/3), (beam.CITATION, 1.0)):
            self.assertAlmostEqual(category[key]['mean'], mean, msg=key)
            self.assertEqual(category[key]['n'], 1)
        cell = value['results']['cog'][episode['cases'][0]['id']]
        self.assertEqual({key: cell[key] for key in ('retrieved_ids', 'evidence_hits', 'evidence_recall',
                                                    'cited_ids', 'cited_hits', 'citation_coverage')},
                         {'retrieved_ids': 1, 'evidence_hits': 1, 'evidence_recall': 0.5,
                          'cited_ids': 3, 'cited_hits': 2, 'citation_coverage': 1.0})
        cell = value['results']['cog'][episode['cases'][1]['id']]
        self.assertEqual((cell['retrieved_ids'], cell['evidence_recall'], cell['citation_coverage']), (0, 0.0, 1.0))
        self.assertIsNone(cell['evidence_precision'])
        self.assertIsNone(cell['evidence_f1'])
        # No event contents or raw citation lists leak into the page contract.
        for cells in value['results'].values():
            self.assertTrue(all('retrieved_source_ids' not in cell and 'direct_source_ids' not in cell for cell in cells.values()))

    def test_source_share_uses_cutoff_or_episode_size_not_zero_fallback(self):
        # A missing cutoff uses all eight events; an explicit smaller cutoff
        # uses only the visible prefix. Explicit zero remains undefined.
        self.episodes[0]['events'] = [{'id': f'20:e{i}', 'text': 'synthetic'} for i in range(8)]
        self.index[0]['events'] = 8
        for question, case, cutoff, ret in zip(self.episodes[0]['cases'], self.report['cases'],
                                              (None, 2, 0), (['20:e0', '20:e1'], ['20:e0'], [])):
            question['gold']['evidence_ids'] = ['20:e0']
            if cutoff is None:
                question.pop('cutoff')
            else:
                question['cutoff'] = cutoff
            case['retrieved_source_ids'] = ret
        self.save_suite()
        self.save()
        value = self.build()
        run = value['runs'][0]
        self.assertEqual(run['metrics'][beam.SOURCE_IDS], {'mean': 1.0, 'n': 3})
        self.assertEqual(run['metrics'][beam.SOURCE_SHARE], {'mean': 0.375, 'n': 2})
        self.assertEqual(run['by_episode']['20']['metrics'][beam.SOURCE_SHARE], {'mean': 0.375, 'n': 2})
        for category, mean, n in (('category-9', 0.25, 1), ('category-8', 0.5, 1),
                                  ('category-7', None, None)):
            self.assertEqual(run['by_category'][category]['metrics'][beam.SOURCE_SHARE], {'mean': mean, 'n': n})

    def test_evidence_glossary_complete_sorted_unique(self):
        score = {'id': beam.SCORE, 'group': 'fixed_reader', 'applies_to': ['fixed_reader'],
                 'aggregation': 'scored 且该指标非 null 的样本均值；按类别另报，不跨基准混比'}
        put(self.glossary, [score, {'id': beam.EVIDENCE, 'name_zh': 'stale'}, score,
                            {'id': beam.EVIDENCE}, {'id': beam.ORDER}, {'id': beam.SOURCE_IDS, 'formula': 'stale'}])
        value = self.build()
        glossary = value['glossary']
        self.assertEqual([entry['id'] for entry in glossary], sorted({*beam.EVIDENCE_METRICS, beam.ORDER, beam.SCORE}))
        entry = next(entry for entry in glossary if entry['id'] == beam.EVIDENCE)
        self.assertEqual(set(entry), {'id', 'group', 'name_zh', 'definition_zh', 'formula', 'range',
                                     'higher_is_better', 'aggregation', 'caveats', 'applies_to'})
        self.assertEqual(entry['name_zh'], '证据召回')
        self.assertEqual(entry['definition_zh'], '返回条目直接代表的来源事件覆盖 gold 证据事件的比例（每条条目只计它直接代表的事件；LeafMemory 条目计其 L0 叶子所属事件；Tree 只计 level 0 节点自身的叶子事件）')
        self.assertEqual(entry['formula'], '|direct_source_ids ∩ evidence_ids| / |evidence_ids|')
        self.assertEqual(entry['range'], '[0,1]')
        self.assertIs(entry['higher_is_better'], True)
        for key in ('group', 'applies_to', 'aggregation'):
            self.assertEqual(entry[key], score[key])
        self.assertEqual(len(entry['caveats']), 5)
        self.assertEqual(entry['caveats'][-1], 'Tree 条目只计 level 0 节点自身的叶子事件；上层节点为摘要闭包不计，覆盖见引用覆盖')
        for caveat, phrases in zip(entry['caveats'], (
                ('引用口径', '直接代表', 'LeafMemory', 'L0'),
                ('阅读器字符预算之前', '被预算丢弃', '不等于「证据正文到达阅读器」'),
                ('400 题', '非 scored'))):
            for phrase in phrases:
                self.assertIn(phrase, caveat)
        self.assertEqual(entry['caveats'][3],
                         '引用闭包（摘要层依赖）不计入；闭包口径见 beam.citation_coverage')
        for metric, name, formula, higher, range_, caveat_count in (
                (beam.SOURCE_IDS, '每题引用来源数', '|direct_source_ids|', None, '[0,∞)', 2),
                (beam.SOURCE_SHARE, '引用占可见事件比例', '|direct_source_ids| / cutoff', None, '[0,1]', 3),
                (beam.PRECISION, '证据精确率', '|direct_source_ids ∩ evidence_ids| / |direct_source_ids|', True, '[0,1]', 2),
                (beam.F1, '证据 F1', '2 * |direct_source_ids ∩ evidence_ids| / (|direct_source_ids| + |evidence_ids|)', True, '[0,1]', 1),
                (beam.CITATION, '引用覆盖', '|retrieved_source_ids ∩ evidence_ids| / |evidence_ids|', None, '[0,1]', 2)):
            definition = next(row for row in glossary if row['id'] == metric)
            self.assertEqual(set(definition), set(entry), metric)
            self.assertEqual(definition['name_zh'], name)
            self.assertTrue(definition['definition_zh'])
            self.assertEqual(definition['formula'], formula)
            self.assertIs(definition['higher_is_better'], higher)
            self.assertEqual(definition['range'], range_)
            self.assertEqual(len(definition['caveats']), caveat_count + (metric != beam.CITATION))
            if metric != beam.CITATION:
                self.assertEqual(definition['caveats'][-1], entry['caveats'][-1])
            for key in ('group', 'aggregation', 'applies_to'):
                self.assertEqual(definition[key], score[key])

    def test_evidence_caveat_uses_suite_counts_and_categories(self):
        for episode in self.episodes:
            for question in episode['cases']:
                question['gold']['evidence_ids'] = [f"{episode['id']}:e0"]
        excluded = self.episodes[0]['cases'][:3]
        for question, category in zip(excluded, ('beta', 'alpha', 'alpha')):
            question['gold']['evidence_ids'] = []
            question['category'] = category
        self.save_suite()
        entry = next(row for row in self.build()['glossary'] if row['id'] == beam.EVIDENCE)
        self.assertEqual(entry['caveats'][2],
                         '无 gold 证据的 3 题（alpha 2、beta 1）不进入分母，非 scored 的题也不进入。')
        for question in excluded:
            question['gold']['evidence_ids'] = ['20:e0']
        self.save_suite()
        entry = next(row for row in self.build()['glossary'] if row['id'] == beam.EVIDENCE)
        self.assertEqual(entry['caveats'][2], '无 gold 证据的 0 题不进入分母，非 scored 的题也不进入。')

    def test_result_fields_nulls_and_unexecuted_cell(self):
        value = self.build()
        cell = value["results"]["cog"]["1:q0"]
        self.assertEqual(cell["items"], [{"expectation": 0.7, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.4, "2": 0.5}}])
        self.assertEqual((cell["answer"], cell["returned_items"], cell["returned_chars"], cell["completion_tokens"]), ("回答", 1, 2, 3))
        self.assertEqual(cell["timings"], {"ingest_ms": 1.25, "query_ms": 2.5, "answer_ms": 3.75})
        self.assertFalse(cell["judge_label"])
        self.assertIsNone(cell["error_type"])
        self.assertIsNone(cell["event_ordering"])
        self.assertIsNone(value["memeval_commit"])
        self.assertIsNone(value["runs"][0]["billing"]["native"])
        removed = self.report["cases"].pop()
        self.report["cases"][0] = {"case_id": "20:q19", "episode_id": "20", "status": "invalid"}
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as errors:
            value = self.build()
        self.assertIn("no primary sample", errors.getvalue())
        self.assertIsNone(value["results"]["cog"][removed["case_id"]]["status"])
        for key in ("answer", "items", "reader_abstain", "truncated", "finish_reason", "completion_tokens", "returned_items", "score"):
            self.assertIsNone(value["results"]["cog"]["20:q19"][key], key)
        self.assertEqual(len(value["results"]["cog"]), 400)

    def test_native_bridge_evaluator_errors_and_resume_log_matching(self):
        native = self.report["cases"][0]
        native.update(status="error", error_type="*jsonrpc.RPCError", error_detail="native failure")
        native["metrics"][beam.SCORE] = 1  # Error rows never enter mean_score.
        transport = self.report["cases"][1]
        transport.update(status="error", error_type="rpc_timeout", error_detail="transport failed")
        evaluator = self.report["cases"][2]
        evaluator.update(status="error", error_type="LLMTransportError", error_detail="LLMTransportError")
        log = self.a / "logs/resume-2/0001-ns-20"
        put(log / "episode.json", {"namespace": "ns-20"})
        record = {"error": "native failure", "time": "2026-10-25T01:00:00Z", "method": "ingest",
                  "rpc_error": {"data": {"details": {"cogletnet": {"events": [{"event_id": "e", "state": "Failed",
                               "error": {"kind": "SUMMARY_UNCOMPRESSIBLE", "message": "fixture", "summary_failure": {"attempts": []}}}]}}}}}
        put_lines(log / "rpc-errors.jsonl", [record])
        baseline = self.other["cases"][0]
        baseline.update(status="error", error_type="*jsonrpc.RPCError", error_detail="RPC failed")
        self.save()
        value = self.build()
        result = value["results"]["cog"]
        self.assertEqual(result[native["case_id"]]["failure"], {"class": "cogletnet-native", "native_kinds": ["SUMMARY_UNCOMPRESSIBLE"], "message": "native failure"})
        self.assertEqual(result[transport["case_id"]]["failure"]["class"], "bridge/transport")
        self.assertEqual(result[evaluator["case_id"]]["failure"]["class"], "evaluator")
        self.assertEqual(value["results"]["bm25"][baseline["case_id"]]["failure"]["class"], "bridge/transport")
        self.assertEqual(value["runs"][0]["failures_by_class"], {"cogletnet-native": 1, "bridge/transport": 1, "evaluator": 1})
        self.assertEqual(value["runs"][0]["by_episode"]["20"]["mean_score"], 0.5)
        self.assertEqual(attribution.native_kinds(log / "rpc-errors.jsonl"), attribution.native_records([record]))
        # An unrelated old error must not be attached simply because ns matches.
        native["error_detail"] = "unrelated failure"
        self.save()
        failure = self.build()["results"]["cog"][native["case_id"]]["failure"]
        self.assertIsNone(failure["class"])
        self.assertEqual(failure["native_kinds"], [])

    def test_resume_history_generated_at_usage_and_locked_warning(self):
        self.report["resume_history"] = [{"resumed_at_utc": "2026-10-26T01:00:00.123456789Z", "statuses": ["error"]}]
        self.report["resume_count"] = 2
        old = copy.deepcopy(self.report)
        old["resume_history"] = [{"resumed_at_utc": "2026-10-25T01:00:00Z", "statuses": ["invalid"]}]
        put(self.a / "resume-history/0002/report.json", old)
        self.save()
        (self.a / ".resume.lock").touch()
        # A staged report must neither add a run nor change generated_at.
        put(self.a / ".memeval-resume-pending/report.json", {"started_at_utc": "2099-01-01T00:00:00Z"})
        for subdir, n in [("0001-ns-1", 2), ("resume-1/0001-ns-1", 3)]:
            path = self.a / "logs" / subdir / "sut.stderr.log"
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('cogletnet_usage ' + json.dumps({"method": "close", "usage": {"calls": n, "input_tokens": n*10, "output_tokens": n}}) + '\n')
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            value = self.build()
        self.assertIn("正在续跑，数据可能过期", stderr.getvalue())
        self.assertEqual(value["generated_at"], "2026-10-26T01:00:00.123456789Z")
        self.assertEqual(value["runs"][0]["resume_history"], [{"at": "2026-10-25T01:00:00Z", "statuses": ["invalid"], "cases": None, "sut_change": None},
                                                           {"at": "2026-10-26T01:00:00.123456789Z", "statuses": ["error"], "cases": None, "sut_change": None}])
        self.assertEqual(value["runs"][0]["billing"]["native"], {"calls": 5, "input_tokens": 50, "output_tokens": 5})
        del self.report["resume_history"]
        self.other["started_at_utc"] = "2026-10-24T00:00:00Z"
        self.save()
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(self.build()["generated_at"], "2026-10-25T01:00:00Z")

    def test_resume_identity_change_is_hash_only_and_counted_once(self):
        hashes = ('manifest_sha256', 'env_lock_sha256', 'source_snapshot_sha256')
        identity = {side: {key: str(i + offset) * 64 for i, key in enumerate(hashes)}
                    for side, offset in (('previous', 1), ('current', 4))}
        change = copy.deepcopy(identity)
        for value in change.values():
            value.update(manifest='/private/manifest.json', env_dir='/private/env', api_key='fake-private-key')
        change['extra'] = 'private metadata'
        entries = [
            {'resumed_at_utc': '2026-10-25T01:00:00Z', 'statuses': ['error']},
            {'resumed_at_utc': '2026-10-26T01:00:00Z', 'statuses': ['scored', 'error'],
             'cases': 328, 'sut_change': change},
        ]
        self.report.update(resume_count=2, resume_history=entries)
        put(self.a / 'resume-history/0003/report.json', self.report)
        self.save()
        value = self.build()
        run = value['runs'][0]
        self.assertEqual(run['sut_changes'], 1)
        self.assertEqual(len(run['resume_history']), 2)
        self.assertEqual(run['resume_history'][1], {
            'at': entries[1]['resumed_at_utc'], 'statuses': entries[1]['statuses'],
            'cases': 328, 'sut_change': identity})
        self.assertIsNone(run['resume_history'][0]['cases'])
        self.assertIsNone(run['resume_history'][0]['sut_change'])
        self.assertNotIn('private', common.encode(run['resume_history']))
        self.assertTrue(all(c['attempt'] is None for c in value['results']['cog'].values()))
        self.assertEqual(common.encode(value), common.encode(self.build()))

    def test_legacy_bytes_unchanged_except_added_provenance_fields(self):
        value = self.build()
        value['glossary'] = [entry for entry in value['glossary'] if entry['id'] not in beam.EVIDENCE_METRICS]
        for run in value['runs']:
            self.assertEqual(run.pop('sut_changes'), 0)
            self.assertIsNone(run['sut'].pop('embedding'))
            self.assertEqual(run.pop('reader'), self.report['reader'])
            self.assertEqual(run['resume_history'], [])
            run['report_path'] = '<report>'  # Temporary directory is not stable.
            for metric in beam.EVIDENCE_METRICS:
                self.assertEqual(run['metrics'].pop(metric), {'mean': None, 'n': None})
                for row in run['by_category'].values():
                    self.assertEqual(row['metrics'].pop(metric), {'mean': None, 'n': None})
            for row in run['by_episode'].values():
                self.assertEqual(row.pop('metrics'), {key: {'mean': None, 'n': None} for key in beam.EVIDENCE_METRICS})
        for cells in value['results'].values():
            for cell in cells.values():
                self.assertIsNone(cell.pop('attempt'))
                self.assertIsNone(cell.pop('evidence_hits'))
                self.assertIsNone(cell.pop('evidence_recall'))
                self.assertEqual(cell.pop('retrieved_ids'), 0)
                self.assertIsNone(cell.pop('evidence_precision'))
                self.assertIsNone(cell.pop('evidence_f1'))
                self.assertEqual(cell.pop('cited_ids'), 0)
                self.assertIsNone(cell.pop('cited_hits'))
                self.assertIsNone(cell.pop('citation_coverage'))
        self.assertEqual(hashlib.sha256(common.encode(value).encode()).hexdigest(),
                         '75e3bceda54afe6a22d86ac48716fbb3de4829b02b9d1f1e8f94417ad5dc8e47')

    def test_cli_determinism_js_json_sha_and_output_files(self):
        first = self.cli()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(first.stderr, "")
        out = self.root / "out"
        before = {p.name: p.read_bytes() for p in out.iterdir()}
        self.assertEqual(set(before), {"beam-100k.json", "beam-100k.js"})
        second = self.cli()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(before, {p.name: p.read_bytes() for p in out.iterdir()})
        value = json.loads(before["beam-100k.json"])
        js = before["beam-100k.js"].decode()
        self.assertTrue(js.startswith("window.MEMEVAL_BEAM = ") and js.endswith(";\n"))
        self.assertNotIn("</script>", js)
        self.assertEqual(json.loads(js[len("window.MEMEVAL_BEAM = "):-2]), value)
        self.assertEqual(value["runs"][0]["report_sha256"], hashlib.sha256((self.a / "report.json").read_bytes()).hexdigest())
        self.assertEqual(value["runs"][0]["report_path"], str((self.a / "report.json").resolve()))

    def test_glossary_unavailable_and_protocol_conflict_are_null(self):
        self.other["reader"]["max_tokens"] = 512
        self.save()
        with patch.object(common, "ROOT", self.root), contextlib.redirect_stderr(io.StringIO()) as stderr:
            value = self.build(glossary=None)
        self.assertEqual([entry['id'] for entry in value['glossary']], sorted(beam.EVIDENCE_METRICS))
        self.assertIn("glossary unavailable", stderr.getvalue())
        self.assertIn("protocol.reader.max_tokens differs", stderr.getvalue())
        self.assertIsNone(value["dataset"]["protocol"]["reader"]["max_tokens"])

    def test_reject_conflicting_inputs_without_traceback(self):
        self.other["suite_sha256"] = "different"
        self.save()
        result = self.cli()
        self.assertEqual(result.returncode, 2)
        self.assertIn("suite_sha256", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertFalse((self.root / "out").exists())
        self.other["suite_sha256"] = "fixture-sha"
        self.other["cases"][0]["episode_id"] = "wrong"
        self.save()
        with self.assertRaisesRegex(ValueError, "mismatched episode"):
            self.build()
        self.other["cases"][0]["episode_id"] = "20"
        self.other["cases"].append({**self.other["cases"][0], "status": "invalid"})
        self.save()
        with self.assertRaisesRegex(ValueError, "conflicting duplicate"):
            self.build()


class ReaderMessageTests(unittest.TestCase):
    def test_request_hash_matches_explicit_go_json_wire(self):
        prompt = {'provider': 'openai-compat', 'model': 'fixture', 'template_id': 'fixture@sha/input=items',
                  'max_tokens': 2048, 'messages': [{'role': 'user', 'content': '中🙂\n"\\\t<>&\u2028\u2029'}]}
        # Go struct order, integer temperature zero, HTML/U+2028/U+2029
        # escaping, UTF-8 (not ASCII escaping), and nil Extra are intentional.
        wire = (r'{"Provider":"openai-compat","Endpoint":"https://reader.invalid/v1/chat/completions",'
                r'"API":"chat/completions","Model":"fixture","Template":"fixture@sha/input=items",'
                r'"Client":"memeval-llm-v1","Sample":"q#0","Purpose":"reader",'
                r'"Messages":[{"role":"user","content":"中🙂\n\"\\\t\u003c\u003e\u0026\u2028\u2029"}],'
                r'"Temperature":0,"MaxTokens":2048,"N":1,"Extra":null').encode() + b'}'
        self.assertEqual(beam.go_json_bytes(json.loads(wire)), wire)
        self.assertEqual(beam.reader_request_sha(prompt, 'q#0', 'https://reader.invalid/v1/chat/completions'),
                         hashlib.sha256(wire).hexdigest())

    def test_unicode_separators_whole_item_truncation_and_verbatim_note(self):
        note = '\n    NOTE: answer only. \n    Question: {query}\n{query}'
        items = [{'text': '中🙂'}, {'text': 'e\u0301'}, {'text': 'tail'}]
        messages, kept, chars = beam.reader_messages(items, ' Q {query} <&> ', 5, note)
        self.assertEqual((kept, chars), (2, 5))
        self.assertEqual(messages, [{'role': 'user', 'content': '中🙂\ne\u0301'},
                                    {'role': 'user', 'content': note.replace('{query}', ' Q {query} <&> ')}])
        self.assertEqual(beam.reader_messages(items, 'q', 4, note)[1:], (1, 2))
        # Drop low-ranked items, even if a smaller later item would fit alone.
        self.assertEqual(beam.reader_messages([{'text': 'toolong'}, {'text': 'x'}], 'q', 3, note),
                         ([{'role': 'user', 'content': note.replace('{query}', 'q')}], 0, 0))

    def test_empty_items_and_empty_text_are_distinct(self):
        note = '\n keep spaces {query}'
        only_note = {'role': 'user', 'content': '\n keep spaces q'}
        self.assertEqual(beam.reader_messages([], 'q', 0, note), ([only_note], 0, 0))
        self.assertEqual(beam.reader_messages([{'text': ''}], 'q', 0, note),
                         ([{'role': 'user', 'content': ''}, only_note], 1, 0))

    def test_local_template_sha_and_mismatch_warning(self):
        raw = beam.READER_TEMPLATES.read_bytes()
        sha = hashlib.sha256(raw).hexdigest()
        self.assertEqual(beam.reader_template({'template_sha256': sha}), (json.loads(raw)['reader_note'], sha))
        with contextlib.redirect_stderr(io.StringIO()) as errors:
            self.assertEqual(beam.reader_template({'template_sha256': 'old'}), (json.loads(raw)['reader_note'], sha))
        self.assertIn('template_sha256 differs', errors.getvalue())


def work_build(seq, content, state="Ready", tree=False):
    members = []
    for level in range(4 if tree else 1):
        members.append({"body": {"reference": {"node": f"n{seq}-{level}", "body": f"b{seq}-{level}"},
                                 "kind": "Leaf" if level == 0 else "Summary", "text": f"{content} L{level}"},
                        "level": level, "ordinal": 0, "inputs": [] if level == 0 else [{"node": f"n{seq}-{level-1}"}],
                        "edge": {"kind": "ContextFallback" if level == 1 else "SummaryParent", "parent": f"n{seq}-{level+1}"} if tree and level < 3 else None})
    return {"kind": "build", "seq": seq, "event_id": f"event-{seq}", "text_sha256": hashlib.sha256(content.encode()).hexdigest(),
            "state": state, "audit": {}, "snapshot": {"members": members, "paths": {"unused": {"text": "unreferenced snapshot path"}}}}


class BeamWorkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="memeval-beam-work-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.suite, self.run = self.root / "suite", self.root / "run"
        put(self.suite / "suite.json", {"suite_id": "work"})
        self.events = [{"source_id": f"e{i}", "content": f"事件 {i}", "role": "user", "turn_group": "batch0"} for i in range(6)]
        self.questions = [{"id": f"q{i}", "query": f"问题 {i}", "gold": {"evidence_ids": [f"e{i}"] if i < 6 else []}}
                          for i in range(8)]
        self.episodes = [{"id": "1", "events": self.events, "cases": self.questions},
                         {"id": "2", "events": [{"source_id": "other", "content": "独立段"}],
                          "cases": [{"id": "other", "query": "独立问题", "gold": {"evidence_ids": []}}]}]
        put_lines(self.suite / "episodes.index.jsonl", [{"id": e["id"], "events": len(e["events"]), "cases": len(e["cases"])} for e in self.episodes])
        self.report = {"suite_id": "work", "cases": [{"case_id": q["id"], "episode_id": e["id"], "namespace": f"ns-{e['id']}",
                        "status": "error" if q["id"] == "q7" else "scored", "repeat_index": 0}
                       for e in self.episodes for q in e["cases"]], "summary": {},
                       "sut": {"name": "cogletnet", "describe": {"config": {"work_trace": "full", "jev_threshold": 0.2}}}}
        self.records = [{"kind": "config", "work_trace": "full", "jev_threshold": 0.5}]
        self.records += [work_build(i, event["content"], "Failed" if i == 0 else "Ready", tree=i == 5) for i, event in enumerate(self.events)]
        reasons = {2: "evidence_cap", 3: "jev_below_threshold", 4: "CONTEXT_BUNDLE_TOO_LARGE"}
        for i in range(7):
            candidates, items, omitted = [], [], {}
            if 2 <= i <= 5:
                level = 3 if i == 5 else 0
                node = f"n{i}-{level}"
                candidate = {"reference": {"node": node, "body": f"b{i}-{level}"}, "source": f"native-{i}", "level": level,
                             "origin": "AncestorExpansion" if i == 5 else "VectorHit", "similarity": 0.7, "p_use": 0.8, "vector_rank": 2}
                candidates.append(candidate)
                if i == 5:
                    items.append({**candidate, "source": {"id": f"native-{i}"},
                                  "path": {"text": "完整路径 </script>", "parts": [{"reference": {"node": node, "body": f"b{i}-{level}"}, "coverage": True}]}})
                else:
                    omitted[node] = reasons[i]
            self.records.append({"kind": "search", "query": f"问题 {i}", "limit": 10,
                                 "sources": {f"native-{i}": f"event-{i}"},
                                 "result": {"candidates": candidates, "items": items, "omitted": omitted,
                                            "context_view": {"tokens": 10, "rendered": "上下文", "visible": []},
                                            "settings": {"jev_threshold": 0.75}, "jev_trace": [], "degraded": False, "warnings": []}})
        self.log = self.run / "logs/0002-ns-1/sut.stderr.log"
        put(self.log.parent / "episode.json", {"namespace": "ns-1"})
        other = self.run / "logs/0003-ns-2/sut.stderr.log"
        put(other.parent / "episode.json", {"namespace": "ns-2"})
        other.write_text('sut_trace {"kind":"config","work_trace":"full","jev_threshold":0.5}\n'
                         + 'sut_trace ' + json.dumps(work_build(0, "独立段")) + '\n', encoding="utf-8")
        self.glossary = self.root / "glossary.json"
        put(self.glossary, [])
        self.save()

    def save(self):
        put(self.run / "report.json", self.report)
        put_lines(self.suite / "episodes.jsonl", self.episodes)
        self.log.write_text('sut_trace {broken JSON\n' + ''.join('sut_trace ' + json.dumps(r, ensure_ascii=False) + '\n' for r in self.records), encoding="utf-8")

    def build(self):
        runs = [("cog", self.run)]
        review = beam.build_review(self.suite, runs, self.glossary, work_names=['cog'])
        files = beam.build_work(review, self.suite, runs, ["cog"])
        return review, files

    def test_compact_build_references_preserve_work_stages_and_bytes(self):
        builds = [r for r in self.records if r['kind'] == 'build']
        first = builds[0]
        first.update(operation_id='scope-op', snapshot_id='scope-snapshot', trace_batch=0)
        first['snapshot']['members'] = [m for row in builds for m in row['snapshot']['members']]
        first['audit'] = {'operation': {'config': {'memory_tokens': 12000}}}
        for row in builds[1:]:
            row.update(operation_id='scope-op', snapshot_id='scope-snapshot', trace_batch=0,
                       audit=copy.deepcopy(first['audit']), snapshot=copy.deepcopy(first['snapshot']))
        self.save()
        original, expanded = self.build()
        for row in builds[1:]:
            row.update(audit={'ref_seq': 0}, snapshot={'ref_seq': 0})
        self.save()
        compact, files = self.build()
        self.assertEqual(compact, original)
        self.assertEqual(files, expanded)
        self.assertEqual(files['work/cog/1.js']['build_config']['memory_tokens'], 12000)
        self.assertEqual(compact['runs'][0]['work']['episodes']['1']['corrupt_lines'], 1)
        out = self.root / 'reference-output'
        beam.write_review(compact, out, files)
        before = {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()}
        again, repeated = self.build()
        beam.write_review(again, out, repeated)
        self.assertEqual(before, {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()})

    def test_capped_reference_anchor_warns_and_work_remains_available(self):
        builds = [r for r in self.records if r['kind'] == 'build']
        for row in builds:
            row.update(operation_id='missing-op', snapshot_id='missing-snapshot', trace_batch=2,
                       audit={'ref_seq': 99}, snapshot={'ref_seq': 99})
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            review, files = self.build()
        self.assertIn('6 unresolved build trace reference lines', stderr.getvalue())
        payload = files['work/cog/1.js']
        self.assertEqual(payload['log']['corrupt_lines'], 7)
        self.assertEqual(review['runs'][0]['work']['episodes']['1']['corrupt_lines'], 7)
        self.assertTrue(all('unresolved build trace reference' in b['audit_error'] for b in payload['builds']))
        self.assertTrue(all(b['nodes'] == [] for b in payload['builds']))
        self.assertEqual(review['results']['cog']['q7']['work']['trace'], 'corrupt')

    def test_stage_tree_corruption_configs_and_text_scope(self):
        review, files = self.build()
        cells = review["results"]["cog"]
        expected = ["not_written", "not_candidate", "capped", "jev_rejected", "not_packed", "covered", "no_evidence", "no_search"]
        self.assertEqual([cells[f"q{i}"]["work"]["stage"] for i in range(8)], expected)
        for i in range(7):
            self.assertEqual(cells[f"q{i}"]["work"]["builds_before"], 6)
        self.assertEqual(cells["q5"]["work"]["evidence"][0]["best"]["level"], 3)
        self.assertEqual(cells["q5"]["work"]["covered"], [1, 1])
        self.assertEqual(cells["q7"]["work"]["trace"], "corrupt")
        self.assertFalse(cells["q7"]["work"]["search"])
        self.assertIsNone(cells["q7"]["work"]["builds_before"])
        summary = review["runs"][0]["work"]["episodes"]["1"]
        self.assertEqual(summary["levels"], {"0": 6, "1": 1, "2": 1, "3": 1})
        self.assertEqual(summary["edges"], {"SummaryParent": 2, "ContextFallback": 1})
        self.assertEqual(summary["states"], {"Ready": 5, "Failed": 1})
        self.assertEqual(summary["corrupt_lines"], 1)
        self.assertEqual(review["runs"][0]["work"]["config"], {"work_trace": "full", "jev_threshold": 0.5})
        payload = files["work/cog/1.js"]
        self.assertEqual(payload["searches"]["q5"]["jev_threshold"], 0.75)
        self.assertEqual(payload["log"]["rows"], {"build": 6, "search": 7, "config": 1})
        self.assertEqual(payload["mapping_mismatches"], 0)
        for payload in files.values():
            texts = payload["texts"]
            refs = set()
            def walk(value):
                if isinstance(value, dict):
                    for key, child in value.items():
                        if key.endswith("_hash") and isinstance(child, str):
                            refs.add(child)
                        walk(child)
                elif isinstance(value, list):
                    for child in value:
                        walk(child)
            walk({k: v for k, v in payload.items() if k != "texts"})
            self.assertEqual(set(texts), refs)
            self.assertNotIn("unreferenced snapshot path", texts.values())
        self.assertTrue(set(files["work/cog/1.js"]["texts"]).isdisjoint(files["work/cog/2.js"]["texts"]))

    def test_farthest_stage_partial_and_best_tiebreak(self):
        self.questions[4]["gold"]["evidence_ids"] = ["e0", "e1", "e2", "e3", "e4"]
        search = next(r for r in self.records if r.get("query") == "问题 4")
        for i in (2, 3):
            other = next(r for r in self.records if r.get("query") == f"问题 {i}")
            search["sources"].update(other["sources"])
            search["result"]["candidates"].extend(copy.deepcopy(other["result"]["candidates"]))
            search["result"]["omitted"].update(other["result"]["omitted"])
        base = search["result"]["candidates"][0]
        for ident, p, sim, reason in [("rejected", 1, 1, "jev_below_threshold"), ("high-p", 0.9, 0.1, "result_limit"), ("best", 0.9, 0.9, "result_limit")]:
            search["result"]["candidates"].append({**base, "reference": {"node": ident}, "p_use": p, "similarity": sim})
            search["result"]["omitted"][ident] = reason
        self.questions[5]["gold"]["evidence_ids"] = ["e5", "e1"]
        self.save()
        review, _ = self.build()
        fourth = review["results"]["cog"]["q4"]["work"]
        self.assertEqual(fourth["stage"], "not_packed")
        self.assertEqual(fourth["evidence"][-1]["best"]["node"], "best")
        self.assertEqual(fourth["omitted"], {"CONTEXT_BUNDLE_TOO_LARGE": 1, "result_limit": 2, "jev_below_threshold": 2, "evidence_cap": 1})
        fifth = review["results"]["cog"]["q5"]["work"]
        self.assertEqual((fifth["stage"], fifth["covered"]), ("partial", [1, 2]))

    def test_build_config_composition_pending_and_reference_scope(self):
        before, _ = self.build()
        builds = [r for r in self.records if r['kind'] == 'build']
        config = {'chunk_low': 10, 'chunk_high': 30, 'chunk_target': 20,
                  'build_text_limits': {'max_bytes': 999}, 'max_levels': 4,
                  'leaf_k': 10, 'upper_k': 5, 'candidate_cap': 20, 'memory_tokens': 200,
                  'jev_threshold': 0.5, 'jev_model': 'fake-jev',
                  'embedding': {'model': 'fake-embedding', 'dimensions': 3},
                  'generator': {'model': 'fake-generator'},
                  'summary_policy': {'strategy': 'fixture'}, 'custom_summary_limit': 11,
                  'unrelated': 'not exported'}
        # First configured build is not the first build. Non-exported changes
        # do not count; each later differing build counts once, not per field.
        for row in builds[1:]:
            row['audit']['operation'] = {'config': copy.deepcopy(config)}
        builds[2]['audit']['operation']['config']['unrelated'] = 'changed'
        builds[3]['audit']['operation']['config'].update(chunk_low=11, chunk_high=40)
        builds[4]['audit']['operation']['config']['custom_summary_limit'] = 12
        summary = {'body': 'b5-0', 'text': 'own summary', 'output_hash': 'own-output',
                   'model': 'fake-generator', 'attempts': 2,
                   'validation': {'faithful': True, 'reason': 'faithful', 'reason_hash': 'own-reason'}}
        builds[5]['snapshot']['members'][1]['input_summaries'] = [summary]
        builds[5]['snapshot']['pending'] = {'1': [summary], '2': [summary, summary]}
        builds[5]['audit']['summaries'] = [{'body': 'b5-1', 'input_tokens': 7, 'copied_span': {'start': 0, 'end': 5},
                                          'failure': {'kind': 'test', 'message': 'diagnostic'}}]
        self.save()
        review, files = self.build()
        payload = files['work/cog/1.js']
        self.assertEqual(payload['version'], 2)
        expected = {k: v for k, v in config.items() if k not in ('embedding', 'generator', 'unrelated')}
        expected.update(embedding_model='fake-embedding', generator_model='fake-generator')
        self.assertEqual(payload['build_config'], expected)
        self.assertEqual(payload['build_config_mismatches'], 2)
        self.assertIsNone(files['work/cog/2.js']['build_config'])
        self.assertEqual(files['work/cog/2.js']['build_config_mismatches'], 0)
        self.assertEqual(review['runs'][0]['work']['episodes']['1']['pending'], 3)
        self.assertEqual(review['runs'][0]['work']['episodes']['2']['pending'], 0)
        build = payload['builds'][5]
        self.assertEqual(build['nodes'][1]['body'], 'b5-1')
        self.assertEqual(build['nodes'][1]['input_summaries'][0]['body'], 'b5-0')
        self.assertEqual(build['pending']['1'], build['nodes'][1]['input_summaries'])
        self.assertEqual(build['summaries'][0]['copied_span'], [0, 5])
        self.assertEqual(build['summaries'][0]['input_tokens'], 7)
        self.assertEqual(build['summaries'][0]['failure'], 'test: diagnostic')
        self.assertEqual(payload['texts']['own-output'], 'own summary')
        self.assertEqual(payload['texts']['own-reason'], 'faithful')
        refs = set()
        def walk(value):
            if isinstance(value, dict):
                for key, child in value.items():
                    if key.endswith('_hash') and isinstance(child, str):
                        refs.add(child)
                    walk(child)
            elif isinstance(value, list):
                for child in value:
                    walk(child)
        walk({k: v for k, v in payload.items() if k != 'texts'})
        self.assertEqual(set(payload['texts']), refs)
        # Added build diagnostics must not affect any question stage/cell.
        self.assertEqual(review['results'], before['results'])

    def test_after_query_hash_mismatch_and_missing_states(self):
        late = next(r for r in self.records if r.get("kind") == "build" and r.get("seq") == 5)
        self.records.remove(late)
        self.records.append(late)
        self.records[5]["text_sha256"] = "mismatch"  # e4: never guess source by seq alone.
        self.save()
        review, files = self.build()
        fifth = review["results"]["cog"]["q5"]["work"]
        self.assertEqual(fifth["stage"], "not_written")
        self.assertEqual(fifth["evidence"][0]["written"], "after_query")
        self.assertEqual(fifth["builds_before"], 5)
        fourth = review["results"]["cog"]["q4"]["work"]
        self.assertEqual(fourth["stage"], "not_written")
        self.assertIsNone(fourth["evidence"][0]["written"])
        self.assertEqual(files["work/cog/1.js"]["mapping_mismatches"], 1)
        self.assertIsNone(files["work/cog/1.js"]["builds"][4]["source_id"])

    def test_cli_repeatable_work_atomic_deterministic_js_json(self):
        command = [sys.executable, "-B", str(SCRIPT), "--suite", str(self.suite), "--run", f"cog={self.run}",
                   "--run", f"second={self.run}", "--work", "cog", "--work", "second", "--glossary", str(self.glossary), "--out", str(self.root / "out")]
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        out = self.root / "out"
        before = {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()}
        self.assertEqual(len(before), 10)  # Main pair plus two episodes per run.
        result = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(before, {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()})
        for run in ("cog", "second"):
            for ep in ("1", "2"):
                js = (out / f"work/{run}/{ep}.js").read_text()
                prefix = f'(window.MEMEVAL_BEAM_WORK = window.MEMEVAL_BEAM_WORK || {{}})["{run}/{ep}"] = '
                self.assertTrue(js.startswith(prefix) and js.endswith(';\n'))
                self.assertEqual(json.loads(js[len(prefix):-2]), json.loads((out / f"work/{run}/{ep}.json").read_text()))
                self.assertNotIn('</script>', js)

    def test_work_validation_off_missing_and_no_legacy_output_changes(self):
        review, _ = self.build()
        with self.assertRaisesRegex(ValueError, "no matching"):
            beam.build_work(review, self.suite, [("cog", self.run)], ["absent"])
        review["runs"][0]["sut"]["name"] = "bm25"
        with self.assertRaisesRegex(ValueError, "requires"):
            beam.build_work(review, self.suite, [("cog", self.run)], ["cog"])
        self.log.write_text('sut_trace {"kind":"config","work_trace":"off"}\n')
        with contextlib.redirect_stderr(io.StringIO()):
            review, files = self.build()
        self.assertEqual(review["results"]["cog"]["q0"]["work"]["trace"], "off")
        self.log.unlink()
        with contextlib.redirect_stderr(io.StringIO()):
            review, _ = self.build()
        self.assertEqual(review["results"]["cog"]["q0"]["work"]["trace"], "missing")
        with self.assertRaisesRegex(ValueError, "unsafe work path"):
            beam.safe_component('../outside')
        # Adding corrupt-line accounting must not change the existing case API.
        self.save()
        trace = common.WorkTraces(self.run)
        case = {"namespace": "ns-1"}
        args = (case, [e['source_id'] for e in self.events], {e['source_id']: e for e in self.events}, "问题 5", {})
        with_corrupt = trace.case(*args)
        self.assertEqual(trace.corrupt_lines['ns-1'], 1)
        self.log.write_text(self.log.read_text().split('\n', 1)[1])
        self.assertEqual(common.WorkTraces(self.run).case(*args), with_corrupt)

    def multi_namespace_fixture(self, assignments):
        self.questions = [{'id': f'q{i}', 'query': f'问题 {i}', 'gold': {'evidence_ids': ['e5']}}
                          for i in range(20)]
        self.episodes = [{'id': '1', 'events': self.events, 'cases': self.questions}]
        self.report['cases'] = [{'case_id': q['id'], 'episode_id': '1', 'namespace': ns,
                                 'status': 'scored', 'repeat_index': 0}
                                for q, ns in reversed(list(zip(self.questions, assignments)))]
        put(self.run / 'report.json', self.report)
        put_lines(self.suite / 'episodes.jsonl', self.episodes)
        put_lines(self.suite / 'episodes.index.jsonl', [{'id': '1', 'events': 6, 'cases': 20}])
        template = next(r for r in self.records if r.get('query') == '问题 5')
        logs = {}
        for ns in dict.fromkeys(assignments):
            records = [{'kind': 'config', 'work_trace': 'full', 'jev_threshold': 0.5}]
            for i, event in enumerate(self.events):
                build = work_build(i, event['content'], tree=i == 5)
                for member in build['snapshot']['members']:
                    member['body']['text'] += ' ' + ns
                build['snapshot']['pending'] = {'1': [{'body': f'b{i}-0', 'text': 'pending ' + ns}]}
                records.append(build)
            # The original namespace has all queries, including stale results
            # for the three retried cases. Only their new namespace may be used.
            for q, assigned in zip(self.questions, assignments):
                if ns != 'A' and assigned != ns:
                    continue
                search = copy.deepcopy(template)
                search['query'] = q['query']
                search['result']['context_view']['rendered'] += ' ' + ns
                if ns != 'A':
                    search['result']['items'] = []
                    search['result']['omitted'] = {'n5-3': 'jev_below_threshold'}
                records.append(search)
            path = self.run / f'logs/resume-{ns}/0001-{ns}/sut.stderr.log'
            put(path.parent / 'episode.json', {'namespace': ns})
            corrupt = 1 if ns == 'A' else 2
            path.write_text('sut_trace {broken\n' * corrupt + ''.join(
                'sut_trace ' + json.dumps(r, ensure_ascii=False) + '\n' for r in records), encoding='utf-8')
            logs[ns] = path
        return logs

    def test_multiple_namespaces_route_per_case_aggregate_and_determinism(self):
        assignments = ['B' if i in (0, 7, 19) else 'A' for i in range(20)]
        self.multi_namespace_fixture(assignments)
        value, files = self.build()
        self.assertEqual(list(files), ['work/cog/1.js', 'work/cog/1-r1.js'])
        for index, ns in enumerate(('A', 'B')):
            filename = 'work/cog/1' + ('-r1' if index else '') + '.js'
            payload = files[filename]
            self.assertEqual((payload['namespace'], payload['namespace_index']), (ns, index))
            self.assertEqual(payload['episode_id'], '1')
            expected = [q['id'] for q, assigned in zip(self.questions, assignments) if assigned == ns]
            self.assertEqual(list(payload['searches']), expected)
            self.assertEqual(len(payload['builds']), 6)  # Each namespace ingests the entire episode.
            refs = set()
            def walk(item):
                if isinstance(item, dict):
                    for key, child in item.items():
                        if key.endswith('_hash') and isinstance(child, str): refs.add(child)
                        walk(child)
                elif isinstance(item, list):
                    for child in item: walk(child)
            walk({k: v for k, v in payload.items() if k != 'texts'})
            self.assertEqual(set(payload['texts']), refs)
            other = 'B' if ns == 'A' else 'A'
            self.assertNotIn('pending ' + other, payload['texts'].values())
            for qid in expected:
                cell = value['results']['cog'][qid]['work']
                self.assertEqual(cell['file'], filename)
                self.assertEqual(cell['stage'], 'covered' if ns == 'A' else 'jev_rejected')
                self.assertEqual(cell['builds_before'], 6)
        summary = value['runs'][0]['work']['episodes']['1']
        self.assertEqual(summary, {'file': 'work/cog/1.js', 'files': list(files), 'namespaces': 2,
                                  'trace': 'full', 'builds': 12, 'searches': 20, 'corrupt_lines': 3,
                                  'pending': 12, 'states': {'Ready': 12}, 'levels': {'0': 12, '1': 2, '2': 2, '3': 2},
                                  'edges': {'SummaryParent': 4, 'ContextFallback': 2}})
        command = [sys.executable, '-B', str(SCRIPT), '--suite', str(self.suite), '--run', f'cog={self.run}',
                   '--work', 'cog', '--glossary', str(self.glossary), '--out', str(self.root / 'multi-out')]
        first = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        out = self.root / 'multi-out'
        before = {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()}
        second = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(first.stdout, second.stdout)
        self.assertEqual(before, {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()})
        self.assertEqual(len(before), 6)
        for stem in ('1', '1-r1'):
            js = (out / f'work/cog/{stem}.js').read_text()
            prefix = f'(window.MEMEVAL_BEAM_WORK = window.MEMEVAL_BEAM_WORK || {{}})["cog/{stem}"] = '
            self.assertTrue(js.startswith(prefix))
            self.assertEqual(json.loads(js[len(prefix):-2]), json.loads((out / f'work/cog/{stem}.json').read_text()))

    def test_namespace_ties_and_secondary_order_follow_suite_not_report(self):
        self.multi_namespace_fixture(['B', 'A'] * 10)
        _, files = self.build()
        self.assertEqual([p['namespace'] for p in files.values()], ['B', 'A'])
        self.multi_namespace_fixture(['B', 'C', 'B', 'C', 'B'] + ['A'] * 15)
        value, files = self.build()
        self.assertEqual(list(files), ['work/cog/1.js', 'work/cog/1-r1.js', 'work/cog/1-r2.js'])
        self.assertEqual([p['namespace'] for p in files.values()], ['A', 'B', 'C'])
        self.assertEqual([p['namespace_index'] for p in files.values()], [0, 1, 2])
        self.assertEqual(value['runs'][0]['work']['episodes']['1']['namespaces'], 3)

    def test_missing_namespace_trace_is_mixed_and_paths_stay_guarded(self):
        logs = self.multi_namespace_fixture(['A'] * 17 + ['B'] * 3)
        logs['B'].unlink()
        with contextlib.redirect_stderr(io.StringIO()):
            value, files = self.build()
        self.assertEqual(value['runs'][0]['work']['episodes']['1']['trace'], 'mixed')
        self.assertEqual(value['results']['cog']['q19']['work']['stage'], 'no_search')
        self.assertEqual(value['results']['cog']['q19']['work']['file'], 'work/cog/1-r1.js')
        self.assertEqual(files['work/cog/1-r1.js']['texts'], {})
        for index in (-1, '../bad', True):
            bad = copy.deepcopy(files)
            bad['work/cog/1-r1.js']['namespace_index'] = index
            with self.assertRaises(ValueError):
                beam.write_review(value, self.root / 'invalid-out', bad)
        bad = copy.deepcopy(files)
        bad['work/cog/1-r1.js']['episode_id'] = '../outside'
        with self.assertRaisesRegex(ValueError, 'unsafe work path'):
            beam.write_review(value, self.root / 'invalid-out', bad)



class ReaderPromptTests(unittest.TestCase):
    save = BeamWorkTests.save

    def setUp(self):
        BeamWorkTests.setUp(self)
        env = patch.dict(os.environ, {'MEMEVAL_OPENAI_BASE_URL': ''})
        env.start()
        self.addCleanup(env.stop)
        self.endpoint = 'https://reader.fixture.invalid/base/'
        self.template_sha = hashlib.sha256(beam.READER_TEMPLATES.read_bytes()).hexdigest()
        self.report['reader'] = {'input_mode': 'items', 'context_budget_chars': 13,
                                 'provider': 'openai-compat', 'model': 'fixture-model',
                                 'effective_max_tokens': 512, 'template_sha256': self.template_sha,
                                 'notes': ['preserve arbitrary reader metadata']}
        self.called = next(c for c in self.report['cases'] if c['case_id'] == 'q5')
        self.called.update(query='report query <&> {query}', sample_id='q5#0',
                           response={'items': [{'text': 'report🙂'}, {'text': '<tag>'}, {'text': 'tail'}]},
                           qa={'reader_request_sha': '0' * 64, 'truncated': True, 'dropped_sessions': 1,
                               'reader_output': {'effective_max_tokens': 2048, 'finish_reason': 'stop',
                                                 'completion_tokens': 21, 'reasoning_tokens': 3}})
        self.no_trace = next(c for c in self.report['cases'] if c['case_id'] == 'q7')
        self.no_trace.update(query='reader ran without a usable search trace', sample_id='q7#0', status='scored',
                             response={'items': []}, qa={'reader_request_sha': '0' * 64, 'truncated': False})
        template = beam.reader_template(self.report['reader'])
        self.ledger_rows = []
        for case in (self.called, self.no_trace):
            prompt = beam.reader_prompt(case, self.report['reader'], template, None)
            sample = 'opaque-evaluator-sample-' + str(len(self.ledger_rows))
            case['qa']['reader_request_sha'] = beam.reader_request_sha(prompt, sample, beam.reader_endpoint(self.endpoint))
            self.ledger_rows.append({'purpose': 'reader', 'request_sha': case['qa']['reader_request_sha'],
                                     'sample_id': sample, 'prompt_tokens': 12345, 'completion_tokens': 21,
                                     'latency_ms': 1250.5, 'ts': '2026-10-31T01:00:00Z',
                                     'model_returned': 'fixture-model-returned', 'extra': 'not exported'})
        self.ledger_path = self.run / 'llm_ledger.jsonl'
        put_lines(self.ledger_path, self.ledger_rows)
        repeat = copy.deepcopy(self.called)
        repeat.update(repeat_index=1, sample_id='q5#1', response={'items': [{'text': 'WRONG REPEAT'}]})
        self.report['cases'].append(repeat)
        self.save()

    def build(self, endpoint=None):
        runs = [('cog', self.run)]
        review = beam.build_review(self.suite, runs, self.glossary)
        files = beam.build_work(review, self.suite, runs, ['cog'], endpoint)
        return review, files

    def test_report_messages_metadata_and_verified_hash(self):
        review, files = self.build(self.endpoint)
        prompt = files['work/cog/1.js']['searches']['q5']['reader_prompt']
        note = json.loads(beam.READER_TEMPLATES.read_bytes())['reader_note']
        self.assertEqual(prompt['messages'], [{'role': 'user', 'content': 'report🙂\n<tag>'},
                                              {'role': 'user', 'content': note.replace('{query}', self.called['query'])}])
        self.assertEqual(prompt['template_id'], 'beam/reader-v1@' + self.template_sha + '/input=items')
        self.assertEqual(beam.reader_endpoint(self.endpoint), 'https://reader.fixture.invalid/base/chat/completions')
        self.assertEqual({k: prompt[k] for k in ('items_returned', 'items_in_prompt', 'dropped_items', 'truncated', 'chars')},
                         {'items_returned': 3, 'items_in_prompt': 2, 'dropped_items': 1, 'truncated': True, 'chars': 13})
        self.assertEqual((prompt['temperature'], prompt['max_tokens'], prompt['n']), (0, 2048, 1))
        self.assertEqual(prompt['reader_output'], self.called['qa']['reader_output'])
        self.assertEqual(prompt['request_sha'], self.called['qa']['reader_request_sha'])
        self.assertEqual(prompt['ledger'], {k: self.ledger_rows[0][k] for k in beam.READER_LEDGER_FIELDS})
        self.assertNotIn('sample_id', prompt['ledger'])
        self.assertTrue(prompt['verified'])
        self.assertIsNone(prompt['verify_note'])
        self.assertEqual(review['runs'][0]['reader'], self.report['reader'])
        self.assertNotIn('reader.fixture.invalid', common.encode(files) + common.encode(review))
        self.assertNotIn('完整路径', common.encode(prompt))
        self.assertNotIn('WRONG REPEAT', common.encode(prompt))
        again, repeated = self.build(self.endpoint)
        self.assertEqual(common.encode(files), common.encode(repeated))
        self.assertEqual(common.encode(review), common.encode(again))

    def test_prompt_without_trace_preserves_search_stage_and_counts(self):
        review, files = self.build(self.endpoint)
        payload = files['work/cog/1.js']
        no_trace = payload['searches']['q7']
        self.assertEqual(set(no_trace), {'reader_prompt'})
        self.assertEqual(len(no_trace['reader_prompt']['messages']), 1)
        self.assertEqual(no_trace['reader_prompt']['max_tokens'], 512)  # report fallback
        self.assertTrue(no_trace['reader_prompt']['verified'])
        self.assertEqual(review['runs'][0]['work']['episodes']['1']['searches'], 7)
        self.assertEqual(review['results']['cog']['q7']['work']['stage'], 'no_search')
        self.assertIsNone(review['results']['cog']['q7']['attempt'])
        self.assertIsNone(payload['searches']['q0']['reader_prompt'])
        self.assertEqual(files['work/cog/2.js']['searches']['other'], {'reader_prompt': None})

    def test_missing_endpoint_environment_fallback_and_cli_override(self):
        _, files = self.build()
        prompt = files['work/cog/1.js']['searches']['q5']['reader_prompt']
        self.assertIsNone(prompt['verified'])
        self.assertIn('未提供 endpoint', prompt['verify_note'])
        with patch.dict(os.environ, {'MEMEVAL_OPENAI_BASE_URL': self.endpoint}):
            _, files = self.build()
            self.assertTrue(files['work/cog/1.js']['searches']['q5']['reader_prompt']['verified'])
            _, files = self.build('https://different.fixture.invalid/v1')
            self.assertFalse(files['work/cog/1.js']['searches']['q5']['reader_prompt']['verified'])
        out = self.root / 'cli-prompt'
        with patch.dict(os.environ, {'MEMEVAL_OPENAI_BASE_URL': 'https://ignored.fixture.invalid'}):
            result = subprocess.run([sys.executable, '-B', str(SCRIPT), '--suite', str(self.suite),
                                     '--run', f'cog={self.run}', '--work', 'cog', '--glossary', str(self.glossary),
                                     '--out', str(out), '--llm-endpoint', self.endpoint], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads((out / 'work/cog/1.json').read_text())['searches']['q5']['reader_prompt']['verified'])
        self.assertNotIn(self.endpoint, result.stdout + result.stderr)

    def test_hash_and_truncation_mismatch_warn_without_changing_messages(self):
        _, original = self.build(self.endpoint)
        expected = original['work/cog/1.js']['searches']['q5']['reader_prompt']
        self.report['reader']['template_sha256'] = 'old template'
        self.called['qa'].update(reader_request_sha='f' * 64, truncated=False, dropped_sessions=0)
        self.ledger_rows[0]['request_sha'] = 'f' * 64
        put_lines(self.ledger_path, self.ledger_rows)
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as errors:
            _, files = self.build(self.endpoint)
        prompt = files['work/cog/1.js']['searches']['q5']['reader_prompt']
        self.assertFalse(prompt['verified'])
        self.assertIn(expected['request_sha'][:12], prompt['verify_note'])
        self.assertEqual(prompt['messages'], expected['messages'])
        self.assertEqual(prompt['template_id'], expected['template_id'])
        self.assertIn('template_sha256 differs', errors.getvalue())
        self.assertIn('truncated differs', errors.getvalue())
        self.assertNotIn('dropped_items differs', errors.getvalue())

    def test_ledger_uses_final_reader_row_and_does_not_export_other_fields(self):
        final = dict(self.ledger_rows[0], prompt_tokens=0, completion_tokens=0, latency_ms=20.5,
                     ts='2026-10-31T02:00:00Z', model_returned='retry-model')
        judge = dict(final, purpose='judge', sample_id='wrong-judge-sample', prompt_tokens=999)
        put_lines(self.ledger_path, [*self.ledger_rows, final, judge])
        with self.ledger_path.open('a') as stream:
            stream.write('{incomplete ledger line\n')
        with contextlib.redirect_stderr(io.StringIO()) as errors:
            _, files = self.build(self.endpoint)
        prompt = files['work/cog/1.js']['searches']['q5']['reader_prompt']
        self.assertTrue(prompt['verified'])
        self.assertEqual(prompt['ledger'], {k: final[k] for k in beam.READER_LEDGER_FIELDS})
        self.assertIn('1 malformed ledger lines skipped', errors.getvalue())
        self.assertNotIn('incomplete ledger line', errors.getvalue())

    def test_missing_ledger_file_or_matching_reader_row_remains_unverified(self):
        for rows in (None, [dict(self.ledger_rows[0], purpose='judge')],
                     [dict(self.ledger_rows[0], request_sha='not-this-request')]):
            with self.subTest(rows=rows):
                if rows is None:
                    self.ledger_path.unlink()
                else:
                    put_lines(self.ledger_path, rows)
                _, files = self.build(self.endpoint)
                prompt = files['work/cog/1.js']['searches']['q5']['reader_prompt']
                self.assertIsNone(prompt['verified'])
                self.assertIsNone(prompt['ledger'])
                self.assertEqual(prompt['verify_note'], 'ledger 无此 request_sha 的 reader 行')
                self.assertEqual(prompt['messages'][0]['content'], 'report🙂\n<tag>')

    def test_ledger_is_read_once_per_directory_even_with_run_aliases(self):
        runs = [('cog', self.run), ('alias', self.run)]
        review = beam.build_review(self.suite, runs, self.glossary)
        opened = []
        original = Path.open
        def tracked(path, *args, **kwargs):
            if path == self.ledger_path:
                opened.append(path)
            return original(path, *args, **kwargs)
        with patch.object(Path, 'open', tracked):
            files = beam.build_work(review, self.suite, runs, ['cog', 'alias'], self.endpoint)
        self.assertEqual(opened, [self.ledger_path])
        for name in ('cog', 'alias'):
            for ident in ('q5', 'q7'):
                self.assertTrue(files[f'work/{name}/1.js']['searches'][ident]['reader_prompt']['verified'])

    def test_unsupported_or_missing_input_and_endpoint_do_not_invent_prompt(self):
        template = beam.reader_template(self.report['reader'])
        ledger = beam.reader_ledger(self.run)
        with contextlib.redirect_stderr(io.StringIO()):
            for override in ({'input_mode': 'sources'}, {'fullcontext': True}, {'context_budget_chars': None}):
                self.assertIsNone(beam.reader_prompt(self.called, {**self.report['reader'], **override}, template, None))
        prompt = beam.reader_prompt(self.called, self.report['reader'], template, 'https://name:fake-secret@fixture.invalid', ledger)
        self.assertIsNone(prompt['verified'])
        self.assertNotIn('fake-secret', common.encode(prompt))
        missing = {**self.called, 'sample_id': None}
        prompt = beam.reader_prompt(missing, self.report['reader'], template, self.endpoint, ledger)
        self.assertTrue(prompt['verified'])  # Never use the report's public sample_id.
        ledger[self.called['qa']['reader_request_sha']]['sample_id'] = None
        prompt = beam.reader_prompt(self.called, self.report['reader'], template, self.endpoint, ledger)
        self.assertIsNone(prompt['verified'])
        self.assertIn('sample_id', prompt['verify_note'])


class BeamAttemptTests(unittest.TestCase):
    setUp = BeamWorkTests.setUp
    save = BeamWorkTests.save
    build = BeamWorkTests.build

    def attempts_fixture(self, old_ids=(5,), new_ids=(4,)):
        ids = list(dict.fromkeys((*old_ids, *new_ids)))
        self.questions = [q for q in self.questions if q['id'] in {f'q{i}' for i in ids}]
        self.episodes = [{'id': '1', 'events': self.events, 'cases': self.questions}]
        self.report['cases'] = [c for c in self.report['cases'] if c['case_id'] in {q['id'] for q in self.questions}]
        put_lines(self.suite / 'episodes.index.jsonl', [{'id': '1', 'events': len(self.events), 'cases': len(ids)}])
        original = copy.deepcopy(self.records)
        builds = [r for r in original if r['kind'] != 'search']
        searches = {i: next(r for r in original if r['kind'] == 'search' and r['query'] == f'问题 {i}') for i in ids}
        self.records = builds + [searches[i] for i in sorted(old_ids)]
        self.save()
        self.write_attempt('root', self.records)
        newer = copy.deepcopy(builds)
        for row in newer:
            if row['kind'] == 'build':
                row['state'] = 'Failed'
                for member in row['snapshot']['members']:
                    member['body']['text'] = 'new body ' + member['body']['text']
        for i in sorted(new_ids):
            search = copy.deepcopy(searches[i])
            search['result']['context_view'].update(tokens=99, rendered='new context')
            newer.append(search)
        self.write_attempt('resume-3', newer)
        return newer

    def write_attempt(self, attempt, records):
        path = self.log if attempt == 'root' else self.run / f'logs/{attempt}/0001-ns-1/sut.stderr.log'
        put(path.parent / 'episode.json', {'namespace': 'ns-1'})
        path.write_text(''.join('sut_trace ' + json.dumps(r, ensure_ascii=False) + '\n' for r in records), encoding='utf-8')

    def test_questions_resolve_independently_with_own_stage_and_texts(self):
        self.attempts_fixture()
        value, files = self.build()
        payload = files['work/cog/1.js']
        self.assertIn('logs/resume-3/', payload['log']['path'])  # Equal votes: newest wins.
        old = value['results']['cog']['q5']['work']
        new = value['results']['cog']['q4']['work']
        self.assertEqual(old['stage'], 'covered')  # The primary attempt's builds all failed.
        self.assertEqual(old['context_tokens'], 10)
        self.assertEqual((old['attempt'], old['attempt_mismatch']), ('root', True))
        self.assertEqual(value['results']['cog']['q5']['attempt'], old['attempt'])
        self.assertEqual(value['results']['cog']['q4']['attempt'], 'resume-3')
        self.assertIn('本题 trace 来自另一次 attempt，段级构建计数可能不对应', old['warnings'])
        self.assertEqual(new['context_tokens'], 99)
        self.assertEqual(new['stage'], 'not_written')
        self.assertNotIn('attempt_mismatch', new)
        self.assertEqual(set(payload['searches']), {'q4', 'q5'})
        self.assertEqual(len(payload['builds']), len(self.events))
        self.assertTrue(all(b['state'] == 'Failed' for b in payload['builds']))
        refs = set()
        def walk(v):
            if isinstance(v, str) and v.startswith('sha256-16:'):
                refs.add(v)
            elif isinstance(v, dict):
                for child in v.values(): walk(child)
            elif isinstance(v, list):
                for child in v: walk(child)
        walk({k: v for k, v in payload.items() if k != 'texts'})
        self.assertEqual(refs, set(payload['texts']))
        self.assertIn('完整路径 </script>', payload['texts'].values())
        self.assertIn('new context', payload['texts'].values())
        out = self.root / 'out'
        beam.write_review(value, out, files)
        before = {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()}
        second, files2 = self.build()
        beam.write_review(second, out, files2)
        self.assertEqual(before, {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()})

    def test_majority_of_matched_questions_can_choose_older_attempt(self):
        self.attempts_fixture(old_ids=(3, 5), new_ids=(4,))
        value, files = self.build()
        self.assertEqual(files['work/cog/1.js']['log']['path'], 'logs/0002-ns-1/sut.stderr.log')
        old = value['results']['cog']['q5']['work']
        new = value['results']['cog']['q4']['work']
        self.assertNotIn('attempt_mismatch', old)
        self.assertEqual((new['attempt'], new['attempt_mismatch']), ('resume-3', True))
        self.assertEqual(value['results']['cog']['q5']['attempt'], 'root')
        self.assertEqual(value['results']['cog']['q4']['attempt'], new['attempt'])
        self.assertEqual(new['stage'], 'not_written')

    def test_cell_attempt_requires_a_matching_work_search(self):
        value, _ = self.build()
        self.assertEqual(value['results']['cog']['q5']['attempt'], 'root')
        self.assertIsNone(value['results']['cog']['q7']['attempt'])
        self.assertIsNone(value['results']['cog']['other']['attempt'])

    def test_latest_matching_attempt_uses_numeric_order_and_error_records(self):
        newer = self.attempts_fixture()
        self.write_attempt('resume-10', [{**newer[-1], 'result': None, 'error': {'kind': 'MODEL_UNAVAILABLE'}}])
        self.write_attempt('resume-11', [{'kind': 'config', 'work_trace': 'off'}])
        value, files = self.build()
        payload = files['work/cog/1.js']
        self.assertIn('logs/resume-10/', payload['log']['path'])
        self.assertEqual(payload['searches']['q4']['error'], {'kind': 'MODEL_UNAVAILABLE'})
        self.assertEqual(value['results']['cog']['q5']['work']['stage'], 'covered')

    def test_single_attempt_legacy_export_bytes_unchanged(self):
        value, files = self.build()
        baseline = {'files': without_reader_prompts(files), 'work': {k: v['work'] for k, v in value['results']['cog'].items()}}
        self.assertEqual(hashlib.sha256(common.encode(baseline).encode()).hexdigest(),
                         '19abb0dc922f7fdb29142933af28b7dcea70adebcf6f65458381846f0a094607')

    def test_latest_complete_attempt_keeps_previous_export_bytes(self):
        self.write_attempt('resume-2', self.records)
        before, files = self.build()
        self.log.unlink()
        after, only_latest = self.build()
        self.assertEqual(common.encode(files), common.encode(only_latest))
        self.assertEqual(common.encode(before), common.encode(after))


class LeafMemoryBeamTests(unittest.TestCase):
    save = BeamWorkTests.save
    build = BeamWorkTests.build

    def setUp(self):
        from test_build_case_review import leaf_memory_fixture
        BeamWorkTests.setUp(self)
        self.events, questions, self.records = leaf_memory_fixture()
        self.questions = [{**q, 'gold': {'evidence_ids': q['evidence_ids']}} for q in questions]
        self.episodes = [{'id': '1', 'events': self.events, 'cases': self.questions}]
        self.report['cases'] = [{'case_id': 'q', 'episode_id': '1', 'namespace': 'ns-1', 'status': 'scored'}]
        put_lines(self.suite / 'episodes.index.jsonl', [{'id': '1', 'events': 6, 'cases': 1}])
        self.save()

    def test_v3_summary_stages_and_output_determinism(self):
        review, files = self.build()
        payload = files['work/cog/1.js']
        self.assertEqual(payload['version'], 3)
        self.assertEqual(payload['representation'], 'leaf_memory')
        cell = review['results']['cog']['q']['work']
        self.assertEqual(cell['stage'], 'covered')
        self.assertEqual(cell['packed']['memories'], 2)
        self.assertEqual(cell['jev_summary']['scored'], 48)
        self.assertEqual(cell['jev_summary']['admitted'], 47)
        self.assertAlmostEqual(cell['jev_summary']['useful_mean'], (47 * .8 + .1) / 48)
        self.assertAlmostEqual(cell['jev_summary']['uncertain_mean'], .1)
        self.assertEqual(cell['omitted'], {'CONTEXT_BUNDLE_TOO_LARGE': 45, 'jev_below_threshold': 1})
        summary = review['runs'][0]['work']['episodes']['1']
        self.assertEqual((summary['representation'], summary['scopes'], summary['snapshots']), ('leaf_memory', 1, 2))
        self.assertEqual(summary['members_by_level'], {'0': 48, '1': 1, '2': 1})
        self.assertEqual(summary['leaf_tokens'], {'mean': 123.5, 'max': 147, 'n': 48})
        out = self.root / 'v3-output'
        beam.write_review(review, out, files)
        first = {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()}
        review2, files2 = self.build()
        beam.write_review(review2, out, files2)
        self.assertEqual(first, {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()})
        self.assertEqual(json.loads(first[Path('work/cog/1.json')]), payload)
        self.assertTrue(first[Path('work/cog/1.js')].startswith(b'(window.MEMEVAL_BEAM_WORK = window.MEMEVAL_BEAM_WORK || {})["cog/1"] = '))

    def test_v6_legacy_new_snapshot_fields_stay_v2(self):
        self.report['sut']['variant'] = 'native-legacy-test'
        self.records[-1]['result']['settings'].update(
            representation='FullPath', jev_input_format='AncestorContextV1',
            jev_prompt_profile='memory-reference-v1')
        self.records[1]['wait_windows'] = [{'method': 'add_batch', 'window_s': 10}]
        self.save()
        review, files = self.build()
        payload = files['work/cog/1.js']
        self.assertEqual(payload['version'], 2)
        self.assertNotIn('scopes', payload)
        self.assertNotIn('representation', payload)
        self.assertNotIn('representation', review['runs'][0]['work']['episodes']['1'])
        self.assertNotIn('representation', review['results']['cog']['q']['work'])
        self.assertIn('summaries', payload['builds'][0])
        # The byte baseline excludes the unrelated wait_windows fixture field.
        self.records[1].pop('wait_windows')
        self.save()
        review, files = self.build()
        baseline = {'files': without_reader_prompts(files), 'work': {k: v['work'] for k, v in review['results']['cog'].items()}}
        self.assertEqual(hashlib.sha256(common.encode(baseline).encode()).hexdigest(),
                         'ecd64cc9b5d6951855f185a7a430379b6af39a0dd4e54917fcb94425a65a71e3')

    def test_leaf_attempt_selection_keeps_question_diagnostics(self):
        old = copy.deepcopy(self.records[-1])
        self.questions.append({**self.questions[0], 'id': 'q-new', 'query': 'new query'})
        self.report['cases'].append({**self.report['cases'][0], 'case_id': 'q-new'})
        self.save()
        newer = copy.deepcopy(self.records[:-1])
        for row in newer:
            if row['kind'] == 'build':
                row['state'] = 'Failed'
        search = copy.deepcopy(old)
        search['query'] = 'new query'
        search['result']['packed']['memories'] = 1
        newer.append(search)
        path = self.run / 'logs/resume-3/0001-ns-1/sut.stderr.log'
        put(path.parent / 'episode.json', {'namespace': 'ns-1'})
        path.write_text(''.join('sut_trace ' + json.dumps(r) + '\n' for r in newer))
        review, files = self.build()
        payload = files['work/cog/1.js']
        previous = review['results']['cog']['q']['work']
        current = review['results']['cog']['q-new']['work']
        self.assertEqual((previous['stage'], previous['attempt']), ('covered', 'root'))
        self.assertTrue(previous['attempt_mismatch'])
        self.assertEqual((previous['packed']['memories'], current['packed']['memories']), (2, 1))
        self.assertEqual(previous['jev_summary']['scored'], 48)
        self.assertEqual(payload['searches']['q']['packed'], old['result']['packed'])
        self.assertEqual(len(payload['searches']['q']['jev_trace']), len(old['result']['jev_trace']))
        self.assertEqual(len(payload['scopes']), 1)
        self.assertTrue(all(b['state'] == 'Failed' for b in payload['builds']))

    def test_leaf_repeated_builds_preserve_stage_and_compact_index(self):
        baseline, _ = self.build()
        failed = copy.deepcopy(self.records[1])
        failed.update(state='Failed', snapshot=None, audit={}, resume_count=0)
        self.records.insert(1, failed)
        self.records[2]['resume_count'] = 1
        self.save()
        review, files = self.build()
        payload = files['work/cog/1.js']
        self.assertEqual(len(payload['builds']), 6)
        self.assertEqual(payload['builds'][0]['attempts'], ['Failed', 'Ready'])
        self.assertEqual(payload['searches']['q']['builds_before'], 6)
        self.assertEqual(review['runs'][0]['work']['episodes']['1']['builds'], 6)
        self.assertEqual(review['results']['cog']['q']['work'], baseline['results']['cog']['q']['work'])

    def test_leaf_compaction_does_not_use_future_ready_state(self):
        failed = copy.deepcopy(self.records[1])
        failed.update(state='Failed', snapshot=None, audit={})
        early = copy.deepcopy(self.records[-1])
        early['query'] = 'early synthetic query'
        self.records[1:1] = [failed, early]
        self.questions.insert(0, {'id': 'early', 'query': early['query'], 'gold': {'evidence_ids': ['e0']}})
        self.report['cases'].insert(0, {'case_id': 'early', 'episode_id': '1', 'namespace': 'ns-1', 'status': 'error'})
        self.save()
        review, files = self.build()
        payload = files['work/cog/1.js']
        self.assertEqual(payload['builds'][0]['state'], 'Ready')
        self.assertEqual(payload['builds'][0]['attempts'], ['Failed', 'Ready'])
        self.assertEqual(payload['searches']['early']['builds_before'], 1)
        self.assertEqual(review['results']['cog']['early']['work']['stage'], 'not_written')
        self.assertEqual(review['results']['cog']['early']['work']['evidence'][0]['written'], 'Failed')
        self.assertEqual(review['results']['cog']['q']['work']['stage'], 'covered')

    def test_leaf_stage_outcomes_not_reinterpreted_as_binary_probabilities(self):
        _, files = self.build()
        payload = files['work/cog/1.js']
        trace = common.WorkTraces(self.run)
        _, chronology = trace.episode('ns-1', self.events, self.questions, {})
        question = {'id': 'q', 'evidence_ids': ['e2']}
        cell = beam.work_cell(question, payload, chronology, 'work/cog/1.js')
        self.assertEqual(cell['stage'], 'not_packed')
        search = payload['searches']['q']
        search['candidates'] = search['candidates'][-1:]
        search['items'] = []
        cell = beam.work_cell({'id': 'q', 'evidence_ids': ['e5']}, payload, chronology, 'work/cog/1.js')
        self.assertEqual(cell['stage'], 'jev_rejected')
        self.assertEqual(cell['jev_summary']['admitted'], 0)
        self.assertIsNone(search['candidates'][0]['p_use'])
        payload['searches'].clear()
        cell = beam.work_cell(question, payload, chronology, 'work/cog/1.js')
        self.assertEqual(cell['stage'], 'no_search')
        self.assertIsNone(cell['packed'])
        self.assertIsNone(cell['jev_summary'])

    def test_leaf_multi_namespace_scope_totals_are_independent(self):
        other = self.run / 'logs/resume-1/0001-ns-new/sut.stderr.log'
        put(other.parent / 'episode.json', {'namespace': 'ns-new'})
        duplicate = copy.deepcopy(self.records)
        duplicate[-1]['query'] = 'synthetic resumed lookup'
        other.write_text(''.join('sut_trace ' + json.dumps(r) + '\n' for r in duplicate), encoding='utf-8')
        self.questions.append({'id': 'q2', 'query': 'synthetic resumed lookup', 'gold': {'evidence_ids': ['e0']}})
        self.report['cases'].append({'case_id': 'q2', 'episode_id': '1', 'namespace': 'ns-new', 'status': 'scored'})
        self.save()
        review, files = self.build()
        self.assertEqual(len(files), 2)
        summary = review['runs'][0]['work']['episodes']['1']
        self.assertEqual((summary['scopes'], summary['snapshots'], summary['namespaces']), (2, 4, 2))
        self.assertEqual(summary['members_by_level'], {'0': 96, '1': 2, '2': 2})
        self.assertEqual(summary['leaf_tokens']['n'], 96)
        self.assertEqual(review['results']['cog']['q2']['work']['file'], 'work/cog/1-r1.js')

class TreeBeamTests(unittest.TestCase):
    save = BeamWorkTests.save
    build = BeamWorkTests.build

    def setUp(self):
        from test_build_case_review import tree_fixture
        BeamWorkTests.setUp(self)
        self.events, questions, self.records = tree_fixture()
        self.questions = [{**q, 'gold': {'evidence_ids': ['e0', 'e1', 'e2']}} for q in questions]
        self.episodes = [{'id': '1', 'events': self.events, 'cases': self.questions}]
        self.questions[0]['category'] = 'tree-category'
        self.report['cases'] = [{'case_id': 'q', 'episode_id': '1', 'namespace': 'ns-1', 'status': 'scored',
                                'response': {'items': [{'id': 'n0', 'source_ids': ['uuid-0']},
                                                       {'id': 'upper-1', 'source_ids': ['uuid-1', 'uuid-2']}]},
                                'retrieved_source_ids': ['e0', 'e1', 'e2']}]
        self.report['sut']['variant'] = 'native-tree-jev3-openai-embedding'
        put_lines(self.suite / 'episodes.index.jsonl', [{'id': '1', 'events': 6, 'cases': 1}])
        self.save()

    def test_tree_covered_sources_stats_and_episode_summary(self):
        review, files = self.build()
        payload = files['work/cog/1.js']
        self.assertEqual((payload['version'], payload['representation']), (3, 'tree'))
        cell = review['results']['cog']['q']['work']
        self.assertEqual(cell['stage'], 'covered')
        self.assertEqual(cell['covered'], [3, 3])
        self.assertEqual([e['stage'] for e in cell['evidence']], ['selected'] * 3)
        self.assertEqual(cell['evidence'][2]['best']['node'], 'upper-1')
        self.assertEqual(cell['representation'], 'tree')
        self.assertEqual(cell['tree'], {'items_by_level': {'0': 1, '1': 1}, 'recorded_items': 1,
                                      'direct_sources': 1, 'covered_sources': 3})
        self.assertNotIn('packed', cell)
        self.assertEqual(cell['jev_summary']['scored'], 3)
        self.assertEqual(cell['jev_summary']['admitted'], 2)
        self.assertAlmostEqual(cell['jev_summary']['useful_mean'], 1.7 / 3)
        summary = review['runs'][0]['work']['episodes']['1']
        self.assertEqual((summary['representation'], summary['scopes'], summary['snapshots']), ('tree', 1, 2))
        self.assertEqual(summary['members_by_level'], {'0': 48, '1': 1, '2': 1})
        self.assertEqual(summary['leaf_tokens'], {'n': 0, 'mean': None, 'max': None})
        self.assertEqual(summary['node_tokens_by_level'], {
            level: {'n': 0, 'mean': None, 'max': None} for level in ('0', '1', '2')})
        out = self.root / 'tree-output'
        beam.write_review(review, out, files)
        first = {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()}
        review, files = self.build()
        beam.write_review(review, out, files)
        self.assertEqual(first, {p.relative_to(out): p.read_bytes() for p in out.rglob('*') if p.is_file()})

    def test_tree_direct_metrics_all_aggregates_and_citation_coverage(self):
        # Even a single-source upper node is excluded when trace proves its level.
        self.report['cases'][0]['response']['items'][1]['source_ids'] = ['uuid-1']
        self.report['cases'][0]['retrieved_source_ids'] = ['e0', 'e1']
        self.records[-1]['result']['items'][1]['reader_sources'] = ['native-1']
        self.save()
        review, _ = self.build()
        cell = review['results']['cog']['q']
        self.assertEqual((cell['evidence_hits'], cell['retrieved_ids'], cell['cited_ids']), (1, 1, 2))
        self.assertEqual((cell['evidence_recall'], cell['evidence_precision'], cell['evidence_f1'], cell['citation_coverage']),
                         (1 / 3, 1.0, 0.5, 2 / 3))
        run = review['runs'][0]
        for metrics in (run['metrics'], run['by_episode']['1']['metrics'], run['by_category']['tree-category']['metrics']):
            for key, mean in ((beam.EVIDENCE, 1 / 3), (beam.PRECISION, 1.), (beam.F1, .5),
                              (beam.SOURCE_IDS, 1.), (beam.SOURCE_SHARE, 1 / 6), (beam.CITATION, 2 / 3)):
                self.assertEqual(metrics[key], {'mean': mean, 'n': 1})

    def test_tree_without_work_or_without_search_warns_and_keeps_coverage(self):
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            review = beam.build_review(self.suite, [('cog', self.run)], self.glossary)
        self.assertIn('single-source', stderr.getvalue())
        cell = review['results']['cog']['q']
        self.assertEqual((cell['retrieved_ids'], cell['cited_ids']), (1, 3))
        self.records.pop()
        self.save()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            review, _ = self.build()
        self.assertIn('single-source', stderr.getvalue())
        self.assertEqual(review['results']['cog']['q']['evidence_recall'], 1 / 3)
        self.assertEqual(review['results']['cog']['q']['work']['stage'], 'no_search')

    def test_tree_missing_log_uses_declared_mode_and_single_source_fallback(self):
        self.report['sut'].pop('variant')
        self.report['sut']['describe']['config']['retrieval_mode'] = 'tree'
        self.save()
        for path in self.run.rglob('sut.stderr.log'):
            path.unlink()
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            review, _ = self.build()
        self.assertIn('single-source', stderr.getvalue())
        self.assertEqual(review['results']['cog']['q']['retrieved_ids'], 1)
        self.assertEqual(review['results']['cog']['q']['cited_ids'], 3)

    def test_tree_repeat_namespace_contributes_own_levels_to_metrics(self):
        other = copy.deepcopy(self.report['cases'][0])
        other.update(namespace='ns-repeat', repeat_index=1)
        other['response']['items'] = [{'id': 'upper-1', 'source_ids': ['uuid-1']}]
        other['retrieved_source_ids'] = ['e1']
        self.report['cases'].append(other)
        self.save()
        records = copy.deepcopy(self.records)
        raw = records[-1]['result']
        raw['items'] = [raw['items'][1]]
        raw['items'][0]['reader_sources'] = ['native-1']
        raw['context_view']['nodes'] = [raw['context_view']['nodes'][1]]
        log = self.run / 'logs/0004-ns-repeat/sut.stderr.log'
        put(log.parent / 'episode.json', {'namespace': 'ns-repeat'})
        log.write_text(''.join('sut_trace ' + json.dumps(r) + '\n' for r in records))
        review, files = self.build()
        self.assertEqual(len(files), 1)  # Repeat traces affect metrics, not page work files.
        self.assertEqual(review['results']['cog']['q']['retrieved_ids'], 1)
        for metrics in (review['runs'][0]['metrics'], review['runs'][0]['by_episode']['1']['metrics'],
                        review['runs'][0]['by_category']['tree-category']['metrics']):
            self.assertEqual(metrics[beam.EVIDENCE], {'mean': 1 / 6, 'n': 2})
            self.assertEqual(metrics[beam.SOURCE_IDS], {'mean': .5, 'n': 2})
            self.assertEqual(metrics[beam.PRECISION], {'mean': 1., 'n': 1})

    def test_tree_cli_uses_search_levels_without_declared_tree_variant(self):
        self.report['sut']['variant'] = 'unspecified'
        self.report['cases'][0]['response']['items'][1]['source_ids'] = ['uuid-1']
        self.report['cases'][0]['retrieved_source_ids'] = ['e0', 'e1']
        self.records[-1]['result']['items'][1]['reader_sources'] = ['native-1']
        self.save()
        out = self.root / 'cli-tree'
        result = subprocess.run([sys.executable, '-B', str(SCRIPT), '--suite', str(self.suite),
                                 '--run', f'cog={self.run}', '--work', 'cog', '--glossary', str(self.glossary),
                                 '--out', str(out)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('single-source', result.stderr)
        review = json.loads((out / 'beam-100k.json').read_text())
        self.assertEqual(review['results']['cog']['q']['retrieved_ids'], 1)
        self.assertEqual(review['runs'][0]['metrics'][beam.EVIDENCE], {'mean': 1 / 3, 'n': 1})

    def test_tree_unreturned_candidates_use_representative_source_only(self):
        self.questions[0]['gold']['evidence_ids'] = ['e2', 'e3', 'e4']
        self.save()
        review, _ = self.build()
        cell = review['results']['cog']['q']['work']
        self.assertEqual(cell['stage'], 'partial')
        self.assertEqual([e['stage'] for e in cell['evidence']], ['selected', 'not_candidate', 'jev_rejected'])
        # A returned candidate's anchor cannot replace a missing coverage list.
        self.records[-1]['result']['items'][1]['reader_sources'] = ['native-unknown']
        self.questions[0]['gold']['evidence_ids'] = ['e1']
        self.save()
        review, _ = self.build()
        self.assertEqual(review['results']['cog']['q']['work']['covered'], [0, 1])

    def test_tree_optional_packed_and_absent_search(self):
        self.records[-1]['result']['packed'] = {'memories': 0, 'visible_ancestors': 1}
        self.save()
        review, _ = self.build()
        self.assertEqual(review['results']['cog']['q']['work']['packed'], self.records[-1]['result']['packed'])
        self.records.pop()
        self.save()
        review, _ = self.build()
        cell = review['results']['cog']['q']['work']
        self.assertEqual(cell['representation'], 'tree')
        self.assertEqual(cell['stage'], 'no_search')
        self.assertIsNone(cell['tree'])
        self.assertIsNone(cell['jev_summary'])

    def test_tree_body_snapshots_without_token_metadata_remain_unknown(self):
        for row in self.records:
            snapshot = row.get('snapshot') or {}
            if 'members' in snapshot:
                snapshot['memories'] = {}
                # Summary-token measurements are a different tokenizer/role;
                # they must never become invented Body embedding lengths.
                row['audit']['summaries'] = [{'body': 'summary-1', 'input_tokens': 900,
                                             'output_tokens': 90, 'accepted': True}]
        self.save()
        review, _ = self.build()
        summary = review['runs'][0]['work']['episodes']['1']
        self.assertEqual(summary['leaf_tokens'], {'n': 0, 'mean': None, 'max': None})
        self.assertEqual(summary['node_tokens_by_level'], {
            level: {'n': 0, 'mean': None, 'max': None} for level in ('0', '1', '2')})


if __name__ == "__main__":
    unittest.main()
