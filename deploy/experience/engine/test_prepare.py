"""Finite executor regressions; private synthetic operations, no subprocesses."""
import base64
import copy
import errno
import hashlib
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import guest
import prepare

from contract import STEP_NAMES, validate_receipt_v2 as validate_receipt
from test_contract import (configuration, listeners_v2 as listeners, preflight_v2 as preflight,
                           receipt_v2 as receipt, tiny_kit)
from bundle import build_bundle, verify_bundle
from prepare import _run_window, _control_envelope, _bounded_run, _Host, _Failure


class Operations:
    def __init__(self, fail=None, mode='exception'):
        self.clock = 10.0
        self.origin = self.clock
        self.deadline = self.origin + 120
        self.fail, self.mode = fail, mode
        self.calls, self.saved, self.published = [], None, None
        self.binding = {key: receipt()[key] for key in (
            'bundle_manifest_sha256', 'kit_manifest_sha256', 'source_root', 'templates_root')}

    def monotonic(self):
        return self.clock

    def call(self, name, deadline, record):
        self.calls.append(name)
        self.clock += .001
        if self.fail == name:
            record.raw('rejected-observation', {'owner_pid': 'unexpected', 'raw': 'retained'})
            if self.mode == 'nonzero':
                record.command({'exit_code': 7, 'stdout': 'partial out', 'stderr': 'partial err',
                                'timed_out': False, 'truncated': False})
            elif self.mode == 'truncated':
                record.command({'exit_code': 0, 'stdout': 'x' * 16384, 'stderr': '',
                                'timed_out': False, 'truncated': True})
            elif self.mode == 'timeout':
                self.clock = self.deadline + .037
                record.attempt(name + '-effect')
                record.command({'exit_code': -9, 'stdout': 'late', 'stderr': '',
                                'timed_out': True, 'truncated': False})
            raise ValueError('injected ' + self.mode)
        r = receipt()
        if name == 'claim_stage':
            record.known('created_private_stage')
        values = {'preflight': preflight(), 'claim_stage': {'exclusive': 'synthetic'},
            'install_files': r['installed_files'], 'daemon_reload': None,
            'effective_configuration': configuration(), 'containerd_start': None,
            'containerd_ready': listeners('containerd_ready'), 'engine_start': None,
            'engine_ready': {'listeners': listeners(), 'engine': r['engine']},
            'networks': r['networks'], 'final_identity': {
                key: copy.deepcopy(r[key]) for key in ('boot_id', 'filesystem', 'runtime_parent',
                    'daemons', 'sockets', 'socket_parents', 'engine', 'networks', 'installed_files')},
            'write_receipt': {'candidate_written': True}}
        record.command({'exit_code': 0, 'stdout': '', 'stderr': '',
                        'timed_out': False, 'truncated': False})
        return values[name]

    def publish(self, value, deadline, record):
        self.calls.append('publish')
        if self.fail == 'publish':
            record.attempt('receipt_publication')
            raise OSError('publication unknown')
        self.published = copy.deepcopy(value)
        record.known('receipt_publication')

    def preserve(self, value):
        self.saved = copy.deepcopy(value)


class WindowTests(unittest.TestCase):
    def test_success_uses_canonical_contract_and_publishes_after_all_steps(self):
        ops = Operations()
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(result['status'], 'engine_ready')
        self.assertEqual(ops.calls, list(STEP_NAMES) + ['publish'])
        validate_receipt(ops.published)
        self.assertFalse(ops.published['execution_enabled'])
        self.assertNotIn('receipt_publication', ops.published['known_effects'])
        self.assertIn('receipt_publication', result['publication']['known_effects'])

    def test_every_phase_retains_raw_and_stops_on_all_failure_classes(self):
        for phase in STEP_NAMES:
            for mode in ('exception', 'nonzero', 'timeout', 'truncated'):
                with self.subTest(phase=phase, mode=mode):
                    ops = Operations(phase, mode)
                    result = _run_window(ops, ops.monotonic)
                    self.assertNotEqual(result['status'], 'engine_ready')
                    self.assertEqual(ops.calls, list(STEP_NAMES[:STEP_NAMES.index(phase) + 1]))
                    self.assertEqual(result, ops.saved)
                    self.assertTrue(any(o['kind'] == 'rejected-observation' for o in result['observations']))
                    self.assertIsNone(ops.published)
                    validate_receipt(result['receipt'])
                    if mode == 'timeout':
                        self.assertGreater(result['receipt']['elapsed_ms'], 120000)
                        self.assertIn('late', result['receipt']['steps'][-1]['stdout'])

    def test_invalid_preflight_has_zero_mutation_or_api(self):
        ops = Operations()
        call = ops.call
        def invalid(name, deadline, record):
            value = call(name, deadline, record)
            if name == 'preflight':
                value['writers'] = [{'foreign': 'writer'}]
            return value
        ops.call = invalid
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(ops.calls, ['preflight'])
        self.assertIn('foreign', str(result['observations']))

    def test_first_start_timeout_prevents_engine_start_api_and_network(self):
        ops = Operations('containerd_start', 'timeout')
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(ops.calls.count('containerd_start'), 1)
        self.assertNotIn('engine_start', ops.calls)
        self.assertNotIn('engine_ready', ops.calls)
        self.assertNotIn('networks', ops.calls)
        self.assertEqual(result['status'], 'unknown')

    def test_partial_install_survives_later_unknown_failure(self):
        ops = Operations()
        call = ops.call
        def fail_after_first(name, deadline, record):
            if name == 'install_files':
                ops.calls.append(name)
                record.known('one_installed_file')
                record.partial('installed_files', receipt()['installed_files'][:1])
                record.attempt('second_file_write')
                raise TimeoutError('second exclusive write unknown')
            return call(name, deadline, record)
        ops.call = fail_after_first
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(result['receipt']['installed_files'], receipt()['installed_files'][:1])
        self.assertIn('second_file_write', result['receipt']['unknown_effects'])
        validate_receipt(result['receipt'])

    def test_socket_owner_uncertainty_stops_before_engine(self):
        ops = Operations()
        call = ops.call
        def invalid(name, deadline, record):
            value = call(name, deadline, record)
            if name == 'containerd_ready':
                value['unix'][0]['owners'][0]['main_pid'] += 1
            return value
        ops.call = invalid
        result = _run_window(ops, ops.monotonic)
        self.assertNotIn('engine_start', ops.calls)
        self.assertNotEqual(result['status'], 'engine_ready')
        self.assertEqual(result['receipt']['daemons'], [])

    def test_deadline_does_not_allow_next_operation_or_final_publication(self):
        ops = Operations()
        call = ops.call
        def expire(name, deadline, record):
            value = call(name, deadline, record)
            if name == 'engine_ready':
                ops.clock = ops.deadline
            return value
        ops.call = expire
        result = _run_window(ops, ops.monotonic)
        self.assertNotIn('networks', ops.calls)
        self.assertNotIn('publish', ops.calls)
        self.assertNotEqual(result['status'], 'engine_ready')

    def test_publication_failure_is_outer_unknown_and_preserves_candidate(self):
        ops = Operations('publish')
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(result['status'], 'unknown')
        self.assertEqual(result['publication']['unknown_effects'], ['receipt_publication'])
        self.assertIsNone(ops.published)
        self.assertEqual(result['receipt']['steps'][-1]['status'], 'succeeded')
        self.assertEqual(result['receipt_role'], 'prepared_body_not_publication_attestation')
        validate_receipt(result['receipt'])

    def test_cross_clock_transport_can_only_shorten_original_budget(self):
        binding = Operations().binding
        sample = {'schema': 'switchback.engine.clock_sample.v1', 'monotonic_ns': 900000000000}
        envelope = _control_envelope(sample, 130000000000, 15000000000, binding)
        self.assertEqual(envelope['deadline_ns'], 1015000000000)
        # Guest receives it five seconds later: it still has110s, not115s.
        self.assertEqual((envelope['deadline_ns'] - 905000000000) // 1000000000, 110)
        with self.assertRaises(ValueError):
            _control_envelope(sample, 130000000000, 130000000000, binding)


class BundleDeadlineTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name).resolve()
        parent = self.home / '.switchback'; parent.mkdir(mode=0o700)
        self.kit = tiny_kit(parent)
        self.kit.rename(parent / 'qualification-kit'); self.kit = parent / 'qualification-kit'
        output = parent / 'bundle-0001'
        build_bundle(self.kit, output)
        output.rename(parent / 'engine-bundle'); self.bundle = parent / 'engine-bundle'

    def test_host_preflight_stops_inside_first_slow_bundle_read(self):
        clock, reads = [1.0], []
        read = os.read
        def slow_read(fd, count):
            reads.append(count)
            result = read(fd, count)
            clock[0] = 122.0
            return result
        def guard(deadline):
            if clock[0] >= deadline:
                raise _Failure('expired')
        with (mock.patch('prepare._host_home', return_value=self.home),
              mock.patch('prepare.sys.platform', 'darwin'),
              mock.patch('prepare.time.monotonic', side_effect=lambda: clock[0]),
              mock.patch('prepare._guard', side_effect=guard),
              mock.patch('bundle.os.read', side_effect=slow_read),
              mock.patch('prepare.subprocess.Popen', side_effect=AssertionError('no command before verification'))):
            host = _Host(1000000000)
            with self.assertRaises(_Failure):
                host.preflight()
        self.assertEqual(len(reads), 1, 'verifier must stop before another read after original deadline')
        self.assertFalse(host.stage_owned)

    def test_canonical_verifier_checks_each_directory_entry(self):
        import bundle
        clock, visited = [0], []
        scan = os.scandir
        class SlowDirectory:
            def __init__(self, path):
                self.iterator = scan(path)
            def __enter__(self):
                return self
            def __exit__(self, *args):
                self.iterator.close()
            def __iter__(self):
                return self
            def __next__(self):
                entry = next(self.iterator)
                visited.append(entry.name); clock[0] += 121
                return entry
        def check():
            if clock[0] >= 120:
                raise _Failure('original deadline during directory enumeration')
        with mock.patch.object(bundle.os, 'scandir', side_effect=SlowDirectory):
            with self.assertRaises(_Failure):
                verify_bundle(self.bundle, self.kit, check=check)
        self.assertEqual(len(visited), 1)

    def test_canonical_reader_checks_each_payload_chunk(self):
        import bundle
        payload = self.home / 'chunked'; payload.write_bytes(b'x' * (3 << 20)); payload.chmod(0o600)
        clock, counts = [0], []
        read = os.read
        def slow_read(fd, count):
            counts.append(count)
            value = read(fd, count); clock[0] += 61
            return value
        def check():
            if clock[0] >= 120: raise _Failure('original deadline between chunks')
        with mock.patch.object(bundle.os, 'read', side_effect=slow_read):
            with self.assertRaisesRegex(_Failure, 'between chunks'):
                bundle._read(self.home, 'chunked', check=check)
        self.assertEqual(counts, [1 << 20, 1 << 20])

    def test_guest_input_collector_stops_inside_first_slow_read_using_control_deadline(self):
        import guest
        clock, reads = [1.0], []
        read = os.read
        def slow_read(fd, count):
            result = read(fd, count); reads.append(count); clock[0] = 122.0
            return result
        def guard(deadline):
            if clock[0] >= deadline:
                raise _Failure('host-derived deadline')
        with (mock.patch.object(guest, '_BUNDLE', self.bundle), mock.patch.object(guest, '_KIT', self.kit),
              mock.patch.object(guest, '_guard', side_effect=guard), mock.patch('bundle.os.read', side_effect=slow_read),
              mock.patch.object(guest, '_GuestOperations', side_effect=AssertionError('no actual phase')),
              mock.patch('subprocess.Popen', side_effect=AssertionError('no command')),
              mock.patch('socket.socket', side_effect=AssertionError('no socket'))):
            with self.assertRaisesRegex(_Failure, 'host-derived deadline'):
                guest._load_guest_inputs({'deadline_ns': 121000000000, 'binding': Operations().binding})
        self.assertEqual(len(reads), 1)

    def test_guest_requires_exact_host_binding_after_full_canonical_verification(self):
        import guest
        import json
        import prepare
        manifest_raw = (self.bundle / 'manifest.json').read_bytes()
        manifest = json.loads(manifest_raw)
        binding = {'bundle_manifest_sha256': prepare._sha(manifest_raw),
                   **{key: manifest[key] for key in ('kit_manifest_sha256', 'source_root', 'templates_root')}}
        read = prepare._read
        def temporary_owned_read(path, bound, uid, check, mode=None):
            return read(path, bound, os.getuid(), check, mode)
        with (mock.patch.object(guest, '_BUNDLE', self.bundle), mock.patch.object(guest, '_KIT', self.kit),
              mock.patch.object(guest, '_guard'), mock.patch.object(guest, '_read', side_effect=temporary_owned_read)):
            _, actual, files = guest._load_guest_inputs({'deadline_ns': 121000000000, 'binding': binding})
            self.assertEqual(actual, binding)
            self.assertEqual(files, configuration())
            drifted = dict(binding, source_root='sha256:' + '0' * 64)
            with self.assertRaisesRegex(ValueError, 'differs from host canonical binding'):
                guest._load_guest_inputs({'deadline_ns': 121000000000, 'binding': drifted})


class SyntheticProcess:
    """Leader exit is observable without running/reaping a real subprocess."""
    def __init__(self, exited):
        self.pid, self.returncode, self.exited, self.polls = 43210, None, exited, 0
        self.stdin, self.stdout, self.stderr = (self.Stream(n) for n in (10, 11, 12))
    class Stream:
        def __init__(self, number):
            self.number, self.closed = number, False
        def fileno(self):
            return self.number
        def close(self):
            self.closed = True
    def poll(self):
        self.polls += 1
        if self.exited:
            self.returncode = 0
        return self.returncode
    def wait(self, timeout):
        self.returncode = 0
        return self.returncode


class RetainedPipeSelector:
    def __init__(self, clock, deadline):
        self.clock, self.deadline, self.keys, self.rounds = clock, deadline, {}, 0
    def register(self, stream, events, data):
        self.keys[stream.fileno()] = SimpleNamespace(fileobj=stream, fd=stream.fileno(), data=data)
    def get_map(self):
        return self.keys
    def select(self, timeout):
        self.rounds += 1
        if self.rounds == 1:
            return [(self.keys[n], 1) for n in (11, 12)]
        self.clock[0] = self.deadline + .037
        return []
    def close(self):
        self.keys.clear()


