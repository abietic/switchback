"""Fixed Linux collector/installer. No public CLI and no import-time I/O.

Only prepare.py actual invokes _entry in a single Lima process. All paths and
commands below are server-owned constants. Private I/O seams are for offline
fixtures; they cannot be selected with arguments, environment or stdin paths.
"""
import copy
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import re
import selectors
import shlex
import socket
import stat
import struct
import sys
import time

from contract import (BUDGET_SECONDS, CONTAINERD_SOCKET, CORE_CGROUP, DATA_ROOT, ENGINE_SOCKET,
    FILES, NETWORK_LABELS, NETWORKS, PACKAGE_PINS, RECEIPT_PATH, RUNTIME_PARENT,
    RUNTIME_ROOT, SOURCE_PREFIX, TTRPC_SOCKET, UNIT_NAMES, _fixed_toml, _json, _network,
    _digest, _object, _require, _unit, validate_configuration, validate_listeners_v2, validate_preflight_v2, CALLBACK_SOCKET, METRICS_SOCKET,
    LIBNETWORK_SOCKET_PATTERN, SOCKET_PARENT_PATHS, DOCKER_RUNTIME_ROOT,
    _listener_negative, _daemons, _sockets, _socket_parents, _boot)
from prepare import (_BINDING, _FRAME, _Failure, _Record, _bounded_run, _encode, _exclusive,
                     _guard, _read, _run_window, _safe_directory, _sha, _wire)
from store import _scan_store
from binaries import _collect_binaries
from fd_lifecycle import (FDSamplingRace, SamplingExhausted, CleanupFailure, SourceVanished,
                          sample_fd, _source, _FDDirectory)

_BUNDLE = Path('/opt/switchback/engine-bundle')
_KIT = Path('/opt/switchback/qualification-kit')
_STAGE = Path(RECEIPT_PATH).parent
_MASKS = ('docker.service', 'docker.socket', 'containerd.service')
_MAX_PROCESSES = 8192
_MAX_FDS = 65536
_MAX_PROC_BYTES = 2 << 20
_API = '/v1.56'


def _parse_stat(raw, pid):
    try:
        left, right = raw.index(b' ('), raw.rindex(b') ')
        _require(int(raw[:left]) == pid, 'proc PID identity differs')
        fields = raw[right + 2:].split()
        _require(len(fields) >= 20 and len(fields[0]) == 1, 'proc stat fields truncated')
        start, flags = int(fields[19]), int(fields[6])
        _require(start >= 0 and flags >= 0, 'proc stat start/flags')
        return {'main_pid': pid, 'start_tick': start, 'flags': flags, 'state': fields[0].decode()}
    except (ValueError, IndexError, UnicodeError) as error:
        raise ValueError('malformed complete proc stat') from error


def _parse_unix(raw):
    lines = raw.decode('ascii').splitlines()
    _require(lines and lines[0].split() == ['Num', 'RefCount', 'Protocol', 'Flags', 'Type', 'St', 'Inode', 'Path'],
             'Unix table header')
    result = []
    for line in lines[1:]:
        fields = line.split(maxsplit=7)
        _require(7 <= len(fields) <= 8 and fields[0].endswith(':'), 'Unix table row truncated')
        _require(all(re.fullmatch('[0-9A-Fa-f]+', item) for item in fields[1:6]), 'Unix table numeric field')
        inode = int(fields[6])
        _require(inode > 0, 'Unix table inode')
        result.append({'path': fields[7] if len(fields) == 8 else '', 'inode': inode,
                       'listener': bool(int(fields[3], 16) & 0x10000), 'owners': []})
    _require(len(result) <= 8192, 'Unix table bound')
    return result


def _parse_tcp(raw, ipv6):
    lines = raw.decode('ascii').splitlines()
    _require(lines and 'local_address' in lines[0] and 'inode' in lines[0], 'TCP table header')
    result = []
    width = 32 if ipv6 else 8
    for line in lines[1:]:
        fields = line.split()
        _require(len(fields) >= 10 and re.fullmatch(r'[0-9]+:', fields[0]) is not None,
                 'TCP table row truncated')
        _require(re.fullmatch(r'[0-9A-Fa-f]{%d}:[0-9A-Fa-f]{4}' % width, fields[1])
                 and re.fullmatch(r'[0-9A-Fa-f]{%d}:[0-9A-Fa-f]{4}' % width, fields[2])
                 and re.fullmatch(r'[0-9A-Fa-f]{2}', fields[3]), 'TCP address/state encoding')
        inode = int(fields[9])
        _require(inode >= 0, 'TCP inode')
        if fields[3] != '0A':
            continue
        address, port = fields[1].split(':')
        raw_address = bytes.fromhex(address)
        if ipv6:
            raw_address = b''.join(raw_address[index:index + 4][::-1] for index in range(0, 16, 4))
        else:
            raw_address = raw_address[::-1]
        result.append({'address': socket.inet_ntop(socket.AF_INET6 if ipv6 else socket.AF_INET, raw_address),
                       'port': int(port, 16), 'inode': inode, 'owners': []})
    _require(len(lines) <= 8193 and all(row['inode'] > 0 and row['port'] > 0 for row in result), 'TCP table bound')
    return result


def _api_json(raw):
    if type(raw) is dict:
        return raw
    return _json(raw)


def _network_value(raw, index):
    value = _api_json(raw)
    _require(value.get('Containers') == {} and value.get('Peers', []) == []
        and value.get('ConfigOnly') is False and value.get('EnableIPv4') is True
        and value.get('ConfigFrom') == {'Network': ''}, 'network has attached/external state')
    ipam = value.get('IPAM', {})
    _require(type(ipam) is dict and type(ipam.get('Config')) is list, 'network IPAM missing')
    configs = []
    for config in ipam['Config']:
        _require(set(config) == {'Subnet', 'Gateway'}, 'unknown network IPAM allocation')
        configs.append({'subnet': config['Subnet'], 'gateway': config['Gateway']})
    try:
        result = {'name': value['Name'], 'id': value['Id'], 'driver': value['Driver'],
            'scope': value['Scope'], 'internal': value['Internal'], 'labels': value['Labels'],
            'enable_ipv6': value['EnableIPv6'], 'attachable': value['Attachable'],
            'ingress': value['Ingress'], 'options': value['Options'] or {},
            'ipam': {'driver': ipam['Driver'], 'options': ipam.get('Options') or {}, 'config': configs}}
    except (KeyError, TypeError) as error:
        raise ValueError('network observation incomplete') from error
    _network(result, index)
    return result


def _prepare_networks(api, observed=lambda value: None):
    """Exactly one inspect/create/inspect sequence per fixed network; no retry."""
    result = []
    for index, (name, internal) in enumerate(NETWORKS):
        status, raw = api('GET', '/networks/' + name)
        if status == 404:
            payload = {'Name': name, 'Driver': 'bridge', 'Internal': internal,
                'EnableIPv6': False, 'Attachable': False, 'Ingress': False,
                'IPAM': {'Driver': 'default', 'Options': {}, 'Config': []},
                'Options': {}, 'Labels': dict(NETWORK_LABELS)}
            status, created = api('POST', '/networks/create', payload)
            _require(status == 201, 'NetworkCreate failed/unknown; no retry')
            created = _api_json(created)
            _object(created, ('Id', 'Warning'), 'NetworkCreate response')
            _require(re.fullmatch('[0-9a-f]{64}', created['Id']) is not None and created['Warning'] == '',
                     'NetworkCreate response identity/Warning')
            status, raw = api('GET', '/networks/' + created['Id'])
        _require(status == 200, 'fixed network inspect failed; no retry')
        result.append(_network_value(raw, index))
        observed(copy.deepcopy(result))
    _require(result[0]['id'] != result[1]['id'], 'duplicate network identities')
    return result


