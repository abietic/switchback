"""Fixed guest operation tests. All sockets/processes are synthetic values."""
import copy
import hashlib
import io
import os
from pathlib import Path
import stat
import struct
import tarfile
import time
from types import SimpleNamespace
from types import MethodType
import unittest
from unittest import mock

from contract import CORE_CGROUP, FILES, NETWORKS, UNIT_NAMES, _unit
from test_contract import (dual_device_inventory, frozen_derived_inventory, listeners_v2 as listeners,
                           preflight_v2 as preflight, receipt_v2 as receipt)
from guest import (_parse_stat, _parse_unix, _parse_tcp, _network_value, _prepare_networks,
                   _collect_listeners, _GuestOperations, _DeadlineSocket, _IO)
from binaries import _ar_binary, _collect_binaries
from test_prepare import _CatalogRecord, _catalog_run


def proc_stat(pid, tick):
    fields = ['S', '1', '2', '3', '0', '0', '0'] + ['0'] * 12 + [str(tick)] + ['0'] * 8
    return ('%d (synthetic) ' % pid + ' '.join(fields)).encode()


class RawRecord:
    def __init__(self):
        self.observations, self.effects = [], []
    def raw(self, kind, value):
        self.observations.append((kind, copy.deepcopy(value)))
    def attempt(self, effect):
        self.effects.append(effect)
    def known(self, effect):
        self.effects.append(effect)


# Exact successful systemctl stdout saved from the first inactive containerd
# effective query on 2026-10-26. Source receipt:
# .scratch/runtime-m1-engine-watchdog-lifecycle-1026/captured-effective.stdout.raw
# SHA256 0aa3f160c01c1400f6716212b3d21139a6ad4b0c60644a2e5aee2de2efbe5424.
# The fixture is embedded so ordinary tests never depend on private receipts.
CAPTURED_PRESTART_WIRE = b'''Type=notify
Restart=no
TimeoutStartUSec=30s
TimeoutStopUSec=10s
RuntimeMaxUSec=infinity
WatchdogUSec=infinity
MainPID=0
ExecStart={ path=/usr/bin/containerd ; argv[]=/usr/bin/containerd --config /etc/switchback/m1-engine/containerd.toml ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }
Slice=switchback-core.slice
Delegate=yes
UMask=0077
User=root
Group=root
KillMode=control-group
Id=switchback-m1-containerd.service
Requires=sysinit.target switchback-core.slice
Requisite=
After=sysinit.target basic.target switchback-core.slice systemd-journald.socket local-fs.target
LoadState=loaded
ActiveState=inactive
FragmentPath=/etc/systemd/system/switchback-m1-containerd.service
DropInPaths=
UnitFileState=static
'''


class Kernel:
    """Raw synthetic /proc and lstat values, consumed by the real collector."""
    def __init__(self, facts=None):
        self.record = RawRecord()
        self.facts = listeners() if facts is None else copy.deepcopy(facts)
        self.files = {}
        self.links = {}
        self.process_rows = []
        for daemon in self.facts['daemons']:
            fds = []
            for row in self.facts['unix']:
                for owner in row['owners']:
                    if owner['main_pid'] == daemon['main_pid']:
                        target = 'socket:[%d]' % row['inode']
                        fds.append({'fd': owner['fd'], 'target': target, 'flags': 2,
                            'device': 0, 'inode': row['inode'], 'mount_id': 8, 'socket_inode': row['inode']})
                        self.links['/proc/%d/fd/%d' % (daemon['main_pid'], owner['fd'])] = target
            self.process_rows.append({**daemon, 'fds': fds})
            self.files['/proc/%d/stat' % daemon['main_pid']] = proc_stat(daemon['main_pid'], daemon['start_tick'])
        for daemon in self.facts['daemons']:
            prefix = '/proc/%d/' % daemon['main_pid']
            self.links[prefix + 'exe'] = daemon['exe']
            self.files[prefix + 'cgroup'] = ('0::' + daemon['cgroup'] + '\n').encode()
        for parent in self.facts['socket_parents']:
            self.files[parent['path']] = SimpleNamespace(st_dev=parent['device'], st_ino=parent['inode'],
                st_mode=stat.S_IFDIR | parent['mode'], st_uid=0, st_gid=0, st_nlink=2)
        for value in self.facts['sockets']:
            self.files[value['path']] = SimpleNamespace(st_dev=value['device'], st_ino=value['inode'],
                st_mode=stat.S_IFSOCK | value['mode'], st_uid=value['uid'], st_gid=value['gid'], st_nlink=1)
            self.files[value['parent_path']] = SimpleNamespace(st_dev=value['parent_device'], st_ino=value['parent_inode'],
                st_mode=stat.S_IFDIR | value['parent_mode'], st_uid=0, st_gid=0, st_nlink=2)
        self.files['/proc/net/unix'] = (b'Num RefCount Protocol Flags Type St Inode Path\n' + b''.join(
            ('000: 00000002 00000000 %s 0001 01 %d %s\n' %
                ('00010000' if row['listener'] else '00000000', row['inode'], row['path'])).encode()
            for row in self.facts['unix']))
        self.files['/proc/net/tcp'] = self.files['/proc/net/tcp6'] = b' sl local_address rem_address st inode\n'
    def check(self):
        pass
    def boot(self):
        return self.facts['boot_id']
    def daemon(self, index, boot, binary_hash):
        return copy.deepcopy(self.facts['daemons'][index])
    def processes(self):
        return copy.deepcopy(self.process_rows)
    def proc(self, path, maximum=None):
        return self.files[path]
    def lstat(self, path):
        return self.files[str(path)]
    def readlink(self, path):
        return self.links[path]


