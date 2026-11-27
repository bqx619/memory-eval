"""paeval-v2 adapter for the unmodified CogletNet Rust library.

Native code runs only inside the evaluator's container. The separate host crate
calls public ingest/build/search APIs; no alternative retrieval algorithm is implemented.
"""
import hashlib
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import subprocess
import selectors
import time
import uuid
import sys

from memeval_bridge import AdapterBase, Unsupported, handle, limited, visible

MAX_FRAME = 16 * 1024 * 1024


class NativeError(RuntimeError):
    def __init__(self, details):
        self.details = details
        message = 'CogletNet native operation failed: ' + str(details.get('kind', 'BATCH_NOT_READY'))
        failure = details.get('model_failure')
        if isinstance(failure, dict):
            # Only the native diagnostic fields belong in the one-line RPC
            # message; never include arbitrary error text or request headers.
            fields = [key + '=' + json.dumps(failure[key], ensure_ascii=False)
                      for key in ('role', 'service', 'phase', 'status', 'elapsed_ms', 'retryable', 'request_id')
                      if failure.get(key) is not None]
            if fields:
                message += ' (' + ', '.join(fields) + ')'
        super().__init__(message)

    def public_error_data(self):
        return {'cogletnet': self.details}


class Native:
    def __init__(self, binary, env):
        self.process = subprocess.Popen([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=None, env=env, bufsize=0)
        os.set_blocking(self.process.stdin.fileno(), False)
        os.set_blocking(self.process.stdout.fileno(), False)

    def call(self, method, timeout=1750, **data):
        deadline = time.monotonic() + timeout
        frame = json.dumps(dict(method=method, **data), ensure_ascii=False).encode() + b'\n'
        if len(frame) > MAX_FRAME:
            raise ValueError('CogletNet native request exceeds frame limit')
        with selectors.DefaultSelector() as selector:
            selector.register(self.process.stdin, selectors.EVENT_WRITE)
            sent = 0
            while sent < len(frame):
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    raise TimeoutError('CogletNet native write deadline exceeded')
                try:
                    sent += os.write(self.process.stdin.fileno(), frame[sent:])
                except BlockingIOError:
                    continue
            selector.unregister(self.process.stdin)
            selector.register(self.process.stdout, selectors.EVENT_READ)
            raw = bytearray()
            while not raw.endswith(b'\n'):
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    raise TimeoutError('CogletNet native response deadline exceeded')
                try:
                    chunk = os.read(self.process.stdout.fileno(), min(65536, MAX_FRAME + 1 - len(raw)))
                except BlockingIOError:
                    continue
                if not chunk:
                    raise RuntimeError('CogletNet native process exited')
                raw.extend(chunk)
                if len(raw) > MAX_FRAME:
                    raise ValueError('CogletNet native response exceeds frame limit')
        response = json.loads(raw)
        if 'error' in response:
            error = response['error']
            raise NativeError(error if isinstance(error, dict) else {'message': str(error)})
        return response['result']

    def close(self):
        # Bound the close RPC itself, including writes and reads, before waiting.
        deadline = time.monotonic() + 45
        try:
            if self.process.poll() is None:
                try:
                    self.call('close', timeout=30)
                    self.process.wait(timeout=max(0, min(5, deadline - time.monotonic())))
                except (OSError, RuntimeError, ValueError, TimeoutError, subprocess.TimeoutExpired):
                    pass
        finally:
            if self.process.poll() is None:
                self.process.terminate()
                try:
                    self.process.wait(timeout=max(0, min(5, deadline - time.monotonic())))
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(timeout=max(0, deadline - time.monotonic()))
            self.process.stdin.close()
            self.process.stdout.close()


class Adapter(AdapterBase):
    def __init__(self, config):
        super().__init__(config)
        if not os.environ.get('HOST_GENERATION_MODEL', '').strip():
            raise ValueError('CogletNet requires HOST_GENERATION_MODEL via manifest llm.env')
        self.native = None
        self.ns = None
        self.sequence = 0
        self.events = {}
        self.sources = {}
        self.requested = {}
        self.event_results = {}
        self.native_writes = {}
        self._native_spec = None
        self.retrieval_mode = config.get('retrieval_mode', 'legacy')
        if self.retrieval_mode not in ('legacy', 'leaf_memory', 'tree'):
            raise ValueError('CogletNet retrieval_mode must be legacy, leaf_memory or tree')
        self.leaf_memory = self.retrieval_mode == 'leaf_memory'
        self.scoped = self.retrieval_mode in ('leaf_memory', 'tree')
        if self.leaf_memory and (os.environ.get('INDEX_REPRESENTATIONS') != 'leaf_memory'
                                or os.environ.get('JEV_PROMPT_PROFILE') != 'leaf-memory-reference-v1'):
            raise ValueError('CogletNet leaf_memory requires leaf_memory index and leaf-memory-reference-v1 JEV profile')
        if self.retrieval_mode == 'tree' and (os.environ.get('INDEX_REPRESENTATIONS') != 'body'
                                            or os.environ.get('JEV_PROMPT_PROFILE') != 'node-reference-v1'):
            raise ValueError('CogletNet tree requires body index and node-reference-v1 JEV profile')
        self.work_trace = os.environ.get('COGLETNET_WORK_TRACE', 'full')
        if self.work_trace not in ('full', 'off'):
            raise ValueError('COGLETNET_WORK_TRACE must be full or off')
        self.trace_sequences = {}
        self.trace_batch = 0
        self.resume_counts, self.wait_windows = {}, {}
        self.search_retry_max_attempts = config.get('search_retry_max_attempts', 3)
        self.search_retry_initial_s = config.get('search_retry_initial_s', 10)
        if type(self.search_retry_max_attempts) is not int or self.search_retry_max_attempts < 1:
            raise ValueError('CogletNet invalid search_retry_max_attempts')
        if (type(self.search_retry_initial_s) not in (int, float)
                or not math.isfinite(self.search_retry_initial_s) or self.search_retry_initial_s <= 0):
            raise ValueError('CogletNet invalid search_retry_initial_s')
        if self.scoped:
            defaults = {'operation_resume_max_attempts': 4, 'operation_resume_initial_s': 30,
                        'operation_resume_max_wait_s': 300}
            self.recovery = {key: config.get(key, value) for key, value in defaults.items()}
            for key, value in self.recovery.items():
                if type(value) is not int or value < (0 if key.endswith('attempts') else 1):
                    raise ValueError('CogletNet invalid ' + key)
            if self.recovery['operation_resume_initial_s'] > self.recovery['operation_resume_max_wait_s']:
                raise ValueError('CogletNet recovery initial wait exceeds maximum')

    def describe(self):
        summary_model = os.environ.get('MEMORY_SUMMARY_MODEL') or os.environ['HOST_GENERATION_MODEL']
        validation_model = os.environ.get('MEMORY_SUMMARY_VALIDATION_MODEL') or summary_model
        result = {
            'name': 'cogletnet', 'version': '0.1.0',
            'capabilities': ['retrieve', 'dump', 'read_only_queries'], 'read_only_queries': True,
            'config': {
                'host_generation_model': os.environ['HOST_GENERATION_MODEL'],
                'generation_reasoning_effort': os.environ.get('GENERATION_REASONING_EFFORT'),
                'summary_model': summary_model,
                'summary_validation_model': validation_model,
                'source_snapshot_sha256': self.config.get('source_snapshot_sha256'),
                'selection': 'Jev', 'representation': 'FullPath', 'failure_policy': 'Strict',
                'summary_prompt_profile': os.environ.get('SUMMARY_PROMPT_PROFILE'),
                'summary_candidate_policy': os.environ.get('SUMMARY_CANDIDATE_POLICY'),
                'summary_length_band': os.environ.get('SUMMARY_LENGTH_BAND'),
                'summary_copy_max_tokens': os.environ.get('SUMMARY_COPY_MAX_TOKENS'),
                'max_level': os.environ.get('MAX_LEVEL'),
                'summary_ratio': os.environ.get('SUMMARY_RATIO'),
                'summary_ratio_min': os.environ.get('SUMMARY_RATIO_MIN'),
                'summary_ratio_max': os.environ.get('SUMMARY_RATIO_MAX'),
                'summary_min_tokens': os.environ.get('SUMMARY_MIN_TOKENS'),
                'summary_language_guard': os.environ.get('SUMMARY_LANGUAGE_GUARD'),
                'leaf_memory_embed_input': os.environ.get('LEAF_MEMORY_EMBED_INPUT'),
                'build_input_bounds': os.environ.get('BUILD_INPUT_BOUNDS', 'conservative-bytes-v1'),
                'jev_threshold': float(os.environ.get('JEV_THRESHOLD', '0.5')),
                'jev_prompt_profile': os.environ.get('JEV_PROMPT_PROFILE', 'memory-reference-v1'),
                'jev_selection_policy': os.environ.get('JEV_SELECTION_POLICY', 'absolute-threshold'),
                'work_trace': self.work_trace,
                'batch_timeout_seconds': self.config.get('batch_timeout_seconds', 1740),
                'ingest_concurrency': self.config.get('ingest_concurrency', 4),
                'llm_notes': ['HOST_GENERATION_MODEL and GENERATION_REASONING_EFFORT use the shared SUT LLM configuration; search does not invoke final-answer generation',
                              'summary model defaults to HOST_GENERATION_MODEL; validation model defaults to the effective summary model; explicit role model and reasoning settings are preserved; OpenAI embedding and Jev are direct services; per-role usage is recorded in cogletnet_usage log lines'],
            },
            'limitations': [
                'scope and as_of unsupported; event owner isolation is enforced through public source IDs',
                'each ingested event is one native Source; native FullPath text is returned without source-text substitution',
                'dump exposes raw ingested sources, not the complete internal summary graph',
                'native_answer and typed_context unsupported; no feedback settlement or MAM learning is simulated',
                'network open: summary/validation gateway, OpenAI embedding and TypeSafe Jev; no single-target egress claim',
                'native default memory budget and estimated byte tokenizers remain active; adapter also enforces top_k and Unicode character budget',
                'local fidelity proofs cover complete byte equality or pure whitespace only; extracted/rewritten candidates still require independent model validation',
                'all requested history must be Ready before scoring; build_progress and usage are diagnostic APIs, not partial-history scoring',
                'sut_trace full records private build/search text on stderr; snapshot point vectors are omitted; diagnostic reads and log writes add measured overhead without changing operation deadlines or retry policy',
            ],
        }
        if self.scoped:
            result['config'].update(self.recovery)
            result['config'].update(representation='LeafMemory' if self.leaf_memory else 'Tree', retrieval_mode=self.retrieval_mode,
                                    index_representations=os.environ.get('INDEX_REPRESENTATIONS'),
                                    jev_output_format='ChoiceThreeClass', jev_admission_basis='useful',
                                    jev_calibration='Uncalibrated',
                                    jev_class_sum_tolerance=float(os.environ.get('JEV_CLASS_SUM_TOLERANCE', '0.02')),
                                    write_mode='scope_events', scope_mapping='owner + session_id, else turn_group, else event id',
                                    event_order='first-seen public event order', ingest_concurrency=1,
                                    ingest_batching='same-scope append batches; sequential publication')
            result['limitations'][1] = ('explicit scopes aggregate public events only within the same owner and session/turn-group; '
                                       'missing session identifiers use one scope per event; each event retains its own Source; '
                                       'append_events/replace_event preserve exact source versions')
        if self.leaf_memory:
            result['limitations'].extend([
                'items use upstream render_leaf_memories for one complete info/current memory each, including citation metadata; '
                'shared ancestors and the format header can repeat across items; the character budget can drop whole items',
                "source_ids name only the memory's own L0 source; summary-layer dependencies stay in the trace (reader_sources); "
                'scope/as_of query filtering and feedback settlement remain unsupported',
                'native packed counts describe the context before the adapter character/top_k budget; '
                'three-class probabilities are native, unnormalised, and not benchmark-calibrated',
            ])
        if self.retrieval_mode == 'tree':
            result['config']['summary_record_time'] = os.environ.get('SUMMARY_RECORD_TIME')
            result['limitations'][5] = ('native token budget remains active; adapter also enforces top_k '
                                        'and Unicode character budget without expanding node text')
            result['config']['llm_notes'].append(
                'Tree Jev uses node-reference-v1 three-class admission over Body vectors across all levels; '
                'reader items contain own node text, no ancestors')
            result['limitations'].extend([
                'Tree items contain own node text, no ancestors or subtree expansion; source_ids name all '
                'direct sources covered by that node; scope/as_of query filtering remains unsupported',
                'recorded windows and v4 summary tags describe public message recording time, not event occurrence; '
                'absent public timestamps remain unknown',
                'native Tree packing uses vector rank and token budget before the adapter character/top_k budget; '
                'three-class probabilities are native, unnormalised, and not benchmark-calibrated',
            ])
        return result

    @staticmethod
    def _scope(event):
        # Owner participates in the boundary so a shared session label cannot
        # mix different users' material into one cross-event summary.
        group = event.get('session_id')
        kind = 'session'
        if not group:
            group = event.get('turn_group') or (event.get('attributes') or {}).get('turn_group')
            kind = 'turn-group'
        if not group:
            group, kind = event['id'], 'event'
        if not isinstance(group, str):
            raise ValueError('CogletNet session/turn-group must be a string')
        identity = json.dumps([event.get('owner', ''), group], ensure_ascii=False, separators=(',', ':'))
        return {'kind': 'memeval-' + kind, 'external_id': hashlib.sha256(identity.encode()).hexdigest()}

    def reset(self, ns):
        self.close()
        ep = Path(os.environ['EP'])
        directory = ep / ('cogletnet-' + hashlib.sha256(ns.encode()).hexdigest()[:16] + '-' + uuid.uuid4().hex)
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        env = dict(os.environ, STATE_PATH=str(directory / 'state.sqlite3'),
                   PRIVACY_JOURNAL_PATH=str(directory / 'privacy.sqlite3'),
                   BLOB_PATH=str(directory / 'blobs'), NAMESPACE=ns)
        binary = self.config.get('binary') or Path(__file__).resolve().parents[1] / 'models/bin/cogletnet-memeval'
        self._native_spec = (binary, env)
        self.ns, self.sequence, self.events, self.sources = ns, 0, {}, {}
        self.requested, self.event_results = {}, {}
        self.native_writes = {}
        self.trace_sequences = {}
        self.trace_batch = 0
        self.resume_counts, self.wait_windows = {}, {}
        self._ensure_native()
        # A content-free marker distinguishes explicitly disabled tracing from
        # old or missing logs. No build/search diagnostics run in off mode.
        self._emit_trace({'kind': 'config', 'work_trace': self.work_trace,
                          'jev_threshold': float(os.environ.get('JEV_THRESHOLD', '0.5'))})

    def _emit_trace(self, row):
        try:
            print('sut_trace ' + json.dumps(row, ensure_ascii=False, separators=(',', ':')),
                  file=sys.stderr, flush=True)
        except Exception:
            # Diagnostic output can never replace the business RPC result.
            pass

    def _trace_build(self, event, result, deadline, cache=None):
        if self.work_trace == 'off':
            return
        row = {'kind': 'build', 'seq': self.trace_sequences[event['id']],
               'event_id': event['id'], 'text_sha256': hashlib.sha256(event['content'].encode()).hexdigest(),
               'state': result.get('state'), 'operation_id': result.get('operation_id'),
               'snapshot_id': (result.get('write') or {}).get('snapshot'),
               'audit': None, 'snapshot': None, 'audit_error': None}
        if cache is not None:
            row['trace_batch'] = cache['batch']
            row['resume_count'] = self.resume_counts.get(row['operation_id'], 0)
            row['wait_windows'] = list(self.wait_windows.get(row['operation_id'], []))
        errors = []

        def diagnostic(method, **args):
            if self.native is None:
                raise RuntimeError('native diagnostic stream unavailable')
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('diagnostics exceed remaining ingest deadline')
            try:
                return self.native.call(method, timeout=remaining, **args)
            except NativeError:
                raise  # A complete error response leaves the stream usable.
            except Exception:
                # A late diagnostic response must not become the next search
                # response. Reopen the same durable state on the next RPC.
                native, self.native = self.native, None
                if isinstance(native, Native) and hasattr(native, 'process'):
                    native.process.kill()
                    native.process.wait(timeout=5)
                    native.process.stdin.close()
                    native.process.stdout.close()
                else:
                    native.close()
                raise

        def capture(field, identity, read):
            # Scoped variants pass a per-ingest cache. Legacy serialization
            # and diagnostic call order are unchanged, without retaining all
            # independent per-event snapshots in memory.
            saved = cache[field].get(identity) if cache is not None and identity else None
            if saved is not None:
                seq, value, failures = saved
                errors.extend(failures)
                row[field] = {'ref_seq': seq} if isinstance(value, dict) else value
                return value
            before = len(errors)
            value = read()
            row[field] = value
            if cache is not None and identity:
                cache[field][identity] = (row['seq'], value, errors[before:])
            return value

        def read_audit():
            try:
                if row['operation_id']:
                    return diagnostic('audit_build', operation_id=row['operation_id'])
                errors.append('audit_build: operation_id unavailable')
            except Exception as error:
                errors.append('audit_build: ' + str(error))
            return None

        def read_snapshot():
            try:
                if row['snapshot_id']:
                    try:
                        return diagnostic('snapshot_manifest_view', snapshot_id=row['snapshot_id'])
                    except Exception as error:
                        errors.append('snapshot_manifest_view: ' + str(error))
                        snapshot = diagnostic('snapshot_manifest', snapshot_id=row['snapshot_id'])
                        # Compatibility fallback: omit vectors without mutating
                        # the native diagnostic response.
                        return dict(snapshot, points=[{k: v for k, v in p.items() if k != 'vector'}
                                                     for p in snapshot.get('points', [])])
                errors.append('snapshot_manifest: snapshot_id unavailable')
            except Exception as error:
                errors.append('snapshot_manifest: ' + str(error))
            return None

        audit = capture('audit', row['operation_id'], read_audit)
        try:
            if isinstance(audit, dict):
                row['snapshot_id'] = row['snapshot_id'] or (audit.get('operation') or {}).get('snapshot')
        except Exception as error:
            errors.append('audit_build: ' + str(error))
        capture('snapshot', row['snapshot_id'], read_snapshot)
        row['audit_error'] = '; '.join(errors) or None
        self._emit_trace(row)

    def _ensure_native(self, timeout=1750):
        if self.native is None:
            self.native = Native(*self._native_spec)
            try:
                extra = {'scheduled_writes': True} if self.scoped else {}
                self.native.call('open', namespace=self.ns, timeout=timeout, **extra)
            except Exception:
                self.close()
                raise

    def _native_timeout(self, seconds):
        limit = self.config.get('batch_timeout_seconds', 1740)
        if type(limit) is not int or not 1 <= limit <= 1740:
            raise ValueError('CogletNet batch_timeout_seconds must be in 1..1740')
        return max(1, min(limit, math.floor(seconds)))

    def ingest(self, ns, events):
        deadline = self._leaf_deadline() if self.scoped else time.monotonic() + 1750
        if ns != self.ns:
            raise ValueError('CogletNet namespace is not active')
        ids = [event['id'] for event in events]
        if len(set(ids)) != len(ids):
            raise ValueError('CogletNet duplicate event in batch')
        for event in events:
            if event['id'] in self.requested and self.requested[event['id']] != event:
                if not self.scoped:
                    raise ValueError('CogletNet event id reused with different payload')
                if self.event_results[event['id']]['state'] != 'Ready':
                    raise ValueError('CogletNet pending event must replay the same payload')
                if self._scope(event) != self._scope(self.requested[event['id']]):
                    raise ValueError('CogletNet replacement cannot change owner or scope')
        data = []
        for event in events:
            self.trace_sequences.setdefault(event['id'], len(self.trace_sequences))
            row = {'id': event['id'], 'text': event['content'],
                   'metadata': {'event_id': event['id'], 'owner': event.get('owner', ''),
                                'role': event['role'], 'timestamp': event.get('timestamp', ''),
                                'session_id': event.get('session_id', '')}}
            changed = event['id'] in self.requested and self.requested[event['id']] != event
            if self.scoped:
                if event['id'] not in self.native_writes or changed:
                    row.update(scope=self._scope(event), event_sequence=self.trace_sequences[event['id']])
                    if event.get('timestamp'):
                        stamp = datetime.fromisoformat(event['timestamp'].replace('Z', '+00:00'))
                        if stamp.tzinfo is None:
                            raise ValueError('CogletNet event timestamp requires a timezone')
                        delta = stamp - datetime(1970, 1, 1, tzinfo=timezone.utc)
                        row['occurred_at'] = ((delta.days * 86400 + delta.seconds) * 1000000 + delta.microseconds)
                    if changed:
                        version = (self.event_results[event['id']].get('source_ref') or {}).get('version')
                        if type(version) is not int or version < 1:
                            raise ValueError('CogletNet replacement requires the exact previous source version')
                        row['expected_source_version'] = version
                    self.native_writes[event['id']] = row
                row = self.native_writes[event['id']]
                if changed:
                    self.event_results[event['id']] = {'event_id': event['id'], 'state': 'NotDispatched'}
            self.requested[event['id']] = dict(event)
            self.event_results.setdefault(event['id'], {'event_id': event['id'], 'state': 'NotDispatched'})
            # A previously Ready scope operation can become Superseded after
            # append; do not redispatch an unchanged, already accepted event.
            if not self.scoped or self.event_results[event['id']]['state'] != 'Ready':
                data.append(row)
        self._ensure_native(timeout=max(0, deadline - time.monotonic()))
        trace_cache = None
        if self.scoped:
            trace_cache = {'batch': self.trace_batch, 'audit': {}, 'snapshot': {}}
            self.trace_batch += 1
            # Accepted is separate from Ready in paeval-v2. Retain queued
            # public inputs in this adapter and never append over a pending
            # scope operation. The next ingest/ready continues this queue.
            self._advance_leaf(deadline, trace_cache, events)
            self.sequence += 1
            return handle(self.sequence, len(events))
        try:
            remaining = deadline - time.monotonic()
            execution_window = math.ceil(remaining) - 10
            if execution_window < 1:
                raise TimeoutError('CogletNet batch execution window exhausted')
            execution_window = self._native_timeout(execution_window)
            reply = self.native.call('add_batch', events=data, concurrency=self.config.get('ingest_concurrency', 4),
                                     timeout_seconds=execution_window, timeout=remaining)
        except Exception as error:
            for event in events:
                if self.event_results[event['id']]['state'] != 'Ready':
                    self.event_results[event['id']] = dict(self.event_results[event['id']], state='Unknown')
            try:
                self.close()  # A timed-out stream cannot safely service another RPC.
            except (OSError, RuntimeError, TimeoutError, subprocess.TimeoutExpired):
                pass
            for event in events:
                self._trace_build(event, self.event_results[event['id']], deadline, trace_cache)
            raise NativeError({'kind': 'NATIVE_RPC_UNKNOWN',
                               'cause': error.details if isinstance(error, NativeError) else {'type': type(error).__name__},
                               'events': [self.event_results[e['id']] for e in events]}) from error
        results = reply.get('events', [])
        if len(results) != len(events) or [r.get('event_id') for r in results] != ids:
            raise ValueError('CogletNet batch result identities/count mismatch')
        for event, result in zip(events, results):
            self.event_results[event['id']] = dict(result, state='Unknown')
            source = (result.get('source_ref') or {}).get('id')
            if source:
                if source in self.sources and self.sources[source] != event['id']:
                    raise ValueError('CogletNet reused a source across public events')
                existing = [s for s, e in self.sources.items() if e == event['id']]
                if existing and existing != [source]:
                    raise ValueError('CogletNet changed source for the same event')
                self.sources[source] = event['id']
            if result.get('state') == 'Ready':
                if not source:
                    raise ValueError('CogletNet Ready event lacks source')
                self.events[event['id']] = dict(event)
            self.event_results[event['id']] = result
        for event, result in zip(events, results):
            self._trace_build(event, result, deadline, trace_cache)
        if not all(r.get('state') == 'Ready' for r in results):
            raise NativeError({'kind': 'BATCH_NOT_READY', 'events': results})
        self.sequence += 1
        return handle(self.sequence, len(events))

    def _require_ready(self):
        if len(self.events) != len(self.requested) or any(r.get('state') != 'Ready' for r in self.event_results.values()):
            raise NativeError({'kind': 'HISTORY_NOT_READY', 'events': list(self.event_results.values())})

    def _leaf_deadline(self, supplied=None):
        # Leave time for structured errors/trace serialization before the
        # framework's per-RPC deadline. The episode timeout remains external.
        seconds = max(0, float(self.config.get('request_timeout_s', 1800)) - 50)
        if supplied:
            stamp = datetime.fromisoformat(supplied.replace('Z', '+00:00'))
            if stamp.tzinfo is None:
                raise ValueError('CogletNet ready deadline requires a timezone')
            seconds = min(seconds, max(0, (stamp - datetime.now(timezone.utc)).total_seconds() - 5))
        return time.monotonic() + seconds

    @staticmethod
    def _recoverable(result):
        error = result.get('error') or {}
        diagnostic = error.get('model_failure') or {}
        if diagnostic.get('retryable') is False:
            return False
        return (error.get('kind') in ('MODEL_RECOVERY_EXHAUSTED', 'OPERATION_TIMED_OUT', 'OPERATION_OUTCOME_UNKNOWN')
                or diagnostic.get('retryable') is True)

    def _not_ready(self):
        pending = [r for r in self.event_results.values() if r.get('state') != 'Ready']
        raise NativeError({'kind': 'BATCH_NOT_READY', 'events': pending,
                           'operation_ids': list(dict.fromkeys(r['operation_id'] for r in pending if r.get('operation_id'))),
                           'resume_counts': dict(self.resume_counts)})

    def _leaf_results(self, rows, results, deadline, cache, window):
        if [r.get('event_id') for r in results] != [row['id'] for row in rows]:
            raise ValueError('CogletNet batch result identities/count mismatch')
        for operation in dict.fromkeys(r.get('operation_id') for r in results if r.get('operation_id')):
            self.wait_windows.setdefault(operation, []).append(dict(window))
        for row, result in zip(rows, results):
            ident = row['id']
            source = (result.get('source_ref') or {}).get('id')
            if source:
                if source in self.sources and self.sources[source] != ident:
                    raise ValueError('CogletNet reused a source across public events')
                existing = [s for s, e in self.sources.items() if e == ident]
                if existing and existing != [source]:
                    raise ValueError('CogletNet changed source for the same event')
                self.sources[source] = ident
            if result.get('state') == 'Ready':
                if not source:
                    raise ValueError('CogletNet Ready event lacks source')
                self.events[ident] = dict(self.requested[ident])
            self.event_results[ident] = result
        for row, result in zip(rows, results):
            self._trace_build(self.requested[row['id']], result, deadline, cache)

    def _advance_leaf(self, deadline, cache=None, replay=()):
        while True:
            pending = [r for r in self.event_results.values() if r.get('state') != 'Ready']
            if not pending:
                # Keep unchanged Ready replay's diagnostic behavior.
                for event in replay:
                    self._trace_build(event, self.event_results[event['id']], deadline, cache)
                return True
            first = next((r for r in pending if r.get('operation_id') and r.get('state') != 'NotDispatched'), pending[0])
            operation = first.get('operation_id')
            if (operation and first.get('state') == 'Failed' and self._recoverable(first)
                    and self.resume_counts.get(operation, 0) >= self.recovery['operation_resume_max_attempts']
                    and not any(r.get('state') == 'Pending' for r in pending)):
                # NotDispatched work cannot pass this failed operation. Preserve
                # its terminal errors instead of spinning until the RPC deadline.
                self._not_ready()
            remaining = deadline - time.monotonic()
            retry_after_s = None
            if operation and first.get('state') != 'Pending':
                hints = [(r.get('error') or {}).get('model_failure') or {} for r in pending
                         if r.get('operation_id') == operation and r.get('state') == 'Failed']
                milliseconds = [h.get('retry_after_ms') for h in hints]
                milliseconds = [v for v in milliseconds
                                if type(v) in (int, float) and math.isfinite(v) and v >= 0]
                if milliseconds:
                    retry_after_s = max(milliseconds) / 1000
                    if retry_after_s + 2 > remaining - 11:
                        self._not_ready()  # Cannot honor the hint within this call window.
            seconds = math.floor(remaining) - 10
            if seconds < 1:
                return False
            if operation and first.get('state') != 'Pending':
                if not self._recoverable(first):
                    self._not_ready()
                if self.resume_counts.get(operation, 0) >= self.recovery['operation_resume_max_attempts']:
                    # Other Pending work still exists. Do not renew this
                    # operation's budget on a later ingest/ready call.
                    return False
                attempt = self.resume_counts.get(operation, 0)
                delay = min(max(self.recovery['operation_resume_initial_s'] * (2 ** min(attempt, 30)),
                                retry_after_s + 2 if retry_after_s is not None else 0),
                            self.recovery['operation_resume_max_wait_s'], remaining - 11)
                time.sleep(delay)
                seconds = math.floor(deadline - time.monotonic()) - 10
                if seconds < 1:
                    return False  # Sleep can overshoot; do not spend a resume attempt.
                self.resume_counts[operation] = attempt + 1
                method, extra = 'resume', {'operation_id': operation, 'scheduled': True}
            elif operation:
                method, extra, delay = 'wait_operation', {'operation_id': operation}, 0
            else:
                if first.get('state') != 'NotDispatched':
                    self._not_ready()
                method, extra, delay = 'add_batch', {'write_mode': 'scope_events', 'concurrency': 1}, 0
            rows = [self.native_writes[r['event_id']] for r in pending
                    if (r.get('operation_id') == operation if operation else r.get('state') == 'NotDispatched')]
            self._ensure_native(timeout=max(0, deadline - time.monotonic()))
            seconds = self._native_timeout(seconds)
            window = {'method': method, 'window_s': seconds, 'backoff_s': delay,
                      'retry_after_s': retry_after_s,
                      'resume_count': self.resume_counts.get(operation, 0)}
            try:
                reply = self.native.call(method, events=rows, timeout_seconds=seconds,
                                         timeout=max(0, deadline - time.monotonic()), **extra)
                results = (reply.get('events', []) if method != 'resume' else
                           [dict(self.event_results[row['id']], state='Pending', error=None) for row in rows])
            except NativeError as error:
                if not operation:
                    raise  # No durable identity: never pretend submission succeeded.
                results = [dict(self.event_results[row['id']], state='Failed', error=error.details) for row in rows]
            except Exception:
                self.close()  # A late response cannot be used as another RPC's result.
                raise
            if cache is None:
                cache = {'batch': self.trace_batch, 'audit': {}, 'snapshot': {}}
                self.trace_batch += 1
            self._leaf_results(rows, results, deadline, cache, window)
            cache, replay = None, ()
            if any(r.get('state') == 'Pending' for r in results) and method != 'resume':
                return False  # Native exhausted this wait window; continue in next RPC.

    def ready(self, ns, h, deadline):
        if self.scoped:
            if ns != self.ns:
                raise ValueError('CogletNet namespace is not active')
            until = self._leaf_deadline(deadline)
            while not self._advance_leaf(until):
                if until - time.monotonic() < 11:
                    self._not_ready()
                time.sleep(min(5, max(0, until - time.monotonic() - 10)))
        self._require_ready()
        return visible()

    def retrieve(self, ns, req):
        self._require_ready()
        if req.get('output_format') == 'typed_context':
            raise Unsupported('CogletNet typed_context')
        if not req.get('query', '').strip() or req['budget']['top_k'] <= 0:
            return {'items': []}
        sources = [source for source, event_id in self.sources.items()
                   if self.events[event_id].get('owner', '') == req.get('user', '')]
        # In CogletNet [] means all sources, so never search an empty allowlist.
        if not sources:
            return {'items': []}
        deadline = self._leaf_deadline()
        self._ensure_native(timeout=max(0, deadline - time.monotonic()))
        result = None
        error = None
        retries = []
        try:
            extra = ({'representation': 'LeafMemory' if self.leaf_memory else 'Tree'}
                     if self.scoped else {})
            for attempt in range(self.search_retry_max_attempts):
                try:
                    result = self.native.call('search', query=req['query'], sources=sources, limit=req['budget']['top_k'],
                                              trace=self.work_trace == 'full',
                                              timeout=max(0, deadline - time.monotonic()), **extra)
                    break
                except NativeError as exc:
                    kind = exc.details.get('kind')
                    failure = exc.details.get('model_failure') or {}
                    retryable = (kind in ('MODEL_UNAVAILABLE', 'MODEL_TIMEOUT', 'MODEL_TRANSPORT',
                                          'MODEL_RATE_LIMITED', 'MODEL_RECOVERY_EXHAUSTED')
                                 or failure.get('retryable') is True)
                    if not retryable or attempt + 1 >= self.search_retry_max_attempts:
                        raise
                    milliseconds = failure.get('retry_after_ms')
                    retry_after_s = (milliseconds / 1000 if type(milliseconds) in (int, float)
                                     and math.isfinite(milliseconds) and milliseconds >= 0 else None)
                    delay = max(self.search_retry_initial_s * (2 ** min(attempt, 30)),
                                retry_after_s + 2 if retry_after_s is not None else 0)
                    if deadline - time.monotonic() <= delay:
                        raise  # Preserve the native error; never renew the RPC window.
                    time.sleep(delay)
                    if time.monotonic() >= deadline:
                        raise
                    retries.append({'kind': kind, 'backoff_s': delay, 'retry_after_s': retry_after_s})
        except NativeError as exc:
            error = exc.details
            raise
        finally:
            if self.work_trace == 'full':
                self._emit_trace({'kind': 'search', 'query': req['query'], 'limit': req['budget']['top_k'],
                                  'jev_threshold': float(os.environ.get('JEV_THRESHOLD', '0.5')),
                                  'sources': dict(self.sources), 'result': result, 'error': error, 'retries': retries})
        if result.get('degraded'):
            raise RuntimeError('CogletNet unexpectedly degraded strict retrieval')
        items = []
        for row in result['items']:
            source = row['source']['id']
            if source not in sources:
                raise ValueError('CogletNet returned unknown source or another owner')
            refs = [source]
            text = row.get('reader_text') if self.retrieval_mode == 'tree' else row['path']['text']
            if self.leaf_memory:
                refs, text = row.get('reader_sources'), row.get('reader_text')
                if not row.get('memory') or not isinstance(text, str) or not text:
                    raise ValueError('CogletNet LeafMemory item lacks native memory/rendered text')
                if (not isinstance(refs, list) or not refs or any(not isinstance(s, str) for s in refs)
                        or len(set(refs)) != len(refs) or source not in refs or any(s not in sources for s in refs)):
                    raise ValueError('CogletNet LeafMemory returned unknown source or another owner in its layer closure')
                refs = [source]  # The memory's own L0 source; dependency closure stays in trace.
            elif self.retrieval_mode == 'tree':
                refs = row.get('reader_sources')
                if not isinstance(text, str) or not text:
                    raise ValueError('CogletNet Tree item lacks native node text')
                if (not isinstance(refs, list) or not refs or any(not isinstance(s, str) for s in refs)
                        or len(set(refs)) != len(refs) or source not in refs or any(s not in sources for s in refs)):
                    raise ValueError('CogletNet Tree returned unknown source or another owner in its direct sources')
            items.append({'id': row['reference']['node'], 'text': text,
                          'source_ids': [self.sources[s] for s in refs], 'score': row.get('rank_utility')})
        if result.get('warnings'):
            print('cogletnet_retrieval_warnings ' + json.dumps(result['warnings']), file=sys.stderr)
        return limited(items, req)

    def dump(self, ns):
        return {'items': [{'id': e['id'], 'text': e['content'], 'layer': 'raw',
                           'timestamp': e.get('timestamp', '')} for e in self.events.values()]}

    def close(self):
        if self.native is not None:
            native, self.native = self.native, None
            native.close()