def _mounts(raw):
    result = []
    for line in raw.decode().splitlines():
        parts = line.split()
        _require('-' in parts and len(parts) >= 10, 'mountinfo row bound')
        split = parts.index('-')
        _require(split >= 6 and len(parts) >= split + 4, 'mountinfo separator fields')
        def unescape(value):
            return re.sub(r'\\([0-7]{3})', lambda match: chr(int(match.group(1), 8)), value)
        result.append({'id': int(parts[0]), 'device': parts[2], 'root': unescape(parts[3]),
            'mountpoint': unescape(parts[4]), 'options': parts[5].split(','),
            'fs_type': parts[split + 1], 'source': unescape(parts[split + 2]),
            'super_options': parts[split + 3].split(',')})
    _require(len(result) <= 8192, 'mount table bound')
    return result


_CATALOG_PROPERTIES = ('Id', 'ActiveState', 'MainPID', 'UnitFileState', 'ExecStart', 'Listen')


def _properties(raw, *, catalogue=False):
    result = {}
    for line in raw.decode().splitlines():
        key, sep, value = line.partition('=')
        _require(sep and (key not in result or
                 (catalogue and key in ('Listen', 'ExecStart'))),
                 'duplicate/malformed systemd property')
        if key in result:
            _require(result[key] and value, 'empty repeated systemd property')
            result[key] += '\n' + value
        else:
            result[key] = value
    return result


def _emit_bytes(raw, deadline):
    """Bound pipe backpressure; postdeadline output is evidence-only, at most1s."""
    now = time.monotonic()
    output_deadline = deadline if now < deadline else now + 1
    descriptor, selector = sys.stdout.fileno(), selectors.DefaultSelector()
    os.set_blocking(descriptor, False)
    selector.register(descriptor, selectors.EVENT_WRITE)
    try:
        view = memoryview(raw)
        while view:
            _guard(output_deadline)
            if not selector.select(min(.1, max(0, output_deadline - time.monotonic()))):
                continue
            count = os.write(descriptor, view[:65536])
            _require(count > 0, 'host evidence transport closed')
            view = view[count:]
    finally:
        selector.close()


class _DeadlineSocket:
    """HTTPResponse's buffered reads still charge every recv to one deadline.

    A per-socket relative timeout alone permits a peer to drip bytes forever.
    Keep the response reader alive when HTTPConnection sees Connection:close,
    and check/reset only to the original absolute remaining time before recv.
    """
    def __init__(self, sock, deadline, monotonic=time.monotonic):
        self.socket, self.deadline, self.monotonic = sock, deadline, monotonic
        self.references, self.owner_closed, self.received = 0, False, bytearray()

    def _remaining(self):
        _guard(self.deadline, self.monotonic)
        self.socket.settimeout(max(.001, self.deadline - self.monotonic()))

    def sendall(self, raw):
        self._remaining()
        self.socket.sendall(raw)

    def makefile(self, *args):
        parent = self
        class Reader(io.RawIOBase):
            def readable(self):
                return True
            def readinto(self, view):
                parent._remaining()
                count = parent.socket.recv_into(view)
                parent.received.extend(bytes(view[:count]))
                _require(len(parent.received) <= (1 << 20) + 16384, 'HTTP wire byte bound')
                return count
            def close(self):
                if not self.closed:
                    parent.references -= 1
                    if parent.owner_closed and parent.references == 0:
                        parent.socket.close()
                super().close()
        self.references += 1
        return io.BufferedReader(Reader(), 8192)

    def close(self):
        self.owner_closed = True
        if self.references == 0:
            self.socket.close()




class _IO:
    def __init__(self, deadline, record):
        self.deadline, self.record = deadline, record

    def check(self):
        _guard(self.deadline)

    def proc(self, path, maximum=_MAX_PROC_BYTES):
        return self._read_proc(path, maximum, False)

    def _source_fdinfo(self, pid, fd):
        return self._read_proc('/proc/%d/fdinfo/%d' % (pid, fd), 16384, True)

    def _read_proc(self, path, maximum, source):
        self.check()
        operation = lambda: os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        fd = _source('source-fdinfo-open', operation) if source else operation()
        primary, data = None, None
        try:
            raw = bytearray()
            while True:
                self.check()
                operation = lambda: os.read(fd, min(65536, maximum + 1 - len(raw)))
                chunk = _source('source-fdinfo-read', operation) if source else operation()
                if not chunk:
                    break
                raw.extend(chunk)
                _require(len(raw) <= maximum, 'proc read bound')
            data = bytes(raw)
            self.record.raw('proc-file', {'path': path, 'raw': data})
        except BaseException as error:
            primary = error
        try:
            os.close(fd)
        except BaseException as error:
            raise CleanupFailure(primary, [error]) from primary
        if primary is not None:
            raise primary
        return data

    def command(self, argv):
        return _bounded_run(argv, self.deadline, self.record)

    def show(self, unit, properties):
        raw = self.command(['/usr/bin/systemctl', 'show', '--no-pager',
                            '--property=' + ','.join(properties), unit])
        return _properties(raw, catalogue=properties == _CATALOG_PROPERTIES)

    def boot(self):
        value = self.proc('/proc/sys/kernel/random/boot_id', 128).decode().strip()
        _require(re.fullmatch('[0-9a-f-]{36}', value) is not None, 'boot ID encoding')
        return value

    def read(self, path, maximum, mode=None):
        raw, value = _read(Path(path), maximum, 0, self.check, mode,
                           lambda value: self.record.raw('file-observation-before-validation', value))
        self.record.raw('bounded-file', {'path': str(path), 'sha256': _sha(raw), 'bytes': len(raw),
            'uid': value.st_uid, 'gid': value.st_gid, 'mode': stat.S_IMODE(value.st_mode),
            'device': value.st_dev, 'inode': value.st_ino})
        return raw, value

    def processes(self):
        pids = self._pids()
        result, descriptors = [], 0
        for pid in pids:
            self.check()
            prefix = '/proc/%d/' % pid
            before = _parse_stat(self.proc(prefix + 'stat', 65536), pid)
            cgroups = self.proc(prefix + 'cgroup', 16384).decode().splitlines()
            _require(len(cgroups) == 1 and cgroups[0].startswith('0::/'), 'process unified cgroup')
            cgroup = cgroups[0][3:]
            try:
                exe = os.readlink(prefix + 'exe')
            except FileNotFoundError:
                _require(before['flags'] & 0x00200000, 'unreadable userspace process executable')
                exe = None
            cmdline = self.proc(prefix + 'cmdline', 65536)
            fds = []
            def verify_identity():
                after = _parse_stat(self.proc(prefix + 'stat', 65536), pid)
                stable = all(before[key] == after[key] for key in ('main_pid', 'start_tick', 'flags'))
                states = before['state'] == after['state'] or {before['state'], after['state']} <= {'R', 'S'}
                _require(stable and states, 'process identity drift during FD enumeration')
                if exe is not None:
                    _require(os.readlink(prefix + 'exe') == exe, 'process executable drift')
                else:
                    try:
                        os.readlink(prefix + 'exe')
                    except FileNotFoundError:
                        pass
                    else:
                        raise ValueError('kernel process executable appeared')
                _require(self.proc(prefix + 'cgroup', 16384).decode().splitlines() == cgroups,
                         'process cgroup drift')
            iterator = _FDDirectory(prefix + 'fd', self.check)
            primary = None
            try:
                entries = []
                for entry in iterator:
                    self.check()
                    _require(re.fullmatch('[0-9]+', entry.name) is not None, 'proc FD encoding')
                    descriptors += 1
                    _require(descriptors <= _MAX_FDS, 'complete FD inventory bound')
                    entries.append(int(entry.name))
                _require(len(entries) == len(set(entries)), 'duplicate proc FD')
                for number in entries:
                    try:
                        fds.append(sample_fd(pid, number, self._source_fdinfo,
                            lambda pin: self.proc('/proc/self/fdinfo/%d' % pin, 16384), self.check))
                    except (SourceVanished, FDSamplingRace) as error:
                        verify_identity()
                        partial = result + [{**before, 'exe': exe, 'cgroup': cgroup,
                                             'fds': list(fds), 'cmdline': cmdline}]
                        if isinstance(error, SourceVanished):
                            raise FDSamplingRace(pid, number, error.primitive, error.original,
                                                 partial=partial) from error
                        error.partial = partial
                        raise
            except BaseException as error:
                primary = error
            try:
                iterator.close()
            except BaseException as error:
                raise CleanupFailure(primary, [error]) from primary
            if primary is not None:
                raise primary
            _require(exe is not None or not fds, 'unreadable userspace process executable')
            _require(before['start_tick'] > 0 or pid == 1 or (exe is None and not fds
                and before['flags'] & 0x00200000), 'zero-start userspace process identity')
            verify_identity()
            result.append({**before, 'exe': exe, 'cgroup': cgroup, 'fds': fds, 'cmdline': cmdline})
        _require(self._pids() == pids, 'process table coverage changed during collection')
        self.record.raw('complete-process-fd-inventory', result)
        return result

    def _pids(self):
        pids, entries = [], 0
        self.check()
        with os.scandir('/proc') as iterator:
            for entry in iterator:
                self.check()
                entries += 1
                _require(entries <= _MAX_PROCESSES * 2, 'complete proc directory bound')
                if entry.name.isdigit():
                    pids.append(int(entry.name))
                    _require(len(pids) <= _MAX_PROCESSES, 'process table bound')
        self.check()
        return sorted(pids)

    def daemon(self, index, boot, binary_hash):
        unit = UNIT_NAMES[index]
        value = self.show(unit, ('Id', 'ActiveState', 'SubState', 'MainPID', 'Result', 'WatchdogUSec'))
        _require(value == {'Id': unit, 'ActiveState': 'active', 'SubState': 'running',
            'MainPID': value.get('MainPID'), 'Result': 'success', 'WatchdogUSec': '0'}
            and value['MainPID'].isdigit(),
            'daemon not notify-ready/running')
        pid = int(value['MainPID'])
        process = _parse_stat(self.proc('/proc/%d/stat' % pid, 65536), pid)
        exe = os.readlink('/proc/%d/exe' % pid)
        cgroup = self.proc('/proc/%d/cgroup' % pid, 16384).decode().strip()
        self.record.raw('daemon-observation-before-validation', {'unit': unit, 'pid': pid,
                        'stat': process, 'exe': exe, 'cgroup': cgroup})
        _require(exe == ('/usr/bin/containerd', '/usr/bin/dockerd')[index]
            and cgroup == '0::' + CORE_CGROUP + '/' + unit, 'daemon process exe/cgroup')
        self.check()
        fd = os.open('/proc/%d/exe' % pid, os.O_RDONLY)
        try:
            value = os.fstat(fd)
            _require(stat.S_ISREG(value.st_mode) and value.st_uid == value.st_gid == 0
                     and value.st_nlink == 1 and value.st_size <= 128 << 20, 'daemon executable object')
            digest, count = hashlib.sha256(), 0
            while True:
                self.check()
                raw = os.read(fd, 65536)
                if not raw:
                    break
                count += len(raw)
                _require(count <= value.st_size, 'daemon ELF grew')
                digest.update(raw)
            _require(count == value.st_size and 'sha256:' + digest.hexdigest() == binary_hash,
                     'daemon executable differs from pinned installed ELF')
        finally:
            os.close(fd)
        _require(_parse_stat(self.proc('/proc/%d/stat' % pid, 65536), pid) == process,
                 'daemon PID/start changed around executable hash')
        return {'unit': unit, 'main_pid': pid, 'start_tick': process['start_tick'], 'exe': exe,
                'exe_sha256': binary_hash, 'cgroup': cgroup[3:], 'boot_id': boot}


