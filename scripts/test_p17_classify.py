"""Exercise embedded classification/decisions and a stubbed loop, never services."""
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('p17_cogletnet_v6.sh')


class VariantNamingTests(unittest.TestCase):
    def test_manifest_variants_have_distinct_run_directories(self):
        script = SCRIPT.read_text()
        # Execute only the actual naming assignments, never the launcher or
        # its preflight/build/container commands (nor its reports mkdir).
        assignments = '\n'.join(re.search(r'^' + key + r'=.*$', script, re.M).group(0).split(';', 1)[0]
                                for key in ('VAR', 'NAME', 'OUT'))
        names = set()
        for manifest, variant in (('manifest.json', ''), ('manifest-leaf-memory.json', 'leaf-memory'),
                                  ('manifest-leaf-memory-leaf-embed.json', 'leaf-memory-leaf-embed'),
                                  ('manifest-leaf-memory-abstractive.json', 'leaf-memory-abstractive'),
                                  ('manifest-tree.json', 'tree')):
            with self.subTest(manifest=manifest):
                result = subprocess.run(['bash', '-eu', '-c', 'MANIFEST=$1\n' + assignments +
                                         '\nprintf "%s\\n" "$VAR" "$NAME" "$OUT/$NAME"', 'naming-test', manifest],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                name = 'cogletnet-v6' + ('-' + variant if variant else '') + '-beam100k-400'
                self.assertEqual(result.stdout.splitlines(), [variant, name, 'reports/p17-beam/' + name])
                names.add(name)
        self.assertEqual(len(names), 5)
        self.assertIn('rm -rf "${OUT:?}/$NAME"', script)
        self.assertIn('--out "$OUT/$NAME"', script)

    def test_tree_name_in_p19_uses_manifest_suffix(self):
        script = SCRIPT.with_name('p19_cogletnet_v7.sh').read_text()
        # Extract assignments only: do not execute the launch/cleanup script.
        assignments = '\n'.join(re.search(r'^' + key + r'=.*$', script, re.M).group(0).split(';', 1)[0]
                                for key in ('VAR', 'NAME', 'OUT'))
        result = subprocess.run(['bash', '-eu', '-c', 'MANIFEST=$1\n' + assignments +
                                 '\nprintf "%s\\n" "$VAR" "$NAME" "$OUT/$NAME"',
                                 'naming-test', 'manifest-tree.json'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ['tree', 'cogletnet-v7-tree-beam100k-400',
                                                     'reports/p19-beam/cogletnet-v7-tree-beam100k-400'])


class ClassificationTests(unittest.TestCase):
    def setUp(self):
        self.script = SCRIPT.read_text()
        self.code = re.search(r'classify\(\) \{ python3 - "\$1" <<\'PY\'\n(.*?)\nPY\n}', self.script, re.S).group(1)
        temp = tempfile.TemporaryDirectory(prefix='memeval-classify-')
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.cases = []

    def episode(self, ns, rpc, native=None, count=2, episode_id=None):
        directory = self.root / ('logs/0001-' + ns)
        directory.mkdir(parents=True)
        self.cases.extend({'case_id': ns + str(i), 'namespace': ns, 'status': 'error', 'error_type': 'process_exited'} for i in range(count))
        if episode_id is not None:
            for case in self.cases[-count:]:
                case['episode_id'] = episode_id
        (directory / 'rpc-errors.jsonl').write_text(json.dumps(rpc) + '\n')
        trace = '' if native is None else 'sut_trace ' + json.dumps({'kind': 'build', 'audit': {'calls': [
            {'attempts': [{'failure': native}]}]}}) + '\n'
        (directory / 'sut.stderr.log').write_text(trace)

    def classify(self):
        (self.root / 'report.json').write_text(json.dumps({'cases': self.cases}))
        result = subprocess.run([sys.executable, '-B', '-c', self.code, str(self.root)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    @staticmethod
    def native(kind, **extra):
        return {'rpc_error': {'data': {'details': {'cogletnet': {'kind': 'BATCH_NOT_READY',
                'events': [{'error': {'kind': kind, **extra}}]}}}}}

    def counts(self, result, **expected):
        fields = dict((key, int(value)) for key, value in re.findall(
            r'\b(gateway|native|host_timeout|host|unattributed)=(\d+)', result.split('|', 1)[0]))
        self.assertEqual(fields, {key: expected.get(key, 0) for key in
                                 ('gateway', 'native', 'host_timeout', 'host', 'unattributed')})
        self.assertEqual(sum(fields.values()), sum(c['status'] == 'error' for c in self.cases))

    def test_outcome_unknown_is_retryable_even_without_model_attempts(self):
        self.episode('paeval_a', self.native('OPERATION_OUTCOME_UNKNOWN'))
        result = self.classify()
        self.assertTrue(result.startswith('gateway 2 '), result)
        self.counts(result, gateway=2)
        self.assertIn("'OPERATION_OUTCOME_UNKNOWN': 1", result)

    def test_process_143_without_native_kind_is_host_timeout(self):
        self.episode('paeval_a', {'error': 'process_exited(143)'}, {'kind': 'MODEL_UNAVAILABLE', 'message': 'connection failed'})
        result = self.classify()
        self.assertTrue(result.startswith('host_timeout 2 '), result)
        self.assertIn('host_timeout episodes: 1 cases: 2', result)
        self.counts(result, host_timeout=2)

    def test_native_failure_takes_precedence_over_exit_143_in_same_episode(self):
        self.episode('paeval_a', dict(self.native('SUMMARY_UNCOMPRESSIBLE'), error='process_exited(143)'))
        result = self.classify()
        self.assertTrue(result.startswith('native 2 '), result)
        self.assertIn('host_timeout episodes: 0 cases: 0', result)
        self.counts(result, native=2)

    def test_mixed_host_timeout_and_gateway_counts(self):
        self.episode('paeval_a', {'error': 'process_exited(143)'}, count=3)
        self.episode('paeval_b', self.native('OPERATION_OUTCOME_UNKNOWN'), count=1)
        result = self.classify()
        self.assertTrue(result.startswith('host_timeout 4 '), result)
        self.assertIn('host_timeout episodes: 1 cases: 3', result)
        self.counts(result, host_timeout=3, gateway=1)

    def test_mixed_native_and_gateway_counts(self):
        self.episode('paeval_a', self.native('SUMMARY_UNCOMPRESSIBLE'), count=3)
        self.episode('paeval_b', self.native('MODEL_UNAVAILABLE'), count=2)
        result = self.classify()
        self.assertTrue(result.startswith('native 5 '), result)
        self.counts(result, native=3, gateway=2)
        self.assertIn("| gateway: {'MODEL_UNAVAILABLE': 1}", result)
        self.assertIn("| native: {'SUMMARY_UNCOMPRESSIBLE': 1}", result)

    def test_native_overrides_gateway_within_episode(self):
        self.episode('paeval_a', self.native('SUMMARY_UNCOMPRESSIBLE'),
                     {'kind': 'MODEL_UNAVAILABLE', 'message': 'connection failed'})
        self.counts(self.classify(), native=2)

    def test_episode_priority_spans_namespaces(self):
        self.episode('paeval_old', self.native('SUMMARY_UNCOMPRESSIBLE'), count=1, episode_id='same')
        self.episode('paeval_new', self.native('OPERATION_OUTCOME_UNKNOWN'), count=3, episode_id='same')
        self.counts(self.classify(), native=4)

    def test_evaluator_gateway_without_logs(self):
        self.cases = [{'case_id': 'q', 'episode_id': 'ep', 'namespace': 'absent',
                       'status': 'error', 'error_type': 'LLMTransportError'}]
        self.counts(self.classify(), gateway=1)

    def test_unattributed_and_scored_cases(self):
        self.cases = [{'case_id': 'unknown', 'namespace': 'absent', 'status': 'error',
                       'error_type': 'unclassified'},
                      {'case_id': 'scored', 'namespace': 'absent', 'status': 'scored'}]
        result = self.classify()
        self.assertTrue(result.startswith('unknown 1 '), result)
        self.counts(result, unattributed=1)
        self.assertIn('| unattributed episodes: 1', result)

    def test_other_exit_codes_and_empty_reports_unchanged(self):
        self.assertEqual(self.classify(), 'none 0 gateway=0 native=0 host_timeout=0 host=0 unattributed=0 corrupt_trace_lines=0')
        self.episode('paeval_a', {'error': 'process_exited(137)'})
        result = self.classify()
        self.assertTrue(result.startswith('gateway 2 '))
        self.counts(result, gateway=2)

    def test_rpc_model_failures_without_trace_are_gateway(self):
        for kind in ('MODEL_RECOVERY_EXHAUSTED', 'MODEL_UNAVAILABLE', 'MODEL_TIMEOUT',
                     'MODEL_TRANSPORT', 'MODEL_RATE_LIMITED', 'MODEL_OTHER'):
            for wrapped in (False, True):
                with self.subTest(kind=kind, wrapped=wrapped):
                    ns = 'paeval_' + kind + str(wrapped)
                    rpc = self.native(kind) if wrapped else {'rpc_error': {'data': {'details': {'cogletnet': {'kind': kind}}}}}
                    self.episode(ns, rpc, count=1)
                    self.cases = self.cases[-1:]
                    self.cases[-1]['error_type'] = '*jsonrpc.RPCError'
                    result = self.classify()
                    self.counts(result, gateway=len(self.cases))
                    self.assertIn(kind, result)

    def test_host_contract_timeouts_are_not_gateway_or_native(self):
        for kind in ('INVALID_ARGUMENT', 'PROTOCOL_FRAME_INVALID', 'NATIVE_RPC_UNKNOWN'):
            for wrapped in (False, True):
                with self.subTest(kind=kind, wrapped=wrapped):
                    detail = {'kind': kind, 'message': 'timeout exceeds native execution window'}
                    rpc = self.native(**detail) if wrapped else {'rpc_error': {'data': {'details': {'cogletnet': detail}}}}
                    self.episode('paeval_' + kind + str(wrapped), rpc)
                    self.cases = self.cases[-2:]
                    result = self.classify()
                    self.counts(result, host=2)
                    self.assertTrue(result.startswith('host 2 '), result)
                    self.assertIn('host_timeout=0 host=2 unattributed=0', result)
                    self.assertIn('| gateway: {} | native: {} | host:', result)

    def test_timeout_message_does_not_reclassify_native_trace(self):
        self.episode('paeval_native', {}, {'kind': 'SUMMARY_UNCOMPRESSIBLE', 'message': 'timeout or EOF'})
        self.counts(self.classify(), native=2)

    def test_mixed_host_and_gateway_episodes_count_separately(self):
        self.episode('paeval_host', self.native('INVALID_ARGUMENT', message='timeout'), count=3)
        self.episode('paeval_gateway', self.native('MODEL_UNAVAILABLE'), count=1)
        self.counts(self.classify(), host=3, gateway=1)

    def test_corrupt_trace_escape_is_repaired_once_and_bad_lines_counted(self):
        self.episode('paeval_a', {}, {'kind': 'MODEL_RECOVERY_EXHAUSTED', 'message': 'dispatch limit exhausted'})
        self.cases[0]['error_type'] = self.cases[1]['error_type'] = '*jsonrpc.RPCError'
        log = self.root / 'logs/0001-paeval_a/sut.stderr.log'
        row = json.loads(log.read_text().removeprefix('sut_trace '))
        row['note'] = 'url?[REDACTED]" http://fixture.invalid and [REDACTED]"inside'
        valid = json.dumps(row)
        broken = valid.replace(r'[REDACTED]\"', '[REDACTED]"')
        with self.assertRaises(json.JSONDecodeError):
            json.loads(broken)
        log.write_text('sut_trace ' + broken + '\n')
        result = self.classify()
        self.counts(result, gateway=2)
        self.assertIn("'MODEL_RECOVERY_EXHAUSTED: dispatch limit exhausted': 1", result)
        self.assertIn('corrupt_trace_lines=0', result)
        with log.open('a') as stream:
            stream.write('sut_trace {truncated\n')
        result = self.classify()
        self.counts(result, gateway=2)
        self.assertIn('corrupt_trace_lines=1', result)


class DecisionTests(unittest.TestCase):
    def setUp(self):
        self.script = SCRIPT.read_text()
        self.function = re.search(r'(decide\(\) \{ python3 .*?\nPY\n})', self.script, re.S).group(1)
        self.code = self.function.split("<<'PY'\n", 1)[1].rsplit('\nPY\n}', 1)[0]

    @staticmethod
    def line(g, native=2, host=1):
        return (f'host_timeout {g + native + host} gateway={g} native={native} '
                f'host_timeout={host} unattributed=0 | gateway: {{}} | native: {{}}')

    def decide(self, g, previous=-1, used=0, maximum=6, stalled=0, line=None):
        line = (line if line is not None else self.line(g)) + f' stalled={stalled}'
        result = subprocess.run([sys.executable, '-B', '-c', self.code, line,
                                 str(previous), str(used), str(maximum)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(result.stdout.splitlines()), 1)
        return result.stdout.strip()

    def test_zero_gateway_stops(self):
        self.assertEqual(self.decide(0), 'stop only native/host_timeout remain')

    def test_host_only_stop_reason_and_gateway_continuation(self):
        self.assertEqual(self.decide(0, line='host 3 gateway=0 native=0 host_timeout=0 host=3 unattributed=0'),
                         'stop host errors remain')
        self.assertEqual(self.decide(1, line='host 4 gateway=1 native=0 host_timeout=0 host=3 unattributed=0'),
                         'continue')

    def test_gateway_continues_despite_other_classes(self):
        self.assertEqual(self.decide(3), 'continue')

    def test_first_stalled_pass_continues(self):
        self.assertEqual(self.decide(3, previous=3, used=1), 'continue')

    def test_two_consecutive_stalled_passes_stop(self):
        for g in (3, 4):
            with self.subTest(g=g):
                self.assertEqual(self.decide(g, previous=3, used=2, stalled=1), 'stop no gateway progress')

    def test_improvement_breaks_stalled_sequence(self):
        self.assertEqual(self.decide(2, previous=3, used=2, stalled=1), 'continue')
        self.assertEqual(self.decide(2, previous=2, used=3, stalled=0), 'continue')

    def test_max_passes_stop(self):
        for used in (6, 7):
            with self.subTest(used=used):
                self.assertEqual(self.decide(1, previous=2, used=used), 'stop max passes')

    def test_human_diagnostics_cannot_override_counts(self):
        self.assertEqual(self.decide(0, line=self.line(0) + ' gateway=99'),
                         'stop only native/host_timeout remain')

    def test_invalid_classification_stops(self):
        self.assertEqual(self.decide(0, line=''), 'stop invalid classification')

    def loop(self, gateways, maximum=6):
        # Extract ONLY the retry loop; every service/run/sleep function is a
        # local stub. The script's preflight, build, Docker and run setup never execute.
        loop = self.script.split('prev=$(errors "$OUT/$NAME")\n', 1)[1].split('\nattribute final', 1)[0]
        with tempfile.TemporaryDirectory(prefix='memeval-retry-loop-') as directory:
            source = f'''set -eu
LOG={shlex.quote(directory)}; OUT=$LOG; NAME=fixture; CM=unused; COMMON=""
MAXPASS={maximum}; GATEWAY_WAIT=600; round=0; AUTH=fake_auth
gateways=({' '.join(str(g) for g in gateways)})
errors() {{ echo $((gateways[round]+3)); }}
classify() {{ g=${{gateways[round]}}; echo "host_timeout $((g+3)) gateway=$g native=2 host_timeout=1 unattributed=0 | gateway: {{}} | native: {{}}"; }}
wait_services() {{ return 0; }}
sleep() {{ echo "stub sleep $1"; }}
fake_auth() {{ round=$((round+1)); echo 'stub resume'; }}
summ() {{ echo "stub summary $round"; }}
residue() {{ echo "stub residue $round"; }}
attribute() {{ echo "stub attribute $1"; }}
{self.function.replace('python3 -', shlex.quote(sys.executable) + ' -B -', 1)}
prev=$(errors "$OUT/$NAME")
{loop}
echo "rounds=$round"
'''
            result = subprocess.run(['bash', '-c', source], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        return result.stdout

    def test_loop_retries_mixed_errors_and_keeps_post_run_checks(self):
        output = self.loop([5, 5, 4, 4, 0])
        self.assertIn('rounds=4', output)
        self.assertIn('stop only native/host_timeout remain', output)
        self.assertIn('host timeout failures remain', output)
        self.assertIn('CogletNet-native failures remain', output)
        self.assertEqual(output.count('stub sleep 600'), 4)
        for i in range(1, 5):
            self.assertIn(f'stub summary {i}\nstub residue {i}\nstub attribute pass{i}', output)

    def test_loop_stops_after_two_stalled_passes(self):
        output = self.loop([5, 5, 5])
        self.assertIn('rounds=2', output)
        self.assertIn('stop no gateway progress', output)

    def test_loop_respects_max_passes(self):
        output = self.loop([5, 4, 3], maximum=2)
        self.assertIn('rounds=2', output)
        self.assertIn('stop max passes', output)


class RedoCasesTests(unittest.TestCase):
    """Only extracted validation/launch fragments run, with all services stubbed."""

    def setUp(self):
        self.script = SCRIPT.read_text()
        temp = tempfile.TemporaryDirectory(prefix='memeval-redo-cases-')
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.report = self.root / 'report.json'
        self.report.write_text(json.dumps({'cases': [
            {'case_id': 'q1', 'sample_id': 'q1#0', 'status': 'scored'},
            {'case_id': 'q1', 'sample_id': 'q1#1', 'status': 'scored'},
            {'case_id': 'q2', 'sample_id': 'q2#0', 'status': 'error'}]}))
        self.ids = self.root / 'cases.txt'

    def validate(self, value):
        self.ids.write_text(value)
        code = re.search(r'validate_redo_cases\(\).*?<<\'PYCODE\'\n(.*?)\nPYCODE\n}',
                         self.script, re.S).group(1)
        return subprocess.run([sys.executable, '-B', '-c', code, str(self.report), str(self.ids)],
                              capture_output=True, text=True)

    def test_local_inputs_are_checked_before_preflight(self):
        result = self.validate('# comment\n\nq1\nq1#1\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('2 IDs', result.stdout)
        self.assertLess(self.script.index('then validate_redo_cases'),
                        self.script.index('wait_services ||'))
        for value in ('', '# only comments\n', 'missing', 'q1#99'):
            with self.subTest(value=value):
                result = self.validate(value)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('nothing started', result.stderr)
        self.report.unlink()
        result = self.validate('q1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('existing report.json', result.stderr)

    def test_redo_launch_preserves_run_and_uses_exact_flags(self):
        fragment = self.script.split('if [ -n "$REDO_CASES" ]; then\n  echo "##### $NAME redo', 1)[1]
        fragment = ('if [ -n "$REDO_CASES" ]; then\n  echo "##### $NAME redo' +
                    fragment.split('\nelse\n  CM=$S/staged', 1)[0])
        run = self.root / 'fixture'
        run.mkdir()
        (run / 'preserved').write_text('original report/history')
        source = f'''set -eu
LOG={shlex.quote(str(self.root))}; OUT=$LOG; NAME=fixture; CM=manifest.json
REDO_CASES={shlex.quote(str(self.ids))}; AUTH=fake_auth; COMMON="--auto-resume-rate-limited 2"
fake_auth() {{ printf '%s\\n' "$@" > "$LOG/argv"; }}
summ() {{ echo 'stub summary'; }}
residue() {{ echo 'stub residue'; }}
attribute() {{ echo "stub attribute $1"; }}
{fragment}
'''
        result = subprocess.run(['bash', '-c', source], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((run / 'preserved').read_text(), 'original report/history')
        self.assertEqual((self.root / 'argv').read_text().splitlines(), [
            'bin/memeval', 'run', '--resume', str(run), '--resume-statuses', 'scored',
            '--resume-cases', '@' + str(self.ids), '--resume-sut-change', '--sut', 'manifest.json',
            '--parallel', '2', '--sut-llm-model', 'gpt-6-luna', '--sut-llm-effort', 'low',
            '--trace-text', '--auto-resume-rate-limited', '2'])
        self.assertIn('stub summary\nstub residue\nstub attribute pass0', result.stdout)

    def test_redo_rechecks_environment_and_archives_launch_manifest(self):
        fragment = self.script.split('if [ -n "$REDO_CASES" ]; then\n  "$AUTH" bin/memeval env check', 1)[1]
        fragment = ('if [ -n "$REDO_CASES" ]; then\n  "$AUTH" bin/memeval env check' +
                    fragment.split('\n\nif [ -n "$REDO_CASES" ]; then', 1)[0])
        previous = self.root / 'fixture-manifest.json'
        previous.write_text('old identity')
        staged = self.root / 'staged.json'
        staged.write_text('new identity')
        source = f'''set -eu
LOG={shlex.quote(str(self.root))}; NAME=fixture; CM={shlex.quote(str(staged))}
REDO_CASES=cases.txt; AUTH=fake_auth
{self.prefix_assignment()}
fake_auth() {{ printf '{{"valid": true}}\\n'; }}
{fragment}
'''
        result = subprocess.run(['bash', '-c', source], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(previous.read_text(), 'new identity')
        archived = list(self.root.glob('fixture-manifest-*.json'))
        self.assertEqual(len(archived), 1)
        self.assertEqual(archived[0].read_text(), 'old identity')
        self.assertTrue((self.root / 'fixture-redo-check.out').is_file())
        self.assertFalse((self.root / 'fixture-check.out').exists())

    def prefix_assignment(self):
        return re.search(r'^TAG=.*$', self.script, re.M).group(0)

    def test_environment_log_prefix_preserves_previous_run(self):
        fragment = self.script.split('SECONDS=0; "$AUTH" bin/memeval env check', 1)[1]
        fragment = ('SECONDS=0; "$AUTH" bin/memeval env check' +
                    fragment.split('\nif [ -n "$REDO_CASES" ]; then', 1)[0])
        for redo in ('cases.txt', ''):
            with self.subTest(redo=redo):
                log = self.root / ('redo' if redo else 'fresh')
                log.mkdir()
                for kind in ('check', 'install'):
                    (log / f'fixture-{kind}.out').write_text('original ' + kind)
                source = f'''set -eu
LOG={shlex.quote(str(log))}; NAME=fixture; CM=staged.json; AUTH=fake_auth
REDO_CASES={shlex.quote(redo)}
{self.prefix_assignment()}
fake_auth() {{
  case "$3" in
    check) echo '{{"valid": false}}'; return 1 ;;
    install) echo '{{"valid": true}}' ;;
    *) return 99 ;;
  esac
}}
{fragment}
'''
                result = subprocess.run(['bash', '-c', source], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                prefix = 'redo-' if redo else ''
                for kind, valid in (('check', False), ('install', True)):
                    target = log / f'fixture-{prefix}{kind}.out'
                    self.assertEqual(json.loads(target.read_text()), {'valid': valid})
                    if redo:
                        self.assertEqual((log / f'fixture-{kind}.out').read_text(), 'original ' + kind)

    def test_attribution_prefix_preserves_previous_run(self):
        function = re.search(r'^attribute\(\).*$', self.script, re.M).group(0)
        for redo in ('cases.txt', ''):
            with self.subTest(redo=redo):
                log = self.root / ('redo' if redo else 'fresh')
                log.mkdir()
                tags = ('pass0', 'pass3', 'final')
                for tag in tags:
                    (log / f'fixture-failures-{tag}.json').write_text('original ' + tag)
                source = f'''set -eu
LOG={shlex.quote(str(log))}; OUT=$LOG; NAME=fixture; REDO_CASES={shlex.quote(redo)}
{self.prefix_assignment()}
python3() {{
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --json ]; then shift; printf '{{"stub":true}}\\n' > "$1"; return 0; fi
    shift
  done
  return 99
}}
{function}
attribute pass0
attribute pass3
attribute final
'''
                result = subprocess.run(['bash', '-c', source], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                prefix = 'redo-' if redo else ''
                for tag in tags:
                    target = log / f'fixture-failures-{prefix}{tag}.json'
                    self.assertEqual(json.loads(target.read_text()), {'stub': True})
                    if redo:
                        self.assertEqual((log / f'fixture-failures-{tag}.json').read_text(), 'original ' + tag)

    def test_snapshot_guards_apply_before_install(self):
        fragment = self.script.split('[ "$SNAP" != "$V5" ]', 1)[1].split('\necho "  snapshot', 1)[0]
        fragment = '[ "$SNAP" != "$V5" ]' + fragment
        for snapshot, redo, expected in [('v5', '', 1), ('run4', 'list', 1),
                                         ('new', 'list', 0), ('run4', '', 0)]:
            with self.subTest(snapshot=snapshot, redo=redo):
                source = f'V5=v5; RUN4=run4; SNAP={snapshot}; REDO_CASES={shlex.quote(redo)}\n{fragment}\n'
                result = subprocess.run(['bash', '-c', source], capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main(verbosity=2)