class WatchdogLifecycleTests(unittest.TestCase):
    """Fixed source files and systemctl wire through the real parser/adapter."""

    @staticmethod
    def files():
        root = Path(__file__).resolve().parent
        return {name: (root / name).read_bytes() for name in FILES}

    @staticmethod
    def wire(changes, source=CAPTURED_PRESTART_WIRE):
        # Only the containerd wire above is captured. All changed fields are
        # explicitly synthetic, including the derived Engine unit response.
        lines = source.decode('ascii').splitlines()
        for key, value in changes.items():
            lines = [key + '=' + value if line.startswith(key + '=') else line for line in lines]
        return ('\n'.join(lines) + '\n').encode('ascii')

    @classmethod
    def inactive_wires(cls, files):
        engine = _unit(files[UNIT_NAMES[1]])
        command = engine['Service']['ExecStart']
        return {
            UNIT_NAMES[0]: CAPTURED_PRESTART_WIRE,
            UNIT_NAMES[1]: cls.wire({
                'Id': UNIT_NAMES[1],
                'FragmentPath': FILES[UNIT_NAMES[1]][0],
                'ExecStart': ('{ path=' + command.split()[0] + ' ; argv[]=' + command
                    + ' ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }'),
                'Requisite': UNIT_NAMES[0],
                'After': 'sysinit.target basic.target switchback-core.slice systemd-journald.socket local-fs.target '
                    + UNIT_NAMES[0],
            }),
        }

    def effective(self, wires, files=None):
        files = self.files() if files is None else files
        by_target = {target: files[name] for name, (target, _) in FILES.items()}
        record = RawRecord()
        io_ops = _IO(time.monotonic() + 30, record)
        io_ops.read = lambda target, maximum, mode: (by_target[target], None)
        io_ops.command = lambda argv: wires[argv[-1]]
        ops = _GuestOperations({}, {}, 0, 120, files, {})
        return ops._effective(io_ops)

    def test_captured_inactive_wire_and_derived_engine_unit_are_accepted(self):
        self.assertEqual(hashlib.sha256(CAPTURED_PRESTART_WIRE).hexdigest(),
            '0aa3f160c01c1400f6716212b3d21139a6ad4b0c60644a2e5aee2de2efbe5424')
        files = self.files()
        self.assertEqual(self.effective(self.inactive_wires(files), files), files)

    @staticmethod
    def active_wire(unit, pid, watchdog='0'):
        text = 'Id=%s\nActiveState=active\nSubState=running\nMainPID=%d\nResult=success\n' % (unit, pid)
        if watchdog is not None:
            text += 'WatchdogUSec=%s\n' % watchdog
        return text.encode('ascii')

    def daemon(self, index, watchdog='0', changes=None):
        unit, pid = UNIT_NAMES[index], 101 + index
        binary = b'\x7fELFsynthetic-pinned-executable'
        digest = 'sha256:' + hashlib.sha256(binary).hexdigest()
        record = RawRecord()
        io_ops = _IO(time.monotonic() + 30, record)
        commands = []
        def command(argv):
            commands.append(argv)
            requested = set(argv[3].removeprefix('--property=').split(','))
            raw = self.active_wire(unit, pid, watchdog)
            if changes:
                raw = self.wire(changes, raw)
            return b''.join(line + b'\n' for line in raw.splitlines()
                            if line.split(b'=', 1)[0].decode() in requested)
        io_ops.command = command
        proc = {'/proc/%d/stat' % pid: proc_stat(pid, 500 + index),
                '/proc/%d/cgroup' % pid: ('0::' + CORE_CGROUP + '/' + unit + '\n').encode()}
        def read_proc(path, maximum=None):
            if path not in proc:
                raise ValueError('synthetic process is absent')
            return proc[path]
        io_ops.proc = read_proc
        reads = iter((binary, b''))
        metadata = SimpleNamespace(st_mode=stat.S_IFREG | 0o755, st_uid=0, st_gid=0,
                                   st_nlink=1, st_size=len(binary))
        with (mock.patch('guest.os.readlink', return_value=('/usr/bin/containerd', '/usr/bin/dockerd')[index]),
              mock.patch('guest.os.open', return_value=7), mock.patch('guest.os.fstat', return_value=metadata),
              mock.patch('guest.os.read', side_effect=lambda fd, size: next(reads)),
              mock.patch('guest.os.close')):
            result = io_ops.daemon(index, '00000000-0000-0000-0000-000000000001', digest)
        return result, commands

    def test_active_ready_requires_disabled_watchdog_through_real_daemon(self):
        for index in range(2):
            with self.subTest(unit=UNIT_NAMES[index]):
                with self.assertRaisesRegex(ValueError, 'daemon not notify-ready/running'):
                    self.daemon(index, 'infinity')
                result, commands = self.daemon(index)
                self.assertEqual(result['unit'], UNIT_NAMES[index])
                self.assertIn('WatchdogUSec', commands[0][3])

    def test_inactive_rejects_other_watchdog_values_on_both_units(self):
        files = self.files()
        good = self.inactive_wires(files)
        for unit in UNIT_NAMES:
            for value in ('0', '5000000', '', ' ', None):
                with self.subTest(unit=unit, value=value):
                    wires = dict(good)
                    wires[unit] = (good[unit].replace(b'WatchdogUSec=infinity\n', b'') if value is None
                                   else self.wire({'WatchdogUSec': value}, good[unit]))
                    with self.assertRaisesRegex(ValueError, 'effective unit lifecycle'):
                        self.effective(wires, files)

    def test_active_rejects_finite_missing_empty_and_whitespace_watchdog(self):
        for unit_index in range(2):
            for value in ('infinity', '5000000', '', ' ', None):
                with self.subTest(unit=UNIT_NAMES[unit_index], value=value):
                    with self.assertRaisesRegex(ValueError, 'daemon not notify-ready/running'):
                        self.daemon(unit_index, value)

    def test_active_state_pid_result_and_identity_drift_still_reject(self):
        for index, unit in enumerate(UNIT_NAMES):
            for changes in ({'Id': 'foreign.service'}, {'ActiveState': 'inactive'},
                            {'SubState': 'dead'}, {'MainPID': '0'},
                            {'MainPID': 'not-a-pid'}, {'Result': 'exit-code'}):
                with self.subTest(unit=unit, changes=changes):
                    message = ('synthetic process is absent' if changes == {'MainPID': '0'}
                               else 'daemon not notify-ready/running')
                    with self.assertRaisesRegex(ValueError, message):
                        self.daemon(index, changes=changes)

    def test_wrong_type_is_rejected_at_both_direct_validation_seams(self):
        files = self.files()
        by_target = {target: files[name] for name, (target, _) in FILES.items()}
        io_ops = _IO(time.monotonic() + 30, RawRecord())
        io_ops.read = lambda target, maximum, mode: (by_target[target], None)
        captured = dict(line.split('=', 1) for line in CAPTURED_PRESTART_WIRE.decode().splitlines())
        captured['WatchdogUSec'] = 0
        io_ops.show = lambda unit, properties: captured
        with self.assertRaisesRegex(ValueError, 'effective unit lifecycle'):
            _GuestOperations({}, {}, 0, 120, files, {})._effective(io_ops)
        active = dict(Id=UNIT_NAMES[0], ActiveState='active', SubState='running',
                      MainPID='101', Result='success', WatchdogUSec=0)
        io_ops.show = lambda unit, properties: active
        io_ops.proc = lambda *args: self.fail('invalid active property reached /proc')
        with self.assertRaisesRegex(ValueError, 'daemon not notify-ready/running'):
            io_ops.daemon(0, 'synthetic-boot', 'synthetic-hash')

    def test_nontext_wire_never_becomes_a_valid_property(self):
        files = self.files()
        good = self.inactive_wires(files)
        for raw in (0, {'WatchdogUSec': 'infinity'}, b'WatchdogUSec=\xff\n'):
            with self.subTest(raw=repr(raw)):
                wires = dict(good)
                wires[UNIT_NAMES[0]] = raw
                with self.assertRaises((AttributeError, UnicodeError, TypeError)):
                    self.effective(wires, files)

    def test_other_inactive_unit_drift_still_rejects(self):
        files = self.files()
        good = self.inactive_wires(files)
        for index, unit in enumerate(UNIT_NAMES):
            cases = {'FragmentPath': '/etc/systemd/system/foreign.service',
                     'DropInPaths': '/etc/systemd/system/override.conf',
                     'ExecStart': '{ path=/usr/bin/true ; argv[]=/usr/bin/true ; ignore_errors=no ; }',
                     'ActiveState': 'active', 'MainPID': '101', 'User': 'nobody',
                     'RuntimeMaxUSec': '1000000'}
            if index == 1:
                cases.update(Requisite='', After='basic.target', Requires='switchback-m1-containerd.service')
            else:
                cases['Requisite'] = UNIT_NAMES[1]
            for key, value in cases.items():
                with self.subTest(unit=unit, key=key):
                    wires = dict(good)
                    wires[unit] = self.wire({key: value}, good[unit])
                    with self.assertRaises(ValueError):
                        self.effective(wires, files)

    def test_duplicate_scalar_and_execstart_reject_at_actual_show_parser(self):
        files = self.files()
        good = self.inactive_wires(files)
        for unit in UNIT_NAMES:
            for key in ('WatchdogUSec', 'ExecStart'):
                with self.subTest(unit=unit, key=key):
                    wires = dict(good)
                    line = next(line for line in good[unit].splitlines(keepends=True)
                                if line.startswith((key + '=').encode()))
                    wires[unit] += line
                    with self.assertRaisesRegex(ValueError, 'duplicate/malformed systemd property'):
                        self.effective(wires, files)

    def test_bounded_systemctl_failures_stop_before_effective_property_parser(self):
        from prepare import _Failure
        files = self.files()
        by_target = {target: files[name] for name, (target, _) in FILES.items()}
        failures = (({'scenario': {'exit_code': 7}}, 'nonzero'),
                    ({'scenario': {'terminal_timeout_at': 1}}, 'deadline'),
                    ({'scenario': {'overflow': True}}, 'output exceeded bound'),
                    ({'boundary': {'identity_lost': True}}, 'identity lost'))
        for options, message in failures:
            with self.subTest(message=message):
                record = _CatalogRecord()
                io_ops = _IO(record.deadline, record)
                io_ops.read = lambda target, maximum, mode: (by_target[target], None)
                def runner(argv, deadline, recorder):
                    self.assertEqual(argv[:3], ['/usr/bin/systemctl', 'show', '--no-pager'])
                    self.assertEqual(argv[-1], UNIT_NAMES[0])
                    return _catalog_run(argv, recorder, CAPTURED_PRESTART_WIRE,
                                        scenario=options.get('scenario'), **options.get('boundary', {}))
                with mock.patch('guest._bounded_run', side_effect=runner):
                    with self.assertRaisesRegex(_Failure, message):
                        _GuestOperations({}, {}, 0, 120, files, {})._effective(io_ops)
                self.assertEqual(len(record.results()), 1)

    def test_invalid_engine_watchdog_stops_listener_and_request_before_connection(self):
        # Kernel supplies only synthetic proc/listener tables. Its daemon stub
        # is replaced with the real _IO.daemon, including the pinned ELF proof.
        for entry in ('collector', 'request'):
            with self.subTest(entry=entry):
                kernel = Kernel()
                binary = b'\x7fELFsynthetic-pinned-executable'
                digest = 'sha256:' + hashlib.sha256(binary).hexdigest()
                first, second = (item['main_pid'] for item in kernel.facts['daemons'])
                for index, pid in enumerate((first, second)):
                    unit = UNIT_NAMES[index]
                    kernel.files['/proc/%d/cgroup' % pid] = ('0::' + CORE_CGROUP + '/' + unit + '\n').encode()
                kernel.daemon = MethodType(_IO.daemon, kernel)
                kernel.show = MethodType(_IO.show, kernel)
                seen = []
                def command(argv):
                    seen.append(argv[-1])
                    index = UNIT_NAMES.index(argv[-1])
                    pid = (first, second)[index]
                    watchdog = '0' if index == 0 else 'infinity'
                    requested = set(argv[3].removeprefix('--property=').split(','))
                    return b''.join(line + b'\n' for line in self.active_wire(argv[-1], pid, watchdog).splitlines()
                                    if line.split(b'=', 1)[0].decode() in requested)
                kernel.command = command
                reads = iter((binary, b''))
                metadata = SimpleNamespace(st_mode=stat.S_IFREG | 0o755, st_uid=0, st_gid=0,
                                           st_nlink=1, st_size=len(binary))
                def readlink(path):
                    if path == '/proc/%d/exe' % first:
                        return '/usr/bin/containerd'
                    return kernel.links[path]
                ops = _GuestOperations({}, {}, 0, 120, {}, {})
                ops._boundary = lambda _: None
                ops.binary_hashes = [digest, digest]
                with (mock.patch('guest.os.readlink', side_effect=readlink),
                      mock.patch('guest.os.open', return_value=7),
                      mock.patch('guest.os.fstat', return_value=metadata),
                      mock.patch('guest.os.read', side_effect=lambda fd, size: next(reads)),
                      mock.patch('guest.os.close'),
                      mock.patch('guest.socket.socket', side_effect=AssertionError('unexpected socket')) as sockets,
                      mock.patch('guest.http.client.HTTPConnection',
                                 side_effect=AssertionError('unexpected HTTP connection')) as http):
                    with self.assertRaisesRegex(ValueError, 'daemon not notify-ready/running'):
                        if entry == 'collector':
                            _collect_listeners(kernel, 'engine_ready', [digest, digest])
                        else:
                            ops._request(kernel, 'GET', '/info')
                    self.assertEqual(sockets.call_count, 0)
                    self.assertEqual(http.call_count, 0)
                self.assertEqual(seen, list(UNIT_NAMES))