def _listener_process_policy(processes, expected):
    """Apply daemon/shim and data-root writer policy to each complete sample."""
    for process in processes:
        executable = (process['exe'] or '').removesuffix(' (deleted)')
        if re.fullmatch(r'(dockerd|containerd|containerd-shim.*)', Path(executable).name):
            _require(process['main_pid'] in expected and all(process[key] == expected[process['main_pid']][key]
                for key in ('main_pid', 'start_tick', 'exe', 'cgroup')), 'active/foreign daemon or shim process')
        for descriptor in process['fds']:
            if (descriptor['flags'] & os.O_ACCMODE in (os.O_WRONLY, os.O_RDWR)
                and (descriptor['target'] == DATA_ROOT or descriptor['target'].startswith(DATA_ROOT + '/'))):
                _require(process['main_pid'] in expected, 'foreign data-root writer before socket connection')


def _latch_listener_negatives(processes, tables, sockets, expected, store_inodes, partial=True):
    _listener_process_policy(processes, expected)
    if store_inodes is not None:
        for process in processes:
            for descriptor in process['fds']:
                if ((descriptor['device'], descriptor['inode']) in store_inodes
                    or descriptor['target'] == DATA_ROOT or descriptor['target'].startswith(DATA_ROOT + '/')):
                    _require(descriptor['flags'] & os.O_ACCMODE not in (os.O_WRONLY, os.O_RDWR),
                             'foreign store writer')
    rows = copy.deepcopy(tables)
    by_inode = {row['inode']: row for values in rows.values() for row in values}
    _require(len(by_inode) == sum(len(values) for values in rows.values()),
             'duplicate cross-family kernel socket inode')
    for process in processes:
        for descriptor in process['fds']:
            row = by_inode.get(descriptor['socket_inode'])
            if row is not None:
                _require(process['exe'] is not None, 'listener owner executable missing')
                row['owners'].append({**{key: process[key] for key in
                    ('main_pid', 'start_tick', 'exe', 'cgroup')}, 'fd': descriptor['fd']})
    protected = {sock['path']: sock['kernel_inode'] for sock in sockets}
    socket_by_inode = {sock['kernel_inode']: sock for sock in sockets}
    owner_fds = {}
    for family in ('unix', 'tcp', 'tcp6'):
        for row in rows[family]:
            _listener_negative(family, row, protected, socket_by_inode, expected, owner_fds, 2, partial=partial)
    return rows


def _collect_listeners(io_ops, phase, binary_hashes):
    return _collect_listeners_with_inventory(io_ops, phase, binary_hashes, None)[0]


def _collect_listeners_with_inventory(io_ops, phase, binary_hashes, store_inodes):
    anchor, attempts, observed = None, [], []
    for number in (1, 2):
        io_ops.check()
        io_ops.record.raw('listener-collection-attempt-start', {'attempt': number})
        try:
            result, inventory, current = _collect_listeners_once(
                io_ops, phase, binary_hashes, anchor, store_inodes, observed)
        except FDSamplingRace as race:
            _require(race.anchor is not None, 'sampling race lacks valid anchor')
            _require(anchor is None or anchor == race.anchor, 'protected anchor drift')
            anchor = race.anchor
            io_ops.record.raw('listener-collection-attempt-failure', {'attempt': number,
                'pid': race.pid, 'fd': race.fd, 'primitive': race.primitive, 'error': str(race.original)})
            attempts.append(race)
            if number == 2:
                raise SamplingExhausted(attempts) from race
        except BaseException as error:
            try:
                io_ops.record.raw('listener-collection-attempt-failure', {'attempt': number,
                    'pid': None, 'fd': None, 'primitive': 'terminal', 'error': str(error)})
            except BaseException as recorder_error:
                raise recorder_error from error
            raise
        else:
            _require(anchor is None or anchor == current, 'protected anchor drift')
            io_ops.record.raw('listener-collection-attempt-success', {'attempt': number})
            return result, inventory
    raise AssertionError('unreachable')