class ProcessBoundaryTests(unittest.TestCase):
    def normal_boundary(self, host_transport, cleanup_error=None, identity_lost=False,
                        darwin_facts=None, converged=False, platform='linux', scenario=None, owned_pid=43210,
                        recorder=None, operation=None):
        import prepare
        scenario = scenario or {}
        clock, events, observations = [10.0], [], []
        process = SyntheticProcess(True)
        process.pid = owned_pid
        if recorder is not None:
            # Reserve fake FDs outside the real tiny-kit/config reader's range.
            for stream in (process.stdin, process.stdout, process.stderr):
                stream.number += 100000
        stdout_fd = process.stdout.fileno()
        native_read, native_write = os.read, os.write
        if identity_lost == 'already-reaped': process.returncode = 0
        def reap(timeout):
            events.append('reap_leader')
            process.returncode = scenario.get('reap_code', scenario.get('exit_code', 0))
            return process.returncode
        process.wait = reap
        class NormalSelector:
            def __init__(self): self.keys, self.rounds = {}, 0
            def register(self, stream, event, label):
                self.keys[label] = SimpleNamespace(fileobj=stream, fd=stream.fileno(), data=label)
            def unregister(self, stream):
                for label, key in list(self.keys.items()):
                    if key.fileobj is stream: del self.keys[label]
            def get_map(self): return self.keys
            def close(self): self.keys.clear()
            def select(self, timeout):
                self.rounds += 1; clock[0] += .001
                if not host_transport: labels = ('stdout', 'stderr')
                elif self.rounds == 1: labels = ('stdout',)
                elif self.rounds == 2: labels = ('control',)
                else: labels = ('stdout', 'stderr')
                return [(self.keys[label], 1) for label in labels if label in self.keys]
        sample = prepare._encode({'schema': 'switchback.engine.clock_sample.v1', 'monotonic_ns': 900000000000}) + b'\n'
        final = prepare._encode({'kind': 'outcome', 'value': {'status': 'engine_ready', 'receipt': receipt()}}) + b'\n'
        stdout = iter((sample, final, b'') if host_transport else (scenario.get('stdout', b'normal output'), b''))
        stderr = iter((scenario.get('stderr', b'normal stderr'), b''))
        observed_count = [0]
        def terminal(pid):
            self.assertEqual(pid, process.pid)
            self.assertIsNone(process.returncode)
            events.append('observe_terminal_without_reap')
            observed_count[0] += 1
            if identity_lost: raise ChildProcessError(errno.ECHILD, 'child identity lost')
            if observed_count[0] == scenario.get('terminal_error_at'):
                raise ChildProcessError(errno.ECHILD, 'child identity lost during proof')
            if observed_count[0] == scenario.get('terminal_none_at'):
                return None
            if observed_count[0] == scenario.get('terminal_timeout_at'):
                clock[0] = 131
            if observed_count[0] == scenario.get('reaped_at'):
                process.returncode = 0
            value = dict(si_pid=pid, si_uid=0, si_signo=int(signal.SIGCHLD), si_code=os.CLD_EXITED,
                         si_status=scenario.get('exit_code', 0))
            if observed_count[0] == scenario.get('terminal_change_at'):
                value.update(scenario['terminal_change'])
            return SimpleNamespace(**value)
        def killpg(pid, sig):
            self.assertEqual((pid, sig), (process.pid, signal.SIGKILL))
            self.assertIsNone(process.returncode, 'never signal a reaped/reusable PGID')
            events.append('signal_owned_group')
            if cleanup_error: raise cleanup_error
        def group(pid):
            self.assertEqual(pid, process.pid)
            self.assertIsNone(process.returncode, 'snapshot requires the retained original child')
            events.append('snapshot_owned_group')
            if scenario.get('snapshot_timeout'): clock[0] = 131
            if scenario.get('snapshot_reaped'): process.returncode = 0
            if scenario.get('snapshot_pid_drift'): process.pid += 1
            if scenario.get('group_error'): raise scenario['group_error']
            if darwin_facts is None: raise RuntimeError('no synthetic group snapshot')
            return darwin_facts
        def group_factory():
            if scenario.get('factory_error'): raise scenario['factory_error']
            if scenario.get('factory_reaped'): process.returncode = 0
            if scenario.get('factory_pid_drift'): process.pid += 1
            return group
        read_count = [0]
        def read(fd, count):
            if fd not in (process.stdout.fileno(), process.stderr.fileno()):
                return native_read(fd, count)
            read_count[0] += 1
            if scenario.get('original_error') or (scenario.get('error_after_partial') and read_count[0] > 2):
                raise ValueError('synthetic original I/O failure')
            if scenario.get('overflow'): return b'x' * (prepare._FRAME + 1 if host_transport else prepare._OUTPUT + 1)
            return next(stdout if fd == stdout_fd else stderr)
        # Host.preflight calls the module function without a clock argument;
        # its Python default captured the real clock before this fixture.
        # Explicitly forward the same synthetic clock used by all boundaries.
        with (mock.patch('prepare._bounded_run', side_effect=lambda argv, deadline, record, monotonic=None, input_data=None:
                  _bounded_run(argv, deadline, record, monotonic or (lambda: clock[0]), input_data)),
              mock.patch('prepare.subprocess.Popen', return_value=process) as spawn,
              mock.patch('prepare.selectors.DefaultSelector', return_value=NormalSelector()),
              mock.patch('prepare.os.set_blocking'),
              mock.patch('prepare.os.read', side_effect=read),
              mock.patch('prepare.os.write', side_effect=lambda fd, raw:
                  len(raw) if fd == process.stdin.fileno() else native_write(fd, raw)),
              mock.patch('prepare.os.killpg', side_effect=killpg),
              mock.patch('prepare._darwin_group_reader', side_effect=group_factory, create=True),
              mock.patch('prepare.sys.platform', platform),
              mock.patch('prepare._waitid_reader', return_value=terminal, create=True),
              mock.patch('prepare._guard', side_effect=lambda deadline, monotonic=None:
                  (_ for _ in ()).throw(_Failure('original deadline')) if clock[0] >= deadline else None),
              mock.patch('prepare.time.monotonic', side_effect=lambda: clock[0]),
              mock.patch('prepare.time.monotonic_ns', side_effect=lambda: int(clock[0] * 1000000000))):
            if host_transport:
                host = _Host(10000000000); host.binding, host.vm = Operations().binding, {}
                host.emit = observations.append
                call = host.transport
            elif recorder is not None:
                observations = recorder.frames
                call = (lambda: operation(recorder)) if operation else lambda: _bounded_run(
                    ['/fixed/synthetic'], recorder.deadline, recorder, lambda: clock[0])
            else:
                def command(value):
                    observations.append(value)
                    if scenario.get('exit_code'):
                        # Use the real recorder's nonzero-exit rejection.
                        outcome = {'observations': []}
                        step = {'name': 'preflight', 'stdout': '', 'stderr': ''}
                        prepare._Record(outcome, step, lambda value: None).command(value)
                record = SimpleNamespace(command=command,
                    raw=lambda kind, value: observations.append({'kind': kind, 'value': value}))
                call = lambda: _bounded_run(['/fixed/synthetic'], 130, record, lambda: clock[0])
            failed = bool(identity_lost or any(key != 'stdout' for key in scenario) or (cleanup_error is not None
                and getattr(cleanup_error, 'errno', None) != errno.ESRCH and not converged))
            if failed:
                expected_error = ValueError if host_transport and any(scenario.get(key) for key in
                    ('original_error', 'overflow', 'exit_code')) else _Failure
                with self.assertRaises(expected_error): call()
            else:
                result = call()
                if host_transport: self.assertEqual(host.result['status'], 'engine_ready')
                elif operation is None: self.assertEqual(result, scenario.get('stdout', b'normal output'))
        if recorder is not None:
            spawn.assert_called_once()
        if identity_lost == 'already-reaped':
            self.assertEqual(events, [])
        elif identity_lost:
            self.assertEqual(events, ['observe_terminal_without_reap'])
        elif converged:
            self.assertEqual(events, ['observe_terminal_without_reap', 'signal_owned_group',
                'observe_terminal_without_reap', 'snapshot_owned_group',
                'observe_terminal_without_reap', 'reap_leader'])
        elif not scenario and platform != 'darwin':
            self.assertEqual(events, ['observe_terminal_without_reap', 'signal_owned_group', 'reap_leader'])
        else:
            self.assertEqual(events.count('signal_owned_group'), 1)
            if 'snapshot_owned_group' in events:
                self.assertLess(events.index('signal_owned_group'), events.index('snapshot_owned_group'))
            if 'reap_leader' in events and 'snapshot_owned_group' in events:
                self.assertLess(events.index('snapshot_owned_group'), events.index('reap_leader'))
        self.assertEqual(process.polls, 0)
        self.assertTrue(all(stream.closed for stream in (process.stdin, process.stdout, process.stderr)))
        if recorder is not None:
            value = next(v['value'] for v in observations if v.get('kind') == 'host-command-result')
        else:
            value = next(v for v in observations if v.get('kind') == 'guest-transport-final-observation') if host_transport else next(v for v in observations if 'argv' in v)
        self.assertEqual(value['exit_code'], None if identity_lost is True else scenario.get(
            'expected_exit', scenario.get('reap_code', scenario.get('exit_code', 0))))
        if cleanup_error: self.assertIn(str(cleanup_error), str(value['cleanup']))
        # The real Host.emit must be able to encode all nested raw proof facts.
        prepare._encode(prepare._wire(value))
        return value

    def test_captured_exit0_eperm_converges_only_with_new_synthetic_sole_leader_proof(self):
        # Captured origin: runtime-m1-resident-engine-execution-1023/
        # guest-target-inventory.json, exit0 and EPERM. That receipt contains no
        # group snapshot; the additional 8192-byte facts below are synthetic.
        captured = {'exit_code': 0, 'errno': errno.EPERM, 'pid': 56779}
        origin = Path(__file__).resolve().parents[3] / '.scratch/runtime-m1-resident-engine-execution-1023/guest-target-inventory.json'
        if origin.exists():
            # Check the original when present; copied source remains self-contained.
            command = json.loads(origin.read_bytes())['command']
            original_signal = next(v for v in command['cleanup'] if v['operation'] == 'kill_owned_group')
            self.assertEqual({'exit_code': command['exit_code'], 'errno': original_signal['errno'],
                              'pid': command['leader_terminal']['si_pid']}, captured)
            self.assertEqual(original_signal['status'], 'failed')
        facts = {'byte_count': 4, 'errno': 0,
                 'raw': captured['pid'].to_bytes(4, sys.byteorder, signed=True) + bytes(8188)}
        for host in (False, True):
            with self.subTest(host_transport=host):
                value = self.normal_boundary(host, PermissionError(errno.EPERM, 'signal denied'),
                    darwin_facts=facts, converged=True, platform='darwin', owned_pid=captured['pid'])
                signal_result = next(v for v in value['cleanup'] if v['operation'] == 'kill_owned_group')
                self.assertEqual(signal_result['status'], 'already_terminal')
                self.assertEqual(signal_result['signal_status'], 'failed')
                self.assertFalse(signal_result['signalled'])
                self.assertEqual(signal_result['errno'], errno.EPERM)
                self.assertIn('already_terminal', str(value['cleanup']))

    def test_darwin_eperm_rejects_incomplete_or_nonsole_snapshots_with_original_errno(self):
        leader = (43210).to_bytes(4, sys.byteorder, signed=True)
        extra = (43211).to_bytes(4, sys.byteorder, signed=True)
        sole = {'byte_count': 4, 'errno': 0, 'raw': leader + bytes(8188)}
        cases = {
            'live-extra-member': dict(sole, byte_count=8, raw=leader + extra + bytes(8184)),
            'zombie-extra-member': dict(sole, byte_count=8, raw=leader + extra + bytes(8184)),
            'duplicate-leader': dict(sole, byte_count=8, raw=leader * 2 + bytes(8184)),
            'wrong-leader': dict(sole, raw=extra + bytes(8188)),
            'negative-pid': dict(sole, raw=(-1).to_bytes(4, sys.byteorder, signed=True) + bytes(8188)),
            'zero-pid': dict(sole, raw=bytes(8192)),
            'empty-result': dict(sole, byte_count=0, raw=bytes(8192)),
            'negative-count': dict(sole, byte_count=-1),
            'full-buffer': dict(sole, byte_count=8192),
            'oversized-result': dict(sole, byte_count=8196),
            'unaligned-result': dict(sole, byte_count=3),
            'truncated-buffer': dict(sole, raw=leader),
            'oversized-buffer': dict(sole, raw=leader + bytes(8189)),
            'sticky-errno': dict(sole, errno=errno.EBUSY),
            'failed-read': dict(sole, byte_count=0, errno=errno.EPERM),
            'bool-count': dict(sole, byte_count=True),
            'bool-errno': dict(sole, errno=False),
            'wrong-raw-type': dict(sole, raw='not native bytes'),
            'missing-fact': {'byte_count': 4, 'raw': sole['raw']},
        }
        for host in (False, True):
            for name, facts in cases.items():
                with self.subTest(host_transport=host, snapshot=name):
                    value = self.normal_boundary(host, PermissionError(errno.EPERM, 'captured signal denied'),
                        darwin_facts=facts, platform='darwin')
                    signal_result = next(v for v in value['cleanup'] if v['operation'] == 'kill_owned_group')
                    self.assertEqual((signal_result['status'], signal_result['signal_status'],
                                      signal_result['signalled'], signal_result['errno']),
                                     ('failed', 'failed', False, errno.EPERM))
                    snapshot = next(v['value']['value'] for v in value['cleanup'] if
                        v['operation'] == 'observe_darwin_terminal_group'
                        and v['value']['kind'] == 'darwin-group-snapshot')
                    import prepare
                    self.assertEqual(snapshot, prepare._wire(facts), 'rejected native facts precede validation')
                    self.assertNotIn('already_terminal', str(value['cleanup']))

    def test_darwin_proof_rejects_lost_reaped_changed_or_unobserved_child(self):
        sole = {'byte_count': 4, 'errno': 0,
                'raw': (43210).to_bytes(4, sys.byteorder, signed=True) + bytes(8188)}
        cases = [
            {'terminal_error_at': 2, 'expected_exit': None},
            {'terminal_error_at': 3, 'expected_exit': None},
            {'reaped_at': 2}, {'reaped_at': 3}, {'snapshot_reaped': True},
            {'factory_reaped': True}, {'factory_pid_drift': True, 'expected_exit': None},
            {'snapshot_pid_drift': True, 'expected_exit': None},
            {'terminal_none_at': 2}, {'terminal_none_at': 3},
            {'terminal_change_at': 2, 'terminal_change': {'si_pid': 43211}, 'expected_exit': None},
            {'terminal_change_at': 3, 'terminal_change': {'si_pid': 43211}, 'expected_exit': None},
            {'terminal_change_at': 2, 'terminal_change': {'si_status': 7}},
            {'terminal_change_at': 3, 'terminal_change': {'si_uid': 501}},
            {'terminal_change_at': 2, 'terminal_change': {'si_code': 99}},
            {'terminal_change_at': 2, 'terminal_change': {'si_uid': False}},
            {'terminal_timeout_at': 2}, {'snapshot_timeout': True},
            {'factory_error': ValueError('unsupported Darwin64 group observation ABI')},
            {'factory_error': OSError(errno.ENOENT, 'fixed libproc unavailable')},
            {'group_error': OSError(errno.EIO, 'native group reader failure')},
        ]
        for host in (False, True):
            for case in cases:
                with self.subTest(host_transport=host, case=case):
                    value = self.normal_boundary(host, PermissionError(errno.EPERM, 'original signal denied'),
                        darwin_facts=sole, platform='darwin', scenario=case)
                    self.assertNotIn('already_terminal', str(value['cleanup']))
                    self.assertTrue(any(v['status'] == 'failed' for v in value['cleanup']))
                    self.assertIn('original signal denied', str(value['cleanup']))

    def test_only_darwin_os_eperm_can_use_group_proof(self):
        sole = {'byte_count': 4, 'errno': 0,
                'raw': (43210).to_bytes(4, sys.byteorder, signed=True) + bytes(8188)}
        impostor = RuntimeError('not an OS signal error'); impostor.errno = errno.EPERM
        for host in (False, True):
            for platform, error in (('linux', PermissionError(errno.EPERM, 'denied')),
                    ('unknown', PermissionError(errno.EPERM, 'denied')),
                    ('darwin', PermissionError(errno.EACCES, 'other errno')),
                    ('darwin', OSError(errno.EIO, 'other error')), ('darwin', impostor)):
                with self.subTest(host_transport=host, platform=platform, error=error):
                    value = self.normal_boundary(host, error, darwin_facts=sole, platform=platform)
                    self.assertFalse(any(v['operation'] == 'observe_darwin_terminal_group' for v in value['cleanup']))

    def test_positive_group_proof_cannot_hide_original_failure_nonzero_truncation_or_reap_drift(self):
        sole = {'byte_count': 4, 'errno': 0,
                'raw': (43210).to_bytes(4, sys.byteorder, signed=True) + bytes(8188)}
        for host in (False, True):
            for case in ({'original_error': True}, {'overflow': True}, {'exit_code': 7}, {'reap_code': 7}):
                with self.subTest(host_transport=host, case=case):
                    value = self.normal_boundary(host, PermissionError(errno.EPERM, 'signal denied'),
                        darwin_facts=sole, converged=True, platform='darwin', scenario=case)
                    self.assertIn('already_terminal', str(value['cleanup']))
                    if case.get('original_error'): self.assertIn('synthetic original I/O failure', str(value['error']))
                    if case.get('overflow'):
                        self.assertIn('bound', str(value['error']))
                        if not host: self.assertTrue(value['truncated'])
                    if case.get('reap_code'): self.assertIn('leader_exit_readback', str(value['cleanup']))

    def test_normal_eof_fences_owned_descendants_before_reaping_terminal_leader(self):
        for host in (False, True):
            for error in (None, ProcessLookupError(errno.ESRCH, 'group already exited'), PermissionError(errno.EPERM, 'signal denied')):
                with self.subTest(host_transport=host, error=error):
                    self.normal_boundary(host, error)

    def test_lost_child_identity_never_signals_reusable_numeric_group(self):
        for host in (False, True):
            for lost in (True, 'already-reaped'):
                with self.subTest(host_transport=host, identity_lost=lost):
                    self.normal_boundary(host, identity_lost=lost)

    def run_boundary(self, host_transport, cleanup_error=None, exited=True, recorder=None):
        clock, calls, commands = [10.0], [], []
        process = SyntheticProcess(exited)
        selector = RetainedPipeSelector(clock, 130)
        def killpg(pid, sig):
            calls.append((pid, sig))
            if cleanup_error:
                raise cleanup_error
        with (mock.patch('prepare.subprocess.Popen', return_value=process),
              mock.patch('prepare.selectors.DefaultSelector', return_value=selector),
              mock.patch('prepare._waitid_reader', return_value=lambda pid: SimpleNamespace(
                  si_pid=pid, si_uid=0, si_signo=int(signal.SIGCHLD), si_code=os.CLD_EXITED, si_status=0)),
              mock.patch('prepare.os.set_blocking'),
              mock.patch('prepare.os.read', side_effect=lambda fd, _: b'partial stdout' if fd == 11 else b'partial stderr'),
              mock.patch('prepare.os.killpg', side_effect=killpg),
              mock.patch('prepare._darwin_group_reader', side_effect=AssertionError('no native group query'), create=True),
              mock.patch('prepare._guard', side_effect=lambda deadline, monotonic=None:
                  (_ for _ in ()).throw(_Failure('original timeout', True, None)) if clock[0] >= deadline else None),
              mock.patch('prepare.time.monotonic', side_effect=lambda: clock[0]),
              mock.patch('prepare.time.monotonic_ns', side_effect=lambda: int(clock[0] * 1000000000))):
            if host_transport:
                host = _Host(10000000000)
                host.binding, host.vm = Operations().binding, {}
                host.emit = commands.append
                with self.assertRaisesRegex(_Failure, 'original timeout'):
                    host.transport()
                value = next(v for v in commands if v['kind'] == 'guest-transport-final-observation')
                self.assertEqual(value['trailing_stdout']['base64'], 'cGFydGlhbCBzdGRvdXQ=')
                self.assertEqual(value['stderr']['base64'], 'cGFydGlhbCBzdGRlcnI=')
            elif recorder is not None:
                with self.assertRaisesRegex(_Failure, 'original timeout'):
                    _bounded_run(['/fixed/synthetic'], recorder.deadline, recorder, lambda: clock[0])
                value = next(v['value'] for v in recorder.frames if v['kind'] == 'host-command-result')
                self.assertEqual(value['stdout']['base64'], 'cGFydGlhbCBzdGRvdXQ=')
                self.assertEqual(value['stderr']['base64'], 'cGFydGlhbCBzdGRlcnI=')
                self.assertTrue(value['timed_out'])
                self.assertIn('original timeout', value['error'])
            else:
                operations = Operations()
                def command_phase(name, deadline, record):
                    return _bounded_run(['/fixed/synthetic'], deadline, record, lambda: clock[0])
                operations.call = command_phase
                result = _run_window(operations, lambda: clock[0])
                validate_receipt(result['receipt'])
                value = next(v['value'] for v in result['observations'] if v['kind'] == 'command-result')
                self.assertIn('partial stdout', result['receipt']['steps'][0]['stdout'])
                self.assertIn('partial stderr', result['receipt']['steps'][0]['stderr'])
                self.assertIn('original timeout', result['error'])
                self.assertEqual(result, operations.saved)
            self.assertEqual(value['exit_code'], 0)
            self.assertEqual(calls, [(process.pid, signal.SIGKILL)])
            self.assertEqual(process.polls, 0, 'reaping leader before signalling can lose group ownership')
            self.assertTrue(all(s.closed for s in (process.stdin, process.stdout, process.stderr)))
            if cleanup_error:
                self.assertIn(str(cleanup_error), str(value['cleanup']))
            return value

    def test_exited_leader_with_retained_pipes_is_still_owned_and_terminated(self):
        for host in (False, True):
            with self.subTest(host_transport=host):
                self.run_boundary(host)

    def test_cleanup_esrch_and_other_errors_keep_original_failure_and_final_evidence(self):
        for host in (False, True):
            for error in (ProcessLookupError(errno.ESRCH, 'group exited'), PermissionError(errno.EPERM, 'signal rejected')):
                with self.subTest(host_transport=host, error=error):
                    self.run_boundary(host, error, exited=False)


class HostRecorderTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name).resolve()
        with mock.patch('prepare._host_home', return_value=self.home):
            self.host = _Host(10000000000)

    def own_stage(self):
        self.host.root.mkdir(mode=0o700, parents=True)
        self.host.stage_owned = True

    def test_shared_supervisor_retains_raw_and_command_with_real_host(self):
        host = self.host
        value = ProcessBoundaryTests().normal_boundary(False, recorder=host)
        self.assertEqual([frame['kind'] for frame in host.frames],
                         ['owned-leader-before-reap', 'host-command-result'])
        self.assertEqual(host.frames[0]['value']['si_pid'], 43210)
        self.assertEqual(value['stdout']['base64'], 'bm9ybWFsIG91dHB1dA==')
        self.assertEqual(value['stderr']['base64'], 'bm9ybWFsIHN0ZGVycg==')
        self.assertEqual(value['deadline_monotonic'], 130)
        self.assertFalse(host.stage_owned)
        self.assertFalse(host.root.exists())


    def test_preflight_checks_real_bundle_config_and_vm_before_claiming_stage(self):
        import prepare
        parent = self.home / '.switchback'; parent.mkdir(mode=0o700)
        kit = tiny_kit(parent)
        kit.rename(parent / 'qualification-kit'); kit = parent / 'qualification-kit'
        bundle = parent / 'bundle-0001'
        build_bundle(kit, bundle)
        bundle.rename(parent / 'engine-bundle')
        config = self.home / 'lima.yaml'; config.write_bytes(b'synthetic config\n'); config.chmod(0o600)
        lima = self.home / 'limactl'; lima.write_bytes(b'synthetic executable'); lima.chmod(0o700)
        vm = {'name': prepare._VM, 'status': 'Running', 'cpus': 4, 'memory': 10737418240,
              'dir': prepare._LIMA_HOME + '/' + prepare._VM}
        with (mock.patch('prepare._host_home', return_value=self.home),
              mock.patch('prepare._VM_CONFIG', str(config)), mock.patch('prepare._LIMA', str(lima)),
              mock.patch('prepare._read', wraps=prepare._read) as read):
            value = ProcessBoundaryTests().normal_boundary(False, recorder=self.host,
                operation=_Host.preflight, platform='darwin', scenario={'stdout': prepare._encode(vm)})
        self.assertEqual(sum(call.args[0] == config for call in read.call_args_list), 2)
        self.assertEqual(value['argv'], [str(lima), 'list', prepare._VM, '--json'])
        self.assertTrue(self.host.stage_owned)
        self.assertEqual(self.host.vm, {'name': prepare._VM, 'vcpus': 4, 'memory_bytes': 10737418240})
        claim = json.loads((self.host.root / 'claim.json').read_bytes())
        self.assertEqual(claim['vm'], vm)
        self.assertEqual(claim['binding'], self.host.binding)
        self.assertEqual(claim['deadline_monotonic_ns'], 130000000000)
        self.assertEqual(self.host.root.stat().st_mode & 0o777, 0o700)
        self.assertEqual([frame['kind'] for frame in self.host.frames][-2:],
                         ['owned-leader-before-reap', 'host-command-result'])
        for index, frame in enumerate(self.host.frames, 1):
            path = self.host.root / ('observation-%05d.json' % index)
            self.assertEqual(json.loads(path.read_bytes()), frame)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.host.root / 'claim.json').stat().st_mode & 0o777, 0o600)

    def test_raw_binary_is_wired_in_memory_and_exclusive_owned_stage(self):
        binary = {'nested': [b'\x00\xff']}
        expected = {'nested': [{'bytes': 2,
            'sha256': 'sha256:06eb7d6a69ee19e5fbdf749018d3d2abfa04bcbd1365db312eb86dc7169389b8',
            'base64': 'AP8='}]}
        self.host.raw('binary-proof', binary)
        self.assertEqual(self.host.frames, [{'kind': 'binary-proof', 'value': expected}])
        self.assertFalse(self.host.root.exists())
        self.own_stage()
        self.host.raw('binary-proof', binary)
        path = self.host.root / 'observation-00002.json'
        self.assertEqual(json.loads(path.read_bytes()), self.host.frames[-1])
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        collision = self.host.root / 'observation-00003.json'
        collision.write_bytes(b'preexisting evidence')
        with self.assertRaises(FileExistsError): self.host.raw('collision', binary)
        self.assertEqual(collision.read_bytes(), b'preexisting evidence')
        self.assertEqual(self.host.frames[-1], {'kind': 'collision', 'value': expected})

    def test_raw_frame_and_total_bounds_keep_prefix_hash_then_reject(self):
        import prepare
        for total in (False, True):
            with self.subTest(total=total):
                self.host.frames.clear(); self.host.bytes = prepare._AUDIT_BYTES if total else 0
                payload = 'small' if total else 'x' * prepare._FRAME
                with self.assertRaisesRegex(_Failure, 'host audit bound') as rejected_error:
                    self.host.raw('oversized', payload)
                self.assertTrue(rejected_error.exception.unknown)
                self.assertIsNone(rejected_error.exception.exit_code)
                rejected, = self.host.frames
                self.assertEqual(rejected['kind'], 'bounded-host-observation-rejected')
                raw = b'{"kind":"oversized","value":"' + payload.encode() + b'"}'
                self.assertEqual(rejected['bytes'], len(raw))
                self.assertEqual(rejected['sha256'], 'sha256:' + hashlib.sha256(raw).hexdigest())
                self.assertEqual(base64.b64decode(rejected['prefix_base64']), raw[:16384])

    def test_real_recorder_retains_partial_failure_cleanup_and_exit_facts(self):
        for scenario in ({'error_after_partial': True}, {'overflow': True},
                         {'exit_code': 7}, {'reap_code': None}):
            with self.subTest(scenario=scenario):
                self.host.frames.clear(); self.host.bytes = 0
                value = ProcessBoundaryTests().normal_boundary(False, recorder=self.host, scenario=scenario)
                if scenario.get('error_after_partial'):
                    self.assertIn('synthetic original I/O failure', value['error'])
                    self.assertEqual(value['stdout']['base64'], 'bm9ybWFsIG91dHB1dA==')
                    self.assertEqual(value['stderr']['base64'], 'bm9ybWFsIHN0ZGVycg==')
                if scenario.get('overflow'):
                    self.assertTrue(value['truncated'])
                    self.assertEqual(value['stdout']['bytes'], 16384)
                if 'reap_code' in scenario: self.assertIsNone(value['exit_code'])
                if 'exit_code' in scenario: self.assertEqual(value['exit_code'], 7)
        self.host.frames.clear(); self.host.bytes = 0
        value = ProcessBoundaryTests().normal_boundary(False, recorder=self.host,
            cleanup_error=PermissionError(errno.EPERM, 'synthetic cleanup denied'))
        self.assertIn('cleanup failed/unknown', value['error'])
        self.assertEqual(value['exit_code'], 0)
        self.assertIn('synthetic cleanup denied', str(value['cleanup']))

    def test_timeout_retains_partial_output_and_first_error_with_cleanup_failure(self):
        value = ProcessBoundaryTests().run_boundary(False, recorder=self.host,
            cleanup_error=PermissionError(errno.EPERM, 'synthetic cleanup denied'))
        self.assertEqual(value['exit_code'], 0)
        self.assertIn('original timeout', value['error'])

    def test_raw_persistence_failure_keeps_command_and_original_io_error_wins(self):
        self.own_stage()
        collision = self.host.root / 'observation-00001.json'
        collision.write_bytes(b'preexisting evidence')
        value = ProcessBoundaryTests().normal_boundary(False, recorder=self.host,
            scenario={'recording_error': True})
        self.assertIn('observation-00001.json', value['error'])
        self.assertEqual(value['exit_code'], 0)
        self.assertEqual([frame['kind'] for frame in self.host.frames],
                         ['owned-leader-before-reap', 'host-command-result'])
        self.assertEqual(collision.read_bytes(), b'preexisting evidence')
        self.host.frames.clear(); self.host.bytes = 0
        value = ProcessBoundaryTests().normal_boundary(False, recorder=self.host,
            scenario={'error_after_partial': True})
        self.assertIn('synthetic original I/O failure', value['error'])
        self.assertEqual(value['stdout']['base64'], 'bm9ybWFsIG91dHB1dA==')
        self.assertEqual(collision.read_bytes(), b'preexisting evidence')


class WaitidObservationTests(unittest.TestCase):
    def test_terminal_exit_and_signal_are_observed_without_reap_and_wait_uses_original_deadline(self):
        import prepare
        for code, status, expected in ((os.CLD_EXITED, 7, 7), (os.CLD_KILLED, 9, -9), (os.CLD_DUMPED, 11, -11)):
            with self.subTest(code=code):
                process = SyntheticProcess(True); observed = []
                reader = lambda pid: SimpleNamespace(si_pid=pid, si_uid=0, si_signo=int(signal.SIGCHLD), si_code=code, si_status=status)
                result = prepare._wait_leader(process, reader, 120, lambda: 10, observed.append)
                self.assertEqual(result['exit_code'], expected)
                self.assertIsNone(process.returncode)
                self.assertEqual(process.polls, 0)
                self.assertEqual(observed[0]['si_status'], status)
        clock = [0]
        def pending(pid): clock[0] = 121; return None
        with mock.patch('prepare.time.sleep', side_effect=AssertionError('no new timeout window')):
            with self.assertRaises(_Failure):
                prepare._wait_leader(SyntheticProcess(False), pending, 120, lambda: clock[0], lambda value: None)

    def test_darwin64_reader_uses_exact_sdk_abi_and_nonreaping_flags_with_fake_libsystem(self):
        import ctypes
        import prepare
        calls = []
        class FakeWaitid:
            def __call__(self, idtype, pid, pointer, options):
                value = pointer._obj
                calls.append((idtype, pid, options))
                self_test.assertEqual(ctypes.sizeof(value), 104)
                self_test.assertEqual((type(value).si_pid.offset, type(value).si_status.offset,
                                       type(value).reserved.offset), (12, 20, 48))
                if len(calls) == 1: return 0  # WNOHANG, child still alive.
                if len(calls) == 3:
                    ctypes.set_errno(errno.ECHILD); return -1
                value.si_pid, value.si_uid, value.si_signo = pid, 501, signal.SIGCHLD
                value.si_code, value.si_status = os.CLD_EXITED, 7
                return 0
        self_test = self
        waitid = FakeWaitid()
        with (mock.patch('prepare.sys.platform', 'darwin'),
              mock.patch('prepare.os.uname', return_value=SimpleNamespace(machine='arm64')),
              mock.patch('ctypes.CDLL', return_value=SimpleNamespace(waitid=waitid)) as load):
            reader = prepare._waitid_reader()
            self.assertIsNone(reader(123))
            terminal = reader(123)
            self.assertEqual((terminal.si_pid, terminal.si_status), (123, 7))
            with self.assertRaises(ChildProcessError): reader(123)
        self.assertEqual(calls, [(1, 123, 37)] * 3)
        load.assert_called_once_with('/usr/lib/libSystem.B.dylib', use_errno=True)

    def test_linux_reader_uses_only_stdlib_waitid_and_original_child(self):
        import prepare
        with (mock.patch('prepare.sys.platform', 'linux'),
              mock.patch('prepare.os.waitid', create=True, return_value=None) as waitid,
              mock.patch('ctypes.CDLL', side_effect=AssertionError('no Darwin binding'))):
            self.assertIsNone(prepare._waitid_reader()(123))
        waitid.assert_called_once_with(os.P_PID, 123, os.WEXITED | os.WNOWAIT | os.WNOHANG)

    def test_unknown_platform_or_abi_fails_before_binding_and_selfcheck_never_loads_it(self):
        import prepare
        for platform in ('darwin', 'unknown'):
            with (mock.patch('prepare.sys.platform', platform),
                  mock.patch('prepare.os.uname', return_value=SimpleNamespace(machine='unsupported')),
                  mock.patch('ctypes.CDLL', side_effect=AssertionError('no uncertain ABI binding')) as load):
                with self.assertRaises(ValueError): prepare._waitid_reader()
                self.assertEqual(load.call_count, 0)
        with (mock.patch('prepare._waitid_reader', side_effect=AssertionError('no wait observation')),
              mock.patch('prepare._darwin_group_reader', side_effect=AssertionError('no group observation')),
              mock.patch('ctypes.CDLL', side_effect=AssertionError('no library load'))):
            self.assertFalse(prepare._self_check()['execution_enabled'])


class DarwinGroupReaderTests(unittest.TestCase):
    def test_fixed_sdk_abi_resets_errno_and_preserves_all_8192_raw_bytes_for_every_return(self):
        import ctypes
        import prepare
        calls = []
        outcomes = iter(((4, 0), (0, errno.EPERM), (-1, errno.EIO), (8192, 0), (8196, 0)))
        class ListPids:
            def __call__(self, kind, pid, buffer, size):
                self_test.assertEqual((kind, pid, size), (2, 43210, 8192))
                self_test.assertEqual(buffer.raw, bytes(8192), 'every buffer is fresh and zero initialized')
                self_test.assertEqual(ctypes.get_errno(), 0, 'native call must not inherit sticky errno')
                buffer.raw = (43210).to_bytes(4, sys.byteorder, signed=True) + bytes(8188)
                count, code = next(outcomes)
                ctypes.set_errno(code); calls.append((kind, pid, size))
                return count
        self_test = self
        native = ListPids()
        with (mock.patch('prepare.sys.platform', 'darwin'),
              mock.patch('prepare.os.uname', return_value=SimpleNamespace(machine='arm64')),
              mock.patch('ctypes.CDLL', return_value=SimpleNamespace(proc_listpids=native)) as load):
            read = prepare._darwin_group_reader()
            self.assertEqual(native.argtypes, (ctypes.c_uint, ctypes.c_uint, ctypes.c_void_p, ctypes.c_int))
            self.assertIs(native.restype, ctypes.c_int)
            for count, code in ((4, 0), (0, errno.EPERM), (-1, errno.EIO), (8192, 0), (8196, 0)):
                ctypes.set_errno(errno.EBUSY)
                facts = read(43210)
                self.assertEqual((facts['byte_count'], facts['errno']), (count, code))
                self.assertEqual(facts['raw'], (43210).to_bytes(4, sys.byteorder, signed=True) + bytes(8188))
                self.assertEqual(len(facts['raw']), 8192)
            for pid in (0, -1, True, 1 << 31):
                with self.assertRaises(ValueError): read(pid)
        self.assertEqual(len(calls), 5)
        load.assert_called_once_with('/usr/lib/libproc.dylib', use_errno=True)
        ctypes.set_errno(0)

    def test_unknown_platform_or_integer_abi_rejects_before_library_load(self):
        import ctypes
        import prepare
        for platform, machine, order in (('linux', 'arm64', 'little'), ('unknown', 'arm64', 'little'),
                ('darwin', 'unsupported', 'little'), ('darwin', 'arm64', 'big')):
            with self.subTest(platform=platform, machine=machine, order=order):
                with (mock.patch('prepare.sys.platform', platform), mock.patch('prepare.sys.byteorder', order),
                      mock.patch('prepare.os.uname', return_value=SimpleNamespace(machine=machine)),
                      mock.patch('ctypes.CDLL', side_effect=AssertionError('no unsupported native ABI')) as load):
                    with self.assertRaises(ValueError): prepare._darwin_group_reader()
                    load.assert_not_called()
        size = ctypes.sizeof
        with (mock.patch('prepare.sys.platform', 'darwin'),
              mock.patch('prepare.os.uname', return_value=SimpleNamespace(machine='x86_64')),
              mock.patch('ctypes.sizeof', side_effect=lambda kind: 8 if kind is ctypes.c_uint else size(kind)),
              mock.patch('ctypes.CDLL', side_effect=AssertionError('no incompatible integer ABI')) as load):
            with self.assertRaises(ValueError): prepare._darwin_group_reader()
            load.assert_not_called()

    def test_missing_library_or_symbol_is_not_a_native_proof(self):
        import prepare
        for native in (OSError(errno.ENOENT, 'fixed libproc unavailable'), SimpleNamespace()):
            with self.subTest(native=native):
                with (mock.patch('prepare.sys.platform', 'darwin'),
                      mock.patch('prepare.os.uname', return_value=SimpleNamespace(machine='arm64')),
                      mock.patch('ctypes.CDLL', side_effect=native if isinstance(native, OSError) else None,
                                 return_value=native)):
                    with self.assertRaises((OSError, AttributeError)): prepare._darwin_group_reader()

    def test_unproven_live_leader_never_queries_group_after_eperm(self):
        import prepare
        process = SyntheticProcess(False)
        with (mock.patch('prepare.sys.platform', 'darwin'),
              mock.patch('prepare.os.killpg', side_effect=PermissionError(errno.EPERM, 'original denied')) as kill,
              mock.patch('prepare._darwin_group_reader', side_effect=AssertionError('no live child group query')) as group):
            cleanup = prepare._cleanup_process(process, SimpleNamespace(close=lambda: None),
                lambda pid: None, None, 120, lambda: 10)
            group.assert_not_called(); kill.assert_called_once_with(process.pid, signal.SIGKILL)
            self.assertEqual(cleanup[1]['errno'], errno.EPERM)
            self.assertEqual(cleanup[1]['status'], 'failed')


# Complete synthetic unit catalogues; saved real prefixes are replayed separately.
_CATALOG_COMMANDS = tuple(('/usr/bin/systemctl', verb, '--all', '--type=service',
                  '--type=socket', '--no-legend', '--no-pager', '--plain')
                 for verb in ('list-unit-files', 'list-units'))


class _CatalogRecord(prepare._Record):
    """Use production command validation/raw wire encoding and finite summaries."""
    def __init__(self):
        outcome = {'observations': []}
        step = {'name': 'preflight', 'stdout': '', 'stderr': '', 'known_effects': []}
        super().__init__(outcome, step, lambda value: None)
        self.observations, self.frames, self.deadline = outcome['observations'], [], 130

    def command(self, value):
        try:
            super().command(value)
        finally:
            # The reused syscall fixture expects its host-shaped control frame;
            # production assertions use only the actual _Record observations.
            self.frames.append({'kind': 'host-command-result', 'value': prepare._wire(value)})

    def results(self):
        return [frame['value'] for frame in self.observations if frame['kind'] == 'command-result']


def _catalog_run(argv, record, stdout, scenario=None, **boundary):
    """Enter the existing syscall isolation seam but retain its actual exception."""
    errors, values = [], []
    def operation(recorder):
        try:
            value = prepare._bounded_run(argv, record.deadline, recorder, lambda: 10)
            values.append(value)
            return value
        except BaseException as error:
            errors.append(error)
            raise
    ProcessBoundaryTests().normal_boundary(False, recorder=record, operation=operation,
        scenario={'stdout': stdout, **(scenario or {})}, **boundary)
    if errors:
        raise errors[0]
    return values[0]