class CollectionTests(unittest.TestCase):
    def test_preflight_and_claim_bind_scans_to_same_filesystem(self):
        expected = preflight()
        expected['store'] = dual_device_inventory()
        expected['store']['entries'].extend(copy.deepcopy(entry)
            for entry in frozen_derived_inventory()['entries']
            if entry['path'].startswith(expected['store']['root'] + '/plugins'))
        calls = []
        def scan(**kwargs):
            calls.append(kwargs['expected_filesystem'])
            return copy.deepcopy(expected['store'])
        values = {'cpu.max': b'120000 100000', 'memory.max': b'3221225472',
                  'memory.swap.max': b'0', 'pids.max': b'1024'}
        record = RawRecord()
        io_ops = SimpleNamespace(record=record, check=lambda: None,
            boot=lambda: expected['boot_id'], processes=lambda: [],
            proc=lambda path, *args: values.get(path.rsplit('/', 1)[-1], b''))
        ops = _GuestOperations({}, expected['vm'], 0, 120, {}, {})
        stat_value = SimpleNamespace(st_dev=2065, st_ino=42, st_mode=stat.S_IFDIR | 0o700)
        with (mock.patch('guest.os.geteuid', return_value=0),
              mock.patch('guest.sys.platform', 'linux'),
              mock.patch('guest._filesystem', return_value=(expected['filesystem'], expected['runtime_parent'])),
              mock.patch('guest._scan_store', side_effect=scan),
              mock.patch('guest._collect_listeners_with_inventory', return_value=(expected['listeners'], [])),
              mock.patch('guest._unit_catalog', return_value=(expected['masks'], [])),
              mock.patch('guest._collect_binaries', return_value=expected['binaries']),
              mock.patch('guest._mounts', return_value=[{'mountpoint': '/sys/fs/cgroup',
                  'fs_type': 'cgroup2', 'root': '/'}]),
              mock.patch('guest._safe_directory'),
              mock.patch('guest.os.lstat', return_value=stat_value),
              mock.patch('guest.os.path.lexists', return_value=False)):
            before = ops._preflight(io_ops)
        self.assertEqual(before['store']['schema'], 'switchback.engine.store_inventory.v3')
        self.assertEqual(calls, [expected['filesystem']])
        with (mock.patch('guest._IO', return_value=io_ops),
              mock.patch('guest._filesystem', return_value=(expected['filesystem'], expected['runtime_parent'])),
              mock.patch('guest._scan_store', side_effect=scan),
              mock.patch('guest._collect_listeners', return_value=expected['listeners']),
              mock.patch.object(ops, '_mkdir', side_effect=RuntimeError('first mutation reached'))):
            with self.assertRaisesRegex(RuntimeError, 'first mutation reached'):
                ops.call('claim_stage', 120, record)
        self.assertEqual(calls, [expected['filesystem'], expected['filesystem']])

    def test_real_collector_derives_fd_and_distinct_filesystem_kernel_inodes(self):
        kernel = Kernel()
        with mock.patch('guest.os.lstat', side_effect=kernel.lstat), mock.patch('guest.os.readlink', side_effect=kernel.readlink):
            actual = _collect_listeners(kernel, 'engine_ready', ['synthetic', 'synthetic'])
        self.assertEqual(actual['sockets'], kernel.facts['sockets'])
        self.assertNotEqual(actual['sockets'][0]['inode'], actual['sockets'][0]['kernel_inode'])
        self.assertTrue(kernel.record.observations)

    def test_owner_uncertainty_has_zero_socket_construction_or_connection(self):
        for mutation in ('owner', 'process', 'permission', 'fd-drift', 'tcp'):
            with self.subTest(mutation=mutation):
                kernel = Kernel()
                if mutation == 'owner':
                    kernel.process_rows[0]['fds'] = []
                elif mutation == 'process':
                    kernel.process_rows.append({'main_pid': 999, 'start_tick': 999, 'exe': '/usr/bin/containerd-shim-runc-v2',
                        'cgroup': '/foreign.scope', 'fds': []})
                elif mutation == 'permission':
                    kernel.files[kernel.facts['sockets'][2]['path']].st_uid = 1000
                elif mutation == 'fd-drift':
                    kernel.links[next(k for k in kernel.links if '/fd/' in k)] = 'socket:[9999]'
                else:
                    kernel.files['/proc/net/tcp'] += (b'0: 00000000:093B 00000000:0000 0A '
                        b'00000000:00000000 00:00000000 00000000 0 0 9999 1\n')
                    kernel.process_rows[0]['fds'].append({'fd': 99, 'target': 'socket:[9999]', 'flags': 2,
                        'device': 0, 'inode': 9999, 'mount_id': 8, 'socket_inode': 9999})
                    kernel.links['/proc/101/fd/99'] = 'socket:[9999]'
                ops = _GuestOperations({}, {}, 0, 120, {}, {})
                ops._boundary = lambda _: None
                ops.binary_hashes = ['synthetic', 'synthetic']
                with (mock.patch('guest.os.lstat', side_effect=kernel.lstat),
                      mock.patch('guest.os.readlink', side_effect=kernel.readlink),
                      mock.patch('guest.socket.socket', side_effect=AssertionError('unexpected socket')) as sockets,
                      mock.patch('guest.os.path.lexists', return_value=False)):
                    with self.assertRaises(ValueError):
                        ops._request(kernel, 'GET', '/info')
                    self.assertEqual(sockets.call_count, 0)

    def test_guest_has_no_public_cli_or_dynamic_path_entry(self):
        import guest
        import prepare
        with (mock.patch('subprocess.Popen', side_effect=AssertionError('process')),
              mock.patch('socket.socket', side_effect=AssertionError('socket'))):
            result = prepare._self_check()
        self.assertFalse(result['execution_enabled'])
        self.assertFalse(hasattr(guest, 'main'))