def _collect_listeners_once(io_ops, phase, binary_hashes, anchor, store_inodes, observed):
    boot = io_ops.boot()
    count = {'preflight': 0, 'containerd_ready': 1, 'engine_ready': 2}[phase]
    _boot(boot)
    before = [io_ops.daemon(index, boot, binary_hashes[index]) for index in range(count)]
    _daemons(before, boot, count)
    def tables_at(suffix):
        raw = {family: io_ops.proc('/proc/net/' + family) for family in ('unix', 'tcp', 'tcp6')}
        io_ops.record.raw('complete-listener-tables' + suffix, raw)
        return {'unix': _parse_unix(raw['unix']), 'tcp': _parse_tcp(raw['tcp'], False),
                'tcp6': _parse_tcp(raw['tcp6'], True)}
    tables = tables_at('')
    paths = (CONTAINERD_SOCKET, TTRPC_SOCKET, ENGINE_SOCKET.removeprefix('unix://'),
             CALLBACK_SOCKET, METRICS_SOCKET)
    if count == 2:
        discovered = [row['path'] for row in tables['unix']
                      if LIBNETWORK_SOCKET_PATTERN.fullmatch(row['path']) and row['listener']]
        _require(len(discovered) == 1, 'sole libnetwork external key listener required')
        paths += (discovered[0],)
    active_paths = paths[:(0, 2, 6)[count]]
    parent_paths = SOCKET_PARENT_PATHS[:(0, 2, 5)[count]]
    def sample():
        socket_stats, parent_stats = {}, {}
        for path in parent_paths + active_paths:
            io_ops.check()
            value = os.lstat(path)
            raw = {'path': path, 'device': value.st_dev, 'inode': value.st_ino,
                   'mode': value.st_mode, 'uid': value.st_uid, 'gid': value.st_gid, 'nlink': value.st_nlink}
            io_ops.record.raw('listener-filesystem-stat', raw)
            (parent_stats if path in parent_paths else socket_stats)[path] = raw
        return socket_stats, parent_stats
    initial_sockets, initial_parents = sample()
    for path in paths[len(active_paths):]:
        _require(not os.path.lexists(path), 'unowned/preexisting daemon socket target')
    parents = []
    for path in parent_paths:
        value = initial_parents[path]
        _require(stat.S_ISDIR(value['mode']), 'socket parent is not directory')
        parents.append({key: value[key] for key in ('path', 'device', 'inode', 'uid', 'gid')})
        parents[-1].update(kind='directory', mode=stat.S_IMODE(value['mode']))
    sockets = []
    for index, path in enumerate(active_paths):
        value = initial_sockets[path]
        parent_path = str(Path(path).parent)
        parent = initial_parents[parent_path]
        matching = [row for row in tables['unix'] if row['path'] == path and row['listener']]
        _require(len(matching) == 1 and stat.S_ISSOCK(value['mode']), 'socket filesystem/kernel listener pairing')
        daemon = before[0 if index < 2 else 1]
        sockets.append({**{key: value[key] for key in ('path', 'device', 'inode', 'nlink', 'uid', 'gid')},
            'kernel_inode': matching[0]['inode'], 'kind': 'socket', 'mode': stat.S_IMODE(value['mode']),
            'owner_unit': daemon['unit'], 'owner_pid': daemon['main_pid'], 'owner_start_tick': daemon['start_tick'],
            'parent_path': parent_path, 'parent_device': parent['device'], 'parent_inode': parent['inode'],
            'parent_uid': parent['uid'], 'parent_gid': parent['gid'], 'parent_mode': stat.S_IMODE(parent['mode'])})
    _sockets(sockets, before, None, (0, 2, 6)[count], 2)
    _socket_parents(parents, sockets)
    current_anchor = (boot, copy.deepcopy(before), copy.deepcopy(initial_sockets),
                      copy.deepcopy(initial_parents), copy.deepcopy(sockets))
    _require(anchor is None or anchor == current_anchor, 'protected anchor drift')
    expected = {value['main_pid']: value for value in before}
    def known_negatives(table):
        # Separate bounded observations can reject; only a fresh complete
        # snapshot can establish complete owner coverage.
        for prior in observed:
            _latch_listener_negatives(prior, table, sockets, expected, store_inodes)
    known_negatives(tables)
    def processes_at(point, table):
        io_ops.record.raw('listener-process-sampling-point', point)
        try:
            result = io_ops.processes()
        except FDSamplingRace as error:
            io_ops.record.raw('listener-partial-process-fd-inventory', error.partial or [])
            observed.append(error.partial or [])
            known_negatives(table)
            # An initial anchor alone cannot establish that the endpoint is still
            # the same at the moment a retry is authorized.
            race_sockets, race_parents = sample()
            race_daemons = [io_ops.daemon(index, boot, binary_hashes[index]) for index in range(count)]
            _require(io_ops.boot() == boot and race_daemons == before
                     and race_sockets == initial_sockets and race_parents == initial_parents,
                     'protected anchor drift before retry')
            for path in paths[len(active_paths):]:
                _require(not os.path.lexists(path), 'unowned/preexisting daemon socket target')
            race_tables = tables_at('-race')
            for sock in sockets:
                matching = [row for row in race_tables['unix']
                            if row['path'] == sock['path'] and row['listener']]
                _require(len(matching) == 1 and matching[0]['inode'] == sock['kernel_inode'],
                         'protected anchor kernel inode drift before retry')
            known_negatives(race_tables)
            error.anchor = current_anchor
            raise
        observed.append(result)
        known_negatives(table)
        return result, _latch_listener_negatives(result, table, sockets, expected, store_inodes,
                                                 partial=(point != 'initial'))
    processes, owned_tables = processes_at('initial', tables)
    by_inode = {row['inode']: row for values in tables.values() for row in values}
    for process in processes:
        prefix = '/proc/%d/' % process['main_pid']
        current = _parse_stat(io_ops.proc(prefix + 'stat', 65536), process['main_pid'])
        _require(current['start_tick'] == process['start_tick'], 'process/FD PID reuse during socket collection')
        if process['exe'] is not None:
            _require(os.readlink(prefix + 'exe') == process['exe']
                     and io_ops.proc(prefix + 'cgroup', 16384).decode().strip() == '0::' + process['cgroup'],
                     'process executable/cgroup drift during socket collection')
        for descriptor in process['fds']:
            if descriptor['socket_inode'] in by_inode:
                _require(os.readlink(prefix + 'fd/%d' % descriptor['fd'])
                    == 'socket:[%d]' % descriptor['socket_inode'], 'FD/socket owner drift')
    closing_processes, unused = processes_at('closing', tables)
    closing_tables = tables_at('-closing')
    known_negatives(closing_tables)
    closing_owned = _latch_listener_negatives(closing_processes, closing_tables, sockets, expected, store_inodes, partial=False)
    final_sockets, final_parents = sample()
    after = [io_ops.daemon(index, boot, binary_hashes[index]) for index in range(count)]
    current_boot = io_ops.boot()
    def inventory(owned):
        return {'schema': 'switchback.engine.listeners.v2', 'phase': phase, 'boot_id': boot,
            'current_boot_id': current_boot, 'complete': dict.fromkeys(('unix', 'tcp', 'tcp6', 'fd_owners', 'processes'), True),
            'daemons': before, 'identity_before': before, 'identity_after': after,
            'sockets': sockets, 'socket_parents': parents, **owned}
    result, closing = inventory(owned_tables), inventory(closing_owned)
    io_ops.record.raw('listener-inventory-before-validation', result)
    _require(initial_sockets == final_sockets and initial_parents == final_parents,
             'socket/parent filesystem identity drift during full collection')
    validate_listeners_v2(result)
    validate_listeners_v2(closing)
    def coverage(inventory):
        return sorted((process['main_pid'], process['start_tick'], process['exe'], process['cgroup'],
            sorted(tuple(descriptor[key] for key in
                         ('fd', 'target', 'flags', 'device', 'inode', 'mount_id', 'socket_inode'))
                   for descriptor in process['fds'] if descriptor['socket_inode'] is not None))
            for process in inventory)
    if tables != closing_tables or coverage(processes) != coverage(closing_processes):
        raise FDSamplingRace(None, None, 'complete-coverage',
            ValueError('listener table/process socket FD coverage changed during full collection'), current_anchor)
    return result, processes, current_anchor