class UnitCatalogCommandBudgetTests(unittest.TestCase):
    def test_exact_commands_return_complete_raw_and_bounded_summary(self):
        for argv in _CATALOG_COMMANDS:
            for size in (16385, 262144):
                with self.subTest(argv=argv, size=size):
                    raw, record = b'x' * size, _CatalogRecord()
                    self.assertEqual(_catalog_run(argv, record, raw), raw)
                    result = record.results()[0]
                    self.assertEqual(result['stdout'], prepare._wire(raw))
                    self.assertEqual(base64.b64decode(result['stdout']['base64']), raw)
                    self.assertEqual(result['exit_code'], 0)
                    self.assertFalse(result['timed_out'] or result['truncated'])
                    self.assertEqual(record.step['stdout'], 'x' * 16384)
                    self.assertEqual(record.step['stderr'], 'normal stderr')
                    self.assertTrue(all(item['status'] != 'failed' for item in result['cleanup']))

    def test_exact_commands_reject_stdout_over_256k(self):
        for argv in _CATALOG_COMMANDS:
            record = _CatalogRecord()
            with self.subTest(argv=argv), self.assertRaisesRegex(
                    prepare._Failure, 'fixed command output exceeded bound'):
                _catalog_run(argv, record, b'x' * 262145)
            result = record.results()[0]
            self.assertEqual(result['stdout'], prepare._wire(b'x' * 262144))
            self.assertTrue(result['truncated'])
            self.assertEqual(len(record.step['stdout']), 16384)

    def test_stderr_keeps_16k_budget_for_every_command_class(self):
        for argv in (*_CATALOG_COMMANDS, ('/fixed/synthetic',)):
            record = _CatalogRecord()
            with self.subTest(argv=argv), self.assertRaisesRegex(
                    prepare._Failure, 'fixed command output exceeded bound'):
                _catalog_run(argv, record, b'ok', {'stderr': b'e' * 16385})
            result = record.results()[0]
            self.assertEqual(result['stderr'], prepare._wire(b'e' * 16384))
            self.assertEqual(record.step['stderr'], 'e' * 16384)
            self.assertTrue(result['truncated'])

    def test_other_and_near_match_stdout_keeps_16k_budget(self):
        for exact in _CATALOG_COMMANDS:
            variants = (('/fixed/synthetic',), ('systemctl', *exact[1:]),
                ('/usr/bin/Systemctl', *exact[1:]),
                (*exact[:2], '--type=service', '--all', *exact[4:]),
                (*exact, 'docker*'), (*exact[:-1], '--plain=true'))
            for argv in variants:
                record = _CatalogRecord()
                with self.subTest(argv=argv), self.assertRaisesRegex(
                        prepare._Failure, 'fixed command output exceeded bound'):
                    _catalog_run(argv, record, b'x' * 16385)
                self.assertEqual(record.results()[0]['stdout']['bytes'], 16384)

    def test_raw_frame_and_cumulative_audit_limits_still_reject(self):
        record = _CatalogRecord()
        with self.assertRaisesRegex(prepare._Failure, 'raw observation exceeded finite frame bound'):
            record.raw('synthetic-oversized-frame', b'x' * (1 << 20))
        self.assertTrue(record.observations[-1]['truncated'])
        record = _CatalogRecord()
        # Actual encoded observations accumulate; no patched limit or counter.
        while record.outcome.get('observation_bytes', 0) + 400000 < 64 << 20:
            record.raw('synthetic-audit-fill', b'x' * 262144)
        with self.assertRaisesRegex(prepare._Failure, 'raw observation exceeded finite frame bound'):
            for _ in range(3):
                record.raw('synthetic-audit-fill', b'x' * 262144)
        self.assertTrue(record.observations[-1]['truncated'])

    def test_maximum_command_survives_guest_and_host_raw_frame_wrapping(self):
        raw, stderr, emitted = b'x' * 262144, b'e' * 16384, []
        operations = guest._GuestOperations.__new__(guest._GuestOperations)
        operations.deadline = 130
        record = _CatalogRecord()
        record.emit = operations.emit
        with mock.patch('guest._emit_bytes', side_effect=lambda data, deadline: emitted.append(data)):
            record.command({'argv': list(_CATALOG_COMMANDS[0]), 'stdout': raw, 'stderr': stderr,
                'exit_code': 0, 'timed_out': False, 'truncated': False, 'error': None,
                'cleanup': [], 'leader_terminal': None})
        self.assertEqual(len(emitted), 1)
        self.assertLessEqual(len(emitted[0]), 1 << 20)
        host = prepare._Host.__new__(prepare._Host)
        host.frames, host.bytes, host.stage_owned = [], 0, False
        # Exact receiver preservation operation, without creating a Host adapter.
        host.emit({'kind': 'guest-frame-raw', 'raw': prepare._wire(emitted[0][:-1])})
        self.assertLessEqual(host.bytes, 1 << 20)
        value = json.loads(base64.b64decode(host.frames[0]['raw']['base64']))
        self.assertEqual(value['kind'], 'observation')
        self.assertEqual(value['value'], record.observations[0])
        self.assertEqual(value['value']['value']['stdout'], prepare._wire(raw))
        self.assertEqual(value['value']['value']['stderr'], prepare._wire(stderr))
        self.assertEqual(len(record.step['stdout']), 16384)
        self.assertEqual(len(record.step['stderr']), 16384)


class UnitCatalogTests(unittest.TestCase):
    def catalogue(self, count=512, missing=None, extra=None, malformed=False,
                  scenario=None, boundary=None, properties=None):
        names = list(guest._MASKS)
        names += ['synthetic-%03d-%s.service' % (index, 'n' * 48)
                  for index in range(count - len(names))]
        if missing:
            names.remove(missing)
        if extra:
            names[-1] = extra
        # Both distinct complete inputs exceed 16KiB; neither is filtered.
        split = len(names) // 2
        lists = (''.join(name + ' disabled disabled\n' for name in names[:split]).encode(),
                 ''.join(name + ' loaded inactive dead complete synthetic unit\n'
                         for name in reversed(names[split:])).encode())
        if malformed:
            lists = (lists[0] + b'bad-row-without-a-unit\n', lists[1])
        record, calls, shown = _CatalogRecord(), [], []
        def runner(argv, deadline, recorder):
            calls.append(tuple(argv))
            if tuple(argv) in _CATALOG_COMMANDS:
                raw = lists[_CATALOG_COMMANDS.index(tuple(argv))]
                return _catalog_run(argv, recorder, raw, scenario, **(boundary or {}))
            self.assertEqual(argv[:3], ['/usr/bin/systemctl', 'show', '--no-pager'])
            self.assertEqual(argv[3], '--property=Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen')
            name = argv[-1]
            shown.append(name)
            values = dict(Id=name, ActiveState='inactive', MainPID='0',
                UnitFileState='masked' if name in guest._MASKS else 'disabled',
                ExecStart='/usr/bin/true', Listen='')
            values.update((properties or {}).get(name, {}))
            raw = ''.join(key + '=' + value + '\n' for key, value in values.items()).encode()
            return _catalog_run(argv, recorder, raw)
        self.record, self.calls, self.shown = record, calls, shown
        self.names, self.lists = names, lists
        # _unit_catalog -> actual _IO.command -> isolation adapter -> actual runner.
        with mock.patch('guest._bounded_run', side_effect=runner):
            result = guest._unit_catalog(guest._IO(130, record))
        return result

    def test_complete_512_names_both_lists_and_every_property_are_observed(self):
        masks, others = self.catalogue()
        self.assertEqual(masks, preflight()['masks'])
        self.assertEqual(others, [])
        self.assertEqual(self.calls[:2], list(_CATALOG_COMMANDS))
        self.assertEqual(self.shown, sorted(self.names))
        self.assertEqual(len(self.shown), 512)
        items = [frame for frame in self.record.observations if frame['kind'] == 'unit-catalog-item']
        self.assertEqual(len(items), 512)
        for raw, result in zip(self.lists, self.record.results()):
            self.assertGreater(len(raw), 16384)
            self.assertEqual(result['stdout'], prepare._wire(raw))
        self.assertEqual(len(self.record.step['stdout'].encode()), 16384)

    def test_513_names_missing_mask_and_malformed_row_reject_before_show(self):
        for options, message in (({'count': 513}, 'unit catalog coverage/bound'),
                ({'missing': 'docker.socket'}, 'unit catalog coverage/bound'),
                ({'malformed': True}, 'complete unit catalog row encoding')):
            with self.subTest(options=options), self.assertRaisesRegex(ValueError, message):
                self.catalogue(**options)
            self.assertEqual(self.shown, [])

    def test_renamed_daemon_execstart_and_foreign_daemon_remain_rejected(self):
        name = 'apparently-unrelated.service'
        for executable in ('/usr/bin/dockerd', '/usr/bin/containerd', '/usr/bin/containerd-shim-runc-v2'):
            with self.subTest(executable=executable):
                masks, others = self.catalogue(extra=name, properties={name: {
                    'ExecStart': '{ path=' + executable + ' ; argv[]=' + executable + ' ; }',
                    'ActiveState': 'active', 'MainPID': '999'}})
                self.assertEqual(others, [{'unit': name, 'active_state': 'active', 'main_pid': 999}])
                facts = preflight()
                facts.update(masks=masks, other_units=others)
                with self.assertRaisesRegex(ValueError, 'active/unknown daemon unit'):
                    guest.validate_preflight_v2(facts)

    def test_new_unit_and_unmasked_defaults_remain_rejected(self):
        for name in guest.UNIT_NAMES:
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'exclusive new unit'):
                self.catalogue(extra=name)
        masks, others = self.catalogue(properties={'docker.service': {'UnitFileState': 'enabled'}})
        facts = preflight()
        facts.update(masks=masks, other_units=others)
        with self.assertRaisesRegex(ValueError, 'default mask/active state differs'):
            guest.validate_preflight_v2(facts)

    def test_nonzero_timeout_truncated_and_uncertain_cleanup_never_reach_parser(self):
        cases = (({'scenario': {'exit_code': 7}}, 'nonzero'),
                 ({'scenario': {'terminal_timeout_at': 1}}, 'deadline'),
                 ({'scenario': {'overflow': True}}, 'output exceeded bound'),
                 ({'boundary': {'cleanup_error': PermissionError(errno.EPERM, 'synthetic refusal')}},
                  'cleanup failed/unknown'))
        for options, message in cases:
            with self.subTest(options=options), self.assertRaisesRegex(prepare._Failure, message):
                self.catalogue(**options)
            self.assertEqual(self.shown, [])
            self.assertEqual(len(self.calls), 1)
            self.assertFalse(any(frame['kind'] == 'unit-catalog-item' for frame in self.record.observations))



