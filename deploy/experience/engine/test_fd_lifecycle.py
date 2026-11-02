"""Offline tests of the actual process and listener collection boundaries."""
import copy
import ctypes
import struct
import errno
import tempfile
import json
from pathlib import Path
import os
import stat
import unittest
from types import SimpleNamespace
from unittest import mock

import guest
from test_guest import Kernel, proc_stat


class Entries:
    def __init__(self, entries): self.entries = entries
    def __enter__(self): return iter(self.entries)
    def __exit__(self, *args): return False
    def __iter__(self): return iter(self.entries)
    def close(self): pass


class DirectoryAPI:
    """Lifecycle-faithful libc boundary fixture; production iterator is real.

    It emits synthetic Linux64 dirent bytes and explicitly owns one handle
    until closedir, including EOF. Real-FD tests override open/close below.
    """
    def __init__(self, scan):
        self.scan, self.active, self.handle = scan, {}, 10000

    def opendir(self, path):
        self.handle += 1
        entries = self.scan(os.fsdecode(path))
        self.active[self.handle] = (iter(entries), entries)
        return self.handle

    def readdir64(self, handle):
        try:
            entry = next(self.active[handle][0])
        except StopIteration:
            return 0
        name = entry.name.encode('ascii') + b'\0'
        self.buffer = ctypes.create_string_buffer(struct.pack('=QqHB', 1, 0, 19 + len(name), 0) + name)
        return ctypes.addressof(self.buffer)

    def closedir(self, handle):
        entries = self.active.pop(handle)[1]
        entries.close()
        return 0


class Collector(Kernel):
    processes = guest._IO.processes
    def __init__(self, failure_point="source-fdinfo"):
        super().__init__()
        self.failure_point = failure_point
        self.collection_attempts = []
        self.close_once = True
        self.pins = {}
        self.next_pin = 900
        self.side_effect_calls = []
        for process in self.process_rows:
            pid = process['main_pid']
            self.files['/proc/%d/cmdline' % pid] = b'synthetic\0'
            for fd in process['fds']:
                self.files['/proc/%d/fdinfo/%d' % (pid, fd['fd'])] = (
                    'pos:\t0\nflags:\t02\nmnt_id:\t8\nino:\t%d\n' % fd['inode']).encode()
    def _pids(self): return sorted(p['main_pid'] for p in self.process_rows)
    def proc(self, path, maximum=None):
        if path.startswith('/proc/self/fdinfo/'):
            info = self.pins[int(path.rsplit('/', 1)[1])]
            return ('flags:\t010000000\nmnt_id:\t8\nino:\t%d\n' % info['inode']).encode()
        return self.files[path]
    def _source_fdinfo(self, pid, fd):
        from fd_lifecycle import SourceVanished
        path = '/proc/%d/fdinfo/%d' % (pid, fd)
        if self.failure_point == 'source-fdinfo' and self.close_once:
            self.close_once = False
            raise SourceVanished('source-fdinfo-open', FileNotFoundError(2, 'closed source FD', path))
        return self.files[path]
    def open(self, path, flags):
        self.fail('pin-open')
        pid, number = int(path.split('/')[2]), int(path.split('/')[-1])
        process = next(p for p in self.process_rows if p['main_pid'] == pid)
        descriptor = next(d for d in process['fds'] if d['fd'] == number)
        self.next_pin += 1
        self.pins[self.next_pin] = descriptor
        return self.next_pin
    def stat(self, path):
        self.fail('source-stat')
        target = self.links[path]
        inode = int(target.split('[')[1][:-1])
        return SimpleNamespace(st_dev=0, st_ino=inode, st_mode=stat.S_IFSOCK)
    def fstat(self, pin):
        info = self.pins[pin]
        return SimpleNamespace(st_dev=0, st_ino=info['inode'], st_mode=stat.S_IFSOCK)
    def readlink(self, path):
        if path.startswith('/proc/self/fd/'):
            return self.pins[int(path.rsplit('/', 1)[1])]['target']
        if '/fd/' in path:
            self.fail('source-readlink')
        return self.links[path]
    def fail(self, point):
        if self.failure_point == point and self.close_once:
            self.close_once = False
            raise FileNotFoundError(2, 'closed source FD')
    def close(self, pin):
        del self.pins[pin]
    def scandir(self, path):
        pid = int(path.split('/')[2])
        process = next(p for p in self.process_rows if p['main_pid'] == pid)
        entries = []
        for fd in process['fds']:
            metadata = SimpleNamespace(st_dev=0, st_ino=fd['inode'], st_mode=stat.S_IFSOCK)
            entries.append(SimpleNamespace(name=str(fd['fd']), path=path+'/'+str(fd['fd']),
                                           stat=lambda follow_symlinks=True, m=metadata: m))
        return Entries(entries)