def _filesystem(io_ops):
    mounts = _mounts(io_ops.proc('/proc/self/mountinfo'))
    data = [mount for mount in mounts if mount['mountpoint'] == '/mnt/lima-sbquota']
    runtime = [mount for mount in mounts if mount['mountpoint'] == RUNTIME_PARENT]
    _require(len(data) == len(runtime) == 1, 'exact existing data/runtime mount required')
    data, runtime = data[0], runtime[0]
    _require(data['fs_type'] == 'xfs' and data['root'] == '/'
        and {'rw', 'prjquota'} <= set(data['options'] + data['super_options']), 'XFS hard project quota mount')
    _require(re.fullmatch(r'/dev/[A-Za-z0-9/_-]+', data['source']) is not None, 'XFS fixed device path encoding')
    device = os.stat(data['source'])
    io_ops.record.raw('filesystem-device-stat', {'source': data['source'], 'mode': device.st_mode,
        'device': device.st_dev, 'rdev': device.st_rdev, 'inode': device.st_ino})
    major, minor = [int(value) for value in data['device'].split(':')]
    _require(stat.S_ISBLK(device.st_mode) and (os.major(device.st_rdev), os.minor(device.st_rdev)) == (major, minor),
             'XFS device/mount binding')
    quota = io_ops.command(['/usr/sbin/xfs_quota', '-x', '-c', 'state -p', '/mnt/lima-sbquota']).decode()
    _require('Project quota state' in quota and re.search(r'Accounting:\s+ON\b', quota)
             and re.search(r'Enforcement:\s+ON\b', quota), 'project quota not enforced')
    uuid = io_ops.command(['/usr/sbin/blkid', '-s', 'UUID', '-o', 'value', data['source']]).decode().strip()
    mount_stat = os.lstat('/mnt/lima-sbquota')
    _require(stat.S_ISDIR(mount_stat.st_mode) and os.major(mount_stat.st_dev) == major
             and os.minor(mount_stat.st_dev) == minor, 'filesystem root identity differs')
    fs = {'mountpoint': '/mnt/lima-sbquota', 'fs_type': data['fs_type'], 'uuid': uuid,
        'major': major, 'minor': minor, 'root_inode': mount_stat.st_ino,
        'quota_enforced': bool(re.search(r'Enforcement:\s+ON\b', quota))}
    runtime_stat, vfs = os.lstat(RUNTIME_PARENT), os.statvfs(RUNTIME_PARENT)
    io_ops.record.raw('runtime-parent-observation-before-validation', {'mode': runtime_stat.st_mode,
        'uid': runtime_stat.st_uid, 'gid': runtime_stat.st_gid, 'device': runtime_stat.st_dev,
        'inode': runtime_stat.st_ino, 'size_bytes': vfs.f_blocks * vfs.f_frsize, 'inodes': vfs.f_files})
    _safe_directory(RUNTIME_PARENT, 0, True)
    parent = {'path': RUNTIME_PARENT, 'device': runtime_stat.st_dev, 'inode': runtime_stat.st_ino,
        'uid': runtime_stat.st_uid, 'gid': runtime_stat.st_gid, 'mode': stat.S_IMODE(runtime_stat.st_mode),
        'fs_type': runtime['fs_type'], 'size_bytes': vfs.f_blocks * vfs.f_frsize, 'inodes': vfs.f_files}
    io_ops.record.raw('boot-filesystem-runtime-facts', {'filesystem': fs, 'runtime_parent': parent, 'mounts': mounts})
    return fs, parent


def _unit_catalog(io_ops):
    commands = (
        ['/usr/bin/systemctl', 'list-unit-files', '--all', '--type=service', '--type=socket',
         '--no-legend', '--no-pager', '--plain'],
        ['/usr/bin/systemctl', 'list-units', '--all', '--type=service', '--type=socket',
         '--no-legend', '--no-pager', '--plain'])
    catalogs = []
    for command in commands:
        current = set()
        raw = io_ops.command(command)
        for line in raw.decode().splitlines():
            fields = line.split()
            _require(fields and re.fullmatch(r'[A-Za-z0-9_.:@\\-]+\.(service|socket)', fields[0]),
                     'complete unit catalog row encoding')
            current.add(fields[0])
        catalogs.append(current)
    installed_names, loaded_names = catalogs
    names = installed_names | loaded_names
    _require(len(names) <= 512 and set(_MASKS) <= names, 'unit catalog coverage/bound')
    templates = {name for name in names if re.fullmatch(r'[^@]+@\.(service|socket)', name)}
    _require(templates <= installed_names and not templates & loaded_names,
             'unit template catalogue provenance')
    masks, others = {}, []
    for name in sorted(names):
        if name in templates:
            io_ops.record.raw('unit-catalog-template', {
                'name': name, 'in_installed_catalog': name in installed_names,
                'in_loaded_catalog': name in loaded_names})
            continue
        value = io_ops.show(name, _CATALOG_PROPERTIES)
        io_ops.record.raw('unit-catalog-item', value)
        relevant = (name in _MASKS or name in UNIT_NAMES or re.search(r'docker|containerd|switchback.*engine', name)
                    or re.search(r'/(?:dockerd|containerd|containerd-shim[^ /;]*)\b', value.get('ExecStart', '')))
        if not relevant:
            continue
        _require(value.get('Id') == name, 'unit catalog identity')
        if name.endswith('.socket'):
            # Socket units have no MainPID property. An inactive unit has no
            # service process; the complete /proc + listener gate is independent.
            pid = int(value.get('MainPID', '0'))
        else:
            _require(value.get('MainPID', '').isdigit(), 'daemon unit MainPID missing')
            pid = int(value['MainPID'])
        if name in _MASKS:
            masks[name] = {'unit_file_state': value.get('UnitFileState'),
                           'active_state': value.get('ActiveState'), 'main_pid': pid}
        elif name in UNIT_NAMES:
            raise ValueError('exclusive new unit already present in systemd catalog')
        else:
            others.append({'unit': name, 'active_state': value.get('ActiveState'), 'main_pid': pid})
    return masks, others




def _validate_containerd_dump(effective, expected_raw, binary_version):
    """Compare the fixed policy, allowing only v2.4.1's displayed default imports.

    Empty input imports are not recursively loaded by containerd's loader even
    though merging into dump defaults retains the initial nonempty list.
    """
    expected = _fixed_toml(expected_raw)
    _require(binary_version == '2.4.1' and type(expected.get('imports')) is list
             and expected['imports'] == [], 'fixed containerd imports/version')
    _require(type(effective) is dict, 'containerd dump object')
    imports = effective.get('imports')
    _require(type(imports) is list and imports in ([], ['/etc/containerd/conf.d/*.toml']),
             'containerd dump imports')
    plugins = effective.get('plugins')
    _require(type(plugins) is dict and all(type(plugins.get(plugin)) is dict
             for plugin in expected['plugins']), 'containerd dump plugins')
    _require(all(type(effective.get(key)) is type(wanted) and effective[key] == wanted
                 for key, wanted in expected.items() if key not in ('plugins', 'imports'))
        and all(all(type(plugins[plugin].get(field)) is type(wanted)
                    and plugins[plugin][field] == wanted for field, wanted in values.items())
                for plugin, values in expected['plugins'].items()),
        'installed containerd config parser effective values differ')