class DeadlineAndArchiveTests(unittest.TestCase):
    def test_process_catalog_stops_during_slow_directory_enumeration(self):
        import guest
        from prepare import _Failure
        clock, visited = [0], []
        class SlowProcesses:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def __iter__(self): return self
            def __next__(self):
                visited.append('123'); clock[0] += 121
                return SimpleNamespace(name='123')
        def guard(deadline):
            if clock[0] >= deadline: raise _Failure('original deadline during process catalog')
        with (mock.patch.object(guest.os, 'scandir', return_value=SlowProcesses()),
              mock.patch.object(guest, '_guard', side_effect=guard),
              mock.patch.object(guest.os, 'open', side_effect=AssertionError('no process payload read'))):
            with self.assertRaisesRegex(_Failure, 'process catalog'):
                guest._IO(120, RawRecord()).processes()
        self.assertEqual(visited, ['123'])

    def test_delayed_invalid_or_expired_clock_control_performs_no_input_or_mutation_io(self):
        import guest
        from prepare import _encode
        binding = {key: receipt()[key] for key in ('bundle_manifest_sha256', 'kit_manifest_sha256', 'source_root', 'templates_root')}
        valid = {'schema': 'switchback.engine.clock_control.v1', 'sample_ns': 1000000000,
            'deadline_ns': 111000000000, 'binding': binding,
            'vm': {'name': 'switchback-core', 'vcpus': 4, 'memory_bytes': 10737418240}}
        for mutation in ('delayed-valid', 'expired', 'extended', 'duplicate', 'partial', 'bad-binding'):
            with self.subTest(mutation=mutation):
                clock, control = [6.0], copy.deepcopy(valid)
                if mutation == 'expired': clock[0] = 112.0
                elif mutation == 'extended': control['deadline_ns'] = 122000000000
                elif mutation == 'bad-binding': control['binding']['source_root'] = 'untrusted'
                raw = _encode(control) + b'\n'
                if mutation == 'duplicate': raw += raw
                elif mutation == 'partial': raw = raw[:-1]
                selector = mock.Mock(); selector.select.return_value = [(None, None)]
                with (mock.patch.object(guest.selectors, 'DefaultSelector', return_value=selector),
                      mock.patch.object(guest.sys, 'stdin', SimpleNamespace(fileno=lambda: 99)),
                      mock.patch.object(guest.os, 'set_blocking'),
                      mock.patch.object(guest.os, 'read', side_effect=[raw, b'']),
                      mock.patch.object(guest.time, 'monotonic', side_effect=lambda: clock[0]),
                      mock.patch.object(guest.time, 'monotonic_ns', side_effect=lambda: int(clock[0] * 1000000000)),
                      mock.patch.object(guest, '_guard'),
                      mock.patch.object(guest, '_read', side_effect=AssertionError('no input reads during control')) as reads,
                      mock.patch.object(guest, '_GuestOperations', side_effect=AssertionError('no operations')) as operations,
                      mock.patch('subprocess.Popen', side_effect=AssertionError('no subprocess')),
                      mock.patch('socket.socket', side_effect=AssertionError('no socket'))):
                    if mutation == 'delayed-valid':
                        actual = guest._read_control(1000000000, 1000000000)
                        self.assertEqual(actual['deadline_ns'], 111000000000)
                        self.assertEqual(actual['deadline_ns'] - int(clock[0] * 1000000000), 105000000000)
                    else:
                        with self.assertRaises(ValueError):
                            guest._read_control(1000000000, 1000000000)
                    self.assertEqual(reads.call_count + operations.call_count, 0)

    def test_binary_pin_proof_precedes_daemon_command_and_rejection_never_executes_it(self):
        from contract import PACKAGE_PINS
        from prepare import _sha
        executable = '/usr/bin/containerd'
        package, package_version, version, _ = PACKAGE_PINS[executable]
        installed = bytearray(64); installed[:6] = b'\x7fELF\x02\x01'
        struct.pack_into('<H', installed, 18, 183)
        installed = bytes(installed)
        def archive(payload):
            data = io.BytesIO()
            with tarfile.open(fileobj=data, mode='w:gz') as tar:
                value = tarfile.TarInfo('./usr/bin/containerd')
                value.size, value.mode, value.uid, value.gid = len(payload), 0o755, 0, 0
                tar.addfile(value, io.BytesIO(payload))
            def member(name, value):
                header = ('%-16s%-12s%-6s%-6s%-8s%-10d`\n' % (name, '0', '0', '0', '100644', len(value))).encode()
                return header + value + (b'\n' if len(value) % 2 else b'')
            return (b'!<arch>\n' + member('debian-binary', b'2.0\n')
                + member('control.tar.gz', b'synthetic') + member('data.tar.gz', data.getvalue()))
        for failure in (None, 'missing', 'archive-hash', 'installed-bytes'):
            with self.subTest(failure=failure):
                events = []
                retained = archive(installed if failure != 'installed-bytes' else installed + b'drift')
                pinned = _sha(retained) if failure != 'archive-hash' else 'sha256:' + '0' * 64
                class BinaryIO:
                    record = RawRecord()
                    def check(self): pass
                    def read(self, path, maximum, mode=None):
                        events.append(('read', str(path)))
                        if str(path) == executable:
                            return installed, SimpleNamespace(st_uid=0, st_gid=0, st_nlink=1,
                                st_mode=stat.S_IFREG | 0o755)
                        if failure == 'missing': raise FileNotFoundError('retained archive missing')
                        return retained, SimpleNamespace()
                    def command(self, argv):
                        events.append(('command', argv[0]))
                        if argv[1] == '--show':
                            return ('\t'.join((package, package_version, 'arm64', 'installed')) + '\n').encode()
                        if argv[1] == '--search': return (package + ': ' + executable + '\n').encode()
                        return ('containerd ' + version).encode()
                with mock.patch('binaries.PACKAGE_PINS', {executable: (package, package_version, version, pinned)}):
                    if failure:
                        with self.assertRaises((ValueError, FileNotFoundError)):
                            _collect_binaries(BinaryIO())
                        self.assertNotIn(('command', executable), events)
                    else:
                        self.assertTrue(_collect_binaries(BinaryIO())[0]['pin_verified'])
                        archive_read = next(i for i, (kind, path) in enumerate(events)
                                            if kind == 'read' and path != executable)
                        self.assertLess(archive_read, events.index(('command', executable)))

    def test_slow_http_peer_cannot_extend_absolute_deadline(self):
        clock, timeouts = [0.0], []
        class SlowPeer:
            def settimeout(self, value):
                timeouts.append(value)
            def recv_into(self, view):
                clock[0] += 60
                view[0] = 65
                return 1
            def close(self):
                pass
        connection = _DeadlineSocket(SlowPeer(), 120, lambda: clock[0])
        stream = connection.makefile('rb')
        self.assertEqual(stream.read(1), b'A')
        self.assertEqual(stream.read(1), b'A')
        with self.assertRaises(RuntimeError):
            stream.read(1)
        self.assertEqual(timeouts, [120, 60])
        stream.close(); connection.close()

    def test_debian_reader_enumerates_archive_without_extracting_or_executing(self):
        data = io.BytesIO()
        payload = b'\x7fELF\x02\x01' + b'synthetic only'
        with tarfile.open(fileobj=data, mode='w:gz') as archive:
            value = tarfile.TarInfo('./usr/bin/dockerd')
            value.size, value.mode, value.uid, value.gid = len(payload), 0o755, 0, 0
            archive.addfile(value, io.BytesIO(payload))
        def ar_member(name, value):
            header = ('%-16s%-12s%-6s%-6s%-8s%-10d`\n' % (name, '0', '0', '0', '100644', len(value))).encode()
            return header + value + (b'\n' if len(value) % 2 else b'')
        raw = b'!<arch>\n' + ar_member('debian-binary', b'2.0\n') + ar_member('control.tar.gz', b'synthetic') + ar_member('data.tar.gz', data.getvalue())
        with mock.patch('subprocess.Popen', side_effect=AssertionError('process')):
            self.assertEqual(_ar_binary(raw, '/usr/bin/dockerd', lambda: None), payload)
        with self.assertRaises(ValueError):
            _ar_binary(raw[:-20], '/usr/bin/dockerd', lambda: None)
        with self.assertRaises(ValueError):
            _ar_binary(raw, '/usr/bin/containerd', lambda: None)