# Captured 2026-10-24 catalogue bytes; ordinary properties after the first four
# were not captured and are explicitly synthetic in the offline runner below.
_CAPTURED_CATALOGS = (
    (
        b'apparmor.service                                   enabled         enabled\n'
        b'apport-autoreport.service                          static          -\n'
        b'apport-coredump-hook@.service                      static          -\n'
        b'apport-forward@.service                            static          -\n'
        b'apport.service                                     enabled         enabled\n'
        b'apt-daily-upgrade.service                          static          -\n'
        b'apt-daily.service                                  static          -\n'
        b'apt-news.service                                   static          -\n'
        b'autovt@.service                                    alias           -\n'
        b'blk-availability.service                           enabled         enabled\n'
        b'bolt.service                                       static          -\n'
        b'cloud-config.service                               enabled         enabled\n'
        b'cloud-final.service                                enabled         enabled\n'
        b'cloud-init-hotplugd.service                        static          -\n'
        b'cloud-init-local.service                           enabled         enabled\n'
        b'cloud-init.service                                 enabled         enabled\n'
        b'console-getty.service                              disabled        disabled\n'
        b'console-setup.service                              enabled         enabled\n'
        b'container-getty@.service                           static          -\n'
        b'containerd.service                                 masked          enabled\n'
        b'cron.service                                       enabled         enabled\n'
        b'cryptdisks-early.service                           masked          enabled\n'
        b'cryptdisks.service                                 masked          enabled\n'
        b'dbus-org.freedesktop.hostname1.service             alias           -\n'
        b'dbus-org.freedesktop.locale1.service               alias           -\n'
        b'dbus-org.freedesktop.login1.service                alias           -\n'
        b'dbus-org.freedesktop.ModemManager1.service         alias           -\n'
        b'dbus-org.freedesktop.resolve1.service              alias           -\n'
        b'dbus-org.freedesktop.timedate1.service             alias           -\n'
        b'dbus-org.freedesktop.timesync1.service             alias           -\n'
        b'dbus.service                                       static          -\n'
        b'debug-shell.service                                disabled        disabled\n'
        b'dm-event.service                                   static          -\n'
        b'dmesg.service                                      enabled         enabled\n'
        b'docker.service                                     masked          enabled\n'
        b'dpkg-db-backup.service                             static          -\n'
        b'e2scrub@.service                                   static          -\n'
        b'e2scrub_all.service                                static          -\n'
        b'e2scrub_fail@.service                              static          -\n'
        b'e2scrub_reap.service                               enabled         enabled\n'
        b'emergency.service                                  static          -\n'
        b'esm-cache.service                                  static          -\n'
        b'finalrd.service                                    enabled         enabled\n'
        b'friendly-recovery.service                          static          -\n'
        b'fstrim.service                                     static          -\n'
        b'fwupd-refresh.service                              static          -\n'
        b'fwupd.service                                      static          -\n'
        b'getty-static.service                               static          -\n'
        b'getty@.service                                     enabled         enabled\n'
        b'grub-common.service                                enabled         enabled\n'
        b'grub-initrd-fallback.service                       enabled         enabled\n'
        b'hwclock.service                                    masked          enabled\n'
        b'initrd-cleanup.service                             static          -\n'
        b'initrd-parse-etc.service                           static          -\n'
        b'initrd-switch-root.service                         static          -\n'
        b'initrd-udevadm-cleanup-db.service                  static          -\n'
        b'iscsi.service                                      alias           -\n'
        b'iscsid.service                                     disabled        enabled\n'
        b'keyboard-setup.service                             enabled         enabled\n'
        b'kmod-static-nodes.service                          static          -\n'
        b'kmod.service                                       alias           -\n'
        b'ldconfig.service                                   static          -\n'
        b'lima-guestagent.service                            enabled         enabled\n'
        b'logrotate.service                                  static          -\n'
        b'lvm2-lvmpolld.service                              static          -\n'
        b'lvm2-monitor.service                               enabled         enabled\n'
        b'lxd-agent.service                                  static          -\n'
        b'lxd-installer@.service                             static          -\n'
        b'man-db.service                                     static          -\n'
        b'mdadm-grow-continue@.service                       static          -\n'
        b'mdadm-last-resort@.service                         static          -\n'
        b'mdcheck_continue.service                           static          -\n'
        b'mdcheck_start.service                              static          -\n'
        b'mdmon@.service                                     static          -\n'
        b'mdmonitor-oneshot.service                          static          -\n'
        b'mdmonitor.service                                  static          -\n'
        b'ModemManager.service                               enabled         enabled\n'
        b'modprobe@.service                                  static          -\n'
        b'motd-news.service                                  static          -\n'
        b'multipath-tools-boot.service                       masked          enabled\n'
        b'multipath-tools.service                            alias           -\n'
        b'multipathd.service                                 enabled         enabled\n'
        b'netplan-ovs-cleanup.service                        enabled-runtime enabled\n'
        b'networkd-dispatcher.service                        enabled         enabled\n'
        b'nftables.service                                   disabled        enabled\n'
        b'open-iscsi.service                                 enabled         enabled\n'
        b'open-vm-tools.service                              enabled         enabled\n'
        b'packagekit-offline-update.service                  static          -\n'
        b'packagekit.service                                 static          -\n'
        b'pam_namespace.service                              static          -\n'
        b'plymouth-halt.service                              static          -\n'
        b'plymouth-kexec.service                             static          -\n'
        b'plymouth-log.service                               alias           -\n'
        b'plymouth-poweroff.service                          static          -\n'
        b'plymouth-quit-wait.service                         static          -\n'
        b'plymouth-quit.service                              static          -\n'
        b'plymouth-read-write.service                        static          -\n'
        b'plymouth-reboot.service                            static          -\n'
        b'plymouth-start.service                             static          -\n'
        b'plymouth-switch-root-initramfs.service             static          -\n'
        b'plymouth-switch-root.service                       static          -\n'
        b'plymouth.service                                   alias           -\n'
        b'polkit.service                                     static          -\n'
        b'pollinate.service                                  enabled         enabled\n'
        b'procps.service                                     alias           -\n'
        b'quotaon.service                                    static          -\n'
        b'rc-local.service                                   static          -\n'
        b'rescue.service                                     static          -\n'
        b'rsync.service                                      disabled        enabled\n'
        b'rsyslog.service                                    enabled         enabled\n'
        b'screen-cleanup.service                             masked          enabled\n'
        b'secureboot-db.service                              enabled         enabled\n'
        b'serial-getty@.service                              indirect        enabled\n'
        b'setvtrgb.service                                   enabled         enabled\n'
        b'snapd.apparmor.service                             enabled         enabled\n'
        b'snapd.autoimport.service                           enabled         enabled\n'
        b'snapd.core-fixup.service                           enabled         enabled\n'
        b'snapd.failure.service                              static          -\n'
        b'snapd.recovery-chooser-trigger.service             enabled         enabled\n'
        b'snapd.seeded.service                               enabled         enabled\n'
        b'snapd.service                                      enabled         enabled\n'
        b'snapd.snap-repair.service                          static          -\n'
        b'snapd.system-shutdown.service                      enabled         enabled\n'
        b'ssh.service                                        disabled        enabled\n'
        b'sudo.service                                       masked          enabled\n'
        b'switchback-created-layer-observer.service          masked          enabled\n'
        b'switchback-deployment-diagnostic1021-b3f.service   transient       -\n'
        b'switchback-deployment-diagnostic1021-nscap.service transient       -\n'
        b'switchback-deployment-diagnostic1021-pid1.service  transient       -\n'
        b'switchback-deployment-probe1020-b3c.service        transient       -\n'
        b'switchback-engine-quota1016-exit-client.service    static          -\n'
        b'switchback-engine1013-containerd.service           masked          enabled\n'
        b'switchback-engine1013-docker.service               masked          enabled\n'
        b'switchback-engine1013-watchdog.service             static          -\n'
        b'switchback-exit1016-proof-client.service           static          -\n'
        b'switchback-native1016-ipc-client.service           static          -\n'
        b'switchback-prestart1014-containerd.service         masked          enabled\n'
        b'switchback-prestart1014-docker.service             masked          enabled\n'
        b'switchback-prestart1014-watchdog.service           static          -\n'
        b'switchback-recovery-metadata1017-read.service      static          -\n'
        b'switchback-volume-metadata1016-read-r1.service     static          -\n'
        b'switchback-volume-metadata1016-read-r2.service     static          -\n'
        b'switchback-volume-metadata1016-read.service        static          -\n'
        b'syslog.service                                     alias           -\n'
        b'sysstat-collect.service                            static          -\n'
        b'sysstat-summary.service                            static          -\n'
        b'sysstat.service                                    enabled         enabled\n'
        b'system-update-cleanup.service                      static          -\n'
        b'systemd-ask-password-console.service               static          -\n'
        b'systemd-ask-password-plymouth.service              static          -\n'
        b'systemd-ask-password-wall.service                  static          -\n'
        b'systemd-backlight@.service                         static          -\n'
        b'systemd-battery-check.service                      static          -\n'
        b'systemd-binfmt.service                             static          -\n'
        b'systemd-boot-check-no-failures.service             disabled        disabled\n'
        b'systemd-bsod.service                               static          -\n'
        b'systemd-confext.service                            disabled        enabled\n'
        b'systemd-exit.service                               static          -\n'
        b'systemd-firstboot.service                          static          -\n'
        b'systemd-fsck-root.service                          enabled-runtime enabled\n'
        b'systemd-fsck@.service                              static          -\n'
        b'systemd-fsckd.service                              static          -\n'
        b'systemd-growfs-root.service                        static          -\n'
        b'systemd-growfs@.service                            static          -\n'
        b'systemd-halt.service                               static          -\n'
        b'systemd-hibernate-resume.service                   static          -\n'
        b'systemd-hibernate.service                          static          -\n'
        b'systemd-hostnamed.service                          static          -\n'
        b'systemd-hwdb-update.service                        static          -\n'
        b'systemd-hybrid-sleep.service                       static          -\n'
        b'systemd-initctl.service                            static          -\n'
        b'systemd-journal-catalog-update.service             static          -\n'
        b'systemd-journal-flush.service                      static          -\n'
        b'systemd-journald.service                           static          -\n'
        b'systemd-journald@.service                          static          -\n'
        b'systemd-kexec.service                              static          -\n'
        b'systemd-localed.service                            static          -\n'
        b'systemd-logind.service                             static          -\n'
        b'systemd-machine-id-commit.service                  static          -\n'
        b'systemd-modules-load.service                       static          -\n'
        b'systemd-network-generator.service                  disabled        enabled\n'
        b'systemd-networkd-wait-online.service               enabled         enabled\n'
        b'systemd-networkd-wait-online@.service              disabled        enabled\n'
        b'systemd-networkd.service                           enabled         enabled\n'
        b'systemd-pcrextend@.service                         static          -\n'
        b'systemd-pcrfs-root.service                         static          -\n'
        b'systemd-pcrfs@.service                             static          -\n'
        b'systemd-pcrlock-file-system.service                disabled        enabled\n'
        b'systemd-pcrlock-firmware-code.service              disabled        enabled\n'
        b'systemd-pcrlock-firmware-config.service            disabled        enabled\n'
        b'systemd-pcrlock-machine-id.service                 disabled        enabled\n'
        b'systemd-pcrlock-make-policy.service                disabled        enabled\n'
        b'systemd-pcrlock-secureboot-authority.service       disabled        enabled\n'
        b'systemd-pcrlock-secureboot-policy.service          disabled        enabled\n'
        b'systemd-pcrmachine.service                         static          -\n'
        b'systemd-pcrphase-initrd.service                    static          -\n'
        b'systemd-pcrphase-sysinit.service                   static          -\n'
        b'systemd-pcrphase.service                           static          -\n'
        b'systemd-poweroff.service                           static          -\n'
        b'systemd-pstore.service                             enabled         enabled\n'
        b'systemd-quotacheck.service                         static          -\n'
        b'systemd-random-seed.service                        static          -\n'
        b'systemd-reboot.service                             static          -\n'
        b'systemd-remount-fs.service                         enabled-runtime enabled\n'
        b'systemd-repart.service                             static          -\n'
        b'systemd-resolved.service                           enabled         enabled\n'
        b'systemd-rfkill.service                             static          -\n'
        b'systemd-soft-reboot.service                        static          -\n'
        b'systemd-storagetm.service                          static          -\n'
        b'systemd-suspend-then-hibernate.service             static          -\n'
        b'systemd-suspend.service                            static          -\n'
        b'systemd-sysctl.service                             static          -\n'
        b'systemd-sysext.service                             disabled        enabled\n'
        b'systemd-sysext@.service                            static          -\n'
        b'systemd-sysupdate-reboot.service                   indirect        enabled\n'
        b'systemd-sysupdate.service                          indirect        enabled\n'
        b'systemd-sysusers.service                           static          -\n'
        b'systemd-time-wait-sync.service                     disabled        disabled\n'
        b'systemd-timedated.service                          static          -\n'
        b'systemd-timesyncd.service                          enabled         enabled\n'
        b'systemd-tmpfiles-clean.service                     static          -\n'
        b'systemd-tmpfiles-setup-dev-early.service           static          -\n'
        b'systemd-tmpfiles-setup-dev.service                 static          -\n'
        b'systemd-tmpfiles-setup.service                     static          -\n'
        b'systemd-tpm2-setup-early.service                   static          -\n'
        b'systemd-tpm2-setup.service                         static          -\n'
        b'systemd-udev-settle.service                        static          -\n'
        b'systemd-udev-trigger.service                       static          -\n'
        b'systemd-udevd.service                              static          -\n'
        b'systemd-update-done.service                        static          -\n'
        b'systemd-update-utmp-runlevel.service               static          -\n'
        b'systemd-update-utmp.service                        static          -\n'
        b'systemd-user-sessions.service                      static          -\n'
        b'systemd-volatile-root.service                      static          -\n'
        b'ua-reboot-cmds.service                             enabled         enabled\n'
        b'ua-timer.service                                   static          -\n'
        b'ubuntu-advantage.service                           enabled         enabled\n'
        b'udev.service                                       alias           -\n'
        b'udisks2.service                                    enabled         enabled\n'
        b'ufw.service                                        enabled         enabled\n'
        b'unattended-upgrades.service                        enabled         enabled\n'
        b'update-notifier-download.service                   static          -\n'
        b'update-notifier-motd.service                       static          -\n'
        b'usb_modeswitch@.service                            static          -\n'
        b'user-runtime-dir@.service                          static          -\n'
        b'user@.service                                      static          -\n'
        b'uuidd.service                                      indirect        enabled\n'
        b'vgauth.service                                     enabled         enabled\n'
        b'vmtoolsd.service                                   alias           -\n'
        b'x11-common.service                                 masked          enabled\n'
        b'xfs_scrub@.service                                 static          -\n'
        b'xfs_scrub_all.service                              static          -\n'
        b'xfs_scrub_fail@.service                            static          -\n'
        b'apport-forward.socket                              enabled         enabled\n'
        b'cloud-init-hotplugd.socket                         enabled         enabled\n'
        b'dbus.socket                                        static          -\n'
        b'dm-event.socket                                    enabled         enabled\n'
        b'docker.socket                                      masked          enabled\n'
        b'iscsid.socket                                      enabled         enabled\n'
        b'lvm2-lvmpolld.socket                               enabled         enabled\n'
        b'lxd-installer.socket                               enabled         enabled\n'
        b'multipathd.socket                                  enabled         enabled\n'
        b'snapd.socket                                       enabled         enabled\n'
        b'ssh.socket                                         enabled         enabled\n'
        b'syslog.socket                                      static          -\n'
        b'systemd-fsckd.socket                               static          -\n'
        b'systemd-initctl.socket                             static          -\n'
        b'systemd-journald-audit.socket                      disabled        enabled\n'
        b'systemd-journald-dev-log.socket                    static          -\n'
        b'systemd-journald-varlink@.socket                   static          -\n'
        b'systemd-journald.socket                            static          -\n'
        b'systemd-journald@.socket                           static          -\n'
        b'systemd-networkd.socket                            enabled         enabled\n'
        b'systemd-pcrextend.socket                           disabled        enabled\n'
        b'systemd-rfkill.socket                              static          -\n'
        b'systemd-sysext.socket                              disabled        enabled\n'
        b'systemd-udevd-control.socket                       static          -\n'
        b'systemd-udevd-kernel.socket                        static          -\n'
        b'uuidd.socket                                       enabled         enabled\n'
    ),
    (
        b'apparmor.service                                   loaded    active   exited    Load AppArmor profiles\n'
        b'apport-autoreport.service                          loaded    inactive dead      Process error reports when automatic reporting is enabled\n'
        b'apport.service                                     loaded    active   exited    automatic crash report generation\n'
        b'apt-daily-upgrade.service                          loaded    inactive dead      Daily apt upgrade and clean activities\n'
        b'apt-daily.service                                  loaded    inactive dead      Daily apt download activities\n'
        b'auditd.service                                     not-found inactive dead      auditd.service\n'
        b'blk-availability.service                           loaded    active   exited    Availability of block devices\n'
        b'cloud-config.service                               loaded    active   exited    Cloud-init: Config Stage\n'
        b'cloud-final.service                                loaded    failed   failed    Cloud-init: Final Stage\n'
        b'cloud-init-hotplugd.service                        loaded    inactive dead      Cloud-init: Hotplug Hook\n'
        b'cloud-init-local.service                           loaded    active   exited    Cloud-init: Local Stage (pre-network)\n'
        b'cloud-init.service                                 loaded    active   exited    Cloud-init: Network Stage\n'
        b'connman.service                                    not-found inactive dead      connman.service\n'
        b'console-screen.service                             not-found inactive dead      console-screen.service\n'
        b'console-setup.service                              loaded    active   exited    Set console font and keymap\n'
        b'cron.service                                       loaded    active   running   Regular background program processing daemon\n'
        b'dbus.service                                       loaded    active   running   D-Bus System Message Bus\n'
        b'display-manager.service                            not-found inactive dead      display-manager.service\n'
        b'dm-event.service                                   loaded    inactive dead      Device-mapper event daemon\n'
        b'dmesg.service                                      loaded    inactive dead      Save initial kernel messages after boot\n'
        b'dpkg-db-backup.service                             loaded    inactive dead      Daily dpkg database backup service\n'
        b'e2scrub_all.service                                loaded    inactive dead      Online ext4 Metadata Check for All Filesystems\n'
        b'e2scrub_reap.service                               loaded    inactive dead      Remove Stale Online ext4 Metadata Check Snapshots\n'
        b'emergency.service                                  loaded    inactive dead      Emergency Shell\n'
        b'fcoe.service                                       not-found inactive dead      fcoe.service\n'
        b'finalrd.service                                    loaded    active   exited    Create final runtime dir for shutdown pivot root\n'
        b'fstrim.service                                     loaded    inactive dead      Discard unused blocks on filesystems from /etc/fstab\n'
        b'fwupd-refresh.service                              loaded    inactive dead      Refresh fwupd metadata and update motd\n'
        b'getty-static.service                               loaded    inactive dead      getty on tty2-tty6 if dbus and logind are not available\n'
        b'getty@tty1.service                                 loaded    active   running   Getty on tty1\n'
        b'grub-common.service                                loaded    inactive dead      Record successful boot for GRUB\n'
        b'grub-initrd-fallback.service                       loaded    inactive dead      GRUB failed boot detection\n'
        b'hv_kvp_daemon.service                              not-found inactive dead      hv_kvp_daemon.service\n'
        b'initrd-cleanup.service                             loaded    inactive dead      Cleaning Up and Shutting Down Daemons\n'
        b'initrd-parse-etc.service                           loaded    inactive dead      Mountpoints Configured in the Real Root\n'
        b'initrd-switch-root.service                         loaded    inactive dead      Switch Root\n'
        b'initrd-udevadm-cleanup-db.service                  loaded    inactive dead      Cleanup udev Database\n'
        b'iscsi-shutdown.service                             not-found inactive dead      iscsi-shutdown.service\n'
        b'iscsid.service                                     loaded    inactive dead      iSCSI initiator daemon (iscsid)\n'
        b'kbd.service                                        not-found inactive dead      kbd.service\n'
        b'keyboard-setup.service                             loaded    active   exited    Set the console keyboard layout\n'
        b'kmod-static-nodes.service                          loaded    active   exited    Create List of Static Device Nodes\n'
        b'ldconfig.service                                   loaded    inactive dead      Rebuild Dynamic Linker Cache\n'
        b'lima-guestagent.service                            loaded    active   running   lima-guestagent\n'
        b'logrotate.service                                  loaded    inactive dead      Rotate log files\n'
        b'lvm2-activation-early.service                      not-found inactive dead      lvm2-activation-early.service\n'
        b'lvm2-lvmpolld.service                              loaded    inactive dead      LVM2 poll daemon\n'
        b'lvm2-monitor.service                               loaded    active   exited    Monitoring of LVM2 mirrors, snapshots etc. using dmeventd or progress polling\n'
        b'man-db.service                                     loaded    inactive dead      Daily man-db regeneration\n'
        b'ModemManager.service                               loaded    active   running   Modem Manager\n'
        b'modprobe@configfs.service                          loaded    inactive dead      Load Kernel Module configfs\n'
        b'modprobe@dm_mod.service                            loaded    inactive dead      Load Kernel Module dm_mod\n'
        b'modprobe@drm.service                               loaded    inactive dead      Load Kernel Module drm\n'
        b'modprobe@efi_pstore.service                        loaded    inactive dead      Load Kernel Module efi_pstore\n'
        b'modprobe@fuse.service                              loaded    inactive dead      Load Kernel Module fuse\n'
        b'modprobe@loop.service                              loaded    inactive dead      Load Kernel Module loop\n'
        b'motd-news.service                                  loaded    inactive dead      Message of the Day\n'
        b'multipathd.service                                 loaded    active   running   Device-Mapper Multipath Device Controller\n'
        b'netplan-ovs-cleanup.service                        loaded    inactive dead      OpenVSwitch configuration for cleanup\n'
        b'networkd-dispatcher.service                        loaded    inactive dead      Dispatcher daemon for systemd-networkd\n'
        b'networking.service                                 not-found inactive dead      networking.service\n'
        b'NetworkManager.service                             not-found inactive dead      NetworkManager.service\n'
        b'open-iscsi.service                                 loaded    inactive dead      Login to default iSCSI targets\n'
        b'open-vm-tools.service                              loaded    inactive dead      Service for virtual machines hosted on VMware\n'
        b'ovsdb-server.service                               not-found inactive dead      ovsdb-server.service\n'
        b'plymouth-quit-wait.service                         loaded    active   exited    Hold until boot process finishes up\n'
        b'plymouth-quit.service                              loaded    active   exited    Terminate Plymouth Boot Screen\n'
        b'plymouth-read-write.service                        loaded    active   exited    Tell Plymouth To Write Out Runtime Data\n'
        b'plymouth-start.service                             loaded    inactive dead      Show Plymouth Boot Screen\n'
        b'plymouth-switch-root.service                       loaded    inactive dead      Plymouth switch root service\n'
        b'polkit.service                                     loaded    active   running   Authorization Manager\n'
        b'pollinate.service                                  loaded    inactive dead      Pollinate to seed the pseudo random number generator\n'
        b'rbdmap.service                                     not-found inactive dead      rbdmap.service\n'
        b'rc-local.service                                   loaded    inactive dead      /etc/rc.local Compatibility\n'
        b'rescue.service                                     loaded    inactive dead      Rescue Shell\n'
        b'rsyslog.service                                    loaded    active   running   System Logging Service\n'
        b'secureboot-db.service                              loaded    inactive dead      Secure Boot updates for DB and DBX\n'
        b'serial-getty@hvc0.service                          loaded    active   running   Serial Getty on hvc0\n'
        b'setvtrgb.service                                   loaded    active   exited    Set console scheme\n'
        b'snapd.apparmor.service                             loaded    active   exited    Load AppArmor profiles managed internally by snapd\n'
        b'snapd.autoimport.service                           loaded    inactive dead      Auto import assertions from block devices\n'
        b'snapd.core-fixup.service                           loaded    inactive dead      Automatically repair incorrect owner/permissions on core devices\n'
        b'snapd.failure.service                              loaded    inactive dead      Failure handling of the snapd snap\n'
        b'snapd.recovery-chooser-trigger.service             loaded    inactive dead      Wait for the Ubuntu Core chooser trigger\n'
        b'snapd.seeded.service                               loaded    active   exited    Wait until snapd is fully seeded\n'
        b'snapd.service                                      loaded    inactive dead      Snap Daemon\n'
        b'snapd.snap-repair.service                          loaded    inactive dead      Automatically fetch and run repair assertions\n'
        b'snapd.system-shutdown.service                      loaded    inactive dead      Ubuntu core (all-snaps) system shutdown helper setup service\n'
        b'ssh.service                                        loaded    active   running   OpenBSD Secure Shell server\n'
        b'sshd-keygen.service                                not-found inactive dead      sshd-keygen.service\n'
        b'sshd.service                                       not-found inactive dead      sshd.service\n'
        b'switchback-deployment-diagnostic1021-b3f.service   loaded    failed   failed    /var/lib/switchback-deployment-diagnostic1021-b3f/probe\n'
        b'switchback-deployment-diagnostic1021-nscap.service loaded    active   exited    /var/lib/switchback-deployment-diagnostic1021-nscap/probe\n'
        b'switchback-deployment-diagnostic1021-pid1.service  loaded    active   exited    /var/lib/switchback-deployment-diagnostic1021-pid1/probe\n'
        b'switchback-deployment-probe1020-b3c.service        loaded    failed   failed    /var/lib/switchback-deployment-probe1020-b3c/probe\n'
        b'switchback-volume-metadata1016-read-r1.service     loaded    failed   failed    Switchback fixed volume metadata readonly inspection\n'
        b'sysstat-collect.service                            loaded    inactive dead      system activity accounting tool\n'
        b'sysstat-summary.service                            loaded    inactive dead      Generate a daily summary of process accounting\n'
        b'sysstat.service                                    loaded    active   exited    Resets System Activity Logs\n'
        b'systemd-ask-password-console.service               loaded    inactive dead      Dispatch Password Requests to Console\n'
        b'systemd-ask-password-plymouth.service              loaded    inactive dead      Forward Password Requests to Plymouth\n'
        b'systemd-ask-password-wall.service                  loaded    inactive dead      Forward Password Requests to Wall\n'
        b'systemd-battery-check.service                      loaded    inactive dead      Check battery level during early boot\n'
        b'systemd-binfmt.service                             loaded    active   exited    Set Up Additional Binary Formats\n'
        b'systemd-bsod.service                               loaded    inactive dead      Displays emergency message in full screen.\n'
        b'systemd-firstboot.service                          loaded    inactive dead      First Boot Wizard\n'
        b'systemd-fsck-root.service                          loaded    active   exited    File System Check on Root Device\n'
        b'systemd-fsck@dev-disk-by\\x2dlabel-BOOT.service     loaded    active   exited    File System Check on /dev/disk/by-label/BOOT\n'
        b'systemd-fsck@dev-disk-by\\x2dlabel-UEFI.service     loaded    active   exited    File System Check on /dev/disk/by-label/UEFI\n'
        b'systemd-fsckd.service                              loaded    inactive dead      File System Check Daemon to report status\n'
        b'systemd-hibernate-resume.service                   loaded    inactive dead      Resume from hibernation\n'
        b'systemd-hibernate.service                          loaded    inactive dead      System Hibernate\n'
        b'systemd-hwdb-update.service                        loaded    inactive dead      Rebuild Hardware Database\n'
        b'systemd-hybrid-sleep.service                       loaded    inactive dead      System Hybrid Suspend+Hibernate\n'
        b'systemd-initctl.service                            loaded    inactive dead      initctl Compatibility Daemon\n'
        b'systemd-journal-catalog-update.service             loaded    inactive dead      Rebuild Journal Catalog\n'
        b'systemd-journal-flush.service                      loaded    active   exited    Flush Journal to Persistent Storage\n'
        b'systemd-journald.service                           loaded    active   running   Journal Service\n'
        b'systemd-logind.service                             loaded    active   running   User Login Management\n'
        b'systemd-machine-id-commit.service                  loaded    inactive dead      Commit a transient machine-id on disk\n'
        b'systemd-modules-load.service                       loaded    active   exited    Load Kernel Modules\n'
        b'systemd-networkd-wait-online.service               loaded    active   exited    Wait for Network to be Configured\n'
        b'systemd-networkd.service                           loaded    active   running   Network Configuration\n'
        b'systemd-oomd.service                               not-found inactive dead      systemd-oomd.service\n'
        b'systemd-pcrmachine.service                         loaded    inactive dead      TPM2 PCR Machine ID Measurement\n'
        b'systemd-pcrphase-initrd.service                    loaded    inactive dead      TPM2 PCR Barrier (initrd)\n'
        b'systemd-pcrphase-sysinit.service                   loaded    inactive dead      TPM2 PCR Barrier (Initialization)\n'
        b'systemd-pcrphase.service                           loaded    inactive dead      TPM2 PCR Barrier (User)\n'
        b'systemd-pstore.service                             loaded    inactive dead      Platform Persistent Storage Archival\n'
        b'systemd-quotacheck.service                         loaded    inactive dead      File System Quota Check\n'
        b'systemd-random-seed.service                        loaded    active   exited    Load/Save OS Random Seed\n'
        b'systemd-remount-fs.service                         loaded    active   exited    Remount Root and Kernel File Systems\n'
        b'systemd-repart.service                             loaded    inactive dead      Repartition Root Disk\n'
        b'systemd-resolved.service                           loaded    active   running   Network Name Resolution\n'
        b'systemd-rfkill.service                             loaded    inactive dead      Load/Save RF Kill Switch Status\n'
        b'systemd-soft-reboot.service                        loaded    inactive dead      Reboot System Userspace\n'
        b'systemd-suspend-then-hibernate.service             loaded    inactive dead      System Suspend then Hibernate\n'
        b'systemd-suspend.service                            loaded    inactive dead      System Suspend\n'
        b'systemd-sysctl.service                             loaded    active   exited    Apply Kernel Variables\n'
        b'systemd-sysext.service                             loaded    inactive dead      Merge System Extension Images into /usr/ and /opt/\n'
        b'systemd-sysusers.service                           loaded    inactive dead      Create System Users\n'
        b'systemd-timesyncd.service                          loaded    active   running   Network Time Synchronization\n'
        b'systemd-tmpfiles-clean.service                     loaded    inactive dead      Cleanup of Temporary Directories\n'
        b'systemd-tmpfiles-setup-dev-early.service           loaded    active   exited    Create Static Device Nodes in /dev gracefully\n'
        b'systemd-tmpfiles-setup-dev.service                 loaded    active   exited    Create Static Device Nodes in /dev\n'
        b'systemd-tmpfiles-setup.service                     loaded    active   exited    Create Volatile Files and Directories\n'
        b'systemd-tpm2-setup-early.service                   loaded    inactive dead      TPM2 SRK Setup (Early)\n'
        b'systemd-tpm2-setup.service                         loaded    inactive dead      TPM2 SRK Setup\n'
        b'systemd-udev-settle.service                        loaded    inactive dead      Wait for udev To Complete Device Initialization\n'
        b'systemd-udev-trigger.service                       loaded    active   exited    Coldplug All udev Devices\n'
        b'systemd-udevd.service                              loaded    active   running   Rule-based Manager for Device Events and Files\n'
        b'systemd-update-done.service                        loaded    inactive dead      Update is Completed\n'
        b'systemd-update-utmp-runlevel.service               loaded    inactive dead      Record Runlevel Change in UTMP\n'
        b'systemd-update-utmp.service                        loaded    active   exited    Record System Boot/Shutdown in UTMP\n'
        b'systemd-user-sessions.service                      loaded    active   exited    Permit User Sessions\n'
        b'systemd-vconsole-setup.service                     not-found inactive dead      systemd-vconsole-setup.service\n'
        b'ua-auto-attach.service                             not-found inactive dead      ua-auto-attach.service\n'
        b'ua-reboot-cmds.service                             loaded    inactive dead      Ubuntu Pro reboot cmds\n'
        b'ua-timer.service                                   loaded    inactive dead      Ubuntu Pro Timer for running repeated jobs\n'
        b'ubuntu-advantage-cloud-id-shim.service             not-found inactive dead      ubuntu-advantage-cloud-id-shim.service\n'
        b'ubuntu-advantage.service                           loaded    inactive dead      Ubuntu Pro Background Auto Attach\n'
        b'udisks2.service                                    loaded    active   running   Disk Manager\n'
        b'ufw.service                                        loaded    active   exited    Uncomplicated firewall\n'
        b'unattended-upgrades.service                        loaded    active   running   Unattended Upgrades Shutdown\n'
        b'update-notifier-download.service                   loaded    inactive dead      Download data for packages that failed at package install time\n'
        b'update-notifier-motd.service                       loaded    inactive dead      Check to see whether there is a new version of Ubuntu available\n'
        b'user-runtime-dir@501.service                       loaded    active   exited    User Runtime Directory /run/user/501\n'
        b'user@501.service                                   loaded    active   running   User Manager for UID 501\n'
        b'uuidd.service                                      loaded    inactive dead      Daemon for generating UUIDs\n'
        b'vgauth.service                                     loaded    inactive dead      Authentication service for virtual machines hosted on VMware\n'
        b'zfs-mount.service                                  not-found inactive dead      zfs-mount.service\n'
        b'apport-forward.socket                              loaded    inactive dead      Unix socket for apport crash forwarding\n'
        b'cloud-init-hotplugd.socket                         loaded    active   listening cloud-init hotplug hook socket\n'
        b'dbus.socket                                        loaded    active   running   D-Bus System Message Bus Socket\n'
        b'dm-event.socket                                    loaded    active   listening Device-mapper event daemon FIFOs\n'
        b'iscsid.socket                                      loaded    active   listening Open-iSCSI iscsid Socket\n'
        b'lvm2-lvmpolld.socket                               loaded    active   listening LVM2 poll daemon socket\n'
        b'lxd-installer.socket                               loaded    active   listening Helper to install lxd snap on demand\n'
        b'multipathd.socket                                  loaded    active   running   multipathd control socket\n'
        b'snapd.socket                                       loaded    active   listening Socket activation for snappy daemon\n'
        b'ssh.socket                                         loaded    active   running   OpenBSD Secure Shell server socket\n'
        b'syslog.socket                                      loaded    active   running   Syslog Socket\n'
        b'systemd-fsckd.socket                               loaded    active   listening fsck to fsckd communication Socket\n'
        b'systemd-initctl.socket                             loaded    active   listening initctl Compatibility Named Pipe\n'
        b'systemd-journald-audit.socket                      loaded    inactive dead      Journal Audit Socket\n'
        b'systemd-journald-dev-log.socket                    loaded    active   running   Journal Socket (/dev/log)\n'
        b'systemd-journald.socket                            loaded    active   running   Journal Socket\n'
        b'systemd-networkd.socket                            loaded    active   running   Network Service Netlink Socket\n'
        b'systemd-pcrextend.socket                           loaded    inactive dead      TPM2 PCR Extension (Varlink)\n'
        b'systemd-rfkill.socket                              loaded    active   listening Load/Save RF Kill Switch Status /dev/rfkill Watch\n'
        b'systemd-sysext.socket                              loaded    active   listening System Extension Image Management (Varlink)\n'
        b'systemd-udevd-control.socket                       loaded    active   running   udev Control Socket\n'
        b'systemd-udevd-kernel.socket                        loaded    active   running   udev Kernel Socket\n'
        b'uuidd.socket                                       loaded    active   listening UUID daemon activation socket\n'
    ),
)
_CAPTURED_PROPERTIES = {'ModemManager.service': b'MainPID=55537\nExecStart={ path=/usr/sbin/ModemManager ; argv[]=/usr/sbin/ModemManager ; ignore_errors=no ; start_time=[Sat 2026-10-24 06:06:55 CST] ; stop_time=[n/a] ; pid=55537 ; code=(null) ; status=0/0 }\nId=ModemManager.service\nActiveState=active\nUnitFileState=enabled\n', 'NetworkManager.service': b'MainPID=0\nId=NetworkManager.service\nActiveState=inactive\nUnitFileState=\n', 'apparmor.service': b'MainPID=0\nExecStart={ path=/lib/apparmor/apparmor.systemd ; argv[]=/lib/apparmor/apparmor.systemd reload ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=apparmor.service\nActiveState=active\nUnitFileState=enabled\n', 'apport-autoreport.service': b'MainPID=0\nExecStart={ path=/usr/share/apport/whoopsie-upload-all ; argv[]=/usr/share/apport/whoopsie-upload-all --timeout 20 ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=apport-autoreport.service\nActiveState=inactive\nUnitFileState=static\n'}
_CAPTURED_TEMPLATE_ERROR = ('apport-coredump-hook@.service', b'Failed to get properties: Unit name apport-coredump-hook@.service is neither a valid invocation ID nor unit name.\n')
_CAPTURED_TEMPLATES = ('apport-coredump-hook@.service', 'apport-forward@.service', 'autovt@.service', 'container-getty@.service', 'e2scrub@.service', 'e2scrub_fail@.service', 'getty@.service', 'lxd-installer@.service', 'mdadm-grow-continue@.service', 'mdadm-last-resort@.service', 'mdmon@.service', 'modprobe@.service', 'serial-getty@.service', 'systemd-backlight@.service', 'systemd-fsck@.service', 'systemd-growfs@.service', 'systemd-journald-varlink@.socket', 'systemd-journald@.service', 'systemd-journald@.socket', 'systemd-networkd-wait-online@.service', 'systemd-pcrextend@.service', 'systemd-pcrfs@.service', 'systemd-sysext@.service', 'usb_modeswitch@.service', 'user-runtime-dir@.service', 'user@.service', 'xfs_scrub@.service', 'xfs_scrub_fail@.service')