class _GuestOperations:
    def __init__(self, binding, vm, origin, deadline, files, manifest):
        self.binding, self.vm, self.origin, self.deadline = binding, vm, origin, deadline
        self.files, self.manifest, self.before, self.installed = files, manifest, None, []
        self.binary_hashes, self.live, self.engine, self.networks = [], None, None, []
        self.stage_owned = False

    def emit(self, observation):
        raw = _encode({'kind': 'observation', 'value': observation}) + b'\n'
        _require(len(raw) <= _FRAME, 'guest observation frame bound')
        _emit_bytes(raw, self.deadline)

    def _boundary(self, io_ops):
        boot = io_ops.boot()
        filesystem, parent = _filesystem(io_ops)
        _require(boot == self.before['boot_id'] and filesystem == self.before['filesystem']
                 and parent == self.before['runtime_parent'], 'boot/filesystem/tmpfs identity drift')

    def _preflight(self, io_ops):
        _require(os.geteuid() == 0 and sys.platform == 'linux', 'fixed guest requires Linux root')
        boot = io_ops.boot()
        filesystem, parent = _filesystem(io_ops)
        store = _scan_store(check=io_ops.check, observe=io_ops.record.raw,
                            expected_filesystem=filesystem)
        store_inodes = set()
        for entry in store['entries']:
            value = os.lstat(entry['path'])
            store_inodes.add((value.st_dev, value.st_ino))
        listeners, processes = _collect_listeners_with_inventory(io_ops, 'preflight', [], store_inodes)
        writers = []
        io_ops.record.raw('store-writer-observations', writers)
        masks, others = _unit_catalog(io_ops)
        binaries = _collect_binaries(io_ops)
        targets = []
        for path in ('/etc/switchback/m1-engine', str(_STAGE), RUNTIME_ROOT,
                     FILES[UNIT_NAMES[0]][0], FILES[UNIT_NAMES[1]][0]):
            _safe_directory(Path(path).parent, 0,
                            observe=lambda value: io_ops.record.raw('target-parent-before-validation', value))
            targets.append({'path': path, 'exists': os.path.lexists(path), 'parents_safe': True})
        core = {'cgroup': CORE_CGROUP}
        for leaf, key in (('cpu.max', 'cpu_max'), ('memory.max', 'memory_max'),
                          ('memory.swap.max', 'memory_swap_max'), ('pids.max', 'pids_max')):
            raw = io_ops.proc('/sys/fs/cgroup' + CORE_CGROUP + '/' + leaf, 256).decode().strip()
            core[key] = raw if key == 'cpu_max' else int(raw)
        # A single observed hierarchy node is charged once. No child/worker
        # allowance or additional aggregate is introduced by this executor.
        hierarchy = [mount for mount in _mounts(io_ops.proc('/proc/self/mountinfo'))
                     if mount['mountpoint'] == '/sys/fs/cgroup' and mount['fs_type'] == 'cgroup2']
        _require(len(hierarchy) == 1 and hierarchy[0]['root'] == '/', 'one unified core accounting hierarchy required')
        core_stat = os.lstat('/sys/fs/cgroup' + CORE_CGROUP)
        accounting_nodes = {(core_stat.st_dev, core_stat.st_ino): copy.deepcopy(core)}
        core['accounted_once'] = len(accounting_nodes) == 1 and stat.S_ISDIR(core_stat.st_mode)
        result = {'schema': 'switchback.engine.preflight.v1', 'boot_id': boot,
            'current_boot_id': io_ops.boot(), 'vm': self.vm, 'filesystem': filesystem,
            'core': core, 'masks': masks, 'unit_catalog_complete': True, 'other_units': others,
            'process_scan_complete': True, 'writers': writers, 'targets': targets,
            'runtime_parent': parent, 'binaries': binaries, 'store': store, 'listeners': listeners}
        io_ops.record.raw('preflight-before-validation', result)
        validate_preflight_v2(result)
        filesystem_after, parent_after = _filesystem(io_ops)
        _require(filesystem_after == filesystem and parent_after == parent and io_ops.boot() == boot,
                 'preflight filesystem/boot drift')
        self.before, self.binary_hashes = result, [item['sha256'] for item in binaries]
        return result

    def _mkdir(self, path, io_ops):
        path = Path(path)
        _safe_directory(path.parent, 0)
        io_ops.check()
        effect = 'created_directory:' + str(path)
        io_ops.record.attempt(effect)
        parent = os.open(str(path.parent), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.mkdir(path.name, 0o700, dir_fd=parent)
            if path == _STAGE:
                self.stage_owned = True
            io_ops.record.known(effect)
            io_ops.check()
            durable = 'directory_fsync:' + str(path)
            io_ops.record.attempt(durable)
            os.fsync(parent)
            io_ops.record.known(durable)
        finally:
            os.close(parent)
        _safe_directory(path, 0, True)

    def _installed(self, io_ops):
        records = []
        for name, (target, mode) in FILES.items():
            raw, value = io_ops.read(target, 16384, mode)
            _require(raw == self.files[name] and value.st_gid == 0, 'installed candidate bytes/owner drift')
            records.append({'source_path': SOURCE_PREFIX + name, 'target_path': target,
                'sha256': _sha(raw), 'mode': stat.S_IMODE(value.st_mode), 'uid': value.st_uid,
                'gid': value.st_gid, 'bytes': len(raw)})
        validate_configuration({name: io_ops.read(target, 16384, mode)[0]
                                for name, (target, mode) in FILES.items()})
        return records

    def _effective(self, io_ops):
        configuration = {name: io_ops.read(target, 16384, mode)[0]
                         for name, (target, mode) in FILES.items()}
        validate_configuration(configuration)
        service = {'Type': 'notify', 'User': 'root', 'Group': 'root', 'UMask': '0077',
            'Slice': 'switchback-core.slice', 'Delegate': 'yes', 'Restart': 'no',
            'TimeoutStartUSec': '30s', 'TimeoutStopUSec': '10s', 'KillMode': 'control-group',
            'RuntimeMaxUSec': 'infinity', 'WatchdogUSec': 'infinity', 'DropInPaths': '',
            'LoadState': 'loaded', 'UnitFileState': 'static'}
        for index, unit in enumerate(UNIT_NAMES):
            props = (*service, 'Id', 'FragmentPath', 'ExecStart', 'Requires', 'Requisite', 'After',
                     'ActiveState', 'MainPID')
            observed = io_ops.show(unit, props)
            io_ops.record.raw('effective-unit-before-validation', observed)
            _require(all(observed.get(key) == expected for key, expected in service.items())
                and observed.get('Id') == unit and observed.get('FragmentPath') == FILES[unit][0]
                and observed.get('ActiveState') == 'inactive' and observed.get('MainPID') == '0',
                'effective unit lifecycle/permissions/fragment policy differs')
            match = re.fullmatch(r'\{ path=([^ ;]+) ; argv\[\]=(.*?) ; ignore_errors=no ; .*\}',
                                 observed.get('ExecStart', ''))
            expected = shlex.split(_unit(self.files[unit])['Service']['ExecStart'])
            _require(match is not None and match.group(1) == expected[0]
                     and shlex.split(match.group(2)) == expected, 'effective ExecStart differs')
            if index == 1:
                _require(observed.get('Requisite', '').split() == [UNIT_NAMES[0]]
                    and UNIT_NAMES[0] in observed.get('After', '').split()
                    and UNIT_NAMES[0] not in observed.get('Requires', '').split(), 'Engine dependency auto-start policy')
            else:
                _require(not observed.get('Requisite'), 'unexpected containerd Requisite')
        return configuration

    def _listener_gate(self, io_ops):
        self._boundary(io_ops)
        value = _collect_listeners(io_ops, 'engine_ready', self.binary_hashes)
        if self.live is not None:
            _require(value['daemons'] == self.live['daemons'] and value['sockets'] == self.live['sockets']
                and value['socket_parents'] == self.live['socket_parents'],
                     'current preconnection process/socket identity drift')
        return value

    def _request(self, io_ops, method, path, body=None):
        _require((method == 'GET' and path in ('/info', '/version', '/containers/json?all=1',
            '/volumes', '/plugins') or method == 'GET' and re.fullmatch(r'/networks/[a-zA-Z0-9_-]+', path)
            or method == 'POST' and path == '/networks/create'), 'unrecognized fixed Engine request')
        before = self._listener_gate(io_ops)
        raw_body = _encode(body) if body is not None else None
        effect = None
        if method == 'POST':
            _require(body['Name'] in dict(NETWORKS), 'fixed NetworkCreate name')
            effect = 'network_create:' + body['Name']
            io_ops.record.attempt(effect)
        connection = http.client.HTTPConnection('localhost')
        wire_socket = [None]
        # Never use HTTPConnection's default TCP connection implementation.
        def connect():
            io_ops.check()
            last = next(s for s in before['sockets'] if s['path'] == ENGINE_SOCKET.removeprefix('unix://'))
            value, parent = os.lstat(last['path']), os.lstat(last['parent_path'])
            _require((value.st_dev, value.st_ino, stat.S_IMODE(value.st_mode), value.st_uid, value.st_gid)
                == (last['device'], last['inode'], last['mode'], last['uid'], last['gid'])
                and (parent.st_dev, parent.st_ino, stat.S_IMODE(parent.st_mode))
                == (last['parent_device'], last['parent_inode'], last['parent_mode']), 'socket drift immediately before connect')
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            try:
                sock.settimeout(max(.001, self.deadline - time.monotonic()))
                sock.connect(ENGINE_SOCKET.removeprefix('unix://'))
            except BaseException:
                sock.close()
                raise
            wire_socket[0] = _DeadlineSocket(sock, self.deadline)
            connection.sock = wire_socket[0]
        connection.connect = connect
        status, response_raw, error, response = None, bytearray(), None, None
        io_ops.record.raw('engine-request-attempt', {'method': method, 'path': _API + path, 'body': body})
        try:
            io_ops.check()
            connection.request(method, _API + path, body=raw_body,
                headers={'Host': 'localhost', 'Connection': 'close', 'Content-Type': 'application/json'})
            io_ops.check()
            response = connection.getresponse()
            status = response.status
            _require(len(response.getheaders()) <= 64 and len(str(response.getheaders()).encode()) <= 16384,
                     'Engine HTTP header bound')
            while True:
                io_ops.check()
                chunk = response.read1(min(65536, (1 << 20) + 1 - len(response_raw)))
                if not chunk:
                    break
                response_raw.extend(chunk)
                _require(len(response_raw) <= 1 << 20, 'Engine HTTP output exceeded bound')
            io_ops.check()
        except BaseException as caught:
            error = caught
            raise
        finally:
            if response is not None:
                response.close()
            connection.close()
            io_ops.record.raw('engine-response-before-validation', {'method': method, 'path': path,
                'status': status, 'raw': bytes(response_raw),
                'wire_raw': bytes(wire_socket[0].received) if wire_socket[0] else b'',
                'error': str(error) if error else None})
        # A completed create status201 confirms an effect, even if the later ID
        # projection or postidentity gate fails. Unknown outcomes are never read
        # back or retried. Non-201 mutation outcomes remain explicitly unknown.
        if effect and status == 201:
            io_ops.record.known(effect)
        elif effect:
            raise _Failure('NetworkCreate did not confirm201; effects unknown, no retry', True, None)
        after = self._listener_gate(io_ops)
        _require(before['daemons'] == after['daemons'] and before['sockets'] == after['sockets']
                 and before['socket_parents'] == after['socket_parents'],
                 'identity changed around Engine response')
        return status, bytes(response_raw)

    def _engine(self, io_ops):
        def get(path, array=False):
            code, raw = self._request(io_ops, 'GET', path)
            _require(code == 200, 'Engine read-only inventory failed')
            if array:
                # Reuse the strict duplicate-key JSON decoder without dropping
                # array or duplicate-key evidence through a permissive decoder.
                value = _json(b'{"rows":' + raw + b'}')['rows']
                _require(type(value) is list and len(value) <= 8192, 'Engine list bound')
                return value
            return _json(raw)
        info, version = get('/info'), get('/version')
        containers, volumes, plugins = get('/containers/json?all=1', True), get('/volumes'), get('/plugins', True)
        io_ops.record.raw('engine-info-inventory-before-validation', {
            'info': info, 'version': version, 'containers': containers, 'volumes': volumes, 'plugins': plugins})
        _require(version.get('Version') == '29.9.0' and version.get('ApiVersion') == '1.56'
            and info.get('Containers') == 0 and containers == [] and plugins == []
            and volumes.get('Volumes') in ([], None) and volumes.get('Warnings') in ([], None),
            'Engine version/business inventory differs')
        # DriverStatus is an independent actual indication, not a fabricated
        # snapshotter=false boolean supplied by an operator or test seam.
        storage_status = info.get('DriverStatus')
        _require(type(storage_status) is list and all(type(row) is list and len(row) == 2 for row in storage_status),
                 'Engine driver status missing')
        _require(not any(row[0] == 'driver-type' and 'snapshotter' in str(row[1]) for row in storage_status),
                 'containerd snapshotter enabled')
        daemon_json = _json(io_ops.read(FILES['daemon.json'][0], 16384, 0o600)[0])
        engine_id, _ = io_ops.read(Path(DATA_ROOT) / 'engine-id', 128)
        _require(info.get('Driver') == 'overlay2' and info.get('DockerRootDir') == DATA_ROOT
            and info.get('CgroupDriver') == 'systemd' and info.get('CgroupVersion') == '2'
            and info.get('NCPU') == 4 and info.get('LiveRestoreEnabled') is False
            and engine_id.decode() == info.get('ID')
            and daemon_json['features']['containerd-snapshotter'] is False,
            'Engine effective root/driver/cgroup/resources differs')
        return {'id': info['ID'], 'version': version['Version'], 'data_root': info['DockerRootDir'],
            'storage_driver': info['Driver'], 'containerd_snapshotter': daemon_json['features']['containerd-snapshotter'],
            'cgroup_parent': daemon_json['cgroup-parent'], 'containers': len(containers),
            'volumes': len(volumes.get('Volumes') or []), 'plugins': len(plugins)}

    def call(self, name, deadline, record):
        _require(deadline == self.deadline, 'guest original deadline changed')
        io_ops = _IO(deadline, record)
        if name == 'preflight':
            return self._preflight(io_ops)
        if name == 'claim_stage':
            self._boundary(io_ops)
            # Last read-only store/listener gate immediately before the first
            # runtime mutation. Never delete old state to make it acceptable.
            _require(_scan_store(check=io_ops.check, observe=record.raw,
                                 expected_filesystem=self.before['filesystem']) == self.before['store'],
                     'store changed before exclusive claim')
            _collect_listeners(io_ops, 'preflight', [])
            self._mkdir(_STAGE, io_ops)
            claim = {'schema': 'switchback.engine.stage_claim.v1', 'binding': self.binding,
                'boot_id': self.before['boot_id'], 'filesystem': self.before['filesystem'],
                'binaries': self.before['binaries'], 'store_inventory_sha256': _sha(_encode(self.before['store'])),
                'configuration': {name: _sha(raw) for name, raw in self.files.items()},
                'origin_monotonic': self.origin, 'deadline_monotonic': self.deadline}
            record.attempt('stage_claim_written')
            _exclusive(_STAGE / 'claim.json', _encode(claim), 0o600, 0, io_ops.check)
            record.known('stage_claim_written')
            return claim
        if name == 'install_files':
            self._boundary(io_ops)
            self._mkdir('/etc/switchback/m1-engine', io_ops)
            for path in (RUNTIME_ROOT, DOCKER_RUNTIME_ROOT, DOCKER_RUNTIME_ROOT + '/extensions',
                         DOCKER_RUNTIME_ROOT + '/libnetwork'):
                self._mkdir(path, io_ops)
            for leaf, (target, mode) in FILES.items():
                effect = 'installed_file:' + target
                record.attempt(effect)
                _exclusive(Path(target), self.files[leaf], mode, 0, io_ops.check)
                record.known(effect)
                # Preserve each individual successful install before a later
                # install/readback can fail; partial file records stay truthful.
                observed, value = io_ops.read(target, 16384, mode)
                partial = {'source_path': SOURCE_PREFIX + leaf, 'target_path': target,
                    'sha256': _sha(observed), 'mode': stat.S_IMODE(value.st_mode),
                    'uid': value.st_uid, 'gid': value.st_gid, 'bytes': len(observed)}
                self.installed.append(partial)
                record.raw('individual-installed-file', partial)
                record.partial('installed_files', self.installed)
            return self._installed(io_ops)
        if name == 'daemon_reload':
            # These config commands are executed only in explicitly authorized
            # actual. self-check never invokes an installed daemon executable.
            io_ops.command(['/usr/bin/dockerd', '--validate', '--config-file=' + FILES['daemon.json'][0]])
            dumped = io_ops.command(['/usr/bin/containerd', '--config', FILES['containerd.toml'][0], 'config', 'dump'])
            import tomllib  # Guest Python3.12; never required for host Python3.9.
            effective = tomllib.loads(dumped.decode())
            # The preflight record already binds installed bytes to the fixed
            # Debian archive and binary version; do not infer a version from dump.
            binaries = [item for item in self.before['binaries'] if item['path'] == '/usr/bin/containerd']
            _require(len(binaries) == 1, 'containerd preflight binary identity')
            _validate_containerd_dump(effective, self.files['containerd.toml'], binaries[0]['version'])
            record.attempt('systemd_daemon_reload')
            io_ops.command(['/usr/bin/systemctl', 'daemon-reload'])
            record.known('systemd_daemon_reload')
            return None
        if name == 'effective_configuration':
            self._boundary(io_ops)
            return self._effective(io_ops)
        if name in ('containerd_start', 'engine_start'):
            self._boundary(io_ops)
            index = 0 if name == 'containerd_start' else 1
            if index == 0:
                _collect_listeners(io_ops, 'preflight', [])
            else:
                current = _collect_listeners(io_ops, 'containerd_ready', self.binary_hashes)
                _require(current['daemons'] == self.live['daemons'] and current['sockets'] == self.live['sockets']
                    and current['socket_parents'] == self.live['socket_parents'],
                         'containerd changed before single Engine Start')
            effect = name + '_once'
            record.attempt(effect)
            possible = (['containerd_metadata_writes_possible'] if index == 0 else
                        ['engine_metadata_writes_gc_possible', 'guest_bridge_nat_forwarding'])
            for item in possible:
                record.attempt(item)
            io_ops.command(['/usr/bin/systemctl', 'start', UNIT_NAMES[index]])
            record.known(effect)
            for item in possible:
                record.known(item)
            return None
        if name == 'containerd_ready':
            self._boundary(io_ops)
            self.live = _collect_listeners(io_ops, name, self.binary_hashes)
            return self.live
        if name == 'engine_ready':
            self._boundary(io_ops)
            # Replace the1-daemon baseline only after validating the complete
            #2-daemon inventory. No API connection precedes this point.
            self.live = _collect_listeners(io_ops, name, self.binary_hashes)
            record.partial_listeners(self.live)
            self.engine = self._engine(io_ops)
            record.known('engine_metadata_writes_gc_possible')
            return {'listeners': self.live, 'engine': self.engine}
        if name == 'networks':
            self.networks = _prepare_networks(lambda method, path, body=None:
                self._request(io_ops, method, path, body), lambda value: record.partial('networks', value))
            return self.networks
        if name == 'final_identity':
            self._boundary(io_ops)
            live = self._listener_gate(io_ops)
            engine = self._engine(io_ops)
            networks = []
            for index, network in enumerate(self.networks):
                status, raw = self._request(io_ops, 'GET', '/networks/' + network['id'])
                _require(status == 200, 'final network readback failed')
                networks.append(_network_value(raw, index))
            self._boundary(io_ops)
            return {'boot_id': io_ops.boot(), 'filesystem': self.before['filesystem'],
                'runtime_parent': self.before['runtime_parent'], 'daemons': live['daemons'],
                'sockets': live['sockets'], 'socket_parents': live['socket_parents'], 'engine': engine, 'networks': networks,
                'installed_files': self._installed(io_ops)}
        if name == 'write_receipt':
            candidate = copy.deepcopy(record.outcome['receipt'])
            candidate['status'] = 'unknown'
            candidate['elapsed_ms'] = max(0, int((time.monotonic() - self.origin) * 1000))
            candidate['steps'][-1]['finished_ms'] = candidate['elapsed_ms']
            candidate['known_effects'] = list(dict.fromkeys(e for s in candidate['steps'] for e in s['known_effects']))
            candidate['unknown_effects'] = []
            raw = _encode(candidate)
            record.attempt('prepared_receipt_candidate_written')
            _exclusive(_STAGE / 'prepared-receipt.json', raw, 0o600, 0, io_ops.check)
            record.known('prepared_receipt_candidate_written')
            return {'candidate_path': str(_STAGE / 'prepared-receipt.json'), 'sha256': _sha(raw)}
        raise ValueError('unknown fixed guest phase')

    def publish(self, receipt, deadline, record):
        io_ops = _IO(deadline, record)
        live = self._listener_gate(io_ops)
        _require(live['daemons'] == receipt['daemons'] and live['sockets'] == receipt['sockets']
            and live['socket_parents'] == receipt['socket_parents']
            and self._installed(io_ops) == receipt['installed_files'], 'identity/config changed before receipt publication')
        record.attempt('operational_receipt_publication')
        _exclusive(Path(RECEIPT_PATH), _encode(receipt), 0o600, 0, io_ops.check)
        record.known('operational_receipt_publication')

    def preserve(self, outcome):
        # A final audit is evidence-only and may occur after the deadline.
        # Do not manufacture identities from partially installed/raw records.
        outcome['partial_installed_observations'] = self.installed
        if self.stage_owned:
            try:
                _exclusive(_STAGE / 'outcome.json', _encode(outcome), 0o600, 0)
            except BaseException as error:
                outcome['guest_audit_error'] = str(error)
                outcome['status'] = 'unknown'


def _read_control(sample, started_ns):
    """One envelope then EOF; no arbitrary timeout/path/command from the host."""
    deadline = (started_ns + BUDGET_SECONDS * 1000000000) / 1000000000
    raw, selector = bytearray(), selectors.DefaultSelector()
    descriptor = sys.stdin.fileno()
    os.set_blocking(descriptor, False)
    selector.register(descriptor, selectors.EVENT_READ)
    try:
        while True:
            _guard(deadline)
            if not selector.select(min(.1, max(0, deadline - time.monotonic()))):
                continue
            data = os.read(descriptor, 4096)
            if not data:
                break
            raw.extend(data)
            _require(len(raw) <= 8192, 'control envelope bound')
        _require(raw.count(b'\n') == 1 and raw.endswith(b'\n'), 'one control envelope and EOF required')
        control = _json(bytes(raw[:-1]))
        _object(control, ('schema', 'sample_ns', 'deadline_ns', 'binding', 'vm'), 'fixed clock control')
        _object(control['binding'], _BINDING, 'host canonical bundle binding')
        for value in control['binding'].values():
            _digest(value)
        now = time.monotonic_ns()
        _require(control['schema'] == 'switchback.engine.clock_control.v1'
            and type(control['sample_ns']) is int and control['sample_ns'] == sample
            and type(control['deadline_ns']) is int
            and now < control['deadline_ns'] <= sample + BUDGET_SECONDS * 1000000000,
            'clock identity/nonpositive/extended deadline')
        _require(control['vm'] == {'name': 'switchback-core', 'vcpus': 4, 'memory_bytes': 10737418240},
                 'host VM geometry binding')
        return control
    finally:
        selector.close()


def _load_guest_inputs(control):
    """Verify canonical bundle/kit only under the one host-derived deadline."""
    from bundle import verify_bundle
    deadline = control['deadline_ns'] / 1000000000
    check = lambda: _guard(deadline)
    check()
    manifest = verify_bundle(_BUNDLE, _KIT, check=check)
    raw, _ = _read(_BUNDLE / 'manifest.json', 1 << 20, 0, check, 0o600)
    binding = {'bundle_manifest_sha256': _sha(raw),
               **{key: manifest[key] for key in _BINDING if key != 'bundle_manifest_sha256'}}
    _require(binding == control['binding'], 'guest verified bundle differs from host canonical binding')
    files = {name: _read(_BUNDLE / 'source' / SOURCE_PREFIX / name, 16384, 0, check)[0]
             for name in FILES}
    validate_configuration(files)
    check()
    return manifest, binding, files


def _entry():
    """Private fixed transport entry. Never called by imports or self-check."""
    started_ns = time.monotonic_ns()
    handshake_deadline = started_ns / 1000000000 + BUDGET_SECONDS
    _require(os.geteuid() == 0 and sys.platform == 'linux', 'private entry requires Linux root')
    # No source enumeration/read precedes clock control. The first sample has
    # no claimed identity; the one host binding is proved by the verifier below.
    sample = started_ns
    _emit_bytes(_encode({'schema': 'switchback.engine.clock_sample.v1',
                        'monotonic_ns': sample}) + b'\n', handshake_deadline)
    control = _read_control(sample, started_ns)
    deadline = control['deadline_ns'] / 1000000000
    origin = deadline - BUDGET_SECONDS
    manifest, binding, files = _load_guest_inputs(control)
    operations = _GuestOperations(binding, control['vm'], origin, deadline, files, manifest)
    outcome = _run_window(operations, time.monotonic)
    compact = {key: value for key, value in outcome.items() if key != 'observations'}
    compact['observation_stream'] = {'count': len(outcome['observations']),
                                    'sha256': _sha(_encode(outcome['observations']))}
    raw = _encode({'kind': 'outcome', 'value': compact}) + b'\n'
    _require(len(raw) <= _FRAME, 'final guest outcome frame bound')
    _emit_bytes(raw, deadline)
    raise SystemExit(0 if outcome['status'] == 'engine_ready' else 1)
