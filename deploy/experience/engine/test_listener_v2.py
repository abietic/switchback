"""Complete six-role contract; all positive metadata is synthetic."""
from pathlib import Path
import unittest
from contract import _json, validate_listeners, validate_listeners_v2, validate_receipt_v2, validate_preflight_v2

class ListenerV2Tests(unittest.TestCase):
    def test_real_kit21_keeps_all_rows_and_remains_rejected(self):
        actual = _json((Path(__file__).parent / 'testdata/listeners-kit21-v1.json').read_bytes())
        self.assertEqual(len(actual['unix']), 139)
        with self.assertRaisesRegex(ValueError, 'hidden/foreign Unix listener'):
            validate_listeners(actual)
        with self.assertRaises(ValueError):
            validate_listeners_v2(actual)

import copy
import hashlib
import json
import stat
import tempfile
import time
from types import SimpleNamespace
from unittest import mock

import contract
import guest
from bundle import build_bundle, _load_kit
from test_contract import (listeners_v2, preflight_v2, receipt_v2, listeners as listeners_v1,
                           receipt as receipt_v1, preflight as preflight_v1, tiny_kit,
                           source_record, encode, rewrite_manifest)
from test_guest import Kernel, RawRecord, proc_stat
from test_prepare import Operations
from prepare import _run_window