_CAPTURED_SYNTHETIC_OTHER_NAMES = (
    'switchback-engine-quota1016-exit-client.service',
    'switchback-engine1013-containerd.service',
    'switchback-engine1013-docker.service',
    'switchback-engine1013-watchdog.service',
    'switchback-prestart1014-containerd.service',
    'switchback-prestart1014-docker.service',
)


class UnitTemplateCatalogTests(unittest.TestCase):
    def exercise(self, installed=None, loaded=(), properties=None, captured=False,
                 show_failure=None, show_boundary=None, raw_properties=None):
        self.record, self.calls, self.shown = _CatalogRecord(), [], []
        if captured:
            lists = _CAPTURED_CATALOGS
        else:
            installed = list(guest._MASKS) + list(installed or ())
            lists = (''.join(name + ' disabled disabled\n' for name in installed).encode(),
                     ''.join(name + ' loaded inactive dead fixture\n' for name in loaded).encode())
        self.lists = lists
        def runner(argv, deadline, recorder):
            self.calls.append(tuple(argv))
            # This fake-boundary control buffer is per child; production raw
            # observations remain cumulative and are never cleared.
            recorder.frames.clear()
            if tuple(argv) in _CATALOG_COMMANDS:
                return _catalog_run(argv, recorder, lists[_CATALOG_COMMANDS.index(tuple(argv))])
            self.assertEqual(argv[:4], ['/usr/bin/systemctl', 'show', '--no-pager',
                '--property=Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen'])
            name = argv[-1]
            self.shown.append(name)
            if captured and name == _CAPTURED_TEMPLATE_ERROR[0]:
                return _catalog_run(argv, recorder, b'', {'exit_code': 1,
                    'stderr': _CAPTURED_TEMPLATE_ERROR[1]})
            # No later ordinary properties were captured. These values describe
            # only an explicitly synthetic neutral runtime, not the real guest.
            values = dict(Id=name, ActiveState='inactive', MainPID='0',
                UnitFileState='masked' if name in guest._MASKS else 'disabled',
                ExecStart='/usr/bin/true', Listen='')
            values.update((properties or {}).get(name, {}))
            if raw_properties is not None and name in raw_properties:
                raw = raw_properties[name]
            else:
                raw = (_CAPTURED_PROPERTIES[name] if captured and name in _CAPTURED_PROPERTIES else
                       ''.join(key + '=' + value + '\n' for key, value in values.items()).encode())
            return _catalog_run(argv, recorder, raw, show_failure, **(show_boundary or {}))
        # Production _IO.command/show, bounded runner, recorder and parser, with
        # only the established fake child/selector/signal boundary substituted.
        with mock.patch('guest._bounded_run', side_effect=runner):
            return guest._unit_catalog(guest._IO(130, self.record))

    def templates(self):
        return [frame['value'] for frame in self.record.observations
                if frame['kind'] == 'unit-catalog-template']

    def items(self):
        return [frame['value'] for frame in self.record.observations
                if frame['kind'] == 'unit-catalog-item']

    def test_captured_full_catalogues_skip_only_28_templates_and_show_all_283_ordinary(self):
        masks, others = self.exercise(captured=True)
        self.assertEqual(masks, preflight()['masks'])
        self.assertEqual(others, [dict(unit=name, active_state='inactive', main_pid=0)
                                 for name in _CAPTURED_SYNTHETIC_OTHER_NAMES])
        all_names = {line.split()[0].decode() for raw in _CAPTURED_CATALOGS for line in raw.splitlines()}
        self.assertEqual(len(all_names), 311)
        self.assertEqual(len(_CAPTURED_TEMPLATES), 28)
        self.assertEqual(self.shown, sorted(all_names - set(_CAPTURED_TEMPLATES)))
        self.assertEqual(len(self.shown), 283)
        self.assertEqual({item['Id'] for item in self.items()}, set(self.shown))
        self.assertEqual(len(self.items()), 283)
        self.assertEqual(self.templates(), [dict(name=name, in_installed_catalog=True,
            in_loaded_catalog=False) for name in _CAPTURED_TEMPLATES])
        self.assertEqual(self.calls[:2], list(_CATALOG_COMMANDS))
        for raw, result in zip(_CAPTURED_CATALOGS, self.record.results()):
            self.assertEqual(result['stdout'], prepare._wire(raw))
        self.assertEqual(tuple(map(len, _CAPTURED_CATALOGS)), (19861, 21630))
        self.assertEqual(tuple(len(raw.splitlines()) for raw in _CAPTURED_CATALOGS), (279, 194))
        for name, raw in _CAPTURED_PROPERTIES.items():
            self.assertIn(guest._properties(raw), self.items())

    def test_synthetic_masks_only_control(self):
        masks, others = self.exercise()
        self.assertEqual(masks, preflight()['masks'])
        self.assertEqual(others, [])
        self.assertEqual(self.templates(), [])
        self.assertEqual(self.shown, sorted(guest._MASKS))

    def test_templates_and_multiple_instances_remain_distinct_with_exact_names(self):
        templates = ('worker@.service', 'worker@.socket', 'docker@.service',
                     'containerd-shim@.service', 'switchback-engine@.service')
        # Multiple @ characters, escaped instance text and ordinary unit names
        # must not be normalized or classified by substring/basename.
        ordinary = ('worker@one.service', 'worker@two.service', 'worker@one.socket',
                    'worker@@.service', 'worker@a@.socket', 'worker@one\\x2dtwo.service',
                    'plain.service', 'plain.socket')
        masks, others = self.exercise(installed=templates + ordinary, loaded=ordinary)
        self.assertEqual(masks, preflight()['masks'])
        self.assertEqual(others, [])
        self.assertEqual(self.shown, sorted(set(guest._MASKS) | set(ordinary)))
        self.assertEqual(self.templates(), [dict(name=name, in_installed_catalog=True,
            in_loaded_catalog=False) for name in sorted(templates)])
        self.assertEqual({item['Id'] for item in self.items()}, set(self.shown))
        # Exact raw dictionaries above also prohibit fabricated MainPID,
        # ActiveState, ExecStart, Listen, or an inactive other_units entry.

    def test_templates_in_manager_catalogue_reject_before_any_show(self):
        for name in ('worker@.service', 'worker@.socket'):
            for installed in ((), (name,)):
                with self.subTest(name=name, installed=installed), self.assertRaisesRegex(
                        ValueError, 'unit template catalogue provenance'):
                    self.exercise(installed=installed, loaded=(name,))
                self.assertEqual(self.shown, [])
                self.assertEqual(self.templates(), [])
                self.assertEqual(len(self.record.results()), 2)

    def test_templates_count_toward_512_bound_and_513_rejects(self):
        template = 'worker@.service'
        ordinary = tuple('unit-%03d.service' % i for i in range(511 - len(guest._MASKS)))
        self.exercise(installed=ordinary + (template,))
        self.assertEqual(len(self.shown), 511)
        self.assertEqual(len(self.templates()), 1)
        with self.assertRaisesRegex(ValueError, 'unit catalog coverage/bound'):
            self.exercise(installed=ordinary + (template, 'another@.socket'))
        self.assertEqual(self.shown, [])

    def test_instantiated_renamed_daemons_still_reject_even_with_template_present(self):
        names = ('renamed@one.service', 'renamed@two.service', 'renamed@three.service')
        executables = ('/usr/bin/dockerd', '/usr/bin/containerd', '/usr/bin/containerd-shim-runc-v2')
        properties = {name: dict(ExecStart='{ path=' + exe + ' ; argv[]=' + exe + ' ; }',
            ActiveState='active', MainPID=str(990 + i))
            for i, (name, exe) in enumerate(zip(names, executables))}
        masks, others = self.exercise(installed=('renamed@.service',) + names,
                                      loaded=names, properties=properties)
        self.assertEqual(others, [dict(unit=name, active_state='active', main_pid=990 + i)
                                 for name, i in sorted((name, i) for i, name in enumerate(names))])
        facts = preflight()
        facts.update(masks=masks, other_units=others)
        with self.assertRaisesRegex(ValueError, 'active/unknown daemon unit'):
            guest.validate_preflight_v2(facts)
        self.assertTrue(set(names) <= set(self.shown))

    def test_non_template_show_errors_still_reject(self):
        cases = (({'show_failure': {'exit_code': 1}}, 'nonzero'),
                 ({'show_failure': {'terminal_timeout_at': 1}}, 'deadline'),
                 ({'show_failure': {'overflow': True}}, 'output exceeded bound'),
                 ({'show_boundary': {'identity_lost': True}}, 'identity lost'),
                 ({'show_boundary': {'cleanup_error': PermissionError(errno.EPERM, 'synthetic refusal')}},
                  'cleanup failed/unknown'))
        for options, message in cases:
            with self.subTest(options=options), self.assertRaisesRegex(prepare._Failure, message):
                self.exercise(installed=('aaa-ordinary.service', 'aaa-template@.service'), **options)
            self.assertEqual(self.shown, ['aaa-ordinary.service'])
            self.assertEqual(len(self.record.results()), 3)
            self.assertEqual(self.items(), [])
            self.assertEqual(self.templates(), [])

    def test_unknown_non_template_show_exit_is_rejected_by_actual_recorder(self):
        record = _CatalogRecord()
        def runner(argv, deadline, recorder):
            if tuple(argv) in _CATALOG_COMMANDS:
                return _catalog_run(argv, recorder,
                    ''.join(name + ' masked disabled\n' for name in guest._MASKS).encode())
            recorder.command(dict(argv=argv, exit_code=None, stdout=b'', stderr=b'',
                                  timed_out=False, truncated=False))
        with mock.patch('guest._bounded_run', side_effect=runner), self.assertRaisesRegex(
                prepare._Failure, 'unknown exit'):
            guest._unit_catalog(guest._IO(130, record))
        self.assertEqual(len(record.results()), 3)



# Exact two-Listen output captured at the 2026-10-24 failed show call.
_ARRAY_LISTEN_RAW = (b'Listen=/run/dmeventd-server (FIFO)\n'
    b'Listen=/run/dmeventd-client (FIFO)\nId=dm-event.socket\n'
    b'ActiveState=active\nUnitFileState=enabled\n')
_ARRAY_PROPERTIES = ('Id', 'ActiveState', 'MainPID', 'UnitFileState', 'ExecStart', 'Listen')