class NetworkTests(unittest.TestCase):
    def test_unknown_first_network_create_has_no_retry_or_second_create(self):
        calls = []
        def api(method, path, body=None):
            calls.append((method, path))
            if method == 'GET':
                return 404, b'{"message":"absent"}'
            raise TimeoutError('NetworkCreate response unknown')
        with self.assertRaises(TimeoutError):
            _prepare_networks(api)
        self.assertEqual(calls, [('GET', '/networks/' + NETWORKS[0][0]),
                                 ('POST', '/networks/create')])

    def test_foreign_existing_network_is_never_recreated(self):
        calls = []
        def api(method, path, body=None):
            calls.append((method, path))
            return 200, {'Name': NETWORKS[0][0], 'Labels': {'foreign': 'owner'}}
        with self.assertRaises(ValueError):
            _prepare_networks(api)
        self.assertEqual(len(calls), 1)

    def test_raw_network_must_reject_attached_container_before_dto_projection(self):
        n = receipt()['networks'][0]
        value = {'Name': n['name'], 'Id': n['id'], 'Driver': n['driver'], 'Scope': n['scope'],
            'Internal': n['internal'], 'Labels': n['labels'], 'EnableIPv6': False,
            'Attachable': False, 'Ingress': False, 'Options': {}, 'Containers': {'hidden': {}},
            'IPAM': {'Driver': 'default', 'Options': {}, 'Config': [
                {'Subnet': '172.30.0.0/24', 'Gateway': '172.30.0.1'}]}}
        with self.assertRaises(ValueError):
            _network_value(value, 0)