class StrictV2Tests(unittest.TestCase):
    def test_shared_corpus_strict_decoding_and_closed_case_abi(self):
        corpus = _json((Path(__file__).parent / 'testdata/listener-v2-corpus.json').read_bytes())
        self.assertEqual(set(corpus), {'schema', 'provenance', 'cases'})
        self.assertEqual(corpus['schema'], 'switchback.engine.listener_corpus.v2')
        self.assertEqual(corpus['provenance'], 'synthetic; no runtime authority')
        self.assertEqual(len({case['name'] for case in corpus['cases']}), len(corpus['cases']))
        accepted = 0
        for case in corpus['cases']:
            with self.subTest(case=case['name']):
                self.assertEqual(set(case), {'name', 'accepted', 'listeners', 'receipt'})
                self.assertIs(type(case['accepted']), bool)
                ok = True
                try:
                    validate_listeners_v2(case['listeners'])
                    validate_receipt_v2(case['receipt'])
                except ValueError:
                    ok = False
                self.assertEqual(ok, case['accepted'])
                accepted += ok
        self.assertEqual(accepted, 3)

    def test_complete139_actual_inventory_accepts_only_with_explicit_synthetic_missing_fs(self):
        corpus = _json((Path(__file__).parent / 'testdata/listener-v2-corpus.json').read_bytes())
        case = next(v for v in corpus['cases'] if v['name'] ==
                    'synthetic_missing_fs_only_complete139_actual_inventory')
        actual = _json((Path(__file__).parent / 'testdata/listeners-kit21-v1.json').read_bytes())
        augmented = case['listeners']
        for field in ('phase', 'boot_id', 'current_boot_id', 'complete', 'daemons',
                      'identity_before', 'identity_after', 'unix', 'tcp', 'tcp6'):
            self.assertEqual(augmented[field], actual[field], field)
        self.assertEqual(augmented['sockets'][:3], actual['sockets'])
        self.assertEqual(len(augmented['unix']), 139)
        nonlisteners = [row for row in augmented['unix'] if not row['listener']
                        and row['path'] == contract.CONTAINERD_SOCKET]
        self.assertEqual({row['inode'] for row in nonlisteners}, {493189, 493723})
        validate_listeners_v2(augmented)
        validate_receipt_v2(case['receipt'])
        with self.assertRaises(ValueError):
            validate_listeners_v2(actual)

    def test_historical_v1_retains_original_semantics(self):
        validate_listeners(listeners_v1())
        contract.validate_receipt(receipt_v1())
        contract.validate_preflight(preflight_v1())
        with self.assertRaises(ValueError):
            validate_listeners(listeners_v2())
        with self.assertRaises(ValueError):
            contract.validate_receipt(receipt_v2())
        with self.assertRaises(ValueError):
            validate_preflight_v2(preflight_v1())

    def test_all_valid_phase_subsets_and_partial_receipt_parent_subsets(self):
        for phase, counts in (('preflight', (0, 0, 0)), ('containerd_ready', (1, 2, 2)),
                              ('engine_ready', (2, 6, 5))):
            with self.subTest(phase=phase):
                facts = listeners_v2(phase)
                validate_listeners_v2(facts)
                self.assertEqual(tuple(len(facts[k]) for k in ('daemons', 'sockets', 'socket_parents')), counts)
                value = receipt_v2()
                if phase != 'engine_ready':
                    value.update(status='unknown', daemons=facts['daemons'], sockets=facts['sockets'],
                                 socket_parents=facts['socket_parents'], engine=None, networks=[])
                    value['steps'] = value['steps'][:1]
                    value['steps'][0].update(status='unknown', exit_code=None, known_effects=[], unknown_effects=[])
                    value['known_effects'], value['unknown_effects'] = [], []
                validate_receipt_v2(value)
        validate_preflight_v2(preflight_v2())

    def test_every_socket_requires_exact_types_mode_owner_and_parent(self):
        for index in range(6):
            for field, bad in (('mode', False), ('uid', False), ('gid', False), ('nlink', True),
                               ('device', True), ('inode', 0), ('kernel_inode', 0), ('kind', 'symlink'),
                               ('owner_pid', True), ('owner_start_tick', 0), ('owner_unit', 'foreign'),
                               ('parent_mode', 0o755), ('parent_inode', 0), ('parent_uid', False),
                               ('parent_device', True), ('extra', 1)):
                with self.subTest(index=index, field=field):
                    value = listeners_v2()
                    value['sockets'][index][field] = bad
                    with self.assertRaises(ValueError):
                        validate_listeners_v2(value)

    def test_every_parent_requires_closed_ordered_directory_metadata(self):
        for index in range(5):
            for field, bad in (('mode', 0o755), ('uid', False), ('gid', False), ('device', True),
                               ('inode', 0), ('kind', 'symlink'), ('path', '/tmp/foreign'), ('extra', 1)):
                with self.subTest(index=index, field=field):
                    value = listeners_v2()
                    value['socket_parents'][index][field] = bad
                    with self.assertRaises(ValueError):
                        validate_listeners_v2(value)
        value = listeners_v2()
        value['socket_parents'][2:4] = reversed(value['socket_parents'][2:4])
        with self.assertRaises(ValueError):
            validate_listeners_v2(value)

    def test_complete_tables_keep_ordinary_rows_and_daemon_unnamed_nonlisteners(self):
        value = listeners_v2()
        owner = dict(value['unix'][5]['owners'][0], fd=100)
        value['unix'].append(dict(path='', inode=99991, listener=False, owners=[owner]))
        foreign = dict(main_pid=2000, start_tick=400, exe='/usr/bin/ordinary', cgroup='/ordinary', fd=101)
        value['unix'].append(dict(path='/run/ordinary.sock', inode=99992, listener=True, owners=[foreign]))
        value['tcp'].append(dict(address='127.0.0.1', port=8080, inode=99993, owners=[dict(foreign, fd=102)]))
        value['tcp6'].append(dict(address='::1', port=8081, inode=99994, owners=[dict(foreign, fd=103)]))
        validate_listeners_v2(value)
        value['unix'][-2]['path'] = '/run/foreign.sock'
        with self.assertRaisesRegex(ValueError, 'nonlistener'):
            validate_listeners_v2(value)

    def test_full_table_required_inodes_and_foreign_owner_identity(self):
        for mutate in (
            lambda v: v['unix'].pop(5),
            lambda v: v['unix'][5]['owners'][0].update(exe='/usr/bin/foreign'),
            lambda v: v['unix'][5]['owners'][0].update(cgroup='/foreign'),
            lambda v: v['unix'][5]['owners'][0].update(start_tick=1),
            lambda v: v['unix'][5]['owners'][0].update(main_pid=999),
            lambda v: v['unix'].append(dict(v['unix'][5], inode=99999, listener=True)),
            lambda v: v['unix'].append(dict(v['unix'][5], inode=99999, path='/run/hidden-docker.sock')),
            lambda v: v['complete'].update(fd_owners=False),
        ):
            value = listeners_v2()
            mutate(value)
            with self.assertRaises(ValueError):
                validate_listeners_v2(value)

    def test_receipt_requires_runtime_tmpfs_and_complete_parent_subset(self):
        for mutate in (
            lambda r: r['runtime_parent'].update(inode=r['runtime_parent']['inode'] + 1),
            lambda r: r['socket_parents'][2].update(device=999),
            lambda r: r['socket_parents'].pop(),
            lambda r: r['sockets'].pop(),
            lambda r: r['socket_parents'][2].update(inode=r['sockets'][5]['inode']),
        ):
            value = receipt_v2()
            mutate(value)
            with self.assertRaises(ValueError):
                validate_receipt_v2(value)

    def test_strict_raw_json_duplicate_and_integer_rejection(self):
        with self.assertRaisesRegex(ValueError, 'duplicate JSON key'):
            _json(b'{"socket_parents":[],"socket_parents":[]}')
        value = listeners_v2()
        value['socket_parents'][0]['inode'] = float(value['socket_parents'][0]['inode'])
        with self.assertRaisesRegex(ValueError, 'integer'):
            validate_listeners_v2(_json(json.dumps(value).encode()))

    def test_actual_provenance_hash_and_all_original_rows(self):
        root = Path(__file__).parent / 'testdata'
        provenance = _json((root / 'listener-v2-provenance.json').read_bytes())
        raw = (root / 'listeners-kit21-v1.json').read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), provenance['fixture_sha256'])
        canonical = json.dumps(_json(raw), sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(hashlib.sha256(canonical).hexdigest(), provenance['extracted_canonical_sha256'])
        self.assertEqual(len(_json(raw)['unix']), provenance['unix_rows'])


class RealCollectorV2Tests(unittest.TestCase):
    def collect(self, kernel, phase='engine_ready'):
        with mock.patch('guest.os.lstat', side_effect=kernel.lstat), \
             mock.patch('guest.os.readlink', side_effect=kernel.readlink), \
             mock.patch('guest.os.path.lexists', return_value=False):
            return guest._collect_listeners(kernel, phase, ['synthetic', 'synthetic'])

    def test_actual_collector_discovers_two_random_names_and_samples_all_paths_twice(self):
        for name in ('0123456789ab', 'fedcba987654'):
            facts = listeners_v2()
            path = contract.DOCKER_RUNTIME_ROOT + '/libnetwork/' + name + '.sock'
            facts['sockets'][5]['path'] = facts['unix'][5]['path'] = path
            kernel = Kernel(facts)
            seen = []
            lstat = kernel.lstat
            kernel.lstat = lambda path: (seen.append(str(path)), copy.deepcopy(lstat(path)))[1]
            actual = self.collect(kernel)
            self.assertEqual(actual['sockets'], facts['sockets'])
            self.assertEqual(actual['socket_parents'], facts['socket_parents'])
            for path in contract.SOCKET_PARENT_PATHS + tuple(s['path'] for s in facts['sockets']):
                self.assertEqual(seen.count(path), 2)
            self.assertEqual(len(actual['unix']), 6)
            kinds = [kind for kind, _ in kernel.record.observations]
            self.assertIn('complete-listener-tables', kinds)
            self.assertIn('listener-process-sampling-point', kinds)
            self.assertIn('listener-inventory-before-validation', kinds)

    @staticmethod
    def add_hidden_socket(kernel, listed=True):
        if listed:
            kernel.files['/proc/net/unix'] += (b'000: 00000002 00000000 00010000 '
                b'0001 01 99999 /run/hidden-docker.sock\n')
        kernel.process_rows[1]['fds'].append(dict(fd=99, target='socket:[99999]',
            flags=2, device=0, inode=99999, mount_id=8, socket_inode=99999))
        kernel.links['/proc/102/fd/99'] = 'socket:[99999]'

    def test_table_before_process_race_rejects_new_daemon_listener(self):
        kernel = Kernel()
        original = kernel.processes
        calls = []
        def processes():
            calls.append(True)
            if len(calls) == 1:
                self.add_hidden_socket(kernel)
            return original()
        kernel.processes = processes
        with self.assertRaisesRegex(ValueError, 'hidden/foreign Unix listener'):
            self.collect(kernel)
        kinds = [kind for kind, _ in kernel.record.observations]
        self.assertIn('complete-listener-tables', kinds)

    def test_new_listener_or_unlisted_socket_fd_after_process_snapshot_rejects(self):
        # The mutation occurs after returning an independent process snapshot.
        # An unlisted stable socket is legal; adding one during collection is not.
        for listed in (False, True):
            for snapshot in (1, 2) if listed else (1,):
                kernel = Kernel()
                original = kernel.processes
                calls = []
                def processes():
                    value = original()
                    calls.append(True)
                    if len(calls) == snapshot:
                        self.add_hidden_socket(kernel, listed)
                    return value
                kernel.processes = processes
                with self.subTest(listed=listed, snapshot=snapshot):
                    if listed:
                        with self.assertRaisesRegex(ValueError, 'hidden/foreign Unix listener'):
                            self.collect(kernel)
                    else:
                        actual = self.collect(kernel)
                        self.assertTrue(actual['complete']['fd_owners'])
                        starts = [v['attempt'] for k, v in kernel.record.observations
                                  if k == 'listener-collection-attempt-start']
                        self.assertEqual(starts, [1, 2])

    def test_all_socket_tables_are_rechecked_after_process_inventory(self):
        for family in ('unix', 'tcp', 'tcp6'):
            kernel = Kernel()
            original = kernel.proc
            reads = []
            def proc(path, maximum=None):
                if path == '/proc/net/' + family:
                    reads.append(True)
                    if len(reads) == 2:
                        if family == 'unix':
                            self.add_hidden_socket(kernel)
                        else:
                            address = b'00000000' if family == 'tcp' else b'0' * 32
                            kernel.files[path] += (b'0: ' + address + b':093B ' + address +
                                b':0000 0A 00000000:00000000 00:00000000 '
                                b'00000000 0 0 99999 1\n')
                return original(path, maximum)
            kernel.proc = proc
            with self.subTest(family=family), self.assertRaises(ValueError):
                self.collect(kernel)

    def test_stable_unlisted_netlink_or_other_socket_fds_remain_valid(self):
        kernel = Kernel()
        self.add_hidden_socket(kernel, listed=False)
        actual = self.collect(kernel)
        self.assertEqual(len(actual['unix']), 6)
        self.assertTrue(actual['complete']['fd_owners'])

    def ordinary_fd_change(self, initial, final):
        kernel = Kernel()
        process = dict(main_pid=103, start_tick=123, exe='/usr/bin/ordinary', cgroup='/',
                       fds=copy.deepcopy(initial))
        kernel.process_rows.append(process)
        kernel.files['/proc/103/stat'] = proc_stat(103, 123)
        kernel.files['/proc/103/cgroup'] = b'0::/\n'
        kernel.links['/proc/103/exe'] = process['exe']
        original, calls = kernel.processes, []
        def processes():
            calls.append(True)
            if len(calls) == 2:
                process['fds'] = copy.deepcopy(final)
            value = original()
            kernel.record.raw('complete-process-fd-inventory', value)
            return value
        kernel.processes = processes
        return kernel

    @staticmethod
    def ordinary_fd(target=contract.DATA_ROOT + '/foreign-write', flags=2):
        return dict(fd=99, target=target, flags=flags, device=10, inode=888888,
                    mount_id=8, socket_inode=None)

    def test_closing_new_ordinary_data_root_writer_is_rejected(self):
        for flags in (1, 2):
            kernel = self.ordinary_fd_change([], [self.ordinary_fd(flags=flags)])
            with self.subTest(flags=flags), self.assertRaisesRegex(ValueError, 'foreign data-root writer'):
                self.collect(kernel)
            inventories = [value for kind, value in kernel.record.observations
                           if kind == 'complete-process-fd-inventory']
            self.assertEqual(len(inventories), 2)
            self.assertEqual(inventories[0][-1]['fds'], [])
            self.assertEqual(inventories[1][-1]['fds'], [self.ordinary_fd(flags=flags)])

    def test_closing_ordinary_fd_flags_or_target_mutation_to_writer_is_rejected(self):
        for initial in (self.ordinary_fd(flags=0), self.ordinary_fd(target='/tmp/ordinary')):
            kernel = self.ordinary_fd_change([initial], [self.ordinary_fd()])
            with self.subTest(initial=initial), self.assertRaisesRegex(ValueError, 'foreign data-root writer'):
                self.collect(kernel)

    def test_closing_readonly_or_outside_data_root_ordinary_fd_changes_remain_valid(self):
        for initial, final in (
            ([], [self.ordinary_fd(flags=0)]),
            ([], [self.ordinary_fd(target='/tmp/ordinary')]),
            ([self.ordinary_fd(flags=0)], [self.ordinary_fd(flags=0, target=contract.DATA_ROOT + '/other')]),
            ([self.ordinary_fd(target='/tmp/ordinary')], [self.ordinary_fd(target='/tmp/other', flags=1)]),
        ):
            kernel = self.ordinary_fd_change(initial, final)
            with self.subTest(initial=initial, final=final):
                actual = self.collect(kernel)
                self.assertTrue(all(actual['complete'].values()))
                inventories = [value for kind, value in kernel.record.observations
                               if kind == 'complete-process-fd-inventory']
                self.assertEqual([sample[-1]['fds'] for sample in inventories], [initial, final])

    def test_distinct_process_samples_are_persisted_once_each(self):
        kernel = Kernel()
        original = kernel.processes
        def processes():
            value = original()
            # Match _IO.processes' existing raw audit behavior.
            kernel.record.raw('complete-process-fd-inventory', value)
            return value
        kernel.processes = processes
        self.collect(kernel)
        inventories = [(kind, value) for kind, value in kernel.record.observations
                       if 'process' in kind]
        self.assertEqual([kind for kind, _ in inventories], [
            'listener-process-sampling-point', 'complete-process-fd-inventory',
            'listener-process-sampling-point', 'complete-process-fd-inventory'])
        self.assertEqual([value for kind, value in inventories
                          if kind == 'listener-process-sampling-point'], ['initial', 'closing'])

    def test_collector_keeps_established_approved_same_path_nonlistener(self):
        facts = listeners_v2()
        facts['unix'].append(dict(path=contract.CONTAINERD_SOCKET, inode=99991,
            listener=False, owners=[dict(facts['unix'][0]['owners'][0], fd=99)]))
        kernel = Kernel(facts)
        actual = self.collect(kernel)
        self.assertEqual(actual['unix'], facts['unix'])
        self.assertEqual(len(actual['unix']), 7)

    def test_real_collector_retains_complete139_unix_and_all_tcp_owner_rows(self):
        corpus = _json((Path(__file__).parent / 'testdata/listener-v2-corpus.json').read_bytes())
        facts = next(case['listeners'] for case in corpus['cases'] if case['name'] ==
                     'synthetic_missing_fs_only_complete139_actual_inventory')
        kernel = Kernel(facts)
        processes = {}
        for family in ('unix', 'tcp', 'tcp6'):
            for row in facts[family]:
                for owner in row['owners']:
                    pid = owner['main_pid']
                    identity = {k: owner[k] for k in ('main_pid', 'start_tick', 'exe', 'cgroup')}
                    process = processes.setdefault(pid, dict(identity, fds=[]))
                    self.assertEqual({k: process[k] for k in identity}, identity)
                    target = 'socket:[%d]' % row['inode']
                    process['fds'].append(dict(fd=owner['fd'], target=target, flags=2,
                        device=0, inode=row['inode'], mount_id=8, socket_inode=row['inode']))
                    prefix = '/proc/%d/' % pid
                    kernel.links[prefix + 'fd/%d' % owner['fd']] = target
                    kernel.links[prefix + 'exe'] = owner['exe']
                    kernel.files[prefix + 'stat'] = proc_stat(pid, owner['start_tick'])
                    kernel.files[prefix + 'cgroup'] = ('0::' + owner['cgroup'] + '\n').encode()
        kernel.process_rows = [processes[pid] for pid in sorted(processes)]
        for family in ('tcp', 'tcp6'):
            rows = []
            ipv6 = family == 'tcp6'
            for index, row in enumerate(facts[family]):
                address = guest.socket.inet_pton(guest.socket.AF_INET6 if ipv6 else
                                                guest.socket.AF_INET, row['address'])
                address = (b''.join(address[i:i + 4][::-1] for i in range(0, 16, 4))
                           if ipv6 else address[::-1])
                rows.append(('%d: %s:%04X %s:0000 0A 00000000:00000000 '
                    '00:00000000 00000000 0 0 %d 1\n' %
                    (index, address.hex().upper(), row['port'], '0' * (32 if ipv6 else 8), row['inode'])).encode())
            kernel.files['/proc/net/' + family] += b''.join(rows)
        actual = self.collect(kernel)
        for family in ('unix', 'tcp', 'tcp6'):
            self.assertEqual(actual[family], facts[family])
        self.assertEqual(tuple(len(actual[f]) for f in ('unix', 'tcp', 'tcp6')), (139, 3, 1))
        self.assertEqual(len(kernel.process_rows), 27)

    def test_actual_preflight_and_containerd_subsets(self):
        for phase in ('preflight', 'containerd_ready'):
            kernel = Kernel(listeners_v2(phase))
            self.assertEqual(self.collect(kernel, phase)['socket_parents'], kernel.facts['socket_parents'])

    def test_missing_duplicate_invalid_and_nonlistener_libnetwork_discovery(self):
        for kind in ('missing', 'duplicate', 'invalid', 'nonlistener'):
            kernel = Kernel()
            raw = kernel.files['/proc/net/unix'].splitlines(keepends=True)
            if kind == 'missing':
                raw.pop()
            elif kind == 'duplicate':
                raw.append(raw[-1].replace(b'92005', b'99005').replace(b'0123456789ab', b'fedcba987654'))
            elif kind == 'invalid':
                raw[-1] = raw[-1].replace(b'0123456789ab', b'not-hex-name')
            else:
                raw[-1] = raw[-1].replace(b'00010000', b'00000000')
            kernel.files['/proc/net/unix'] = b''.join(raw)
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, 'sole libnetwork'):
                self.collect(kernel)
            self.assertIn('complete-listener-tables', [k for k, _ in kernel.record.observations])

    def test_each_socket_and_parent_drift_between_first_and_last_lstat(self):
        facts = listeners_v2()
        for target in [s['path'] for s in facts['sockets']] + list(contract.SOCKET_PARENT_PATHS):
            kernel = Kernel()
            calls, lstat = {}, kernel.lstat
            def drifting(path):
                path = str(path)
                calls[path] = calls.get(path, 0) + 1
                value = copy.deepcopy(lstat(path))
                if path == target and calls[path] == 2:
                    value.st_ino += 1000
                return value
            kernel.lstat = drifting
            with self.subTest(path=target), self.assertRaisesRegex(ValueError, 'filesystem identity drift'):
                self.collect(kernel)
            self.assertIn('listener-inventory-before-validation', [k for k, _ in kernel.record.observations])

    def test_front_or_back_wrong_socket_and_parent_type_is_rejected(self):
        for target in (contract.CALLBACK_SOCKET, contract.DOCKER_RUNTIME_ROOT + '/extensions'):
            for mode in (stat.S_IFLNK | 0o777, stat.S_IFREG | 0o700):
                kernel = Kernel()
                kernel.files[target].st_mode = mode
                with self.subTest(target=target, mode=mode), self.assertRaises(ValueError):
                    self.collect(kernel)

    def test_process_fd_exe_cgroup_boot_and_daemon_hash_drift(self):
        for kind in ('fd', 'exe', 'cgroup', 'pid', 'boot', 'hash'):
            kernel = Kernel()
            daemon = kernel.facts['daemons'][1]
            prefix = '/proc/%d/' % daemon['main_pid']
            if kind == 'fd':
                kernel.links[prefix + 'fd/15'] = 'socket:[99999]'
            elif kind == 'exe':
                kernel.links[prefix + 'exe'] = '/usr/bin/foreign'
            elif kind == 'cgroup':
                kernel.files[prefix + 'cgroup'] = b'0::/foreign\n'
            elif kind == 'pid':
                kernel.files[prefix + 'stat'] = proc_stat(daemon['main_pid'], daemon['start_tick'] + 1)
            elif kind == 'boot':
                boots = iter([kernel.facts['boot_id'], '11111111-1111-1111-1111-111111111111'])
                kernel.boot = lambda: next(boots)
            else:
                original, calls = kernel.daemon, []
                def changing(index, boot, binary_hash):
                    calls.append(index)
                    value = original(index, boot, binary_hash)
                    if len(calls) > 2 and index == 1:
                        value['exe_sha256'] = 'sha256:' + '0' * 64
                    return value
                kernel.daemon = changing
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                self.collect(kernel)

    def test_foreign_fd_and_extra_daemon_tcp6_still_reject(self):
        for family in ('unix', 'tcp6'):
            kernel = Kernel()
            if family == 'unix':
                d = dict(kernel.process_rows[1], main_pid=999, exe='/usr/bin/ordinary', cgroup='/ordinary')
                d['fds'] = [dict(kernel.process_rows[1]['fds'][-1], fd=99)]
                kernel.process_rows.append(d)
                kernel.files['/proc/999/stat'] = proc_stat(999, d['start_tick'])
                kernel.files['/proc/999/cgroup'] = b'0::/ordinary\n'
                kernel.links['/proc/999/exe'] = '/usr/bin/ordinary'
                kernel.links['/proc/999/fd/99'] = d['fds'][0]['target']
            else:
                kernel.files['/proc/net/tcp6'] += (b'0: 00000000000000000000000000000000:093B '
                    b'00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 '
                    b'00000000 0 0 99999 1\n')
                kernel.process_rows[1]['fds'].append(dict(fd=99, target='socket:[99999]', flags=2,
                    device=0, inode=99999, mount_id=8, socket_inode=99999))
                kernel.links['/proc/102/fd/99'] = 'socket:[99999]'
            with self.subTest(family=family), self.assertRaises(ValueError):
                self.collect(kernel)