# Exact 39 show outputs from the later 2026-10-24 window. List bytes were
# proved equal to _CAPTURED_CATALOGS against the saved raw command frames.
# The remaining 244 ordinary show responses in this replay are synthetic.
_ARRAY_CAPTURED_PROPERTIES = {'ModemManager.service': b'MainPID=55537\nExecStart={ path=/usr/sbin/ModemManager ; argv[]=/usr/sbin/ModemMa'
                         b'nager ; ignore_errors=no ; start_time=[Sat 2026-10-24 06:06:55 CST] ; stop_time='
                         b'[n/a] ; pid=55537 ; code=(null) ; status=0/0 }\nId=ModemManager.service\nActiveSta'
                         b'te=active\nUnitFileState=enabled\n',
 'NetworkManager.service': b'MainPID=0\nId=NetworkManager.service\nActiveState=inactive\nUnitFileState=\n',
 'apparmor.service': b'MainPID=0\nExecStart={ path=/lib/apparmor/apparmor.systemd ; argv[]=/lib/apparmor/app'
                     b'armor.systemd reload ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0'
                     b' ; code=(null) ; status=0/0 }\nId=apparmor.service\nActiveState=active\nUnitFileSta'
                     b'te=enabled\n',
 'apport-autoreport.service': b'MainPID=0\nExecStart={ path=/usr/share/apport/whoopsie-upload-all ; argv[]=/u'
                              b'sr/share/apport/whoopsie-upload-all --timeout 20 ; ignore_errors=no ; start_'
                              b'time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=apport-'
                              b'autoreport.service\nActiveState=inactive\nUnitFileState=static\n',
 'apport-forward.socket': b'Listen=/run/apport.socket (Stream)\nId=apport-forward.socket\nActiveState=inac'
                          b'tive\nUnitFileState=enabled\n',
 'apport.service': b'MainPID=0\nExecStart={ path=/usr/share/apport/apport ; argv[]=/usr/share/apport/appor'
                   b't --start ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) '
                   b'; status=0/0 }\nId=apport.service\nActiveState=active\nUnitFileState=enabled\n',
 'apt-daily-upgrade.service': b'MainPID=0\nExecStart={ path=/usr/lib/apt/apt.systemd.daily ; argv[]=/usr/lib/'
                              b'apt/apt.systemd.daily install ; ignore_errors=no ; start_time=[Sat 2026-10-2'
                              b'4 06:06:52 CST] ; stop_time=[Sat 2026-10-24 06:06:56 CST] ; pid=55283 ; code'
                              b'=exited ; status=0 }\nId=apt-daily-upgrade.service\nActiveState=inactive\nU'
                              b'nitFileState=static\n',
 'apt-daily.service': b'MainPID=0\nExecStart={ path=/usr/lib/apt/apt.systemd.daily ; argv[]=/usr/lib/apt/apt.'
                      b'systemd.daily update ; ignore_errors=no ; start_time=[Sat 2026-10-24 18:04:33 CST] ;'
                      b' stop_time=[Sat 2026-10-24 18:04:33 CST] ; pid=57867 ; code=exited ; status=0 }\nId=a'
                      b'pt-daily.service\nActiveState=inactive\nUnitFileState=static\n',
 'apt-news.service': b'MainPID=0\nExecStart={ path=/usr/bin/python3 ; argv[]=/usr/bin/python3 /usr/lib/ubunt'
                     b'u-advantage/apt_news.py ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pi'
                     b'd=0 ; code=(null) ; status=0/0 }\nId=apt-news.service\nActiveState=inactive\nUnitFi'
                     b'leState=static\n',
 'auditd.service': b'MainPID=0\nId=auditd.service\nActiveState=inactive\nUnitFileState=\n',
 'blk-availability.service': b'MainPID=0\nId=blk-availability.service\nActiveState=active\nUnitFileState=e'
                             b'nabled\n',
 'bolt.service': b'MainPID=0\nExecStart={ path=/usr/libexec/boltd ; argv[]=/usr/libexec/boltd ; ignore_error'
                 b's=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=bolt.s'
                 b'ervice\nActiveState=inactive\nUnitFileState=static\n',
 'cloud-config.service': b'MainPID=0\nExecStart={ path=/usr/bin/cloud-init ; argv[]=/usr/bin/cloud-init modu'
                         b'les --mode=config ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid='
                         b'0 ; code=(null) ; status=0/0 }\nId=cloud-config.service\nActiveState=active\nUn'
                         b'itFileState=enabled\n',
 'cloud-final.service': b'MainPID=0\nExecStart={ path=/usr/bin/cloud-init ; argv[]=/usr/bin/cloud-init modu'
                        b'les --mode=final ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0'
                        b' ; code=(null) ; status=0/0 }\nId=cloud-final.service\nActiveState=failed\nUnitFile'
                        b'State=enabled\n',
 'cloud-init-hotplugd.service': b'MainPID=0\nExecStart={ path=/bin/bash ; argv[]=/bin/bash -c read args <&3'
                                b'; echo "args=$args";                          exec /usr/bin/cloud-init d'
                                b'evel hotplug-hook $args;                          exit 0 ; ignore_errors'
                                b'=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status='
                                b'0/0 }\nId=cloud-init-hotplugd.service\nActiveState=inactive\nUnitFileState='
                                b'static\n',
 'cloud-init-hotplugd.socket': b'Listen=/run/cloud-init/share/hook-hotplug-cmd (FIFO)\nId=cloud-init-hotpl'
                               b'ugd.socket\nActiveState=active\nUnitFileState=enabled\n',
 'cloud-init-local.service': b'MainPID=0\nExecStart={ path=/usr/bin/cloud-init ; argv[]=/usr/bin/cloud-init '
                             b'init --local ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0'
                             b' ; code=(null) ; status=0/0 }\nId=cloud-init-local.service\nActiveState=active'
                             b'\nUnitFileState=enabled\n',
 'cloud-init.service': b'MainPID=0\nExecStart={ path=/usr/bin/cloud-init ; argv[]=/usr/bin/cloud-init init'
                       b' ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; sta'
                       b'tus=0/0 }\nId=cloud-init.service\nActiveState=active\nUnitFileState=enabled\n',
 'connman.service': b'MainPID=0\nId=connman.service\nActiveState=inactive\nUnitFileState=\n',
 'console-getty.service': b'MainPID=0\nExecStart={ path=/sbin/agetty ; argv[]=/sbin/agetty -o -p -- \\u --'
                          b'noclear --keep-baud - 115200,38400,9600 $TERM ; ignore_errors=yes ; start_time=['
                          b'n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=console-getty.ser'
                          b'vice\nActiveState=inactive\nUnitFileState=disabled\n',
 'console-screen.service': b'MainPID=0\nId=console-screen.service\nActiveState=inactive\nUnitFileState=\n',
 'console-setup.service': b'MainPID=0\nExecStart={ path=/lib/console-setup/console-setup.sh ; argv[]=/lib/con'
                          b'sole-setup/console-setup.sh ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n'
                          b'/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=console-setup.service\nActiveStat'
                          b'e=active\nUnitFileState=enabled\n',
 'containerd.service': b'MainPID=0\nId=containerd.service\nActiveState=inactive\nUnitFileState=masked\n',
 'cron.service': b'MainPID=699\nExecStart={ path=/usr/sbin/cron ; argv[]=/usr/sbin/cron -f -P $EXTRA_OPTS ; '
                 b'ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0'
                 b' }\nId=cron.service\nActiveState=active\nUnitFileState=enabled\n',
 'cryptdisks-early.service': b'MainPID=0\nId=cryptdisks-early.service\nActiveState=inactive\nUnitFileState'
                             b'=masked\n',
 'cryptdisks.service': b'MainPID=0\nId=cryptdisks.service\nActiveState=inactive\nUnitFileState=masked\n',
 'dbus-org.freedesktop.ModemManager1.service': b'MainPID=55537\nExecStart={ path=/usr/sbin/ModemManager ; '
                                               b'argv[]=/usr/sbin/ModemManager ; ignore_errors=no ; start_tim'
                                               b'e=[Sat 2026-10-24 06:06:55 CST] ; stop_time=[n/a] ; pid=5553'
                                               b'7 ; code=(null) ; status=0/0 }\nId=ModemManager.service\nA'
                                               b'ctiveState=active\nUnitFileState=enabled\n',
 'dbus-org.freedesktop.hostname1.service': b'MainPID=0\nExecStart={ path=/usr/lib/systemd/systemd-hostname'
                                           b'd ; argv[]=/usr/lib/systemd/systemd-hostnamed ; ignore_errors=no'
                                           b' ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; st'
                                           b'atus=0/0 }\nId=systemd-hostnamed.service\nActiveState=inactive'
                                           b'\nUnitFileState=static\n',
 'dbus-org.freedesktop.locale1.service': b'MainPID=0\nExecStart={ path=/usr/lib/systemd/systemd-localed ; ar'
                                         b'gv[]=/usr/lib/systemd/systemd-localed ; ignore_errors=no ; start'
                                         b'_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0'
                                         b' }\nId=systemd-localed.service\nActiveState=inactive\nUnitFileS'
                                         b'tate=static\n',
 'dbus-org.freedesktop.login1.service': b'MainPID=715\nExecStart={ path=/usr/lib/systemd/systemd-logind ; a'
                                        b'rgv[]=/usr/lib/systemd/systemd-logind ; ignore_errors=no ; start'
                                        b'_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0'
                                        b' }\nId=systemd-logind.service\nActiveState=active\nUnitFileState=st'
                                        b'atic\n',
 'dbus-org.freedesktop.resolve1.service': b'MainPID=42664\nExecStart={ path=/usr/lib/systemd/systemd-resolved'
                                          b' ; argv[]=/usr/lib/systemd/systemd-resolved ; ignore_errors=no ;'
                                          b' start_time=[Thu 2026-10-22 06:05:55 CST] ; stop_time=[n/a] ; pi'
                                          b'd=42664 ; code=(null) ; status=0/0 }\nId=systemd-resolved.service'
                                          b'\nActiveState=active\nUnitFileState=enabled\n',
 'dbus-org.freedesktop.timedate1.service': b'MainPID=0\nExecStart={ path=/usr/lib/systemd/systemd-timedate'
                                           b'd ; argv[]=/usr/lib/systemd/systemd-timedated ; ignore_errors=no'
                                           b' ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; st'
                                           b'atus=0/0 }\nId=systemd-timedated.service\nActiveState=inactive'
                                           b'\nUnitFileState=static\n',
 'dbus-org.freedesktop.timesync1.service': b'MainPID=42666\nExecStart={ path=/usr/lib/systemd/systemd-time'
                                           b'syncd ; argv[]=/usr/lib/systemd/systemd-timesyncd ; ignore_error'
                                           b's=no ; start_time=[Thu 2026-10-22 06:05:55 CST] ; stop_time=[n/a'
                                           b'] ; pid=42666 ; code=(null) ; status=0/0 }\nId=systemd-timesy'
                                           b'ncd.service\nActiveState=active\nUnitFileState=enabled\n',
 'dbus.service': b'MainPID=700\nExecStart={ path=/usr/bin/dbus-daemon ; argv[]=@dbus-daemon --system --addre'
                 b'ss=systemd: --nofork --nopidfile --systemd-activation --syslog-only ; ignore_errors=no ;'
                 b' start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId=dbus.service'
                 b'\nActiveState=active\nUnitFileState=static\n',
 'dbus.socket': b'Listen=/run/dbus/system_bus_socket (Stream)\nId=dbus.socket\nActiveState=active\nUnitFileSt'
                b'ate=static\n',
 'debug-shell.service': b'MainPID=0\nExecStart={ path=/usr/bin/bash ; argv[]=/usr/bin/bash ; ignore_errors='
                        b'no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\nId='
                        b'debug-shell.service\nActiveState=inactive\nUnitFileState=disabled\n',
 'display-manager.service': b'MainPID=0\nId=display-manager.service\nActiveState=inactive\nUnitFileState=\n',
 'dm-event.service': b'MainPID=0\nExecStart={ path=/usr/sbin/dmeventd ; argv[]=/usr/sbin/dmeventd -f ; ignor'
                     b'e_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 '
                     b'}\nId=dm-event.service\nActiveState=inactive\nUnitFileState=static\n',
 'dm-event.socket': b'Listen=/run/dmeventd-server (FIFO)\nListen=/run/dmeventd-client (FIFO)\nId=dm-event.so'
                    b'cket\nActiveState=active\nUnitFileState=enabled\n'}

class UnitArrayPropertyTests(unittest.TestCase):
    def show(self, raw, properties=_ARRAY_PROPERTIES, scenario=None, boundary=None):
        record = _CatalogRecord()
        argv = ['/usr/bin/systemctl', 'show', '--no-pager',
                '--property=' + ','.join(properties), 'dm-event.socket']
        def runner(actual, deadline, recorder):
            self.assertEqual(actual, argv)
            return _catalog_run(actual, recorder, raw, scenario, **(boundary or {}))
        self.record = record
        with mock.patch('guest._bounded_run', side_effect=runner):
            return guest._IO(130, record).show('dm-event.socket', properties)

    def test_exact_catalogue_show_preserves_both_captured_listeners(self):
        result = self.show(_ARRAY_LISTEN_RAW)
        self.assertEqual(result, dict(Listen='/run/dmeventd-server (FIFO)\n/run/dmeventd-client (FIFO)',
            Id='dm-event.socket', ActiveState='active', UnitFileState='enabled'))
        self.assertEqual(self.record.results()[0]['stdout'], prepare._wire(_ARRAY_LISTEN_RAW))

    def test_single_captured_listener_and_duplicate_scalar_controls(self):
        for listener in (b'/run/dmeventd-server (FIFO)', b'/run/dmeventd-client (FIFO)'):
            with self.subTest(listener=listener):
                self.assertEqual(self.show(b'Listen=' + listener + b'\n')['Listen'], listener.decode())
        with self.assertRaisesRegex(ValueError, 'duplicate/malformed systemd property'):
            self.show(b'Id=dm-event.socket\nId=dm-event.socket\n')

    def test_catalogue_scans_second_and_last_execstart_for_running_daemons(self):
        name = 'apparently-unrelated@one.service'
        for executable in ('/usr/bin/dockerd', '/usr/bin/containerd', '/usr/bin/containerd-shim-runc-v2'):
            for position in (1, 2):
                with self.subTest(executable=executable, position=position):
                    commands = ['/usr/bin/true', '/usr/bin/printf', '/usr/bin/false']
                    commands[position] = executable
                    raw = (''.join('ExecStart={ path=' + command + ' ; argv[]=' + command + ' ; }\n'
                                   for command in commands) + 'Id=' + name + '\nActiveState=active\n'
                           'MainPID=999\nUnitFileState=enabled\n').encode()
                    fixture = UnitTemplateCatalogTests()
                    masks, others = fixture.exercise(installed=('apparently-unrelated@.service', name),
                        loaded=(name,), raw_properties={name: raw})
                    self.assertEqual(others, [dict(unit=name, active_state='active', main_pid=999)])
                    self.assertIn(name, fixture.shown)
                    observed = next(item for item in fixture.items() if item['Id'] == name)
                    self.assertEqual(observed['ExecStart'], '\n'.join(
                        '{ path=' + command + ' ; argv[]=' + command + ' ; }' for command in commands))
                    facts = preflight()
                    facts.update(masks=masks, other_units=others)
                    with self.assertRaisesRegex(ValueError, 'active/unknown daemon unit'):
                        guest.validate_preflight_v2(facts)


    def test_repeated_array_values_preserve_order_spaces_and_identical_multiplicity(self):
        raw = ('Listen= /run/first (Stream) \nExecStart={ path=/usr/bin/true ; argv[]=/usr/bin/true ; }\n'
               'Listen=/run/second (FIFO)\nExecStart={ path=/usr/bin/printf ; argv[]=/usr/bin/printf héllo ; }\n'
               'Listen= /run/first (Stream) \nExecStart={ path=/usr/bin/true ; argv[]=/usr/bin/true ; }\n').encode()
        result = self.show(raw)
        self.assertEqual(result['Listen'], ' /run/first (Stream) \n/run/second (FIFO)\n /run/first (Stream) ')
        self.assertEqual(result['ExecStart'], '{ path=/usr/bin/true ; argv[]=/usr/bin/true ; }\n'
            '{ path=/usr/bin/printf ; argv[]=/usr/bin/printf héllo ; }\n'
            '{ path=/usr/bin/true ; argv[]=/usr/bin/true ; }')
        self.assertEqual(self.record.results()[0]['stdout'], prepare._wire(raw))

    def test_every_empty_repeated_array_item_rejects(self):
        for key in ('Listen', 'ExecStart'):
            for items in (('', 'one'), ('one', ''), ('', ''), ('', 'one', 'two'),
                          ('one', '', 'two'), ('one', 'two', '')):
                raw = ''.join(key + '=' + item + '\n' for item in items).encode()
                with self.subTest(key=key, items=items), self.assertRaisesRegex(
                        ValueError, 'empty repeated systemd property'):
                    self.show(raw)

    def test_absent_and_single_empty_properties_keep_existing_semantics(self):
        for raw, expected in ((b'', {}), (b'Listen=\nExecStart=\n', dict(Listen='', ExecStart='')),
                              (b'Id=dm-event.socket\n', dict(Id='dm-event.socket'))):
            with self.subTest(raw=raw):
                self.assertEqual(self.show(raw), expected)
                self.assertEqual(guest._properties(raw), expected)

    def test_duplicate_scalars_and_unknown_keys_reject_even_when_identical(self):
        for key in ('Id', 'ActiveState', 'MainPID', 'UnitFileState', 'Unknown', 'catalogue'):
            for second in ('same', 'different', ''):
                with self.subTest(key=key, second=second), self.assertRaisesRegex(
                        ValueError, 'duplicate/malformed systemd property'):
                    self.show((key + '=same\n' + key + '=' + second + '\n').encode())

    def test_malformed_rows_still_reject_at_show_seam(self):
        for raw in (b'not-a-property\n', b'Id=dm-event.socket\n\nListen=/run/one (FIFO)\n',
                    b'Listen=/run/one (FIFO)\nListen=/run/two (FIFO)\nbroken\n'):
            with self.subTest(raw=raw), self.assertRaisesRegex(ValueError, 'duplicate/malformed systemd property'):
                self.show(raw)
        with self.assertRaises(UnicodeError):
            self.show(b'Listen=\xff\n')

    def test_default_and_non_catalogue_queries_cannot_enable_repetition_from_input(self):
        contexts = (_ARRAY_PROPERTIES[::-1], _ARRAY_PROPERTIES[:-1], ('Listen',), ('ExecStart',),
                    ('Id', 'Listen', 'ExecStart'), (*_ARRAY_PROPERTIES, 'Unknown'))
        for key in ('Listen', 'ExecStart'):
            # Even a plausible mode field in the raw data cannot enable a query.
            raw = ('catalogue=True\n' + key + '=one\n' + key + '=two\n').encode()
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'duplicate/malformed systemd property'):
                guest._properties(raw)
            for properties in contexts:
                with self.subTest(key=key, properties=properties), self.assertRaisesRegex(
                        ValueError, 'duplicate/malformed systemd property'):
                    self.show(raw, properties=properties)

    def test_installed_own_unit_effective_query_keeps_execstart_strictly_unique(self):
        files, record, calls = configuration(), _CatalogRecord(), []
        operations = guest._GuestOperations.__new__(guest._GuestOperations)
        operations.files = files
        io_ops = guest._IO(130, record)
        by_target = {target: files[name] for name, (target, mode) in guest.FILES.items()}
        io_ops.read = lambda target, bound, mode: (by_target[target], None)
        def runner(argv, deadline, recorder):
            calls.append(argv)
            self.assertEqual(argv[:3], ['/usr/bin/systemctl', 'show', '--no-pager'])
            self.assertEqual(argv[-1], guest.UNIT_NAMES[0])
            self.assertEqual(argv[3], '--property=Type,User,Group,UMask,Slice,Delegate,Restart,'
                'TimeoutStartUSec,TimeoutStopUSec,KillMode,RuntimeMaxUSec,WatchdogUSec,DropInPaths,'
                'LoadState,UnitFileState,Id,FragmentPath,ExecStart,Requires,Requisite,After,ActiveState,MainPID')
            return _catalog_run(argv, recorder,
                b'ExecStart={ path=/usr/bin/true ; argv[]=/usr/bin/true ; }\n'
                b'ExecStart={ path=/usr/bin/false ; argv[]=/usr/bin/false ; }\n')
        with mock.patch('guest._bounded_run', side_effect=runner), self.assertRaisesRegex(
                ValueError, 'duplicate/malformed systemd property'):
            operations._effective(io_ops)
        self.assertEqual(len(calls), 1)
        self.assertEqual(len(record.results()), 1)

    def test_command_failure_classes_reject_before_catalogue_property_parsing(self):
        cases = (({'scenario': {'exit_code': 7}}, 'nonzero'),
                 ({'scenario': {'terminal_timeout_at': 1}}, 'deadline'),
                 ({'scenario': {'overflow': True}}, 'output exceeded bound'),
                 ({'boundary': {'identity_lost': True}}, 'identity lost'),
                 ({'boundary': {'cleanup_error': PermissionError(errno.EPERM, 'synthetic refusal')}},
                  'cleanup failed/unknown'))
        for options, message in cases:
            with self.subTest(options=options), self.assertRaisesRegex(prepare._Failure, message):
                self.show(_ARRAY_LISTEN_RAW, **options)
            self.assertEqual(len(self.record.results()), 1)

    def test_full_saved_catalogue_replays_39_real_shows_with_244_explicitly_synthetic(self):
        fixture = UnitTemplateCatalogTests()
        masks, others = fixture.exercise(captured=True, raw_properties=_ARRAY_CAPTURED_PROPERTIES)
        self.replay_fixture = fixture
        installed, loaded = ({line.split()[0].decode() for line in raw.splitlines()}
                             for raw in _CAPTURED_CATALOGS)
        names, templates = installed | loaded, set(_CAPTURED_TEMPLATES)
        ordinary = names - templates
        self.assertEqual((len(installed), len(loaded), len(names), len(templates), len(ordinary)),
                         (279, 194, 311, 28, 283))
        self.assertTrue(templates <= installed)
        self.assertFalse(templates & loaded)
        self.assertEqual(len(_ARRAY_CAPTURED_PROPERTIES), 39)
        self.assertEqual(len(ordinary - _ARRAY_CAPTURED_PROPERTIES.keys()), 244)
        self.assertTrue(set(_ARRAY_CAPTURED_PROPERTIES) <= ordinary)
        self.assertEqual(_ARRAY_CAPTURED_PROPERTIES['dm-event.socket'], _ARRAY_LISTEN_RAW)
        self.assertEqual(fixture.shown, sorted(ordinary))
        self.assertEqual(len(fixture.items()), 283)
        self.assertEqual(fixture.templates(), [dict(name=name, in_installed_catalog=True,
            in_loaded_catalog=False) for name in sorted(templates)])
        self.assertEqual(masks, preflight()['masks'])
        self.assertEqual(others, [dict(unit=name, active_state='inactive', main_pid=0)
                                 for name in _CAPTURED_SYNTHETIC_OTHER_NAMES])
        actual = {tuple(result['argv']): result for result in fixture.record.results()}
        for argv, raw in zip(_CATALOG_COMMANDS, _CAPTURED_CATALOGS):
            self.assertEqual(actual[argv]['stdout'], prepare._wire(raw))
        for name, raw in _ARRAY_CAPTURED_PROPERTIES.items():
            argv = ('/usr/bin/systemctl', 'show', '--no-pager',
                    '--property=Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen', name)
            self.assertEqual(actual[argv]['stdout'], prepare._wire(raw))
        target = next(item for item in fixture.items() if item['Id'] == 'dm-event.socket')
        self.assertEqual(target, dict(Listen='/run/dmeventd-server (FIFO)\n/run/dmeventd-client (FIFO)',
            Id='dm-event.socket', ActiveState='active', UnitFileState='enabled'))