class ProcParsingTests(unittest.TestCase):
    @staticmethod
    def _collect_raw_process(pid, before_raw, after_raw, no_exe=False):
        record = RawRecord()
        io_ops = _IO(time.monotonic() + 120, record)
        stat_reads = iter((before_raw, after_raw))

        class ProcEntry:
            def __init__(self, name): self.name = str(name)

        class Scan:
            def __init__(self, entries): self.entries = entries
            def __enter__(self): return iter(self.entries)
            def __exit__(self, *args): pass
            def __iter__(self): return iter(self.entries)
            def close(self): pass

        def scandir(path):
            if path == '/proc':
                return Scan([ProcEntry(pid)])
            if path == '/proc/%d/fd' % pid: return Scan([])
            raise AssertionError('unexpected scandir path: ' + path)

        def proc(path, maximum=None):
            if path == '/proc/%d/stat' % pid:
                raw = next(stat_reads)
            elif path == '/proc/%d/cgroup' % pid: raw = b'0::/user.slice/user-501.slice/session-4.scope\n'
            elif path == '/proc/%d/cmdline' % pid: raw = b'sshd: yuhaichuan@notty\x00'
            else: raise AssertionError('unexpected proc path: ' + path)
            record.raw('proc-file', {'path': path, 'raw': raw})
            return raw

        def readlink(path):
            if path == '/proc/%d/exe' % pid:
                if no_exe: raise FileNotFoundError(path)
                return '/usr/sbin/sshd'
            raise AssertionError('unexpected readlink path: ' + path)

        with (mock.patch.object(io_ops, 'proc', side_effect=proc),
              mock.patch('guest.os.scandir', side_effect=scandir),
              mock.patch('guest._FDDirectory', side_effect=lambda path, check: scandir(path)),
              mock.patch('guest.os.readlink', side_effect=readlink)):
            value = io_ops.processes()
        return value, record

    @staticmethod
    def _stat_bytes(pid, state, tick, flags=0):
        fields = [state, '1', '2', '3', '0', '0', str(flags)] + ['0'] * 12 + [str(tick)] + ['0'] * 8
        return ('%d (synthetic) ' % pid + ' '.join(fields) + '\n').encode()

    def test_captured_real_stat_pair_through_process_collector(self):
        # Exact /proc/1348/stat bytes from Engine guest frames 1918 and 1931.
        before_raw = (b'1348 (sshd) S 1298 1298 1298 0 -1 4194624 782597 5211397 0 31 98 154 '
            b'2107 977 20 0 1 0 544 17178624 1640 18446744073709551615 209421023182848 '
            b'209421024058676 281474698677824 0 0 0 0 4096 65536 1 0 0 17 1 0 0 0 0 0 '
            b'209421024151968 209421024167368 209421214420992 281474698681879 '
            b'281474698681900 281474698681900 281474698682345 0\n')
        after_raw = (b'1348 (sshd) R 1298 1298 1298 0 -1 4194624 782597 5211397 0 31 98 154 '
            b'2107 977 20 0 1 0 544 17178624 1640 18446744073709551615 209421023182848 '
            b'209421024058676 281474698677824 0 0 0 0 4096 65536 1 0 0 17 1 0 0 0 0 0 '
            b'209421024151968 209421024167368 209421214420992 281474698681879 '
            b'281474698681900 281474698681900 281474698682345 0\n')
        cmdline_raw = b'sshd: yuhaichuan@notty\x00'
        processes, record = self._collect_raw_process(1348, before_raw, after_raw)
        self.assertEqual(len(processes), 1)
        self.assertEqual(processes[0]['state'], 'S')
        self.assertEqual(processes[0]['start_tick'], 544)
        self.assertEqual(processes[0]['flags'], 4194624)
        self.assertEqual(processes[0]['cgroup'], '/user.slice/user-501.slice/session-4.scope')
        self.assertEqual(processes[0]['cmdline'], cmdline_raw)
        observed_stats = [value['raw'] for kind, value in record.observations
                          if kind == 'proc-file' and value['path'] == '/proc/1348/stat']
        self.assertEqual(observed_stats, [before_raw, after_raw])

    def test_process_collector_allows_only_stable_identity_and_same_or_r_s_state(self):
        base = self._stat_bytes(1348, 'S', 544, 4194624)
        accepted = (
            ('R-to-S', self._stat_bytes(1348, 'R', 544, 4194624), self._stat_bytes(1348, 'S', 544, 4194624)),
            ('S-to-R', self._stat_bytes(1348, 'S', 544, 4194624), self._stat_bytes(1348, 'R', 544, 4194624)),
            ('same-state', base, base),
            ('same-unknown-state', self._stat_bytes(1348, '?', 544, 4194624),
             self._stat_bytes(1348, '?', 544, 4194624)),
        )
        for name, before, after in accepted:
            with self.subTest(name=name):
                processes, _ = self._collect_raw_process(1348, before, after)
                self.assertEqual(len(processes), 1)

        rejected = (
            ('start-tick', base, self._stat_bytes(1348, 'S', 545, 4194624)),
            ('flags-kthread-bit', base, self._stat_bytes(1348, 'S', 544, 4194624 | 0x00200000)),
            ('flags-other-bit', base, self._stat_bytes(1348, 'S', 544, 4194624 ^ 0x100)),
            ('r-to-z', self._stat_bytes(1348, 'R', 544, 4194624), self._stat_bytes(1348, 'Z', 544, 4194624)),
            ('z-to-r', self._stat_bytes(1348, 'Z', 544, 4194624), self._stat_bytes(1348, 'R', 544, 4194624)),
            ('s-to-x', self._stat_bytes(1348, 'S', 544, 4194624), self._stat_bytes(1348, 'X', 544, 4194624)),
            ('x-to-s', self._stat_bytes(1348, 'X', 544, 4194624), self._stat_bytes(1348, 'S', 544, 4194624)),
            ('r-to-other', self._stat_bytes(1348, 'R', 544, 4194624), self._stat_bytes(1348, 'D', 544, 4194624)),
            ('other-to-r', self._stat_bytes(1348, 'D', 544, 4194624), self._stat_bytes(1348, 'R', 544, 4194624)),
            ('s-to-unknown', self._stat_bytes(1348, 'S', 544, 4194624), self._stat_bytes(1348, '?', 544, 4194624)),
            ('unknown-to-s', self._stat_bytes(1348, '?', 544, 4194624), self._stat_bytes(1348, 'S', 544, 4194624)),
        )
        for name, before, after in rejected:
            with self.subTest(name=name):
                with self.assertRaisesRegex(ValueError, 'process identity drift during FD enumeration'):
                    self._collect_raw_process(1348, before, after)

        # A proc stat PID is checked by _parse_stat against the directory name,
        # so a changed PID cannot occur in ordinary kernel bytes at one path.
        # Mutate only the parsed second observation to exercise the collector's
        # identity decision while keeping the real parser on both raw frames.
        import guest
        parse_stat = guest._parse_stat
        calls = [0]
        def changed_pid(raw, pid):
            value = parse_stat(raw, pid)
            calls[0] += 1
            if calls[0] == 2: value['main_pid'] += 1
            return value
        with mock.patch('guest._parse_stat', side_effect=changed_pid):
            with self.assertRaisesRegex(ValueError, 'process identity drift during FD enumeration'):
                self._collect_raw_process(1348, base, base)

    def test_process_collector_keeps_pid1_and_kernel_thread_zero_tick_exceptions(self):
        for name, pid, flags, no_exe in (
            ('pid1', 1, 0, False),
            ('kernel-thread', 1348, 0x00200000, True),
        ):
            raw = self._stat_bytes(pid, 'S', 0, flags)
            with self.subTest(name=name):
                processes, _ = self._collect_raw_process(pid, raw, raw, no_exe=no_exe)
                self.assertEqual(processes[0]['start_tick'], 0)

    def test_stat_comm_spaces_and_right_parentheses_preserve_start_tick(self):
        fields = ['S', '1', '2', '3', '0', '0', '0'] + ['0'] * 12 + ['12345'] + ['0'] * 8
        value = _parse_stat(('123 (name ) with spaces) ' + ' '.join(fields)).encode(), 123)
        self.assertEqual(value['start_tick'], 12345)
        self.assertEqual(value['main_pid'], 123)
        with self.assertRaises(ValueError):
            _parse_stat(b'123 (truncated) S 1', 123)

    def test_complete_unix_table_keeps_nonlisteners_and_abstract_names(self):
        raw = (b'Num RefCount Protocol Flags Type St Inode Path\n'
               b'000: 00000002 00000000 00010000 0001 01 301 /run/a.sock\n'
               b'001: 00000002 00000000 00000000 0002 01 302 @abstract\n')
        rows = _parse_unix(raw)
        self.assertEqual([r['listener'] for r in rows], [True, False])
        self.assertEqual(rows[1]['path'], '@abstract')
        with self.assertRaises(ValueError):
            _parse_unix(raw + b'bad truncated\n')

    def test_tcp_parser_validates_all_rows_and_emits_only_listeners(self):
        raw = (b'  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n'
            b' 0: 0100007F:093B 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 400 1\n'
            b' 1: 0100007F:093B 0100007F:1234 01 00000000:00000000 00:00000000 00000000 0 0 401 1\n')
        rows = _parse_tcp(raw, False)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]['port'], 2363)
        with self.assertRaises(ValueError):
            _parse_tcp(raw + b'bad\n', False)


if __name__ == '__main__':
    unittest.main()