class LiveGateV2Tests(unittest.TestCase):
    def operations(self):
        ops = guest._GuestOperations({}, {}, time.monotonic(), time.monotonic() + 120, {}, {})
        ops._boundary = lambda _: None
        ops.binary_hashes = ['synthetic', 'synthetic']
        ops.live = listeners_v2()
        return ops

    def test_current_gate_and_publication_include_parent_identity(self):
        ops = self.operations()
        changed = listeners_v2()
        changed['socket_parents'][2]['inode'] += 1
        with mock.patch('guest._collect_listeners', return_value=changed):
            with self.assertRaisesRegex(ValueError, 'identity drift'):
                ops._listener_gate(SimpleNamespace())
        ops._listener_gate = lambda _: changed
        with mock.patch('guest._exclusive', side_effect=AssertionError('unexpected publication')):
            with self.assertRaisesRegex(ValueError, 'before receipt publication'):
                ops.publish(receipt_v2(), ops.deadline, RawRecord())

    def test_api_connect_selects_exact_docker_role_and_compares_parent_after(self):
        for drift in (False, True):
            ops = self.operations()
            kernel = Kernel()
            facts = listeners_v2()
            after = copy.deepcopy(facts)
            if drift:
                after['socket_parents'][2]['inode'] += 1
            gates = iter((facts, after))
            ops._listener_gate = lambda _: next(gates)
            connection = mock.Mock()
            connection.request.side_effect = lambda *a, **k: connection.connect()
            response = mock.Mock(status=200)
            response.getheaders.return_value = []
            response.read1.return_value = b''
            connection.getresponse.return_value = response
            sock = mock.Mock()
            io_ops = SimpleNamespace(check=lambda: None, record=RawRecord())
            with mock.patch('guest.http.client.HTTPConnection', return_value=connection), \
                 mock.patch('guest.socket.socket', return_value=sock), \
                 mock.patch('guest.os.lstat', side_effect=kernel.lstat):
                if drift:
                    with self.assertRaisesRegex(ValueError, 'identity changed around Engine response'):
                        ops._request(io_ops, 'GET', '/info')
                else:
                    self.assertEqual(ops._request(io_ops, 'GET', '/info'), (200, b''))
            sock.connect.assert_called_once_with(contract.ENGINE_SOCKET.removeprefix('unix://'))

    def test_final_identity_parent_drift_never_publishes(self):
        ops = Operations()
        original = ops.call
        def call(name, deadline, record):
            value = original(name, deadline, record)
            if name == 'final_identity':
                value['socket_parents'][2]['inode'] += 1
            return value
        ops.call = call
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(result['status'], 'failed')
        self.assertIsNone(ops.published)
        self.assertEqual(result['receipt']['socket_parents'], receipt_v2()['socket_parents'])

    def test_exclusive_install_precreates_exact_private_runtime_directories(self):
        ops = self.operations()
        ops.files = {name: b'synthetic' for name in contract.FILES}
        made = []
        ops._mkdir = lambda path, _: made.append(str(path))
        class StopAfterDirectories(Exception):
            pass
        with mock.patch('guest._IO', return_value=SimpleNamespace(check=lambda: None)), \
             mock.patch('guest._exclusive', side_effect=StopAfterDirectories):
            with self.assertRaises(StopAfterDirectories):
                ops.call('install_files', ops.deadline, RawRecord())
        self.assertEqual(made, ['/etc/switchback/m1-engine', contract.RUNTIME_ROOT,
            contract.DOCKER_RUNTIME_ROOT, contract.DOCKER_RUNTIME_ROOT + '/extensions',
            contract.DOCKER_RUNTIME_ROOT + '/libnetwork'])

    def test_existing_private_directory_is_never_adopted_or_repaired(self):
        ops = self.operations()
        io_ops = SimpleNamespace(check=lambda: None, record=RawRecord())
        with mock.patch('guest._safe_directory'), mock.patch('guest.os.open', return_value=9), \
             mock.patch('guest.os.close'), mock.patch('guest.os.mkdir', side_effect=FileExistsError), \
             mock.patch('guest.os.chmod', side_effect=AssertionError('chmod repair')):
            with self.assertRaises(FileExistsError):
                ops._mkdir(contract.DOCKER_RUNTIME_ROOT + '/libnetwork', io_ops)

    def test_failed_unvalidated_listener_keeps_last_valid_parent_subset(self):
        ops = Operations()
        original = ops.call
        def call(name, deadline, record):
            value = original(name, deadline, record)
            if name == 'engine_ready':
                value['listeners']['socket_parents'][2]['mode'] = 0o755
            return value
        ops.call = call
        result = _run_window(ops, ops.monotonic)
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['receipt']['socket_parents'], receipt_v2()['socket_parents'][:2])
        self.assertEqual(result['receipt']['sockets'], receipt_v2()['sockets'][:2])
        validate_receipt_v2(result['receipt'])
        early = Operations('preflight')
        result = _run_window(early, early.monotonic)
        self.assertEqual(result['receipt']['socket_parents'], [])

    def test_api_failure_after_valid_listener_retains_latest_atomic_subset(self):
        for error in (ValueError('synthetic API failure'), TimeoutError('synthetic API timeout')):
            ops = Operations()
            original = ops.call
            collector = guest._GuestOperations({}, {}, ops.origin, ops.deadline, {}, {})
            collector._boundary = lambda _: None
            collector.binary_hashes = ['synthetic', 'synthetic']
            def fail_api(_):
                raise error
            collector._engine = fail_api
            def call(name, deadline, record):
                if name != 'engine_ready':
                    return original(name, deadline, record)
                with mock.patch('guest._IO', return_value=SimpleNamespace()), \
                     mock.patch('guest._collect_listeners', return_value=listeners_v2()):
                    return collector.call(name, deadline, record)
            ops.call = call
            result = _run_window(ops, ops.monotonic)
            with self.subTest(error=type(error).__name__):
                self.assertEqual(result['status'], 'unknown' if isinstance(error, TimeoutError) else 'failed')
                self.assertEqual(result['receipt']['steps'][-1]['status'], result['status'])
                self.assertEqual(result['receipt']['steps'][-1]['name'], 'engine_ready')
                for key in ('daemons', 'sockets', 'socket_parents'):
                    self.assertEqual(result['receipt'][key], listeners_v2()[key])
                self.assertIsNone(result['receipt']['engine'])
                self.assertIsNone(ops.published)
                self.assertEqual(result['error'], str(error))
                for authority in ('runtime_qualification', 'capacity_ready', 'execution_enabled', 'paid_consumed'):
                    self.assertIs(result['receipt'][authority], False)
                self.assertIn('partial-listeners', [o['kind'] for o in result['observations']])
                validate_receipt_v2(result['receipt'])

    def test_partial_listener_rejection_never_leaks_any_identity_field(self):
        for defect in ('phase', 'boot', 'parents'):
            ops = Operations()
            original = ops.call
            def call(name, deadline, record):
                if name != 'engine_ready':
                    return original(name, deadline, record)
                facts = listeners_v2()
                if defect == 'phase':
                    facts = listeners_v2('containerd_ready')
                elif defect == 'boot':
                    facts['boot_id'] = facts['current_boot_id'] = '11111111-1111-1111-1111-111111111111'
                    for values in ('daemons', 'identity_before', 'identity_after'):
                        for daemon in facts[values]:
                            daemon['boot_id'] = facts['boot_id']
                else:
                    facts['socket_parents'][4]['mode'] = 0o755
                record.partial_listeners(facts)
                raise AssertionError('invalid partial listener accepted')
            ops.call = call
            result = _run_window(ops, ops.monotonic)
            with self.subTest(defect=defect):
                self.assertEqual(result['status'], 'failed')
                if defect in ('phase', 'boot'):
                    self.assertIn('partial listener phase/boot mismatch', result['error'])
                else:
                    self.assertIn('parent', result['error'])
                for key in ('daemons', 'sockets', 'socket_parents'):
                    self.assertEqual(result['receipt'][key], listeners_v2('containerd_ready')[key])
                self.assertIsNone(result['receipt']['engine'])
                self.assertIn('partial-listeners', [o['kind'] for o in result['observations']])
                validate_receipt_v2(result['receipt'])

    def test_bundle_requires_live_v2_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            kit = tiny_kit(Path(directory).resolve())
            _load_kit(kit)
            inputs = Path(kit) / 'kit-inputs-arm64.json'
            value = json.loads(inputs.read_bytes())
            value['source'] = [r for r in value['source'] if not r['path'].endswith('/testdata/receipt-v2.json')]
            for architecture in ('arm64', 'amd64'):
                target = Path(kit) / ('kit-inputs-' + architecture + '.json')
                architecture_value = json.loads(target.read_bytes())
                architecture_value['source'] = value['source']
                target.write_bytes(encode(architecture_value))
            source = Path(kit) / 'source/deploy/experience/engine/testdata/receipt-v2.json'
            source.unlink()
            build = json.loads((Path(kit) / 'build-receipt.json').read_bytes())
            from test_contract import sha
            root_hash = sha(encode(value['source']))
            build.update(source_input_root=root_hash, before=value['source'], after=value['source'])
            (Path(kit) / 'build-receipt.json').write_bytes(encode(build))
            rewrite_manifest(kit)
            with self.assertRaisesRegex(ValueError, 'incomplete/unknown Engine source inputs'):
                _load_kit(kit)