# Exact saved4762-byte config dump; only the IO boundary is synthetic.
# Python3.9 uses the typed stdlib-tomllib parse below. Task-private Python3.14
# replay byte/hash-checks both fixtures against the original saved command.
_CONTAINERD_DUMP_ZLIB = 'eJzVV1tv2zYUfvev8JuBbbbjtGv30qesG4IBS1EMA4qgIGjySCLMi0pSStJfv3MoKpbly9QUGLA3ied+5ccWfFDOzt/NX8+8cxE/Fmtj41orw5dh+6Vxka+lEzvwa+Fs5MqCl4tZiDxC4vaNXYcHFUW15WK3BFsiy9pslh6CkoC6hnIRTD1RLJp6MZMq8K0GyWrdIEdA2fuFcqu9zlXpa7FqNyvh1eKn+YiIh0TDaEoIZ8noTFQGjum2o9tTqpWN4C3XxODqeIKBjC5b8KpQ4Iltq6xU/pjTOKui8/uj5BRgkn1cfJ55+NIofzEJAXzbiVE6ji3s6TGeZqiUIbLhFr32aNU5w4Jwnsp8NVOmdj52ptcQxaCq9Fms5PqHVXRGo+TsXsK2KT/P5nMNLWgq+AJ/CucNj/2fdiWLngtgSuJZwXUAFM0xknD/vRr5KlVRdMGAd0VYEO98bnZFYFgI7OfkZncKlvqHYSYZJh8eny0NiNJQkeLT3ovztsvUa0FUIBud8kSKat4ENFJhzSqnKZyr1dV1IknQQD4dUjuPG5yiMWlz1RF7EwwVcHJtcRUWHYXaoqn3BBQxSLvgtYHIJY88NaHTMbtNHDhqLFTcK1uy2mklkko6AdnZs46FJyumZAfteCVC34fL2js8qqDpi0SnrFIhutJzM0llno3Iwy6NeeldU/f60Le9jWnqcNaXFbdSj1vonEjeDqv2OjnRl1zzSA3djYRWtnlcc2/evF63v9AMnNe3n8U0JlkflxKbIPTTMZ83at8pZf6epDVtgCOl37SoVwFX/nk3UvPyR+ZBtMygAVwZLKivtCo2b96+fXu9efPMFMDKs0yTA1pGUZ/LVNSBCT76Bx8PT3bwNGJxBpuLWW5gT/gPw8rDciqqSfLdJv/OQndKXtpwSkDqY1zIy/yfHZJQ8EbHNBwPXO9wvWQK7RJWTBrWgQmavDCysdXYpcoxun9UyQqlB4X0Mh4TLtk6ugC7G6Ld3yU0FBAZXuET83x5KQfLa9z6MWZwQNGQq9k04TFW81jtYwrCczQ3ihSvvfhUD3uYdtzxVYht7QFhG8tqJlVg5GP0z/ftyL/pSiS0htf1c45PBFo7p0eDueV4wyZI1U9bJvDUUB6Ma+HgdkfsKLiXLCU2HJCOUnYAHb4pmiEEORGJazU7U4+MPoowQh+p0thnyhBA2OpRVN1gjZLQQxi0JQfHCBDAs/6aFxwxRXZgeoAWMUp7pimna8HiePTmUqoa6gkcLqb5NoHGQTrOlDho98BE5R7swfGJhE/39OsLOhxxrA0Zk2K3cd1jUrwokE003lMBJDqqHZfk06segfXErlYF0IBvG0K4B3ftgLOpSQm+B5LIQFkFYsd6YMICJhQxO8iD3OSdeJj6sQFb825kNojIO8RFAQ1ycU+QyDUxAfVxNjpSQpr4NAK72MPXM6z0EMPOBi80cNvUSWJjLsr0YJM2N71HQxK6viSSlvzQxM//yk2pnsgaqiZSiRP7q0vsCcymN/Szz7P7EHE9G8KzAq9y57s30NHpuPecUMI/1RHzJ3D4O2jA++XKhYA6v9tw6SK+T0+OdWuTZPdIXaVGyoI/gk36EP7390ZsfLcYp2nIOzw3i4hymV3LC9uXnT9LOidTqG6J8CwsSYbepuMXZh/jmrgOr+bF3c3tzcdPH/5if7z/9OHj3d+3v77/yG7u/vzt9vd3Z/X0H4QKMbWtosTRaGTo/tKsr8qvqn556kn6+/Of1PzPi/AP5AopbA=='
_CONTAINERD_DUMP_PARSED = {'cgroup': {'path': ''},
 'debug': {'format': '', 'level': '', 'log_trace_id': False},
 'disabled_plugins': ['io.containerd.grpc.v1.cri',
                      'io.containerd.cri.v1.images',
                      'io.containerd.cri.v1.runtime',
                      'io.containerd.nri.v1.nri',
                      'io.containerd.internal.v1.opt',
                      'io.containerd.image-verifier.v1.bindir',
                      'io.containerd.monitor.container.v1.restart'],
 'imports': ['/etc/containerd/conf.d/*.toml'],
 'oom_score': 0,
 'plugins': {'io.containerd.differ.v1.erofs': {'enable_dmverity': False,
                                               'enable_tar_index': False,
                                               'mkfs_options': []},
             'io.containerd.gc.v1.scheduler': {'deletion_threshold': 0,
                                               'mutation_threshold': 100,
                                               'pause_threshold': 0.02,
                                               'schedule_delay': '0s',
                                               'startup_delay': '100ms'},
             'io.containerd.metadata.v1.bolt': {'content_sharing_policy': 'shared',
                                                'no_sync': False},
             'io.containerd.metrics.v1.grpc-prometheus': {'grpc_histogram': False},
             'io.containerd.monitor.task.v1.cgroups': {'no_prometheus': False},
             'io.containerd.mount-handler.v1.erofs': {},
             'io.containerd.runtime.v2.task': {'platforms': ['linux/arm64/v8']},
             'io.containerd.server.v1.debug': {'address': '',
                                               'gid': 0,
                                               'uid': 0},
             'io.containerd.server.v1.grpc': {'address': '/run/switchback-engine/m1-resident/containerd.sock',
                                              'gid': 0,
                                              'max_recv_message_size': 16777216,
                                              'max_send_message_size': 16777216,
                                              'uid': 0},
             'io.containerd.server.v1.grpc-tcp': {'address': '',
                                                  'max_recv_message_size': 16777216,
                                                  'max_send_message_size': 16777216,
                                                  'tls_ca': '',
                                                  'tls_cert': '',
                                                  'tls_common_name': '',
                                                  'tls_key': ''},
             'io.containerd.server.v1.metrics': {'address': ''},
             'io.containerd.server.v1.ttrpc': {'address': '/run/switchback-engine/m1-resident/containerd.ttrpc',
                                               'gid': 0,
                                               'uid': 0},
             'io.containerd.service.v1.diff-service': {'default': ['walking'],
                                                       'sync_fs': False},
             'io.containerd.service.v1.tasks-service': {'blockio_config_file': '',
                                                        'rdt_config_file': ''},
             'io.containerd.shim.v1.manager': {'env': [],
                                               'socket_dir': '/run/switchback-engine/m1-resident/s'},
             'io.containerd.snapshotter.v1.blockfile': {'fs_type': '',
                                                        'mount_options': [],
                                                        'recreate_scratch': False,
                                                        'root_path': '',
                                                        'scratch_file': ''},
             'io.containerd.snapshotter.v1.btrfs': {'root_path': ''},
             'io.containerd.snapshotter.v1.devmapper': {'async_remove': False,
                                                        'base_image_size': '',
                                                        'discard_blocks': False,
                                                        'fs_options': '',
                                                        'fs_type': '',
                                                        'pool_name': '',
                                                        'root_path': ''},
             'io.containerd.snapshotter.v1.erofs': {'default_size': '',
                                                    'dmverity_mode': '',
                                                    'enable_fsverity': False,
                                                    'layer_content_caches': [],
                                                    'ovl_mount_options': [],
                                                    'root_path': '',
                                                    'set_immutable': False},
             'io.containerd.snapshotter.v1.native': {'root_path': ''},
             'io.containerd.snapshotter.v1.overlayfs': {'mount_options': [],
                                                        'root_path': '',
                                                        'slow_chown': False,
                                                        'sync_remove': False,
                                                        'upperdir_label': False},
             'io.containerd.snapshotter.v1.zfs': {'root_path': ''},
             'io.containerd.transfer.v1.local': {'check_platform_supported': False,
                                                 'concurrent_layer_fetch_buffer': 0,
                                                 'config_path': '',
                                                 'max_concurrent_downloads': 3,
                                                 'max_concurrent_unpacks': 1,
                                                 'max_concurrent_uploaded_layers': 3}},
 'required_plugins': ['io.containerd.server.v1.grpc',
                      'io.containerd.server.v1.ttrpc',
                      'io.containerd.shim.v1.manager'],
 'root': '/mnt/lima-sbquota/docker/containerd',
 'state': '/run/switchback-engine/m1-resident/containerd',
 'stream_processors': {'io.containerd.ocicrypt.decoder.v1.tar': {'accepts': ['application/vnd.oci.image.layer.v1.tar+encrypted'],
                                                                 'args': ['--decryption-keys-path',
                                                                          '/etc/containerd/ocicrypt/keys'],
                                                                 'env': ['OCICRYPT_KEYPROVIDER_CONFIG=/etc/containerd/ocicrypt/ocicrypt_keyprovider.conf'],
                                                                 'path': 'ctd-decoder',
                                                                 'returns': 'application/vnd.oci.image.layer.v1.tar'},
                       'io.containerd.ocicrypt.decoder.v1.tar.gzip': {'accepts': ['application/vnd.oci.image.layer.v1.tar+gzip+encrypted'],
                                                                      'args': ['--decryption-keys-path',
                                                                               '/etc/containerd/ocicrypt/keys'],
                                                                      'env': ['OCICRYPT_KEYPROVIDER_CONFIG=/etc/containerd/ocicrypt/ocicrypt_keyprovider.conf'],
                                                                      'path': 'ctd-decoder',
                                                                      'returns': 'application/vnd.oci.image.layer.v1.tar+gzip'}},
 'temp': '/run/switchback-engine/m1-resident/tmp',
 'timeouts': {'io.containerd.timeout.bolt.open': '0s',
              'io.containerd.timeout.cri.defercleanup': '1m0s',
              'io.containerd.timeout.metrics.shimstats': '2s',
              'io.containerd.timeout.shim.cleanup': '5s',
              'io.containerd.timeout.shim.load': '5s',
              'io.containerd.timeout.shim.shutdown': '3s',
              'io.containerd.timeout.task.state': '2s'},
 'version': 4}


class ContainerdDumpTests(unittest.TestCase):
    def _call(self, effective=None, version='2.4.1', input_raw=None):
        raw = __import__('zlib').decompress(base64.b64decode(_CONTAINERD_DUMP_ZLIB))
        self.assertEqual(len(raw), 4762)
        self.assertEqual(hashlib.sha256(raw).hexdigest(),
            '918541f7a970f4ac23c2084e2ef49601f34e5f7a32e42c28f5dd406984d72b64')
        operation = guest._GuestOperations({}, {}, 0, 7.0, configuration(), {})
        operation.before = preflight()  # Synthetic validated preflight DTO.
        operation.before['binaries'][0]['version'] = version
        if input_raw is not None:
            operation.files['containerd.toml'] = input_raw
        calls, effects = [], []
        expected_argv = [
            ['/usr/bin/dockerd', '--validate', '--config-file=/etc/switchback/m1-engine/daemon.json'],
            ['/usr/bin/containerd', '--config', '/etc/switchback/m1-engine/containerd.toml', 'config', 'dump'],
            ['/usr/bin/systemctl', 'daemon-reload']]
        test = self
        class ReplayIO:
            def __init__(self, deadline, record):
                test.assertEqual(deadline, 7.0)
            def command(self, argv):
                calls.append(argv)
                test.assertLessEqual(len(calls), len(expected_argv))
                test.assertEqual(argv, expected_argv[len(calls) - 1])
                return raw if len(calls) == 2 else b''
        record = SimpleNamespace(attempt=lambda value: effects.append(('attempt', value)),
                                 known=lambda value: effects.append(('known', value)))
        try:
            import tomllib
        except ImportError:
            def loads(text):
                test.assertEqual(text.encode(), raw)
                return copy.deepcopy(_CONTAINERD_DUMP_PARSED)
            tomllib = SimpleNamespace(loads=loads)
        # Mutated observations below are explicitly synthetic parser DTOs;
        # the saved dump positive case uses real tomllib when available.
        if effective is not None:
            def loads(text):
                test.assertEqual(text.encode(), raw)
                return copy.deepcopy(effective)
            tomllib = SimpleNamespace(loads=loads)
        with mock.patch('guest._IO', ReplayIO), mock.patch.dict(sys.modules, {'tomllib': tomllib}):
            try:
                self.assertIsNone(operation.call('daemon_reload', 7.0, record))
            except ValueError:
                self.assertEqual(calls, expected_argv[:2])
                self.assertEqual(effects, [])
                raise
        self.assertEqual(calls, expected_argv)
        self.assertEqual(effects, [('attempt', 'systemd_daemon_reload'), ('known', 'systemd_daemon_reload')])

    def test_saved_dump_reaches_original_daemon_reload(self):
        self._call()

    def test_equal_imports_control_reaches_original_daemon_reload(self):
        value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
        value['imports'] = []  # Synthetic equal-output control.
        self._call(value)

    def test_synthetic_output_imports_reject_before_systemctl(self):
        for imports in (["/other/*.toml"], ["/etc/containerd/conf.d/*.toml", "/other/*.toml"],
                        ["/etc/containerd/conf.d/*.toml"] * 2, "", None, {}, False, [1]):
            with self.subTest(imports=imports):
                value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
                value['imports'] = imports
                with self.assertRaisesRegex(ValueError, 'containerd dump imports'):
                    self._call(value)
        value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
        del value['imports']
        with self.assertRaisesRegex(ValueError, 'containerd dump imports'):
            self._call(value)

    def test_synthetic_wrong_binary_versions_reject_both_output_forms(self):
        for version in ('2.4.0', '2.4.2', 'v2.4.1', '', None, 2):
            for imports in ([], ['/etc/containerd/conf.d/*.toml']):
                with self.subTest(version=version, imports=imports):
                    value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
                    value['imports'] = imports
                    with self.assertRaisesRegex(ValueError, 'fixed containerd imports/version'):
                        self._call(value, version=version)

    def test_synthetic_nonempty_or_malformed_input_imports_reject(self):
        original = configuration()['containerd.toml']
        for replacement in (b'imports = ["/other/*.toml"]', b'imports = ""', b'imports = false',
                            b'imports = {}', b'imports = [1]', b'imports = [', b'',
                            b'imports = []\nimports = []'):
            with self.subTest(replacement=replacement):
                with self.assertRaises(ValueError):
                    self._call(input_raw=original.replace(b'imports = []', replacement))

    def test_synthetic_compared_field_drift_rejects_before_systemctl(self):
        mutations = [('version', 3), ('version', True), ('root', '/other'), ('state', '/other'),
                     ('temp', '/other'), ('disabled_plugins', []), ('required_plugins', []),
                     ('disabled_plugins', 'disabled'), ('required_plugins', {})]
        for key, replacement in mutations:
            with self.subTest(key=key, replacement=replacement):
                value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
                value[key] = replacement
                with self.assertRaisesRegex(ValueError, 'effective values differ'):
                    self._call(value)
        plugin_mutations = [
            ('io.containerd.server.v1.grpc', 'address', '/other.sock'),
            ('io.containerd.server.v1.ttrpc', 'address', '/other.ttrpc'),
            ('io.containerd.server.v1.grpc-tcp', 'address', '127.0.0.1:1234'),
            ('io.containerd.server.v1.debug', 'address', '127.0.0.1:1234'),
            ('io.containerd.server.v1.metrics', 'address', '127.0.0.1:1234'),
            ('io.containerd.shim.v1.manager', 'socket_dir', '/other'),
            ('io.containerd.shim.v1.manager', 'env', ['X=1']),
            ('io.containerd.server.v1.grpc', 'uid', False),
            ('io.containerd.server.v1.ttrpc', 'gid', False),
            ('io.containerd.server.v1.debug', 'uid', 1)]
        for plugin, key, replacement in plugin_mutations:
            with self.subTest(plugin=plugin, key=key, replacement=replacement):
                value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
                value['plugins'][plugin][key] = replacement
                with self.assertRaisesRegex(ValueError, 'effective values differ'):
                    self._call(value)
        for replacement in (None, [], 'plugins'):
            value = copy.deepcopy(_CONTAINERD_DUMP_PARSED)
            value['plugins'] = replacement
            with self.subTest(plugins=replacement), self.assertRaisesRegex(ValueError, 'dump plugins'):
                self._call(value)

if __name__ == '__main__':
    unittest.main()