class LifecycleTest(unittest.TestCase):
    def collect(self, io_ops, store_inodes=None):
        api = getattr(io_ops, 'directory_api', None) or DirectoryAPI(io_ops.scandir)
        with (mock.patch('fd_lifecycle._linux_directory_api', return_value=api),
              mock.patch.object(guest.os, 'lstat', side_effect=io_ops.lstat),
              mock.patch.object(guest.os, 'readlink', side_effect=io_ops.readlink),
              mock.patch.object(guest.os, 'scandir', side_effect=io_ops.scandir),
              mock.patch.object(guest.os, 'open', side_effect=io_ops.open),
              mock.patch.object(guest.os, 'stat', side_effect=io_ops.stat),
              mock.patch.object(guest.os, 'fstat', side_effect=io_ops.fstat),
              mock.patch.object(guest.os, 'close', side_effect=io_ops.close),
              mock.patch.object(guest.os.path, 'lexists', return_value=False)):
            return guest._collect_listeners_with_inventory(io_ops, 'engine_ready',
                ['synthetic', 'synthetic'], store_inodes)[0]

    def test_closing_fd_requires_a_second_complete_collection(self):
        for point in ('pin-open', 'source-readlink', 'source-fdinfo', 'source-stat'):
            with self.subTest(point=point):
                io_ops = Collector(point)
                result = self.collect(io_ops)
                self.assertEqual(result['complete'], dict.fromkeys(
                    ('unix', 'tcp', 'tcp6', 'fd_owners', 'processes'), True))
                self.assertEqual([value['attempt'] for kind, value in io_ops.record.observations
                                  if kind == 'listener-collection-attempt-start'], [1, 2])
                self.assertEqual(io_ops.side_effect_calls, [])
                self.assertFalse(io_ops.pins)

    def test_unowned_complete_rows_are_terminal_even_with_closing_race(self):
        from fd_lifecycle import SourceVanished
        extras = {
            'tcp': b'0: 0100007F:1770 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 99999 1\n',
            'unix': b'0000000000000000: 00000002 00000000 00010000 0001 01 99999 /tmp/ordinary.sock\n',
        }
        for family, extra in extras.items():
            for sample in ('initial', 'closing'):
                with self.subTest(family=family, sample=sample):
                    io_ops = Collector(None)
                    path = '/proc/net/' + family
                    if sample == 'initial':
                        io_ops.files[path] += extra
                    else:
                        original_proc, calls = io_ops.proc, [0]
                        def proc(name, maximum=None):
                            value = original_proc(name, maximum)
                            if name == path:
                                calls[0] += 1
                                if calls[0] == 2: return value + extra
                            return value
                        io_ops.proc = proc
                    original_source, count = io_ops._source_fdinfo, [0]
                    def source(pid, fd):
                        count[0] += 1
                        full = 2 * sum(len(p['fds']) for p in io_ops.process_rows)
                        if sample == 'initial' and count[0] == full + 1:
                            raise SourceVanished('source-fdinfo-open', FileNotFoundError(2, 'closing race'))
                        return original_source(pid, fd)
                    io_ops._source_fdinfo = source
                    original_record = io_ops.record.raw
                    def record(kind, value):
                        if kind == 'listener-collection-attempt-start' and value['attempt'] == 2:
                            io_ops.files[path] = io_ops.files[path].replace(extra, b'')
                        original_record(kind, value)
                    io_ops.record.raw = record
                    with self.assertRaisesRegex(ValueError, 'unowned/unreadable listener'):
                        self.collect(io_ops)
                    self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                                      if k == 'listener-collection-attempt-start'], [1])
                    self.assertEqual(io_ops.side_effect_calls, [])

    def test_invalid_source_targets_are_terminal_on_both_reads(self):
        for read in (1, 2):
            for invalid in ('/tmp/invalid-\udcff', '/tmp/invalid\0target', 'x' * 4097, b'bytes'):
                with self.subTest(read=read, invalid=repr(invalid)):
                    io_ops = Collector(None)
                    original, count = io_ops.readlink, [0]
                    def link(path):
                        value = original(path)
                        if '/fd/' in path and not path.startswith('/proc/self/'):
                            count[0] += 1
                            if count[0] == read: return invalid
                        return value
                    io_ops.readlink = link
                    with self.assertRaises((ValueError, UnicodeError, TypeError)):
                        self.collect(io_ops)
                    self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                                      if k == 'listener-collection-attempt-start'], [1])
                    self.assertEqual(io_ops.side_effect_calls, [])

    def test_inode_alias_writer_is_latched_before_retry(self):
        io_ops = Collector('source-fdinfo')
        original = io_ops._source_fdinfo
        count = [0]
        def second(pid, fd):
            count[0] += 1
            if count[0] <= 2:
                io_ops.close_once = False
                value = original(pid, fd)
                io_ops.close_once = True
                return value
            return original(pid, fd)
        io_ops._source_fdinfo = second
        inode = io_ops.process_rows[0]['fds'][0]['inode']
        with self.assertRaisesRegex(ValueError, 'store writer'):
            self.collect(io_ops, {(0, inode)})
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_strict_fdinfo_and_source_flags(self):
        from fd_lifecycle import decode_fdinfo
        self.assertEqual(decode_fdinfo(b'flags:\t02000002\nmnt_id:\t8\nino:\t42\n'),
                         {'flags': 0o2000002, 'mnt_id': 8, 'ino': 42})
        for raw in (b'flags: 02\nflags: 01\nmnt_id: 8\nino: 42\n',
                    b'flags: 08\nmnt_id: 8\nino: 42\n',
                    b'flags: 02\nmnt_id: 8\n', b'flags: 02\nmnt_id: 8\nino: x\n'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                decode_fdinfo(raw)

    def test_retry_never_masks_pin_close_failure(self):
        from fd_lifecycle import CleanupFailure, FDSamplingRace, SourceVanished
        io_ops = Collector('source-fdinfo')
        def broken_close(pin): raise OSError('injected pin close')
        io_ops.close = broken_close
        with self.assertRaises(CleanupFailure) as captured:
            self.collect(io_ops)
        self.assertIsInstance(captured.exception.primary, SourceVanished)
        self.assertEqual(len(captured.exception.errors), 1)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_repeated_fd_race_exhausts_exactly_two_attempts(self):
        from fd_lifecycle import SamplingExhausted, SourceVanished
        io_ops = Collector()
        def missing(pid, fd):
            raise SourceVanished('source-fdinfo-open', FileNotFoundError(2, 'gone'))
        io_ops._source_fdinfo = missing
        with self.assertRaises(SamplingExhausted) as captured:
            self.collect(io_ops)
        self.assertEqual(len(captured.exception.attempts), 2)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1, 2])

    def test_recorder_enoent_never_triggers_retry(self):
        io_ops = Collector()
        original = io_ops.record.raw
        def raw(kind, value):
            if kind == 'listener-process-sampling-point':
                raise FileNotFoundError(2, 'recorder failed')
            original(kind, value)
        io_ops.record.raw = raw
        with self.assertRaises(FileNotFoundError):
            self.collect(io_ops)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_protected_socket_drift_between_attempts_is_terminal(self):
        io_ops = Collector()
        original = io_ops.record.raw
        def raw(kind, value):
            if kind == 'listener-collection-attempt-start' and value['attempt'] == 2:
                socket = io_ops.facts['sockets'][0]['path']
                io_ops.files[socket].st_ino += 10000
            original(kind, value)
        io_ops.record.raw = raw
        with self.assertRaisesRegex(ValueError, 'protected anchor drift'):
            self.collect(io_ops)

    def test_anchor_is_rechecked_before_authorizing_retry(self):
        io_ops = Collector('source-fdinfo')
        original, seen = io_ops.lstat, {}
        target = io_ops.facts['sockets'][0]['path']
        def lstat(path):
            value = copy.copy(original(path))
            seen[str(path)] = seen.get(str(path), 0) + 1
            if str(path) == target and seen[str(path)] == 2:
                value.st_ino += 10000
            return value
        io_ops.lstat = lstat
        with self.assertRaisesRegex(ValueError, 'protected anchor drift before retry'):
            self.collect(io_ops)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_source_proc_recorder_error_and_close_error_are_not_races(self):
        from fd_lifecycle import CleanupFailure
        class Record:
            def raw(self, kind, value): raise FileNotFoundError(2, 'record')
        io_ops = guest._IO(float('inf'), Record())
        with (mock.patch.object(guest.os, 'open', return_value=77),
              mock.patch.object(guest.os, 'read', side_effect=[b'flags: 02\n', b'']),
              mock.patch.object(guest.os, 'close', side_effect=OSError('close'))):
            with self.assertRaises(CleanupFailure) as captured:
                io_ops._source_fdinfo(101, 10)
        self.assertIsInstance(captured.exception.primary, FileNotFoundError)
        self.assertEqual(len(captured.exception.errors), 1)

    def test_fd_directory_is_exhausted_before_pin_and_remains_open(self):
        io_ops = Collector(None)
        original_scan, original_open = io_ops.scandir, io_ops.open
        state = {'exhausted': False, 'closed': True}
        class Tracked:
            def __init__(self, entries): self.entries = entries
            def __iter__(self):
                state.update(exhausted=False, closed=False)
                yield from self.entries
                state['exhausted'] = True
            def close(self): state['closed'] = True
        def scan(path):
            return Tracked(original_scan(path).entries)
        def open_pin(path, flags):
            self.assertTrue(state['exhausted'])
            self.assertFalse(state['closed'])
            return original_open(path, flags)
        io_ops.scandir, io_ops.open = scan, open_pin
        self.collect(io_ops)
        self.assertTrue(state['closed'])

    def test_fd_directory_close_failure_preserves_race(self):
        from fd_lifecycle import CleanupFailure, FDSamplingRace
        io_ops = Collector('source-fdinfo')
        original = io_ops.scandir
        def scan(path):
            iterator = original(path)
            def close(): raise OSError('directory close')
            iterator.close = close
            return iterator
        io_ops.scandir = scan
        with self.assertRaises(CleanupFailure) as captured:
            self.collect(io_ops)
        self.assertIsInstance(captured.exception.primary, FDSamplingRace)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_process_identity_drift_cannot_be_retried(self):
        io_ops = Collector('source-fdinfo')
        original = io_ops.proc
        calls = [0]
        def proc(path, maximum=None):
            if path == '/proc/101/stat':
                calls[0] += 1
                if calls[0] == 2: return proc_stat(101, 999999)
            return original(path, maximum)
        io_ops.proc = proc
        with self.assertRaisesRegex(ValueError, 'process identity drift'):
            self.collect(io_ops)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_source_object_reuse_restarts_whole_real_collection(self):
        for changed in ('inode', 'mount', 'socket'):
            with self.subTest(changed=changed):
                io_ops = Collector(None)
                used = [False]
                if changed == 'inode':
                    original = io_ops.stat
                    def stat_source(path):
                        value = original(path)
                        if not used[0]: used[0] = True; value.st_ino += 1
                        return value
                    io_ops.stat = stat_source
                elif changed == 'mount':
                    original = io_ops._source_fdinfo
                    def fdinfo(pid, fd):
                        data = original(pid, fd)
                        if not used[0]: used[0] = True; return data.replace(b'mnt_id:\t8', b'mnt_id:\t9')
                        return data
                    io_ops._source_fdinfo = fdinfo
                else:
                    original = io_ops.readlink
                    def link(path):
                        value = original(path)
                        if '/fd/' in path and not path.startswith('/proc/self/') and not used[0]:
                            used[0] = True; return 'socket:[99999]'
                        return value
                    io_ops.readlink = link
                self.collect(io_ops)
                self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                                  if k == 'listener-collection-attempt-start'], [1, 2])

    def test_second_source_fdinfo_is_checked_and_not_stitched(self):
        from fd_lifecycle import SourceVanished
        for mutation in ('vanished', 'mount', 'flags'):
            with self.subTest(mutation=mutation):
                io_ops = Collector(None)
                original, count = io_ops._source_fdinfo, [0]
                def fdinfo(pid, fd):
                    count[0] += 1
                    data = original(pid, fd)
                    if count[0] == 2:
                        if mutation == 'vanished':
                            raise SourceVanished('source-fdinfo-read', FileNotFoundError(2, 'gone'))
                        if mutation == 'mount': return data.replace(b'mnt_id:\t8', b'mnt_id:\t9')
                        return data.replace(b'flags:\t02', b'flags:\t01')
                    return data
                io_ops._source_fdinfo = fdinfo
                if mutation == 'flags':
                    with self.assertRaisesRegex(ValueError, 'flags drift'):
                        self.collect(io_ops)
                    expected = [1]
                else:
                    self.collect(io_ops)
                    expected = [1, 2]
                self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                                  if k == 'listener-collection-attempt-start'], expected)

    def test_foreign_daemon_observed_before_race_is_latched(self):
        io_ops = Collector('source-fdinfo')
        io_ops.links['/proc/101/exe'] = '/usr/bin/dockerd'
        with self.assertRaisesRegex(ValueError, 'foreign daemon'):
            self.collect(io_ops)
        self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                          if k == 'listener-collection-attempt-start'], [1])

    def test_combined_flags_and_object_drift_is_terminal(self):
        for drift in ('mount', 'inode', 'target'):
            with self.subTest(drift=drift):
                io_ops = Collector(None)
                source, calls = io_ops._source_fdinfo, [0]
                def fdinfo(pid, fd):
                    calls[0] += 1
                    raw = source(pid, fd)
                    if calls[0] == 1 and drift == 'mount':
                        return raw.replace(b'mnt_id:\t8', b'mnt_id:\t9')
                    if calls[0] == 1 and drift == 'inode':
                        return raw.replace(b'ino:\t', b'ino:\t9')
                    if calls[0] == 2:
                        return raw.replace(b'flags:\t02', b'flags:\t01')
                    return raw
                io_ops._source_fdinfo = fdinfo
                link, seen = io_ops.readlink, [False]
                def readlink(path):
                    raw = link(path)
                    if drift == 'target' and '/fd/' in path and not path.startswith('/proc/self/') and not seen[0]:
                        seen[0] = True
                        return 'socket:[987654]'
                    return raw
                io_ops.readlink = readlink
                with self.assertRaisesRegex(ValueError, 'flags drift'):
                    self.collect(io_ops)
                self.assertEqual([v['attempt'] for k, v in io_ops.record.observations
                                  if k == 'listener-collection-attempt-start'], [1])
                self.assertFalse(io_ops.pins)

    def test_real_stdlib_scandir_eof_closes_before_explicit_close(self):
        # Actual host standard-library behavior, not Linux procfs evidence.
        with tempfile.TemporaryDirectory() as path:
            Path(path, 'entry').touch()
            anchor = os.open(path, os.O_RDONLY)
            try:
                expected_fd = os.dup(anchor)
                os.close(expected_fd)
                iterator = os.scandir(path)
                self.assertEqual(os.fstat(expected_fd).st_ino, os.fstat(anchor).st_ino)
                self.assertEqual([entry.name for entry in iterator], ['entry'])
                with self.assertRaises(OSError) as caught:
                    os.fstat(expected_fd)
                self.assertEqual(caught.exception.errno, errno.EBADF)
                iterator.close()
            finally:
                os.close(anchor)

    def test_real_directory_fd_survives_process_sampling(self):
        # The old collector uses a real scandir and fails after EOF. After the
        # fix this same test also binds the private native API to owned real
        # host FDs (Linux dirent bytes are synthetic, not native Linux calls).
        io_ops = Collector(None)
        open_pin, fstat = io_ops.open, os.fstat
        owned_open, owned_close = os.open, os.close
        with tempfile.TemporaryDirectory() as path:
            for row in io_ops.process_rows:
                sub = Path(path, str(row['main_pid'])); sub.mkdir()
                for fd in row['fds']: (sub / str(fd['fd'])).touch()
            state = {'fd': None, 'samples': 0}
            def pin(proc_path, flags):
                fstat(state['fd'])
                state['samples'] += 1
                return open_pin(proc_path, flags)
            api = DirectoryAPI(io_ops.scandir)
            def native_open(proc_path):
                proc_path = os.fsdecode(proc_path)
                sub = Path(path, proc_path.split('/')[2])
                state['fd'] = owned_open(sub, os.O_RDONLY)
                entries = io_ops.scandir(proc_path)
                api.active[state['fd']] = (iter(entries), entries)
                return state['fd']
            def native_close(handle):
                api.active.pop(handle)
                owned_close(handle)
                return 0
            api.opendir, api.closedir = native_open, native_close
            io_ops.directory_api, io_ops.open = api, pin
            self.collect(io_ops)
            self.assertGreater(state['samples'], 0)
            self.assertFalse(api.active)
            with self.assertRaises(OSError) as caught:
                fstat(state['fd'])
            self.assertEqual(caught.exception.errno, errno.EBADF)

    def test_real_host_libc_directory_eof_keeps_owned_fd(self):
        # Uses the host ABI's opaque DIR* only; never decodes Darwin dirent as
        # Linux dirent64, and never calls Linux-only syscalls on Darwin.
        api = ctypes.CDLL(None, use_errno=True)
        api.opendir.argtypes, api.opendir.restype = [ctypes.c_char_p], ctypes.c_void_p
        api.readdir.argtypes, api.readdir.restype = [ctypes.c_void_p], ctypes.c_void_p
        api.dirfd.argtypes, api.dirfd.restype = [ctypes.c_void_p], ctypes.c_int
        api.closedir.argtypes, api.closedir.restype = [ctypes.c_void_p], ctypes.c_int
        with tempfile.TemporaryDirectory() as path:
            Path(path, 'entry').touch()
            handle = api.opendir(os.fsencode(path))
            self.assertTrue(handle)
            fd = api.dirfd(handle)
            try:
                before = os.fstat(fd)
                count = 0
                while True:
                    ctypes.set_errno(0)
                    if not api.readdir(handle):
                        self.assertEqual(ctypes.get_errno(), 0)
                        break
                    count += 1
                    self.assertLess(count, 10)
                self.assertGreaterEqual(count, 3)
                self.assertEqual(os.fstat(fd), before)
            finally:
                self.assertEqual(api.closedir(handle), 0)
            with self.assertRaises(OSError) as caught: os.fstat(fd)
            self.assertEqual(caught.exception.errno, errno.EBADF)

    def test_native_binding_self_fd_is_sampled_after_eof_before_close(self):
        io_ops = Collector(None)
        owned_open, owned_close, owned_stat = os.open, os.close, os.fstat
        scan, pin_open = io_ops.scandir, io_ops.open
        pin_stat, source_stat = io_ops.fstat, io_ops.stat
        self_pid = io_ops.process_rows[0]['main_pid']
        state = {'fd': None, 'eof': False, 'self_samples': 0}
        api = DirectoryAPI(scan)
        with tempfile.TemporaryDirectory() as path:
            def native_open(raw):
                pid = int(os.fsdecode(raw).split('/')[2])
                fd = owned_open(path, os.O_RDONLY)
                state.update(fd=fd, eof=False, pid=pid)
                if pid == self_pid:
                    value = owned_stat(fd)
                    row = next(p for p in io_ops.process_rows if p['main_pid'] == pid)
                    self.assertNotIn(fd, [d['fd'] for d in row['fds']])
                    row['fds'].append({'fd': fd, 'target': path, 'flags': 0, 'device': value.st_dev,
                        'inode': value.st_ino, 'mount_id': 8, 'socket_inode': None})
                    io_ops.links['/proc/%d/fd/%d' % (pid, fd)] = path
                    io_ops.files['/proc/%d/fdinfo/%d' % (pid, fd)] = (
                        'flags: 0\nmnt_id: 8\nino: %d\n' % value.st_ino).encode()
                entries = scan(os.fsdecode(raw))
                api.active[fd] = (iter(entries), entries)
                return fd
            read = api.readdir64
            def native_read(handle):
                value = read(handle)
                if not value: state['eof'] = True
                return value
            def native_close(handle):
                api.active.pop(handle)
                if state['pid'] == self_pid:
                    row = next(p for p in io_ops.process_rows if p['main_pid'] == self_pid)
                    row['fds'][:] = [d for d in row['fds'] if d['fd'] != handle]
                owned_close(handle)
                return 0
            def pin(pathname, flags):
                self.assertTrue(state['eof'])
                owned_stat(state['fd'])
                if pathname == '/proc/%d/fd/%d' % (self_pid, state['fd']): state['self_samples'] += 1
                return pin_open(pathname, flags)
            def fstat(fd):
                return owned_stat(state['fd']) if io_ops.pins[fd]['target'] == path else pin_stat(fd)
            def stat_source(pathname):
                return owned_stat(state['fd']) if io_ops.links[pathname] == path else source_stat(pathname)
            api.opendir, api.readdir64, api.closedir = native_open, native_read, native_close
            io_ops.directory_api, io_ops.open, io_ops.fstat, io_ops.stat = api, pin, fstat, stat_source
            result = self.collect(io_ops)
            self.assertTrue(all(result['complete'].values()))
            self.assertEqual(state['self_samples'], 2)
            self.assertFalse(api.active)
            self.assertFalse(io_ops.pins)
            self.assertEqual([v['attempt'] for k,v in io_ops.record.observations if k == 'listener-collection-attempt-start'], [1])

    def test_native_directory_failures_stay_hard_and_close_once(self):
        from fd_lifecycle import _FDDirectory, CleanupFailure
        for fault in ('open', 'read', 'record-size', 'name-nul', 'name-ascii', 'dots', 'close', 'read-close', 'deadline'):
            with self.subTest(fault=fault):
                io_ops = Collector(None)
                api = DirectoryAPI(io_ops.scandir)
                opened, closed = [], []
                original_open, original_read, original_close = api.opendir, api.readdir64, api.closedir
                def opendir(path):
                    if fault == 'open': ctypes.set_errno(errno.EACCES); return 0
                    handle = original_open(path); opened.append(handle); return handle
                def readdir(handle):
                    if fault in ('read', 'read-close'): ctypes.set_errno(errno.EIO); return 0
                    if fault == 'deadline': raise TimeoutError('original deadline')
                    if fault in ('record-size', 'name-nul', 'name-ascii', 'dots'):
                        name = {'record-size': b'1\0', 'name-nul': b'123', 'name-ascii': b'\xff\0', 'dots': b'.\0'}[fault]
                        size = 19 if fault == 'record-size' else 19 + len(name)
                        api.buffer = ctypes.create_string_buffer(struct.pack('=QqHB', 1, 0, size, 0) + name)
                        return ctypes.addressof(api.buffer)
                    return original_read(handle)
                def closedir(handle):
                    closed.append(handle); original_close(handle)
                    if fault in ('close', 'read-close'): ctypes.set_errno(errno.EBADF); return -1
                    return 0
                api.opendir, api.readdir64, api.closedir = opendir, readdir, closedir
                io_ops.directory_api = api
                with self.assertRaises((ValueError, OSError, CleanupFailure)) as caught:
                    self.collect(io_ops)
                self.assertEqual(opened, closed)
                self.assertFalse(api.active)
                self.assertEqual([v['attempt'] for k,v in io_ops.record.observations if k == 'listener-collection-attempt-start'], [1])
                if fault == 'read-close':
                    self.assertEqual(caught.exception.primary.errno, errno.EIO)
                    self.assertEqual(caught.exception.errors[0].errno, errno.EBADF)
        with mock.patch('fd_lifecycle.sys.platform', 'darwin'), mock.patch('fd_lifecycle.ctypes.CDLL') as loader:
            with self.assertRaises(OSError): _FDDirectory('/proc/1/fd', lambda: None)
            loader.assert_not_called()
        with mock.patch('fd_lifecycle.sys.platform', 'linux'), mock.patch('fd_lifecycle.os.uname', return_value=SimpleNamespace(machine='aarch64')), mock.patch('fd_lifecycle.ctypes.CDLL', side_effect=AttributeError('missing readdir64')):
            with self.assertRaises(AttributeError): _FDDirectory('/proc/1/fd', lambda: None)

    def test_anchor_table_keeps_earlier_complete_and_partial_owners(self):
        from fd_lifecycle import SourceVanished
        for when in ('initial-race', 'closing-race-before-owner', 'closing-race-after-owner'):
            with self.subTest(when=when):
                io_ops = Collector(None)
                row = io_ops.process_rows[0]
                row['fds'].append({'fd':999, 'target':'socket:[987654]', 'flags':2, 'device':0,
                    'inode':987654,'mount_id':8,'socket_inode':987654})
                io_ops.links['/proc/%d/fd/999' % row['main_pid']] = 'socket:[987654]'
                io_ops.files['/proc/%d/fdinfo/999' % row['main_pid']] = b'flags: 02\nmnt_id: 8\nino: 987654\n'
                source, calls = io_ops._source_fdinfo, [0]
                full = 2 * sum(len(p['fds']) for p in io_ops.process_rows)
                owner_end = 2 * len(row['fds'])
                fail_at = {'initial-race': owner_end+1, 'closing-race-before-owner': full+1,
                           'closing-race-after-owner': full+owner_end+1}[when]
                def fdinfo(pid,fd):
                    calls[0] += 1
                    if calls[0] == fail_at:
                        raise SourceVanished('source-fdinfo-open', FileNotFoundError(2,'closing race'))
                    return source(pid,fd)
                io_ops._source_fdinfo = fdinfo
                proc, table_reads = io_ops.proc, [0]
                def read(path,maximum=None):
                    raw = proc(path,maximum)
                    if path == '/proc/net/tcp':
                        table_reads[0] += 1
                        if table_reads[0] == 2:
                            return raw+b'0: 0100007F:2382 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 987654 1\n'
                    return raw
                io_ops.proc = read
                with self.assertRaisesRegex(ValueError, 'TCP'):
                    self.collect(io_ops)
                self.assertEqual([v['attempt'] for k,v in io_ops.record.observations if k == 'listener-collection-attempt-start'], [1])
                self.assertEqual(table_reads[0], 2)

    def test_shared_corpus_executes_real_sampler(self):
        from fd_lifecycle import sample_fd, SourceVanished, FDSamplingRace, CleanupFailure
        corpus = json.loads((Path(__file__).parent / 'testdata/fd-lifecycle-corpus.json').read_bytes())
        self.assertEqual(set(corpus), {'schema', 'cases'})
        self.assertEqual(corpus['schema'], 'switchback.engine.fd_lifecycle.corpus.v1')
        self.assertEqual(len({c['id'] for c in corpus['cases']}), len(corpus['cases']))
        keys = {'id', 'fdinfo', 'pin_fdinfo', 'pin', 'source_before', 'source_after', 'primitive_error', 'expected'}
        for case in corpus['cases']:
            with self.subTest(case=case['id']):
                self.assertEqual(set(case) - {'fdinfo_after'}, keys)
                err = case['primitive_error']
                counts = {'source-readlink': 0, 'source-stat': 0, 'source-info': 0}
                def fail(primitive):
                    if err is not None and err['primitive'] == primitive:
                        raise OSError(err['errno'], primitive)
                def metadata(value):
                    return SimpleNamespace(st_dev=value['device'], st_ino=value['inode'], st_mode=value['mode'])
                def open_pin(path, flags):
                    fail('pin-open'); return 900
                def fstat(pin):
                    fail('pin-fstat'); return metadata(case['pin'])
                def link(path):
                    if path == '/proc/self/fd/900':
                        fail('pin-readlink'); return case['pin']['target']
                    counts['source-readlink'] += 1
                    suffix = 'before' if counts['source-readlink'] == 1 else 'after'
                    fail('source-readlink-' + suffix)
                    return case['source_' + suffix]['target']
                def stat_source(path):
                    counts['source-stat'] += 1
                    suffix = 'before' if counts['source-stat'] == 1 else 'after'
                    fail('source-stat-' + suffix)
                    return metadata(case['source_' + suffix])
                def source_info(pid, fd):
                    if err and err['primitive'] == 'source-fdinfo-open':
                        error = OSError(err['errno'], 'source-fdinfo-open')
                        if err['errno'] == 2:
                            raise SourceVanished('source-fdinfo-open', error)
                        raise error
                    counts['source-info'] += 1
                    return case.get('fdinfo_after', case['fdinfo']).encode() if counts['source-info'] == 2 else case['fdinfo'].encode()
                def pin_info(fd):
                    fail('pin-fdinfo'); return case['pin_fdinfo'].encode()
                def close(fd): fail('pin-close')
                outcome = 'stable'
                try:
                    with (mock.patch('fd_lifecycle.os.open', side_effect=open_pin),
                          mock.patch('fd_lifecycle.os.fstat', side_effect=fstat),
                          mock.patch('fd_lifecycle.os.readlink', side_effect=link),
                          mock.patch('fd_lifecycle.os.stat', side_effect=stat_source),
                          mock.patch('fd_lifecycle.os.close', side_effect=close)):
                        result = sample_fd(101, 10, source_info, pin_info, lambda: None)
                    self.assertEqual(result['flags'], 2)
                    self.assertEqual(result['mount_id'], int(next(line.split(':', 1)[1] for line in case['fdinfo'].splitlines() if line.startswith('mnt_id:'))))
                except (SourceVanished, FDSamplingRace):
                    outcome = 'sampling_race'
                except (ValueError, OSError, CleanupFailure):
                    outcome = 'hard_failure'
                self.assertEqual(outcome, case['expected'])

if __name__ == '__main__': unittest.main()
