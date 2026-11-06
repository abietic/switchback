#!/usr/bin/env python3
"""One finite preparation window. Only `actual` constructs production adapters.

self-check is static: configuration/schema/AST only, without guest, subprocess,
socket, installed daemon validator, environment mutation or service discovery.
"""
import ast
import base64
import copy
import errno
import hashlib
import json
import os
import pwd
from pathlib import Path
import selectors
import signal
import stat
import subprocess
import sys
import time

from contract import (BUDGET_SECONDS, FILES, STEP_NAMES, _json, _object, _require,
                      validate_configuration, validate_listeners_v2, validate_preflight_v2,
                      validate_receipt_v2)

_OUTPUT = 16384
_UNIT_CATALOG_COMMANDS = frozenset((
    ('/usr/bin/systemctl', 'list-unit-files', '--all', '--type=service', '--type=socket',
     '--no-legend', '--no-pager', '--plain'),
    ('/usr/bin/systemctl', 'list-units', '--all', '--type=service', '--type=socket',
     '--no-legend', '--no-pager', '--plain'),
))
_UNIT_CATALOG_STDOUT = 262144
_FRAME = 1 << 20
_AUDIT_BYTES = 64 << 20
_LIMA = '/Volumes/SwitchbackCore/toolchain/lima-2.2.1/bin/limactl'
_LIMA_HOME = '/Volumes/SwitchbackCore/lima'
_VM = 'switchback-core'
_VM_CONFIG = _LIMA_HOME + '/' + _VM + '/lima.yaml'
_GUEST_ENTRY = ("import sys;sys.path.insert(0,'/opt/switchback/engine-bundle/source/"
                "deploy/experience/engine');from guest import _entry;_entry()")
_AUTHORITY = ('runtime_qualification', 'capacity_ready', 'execution_enabled', 'paid_consumed')
_BINDING = ('bundle_manifest_sha256', 'kit_manifest_sha256', 'source_root', 'templates_root')


def _sha(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


def _host_home():
    # HOME/PYTHONPATH/DOCKER_HOST environment overrides do not select inputs.
    return Path(pwd.getpwuid(os.getuid()).pw_dir)


def _encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':'), allow_nan=False).encode()


def _wire(value):
    if type(value) is bytes:
        return {'bytes': len(value), 'sha256': _sha(value), 'base64': base64.b64encode(value).decode()}
    if type(value) is dict:
        return {str(key): _wire(item) for key, item in value.items()}
    if type(value) in (list, tuple):
        return [_wire(item) for item in value]
    return value


def _text(value):
    raw = value if type(value) is bytes else str(value).encode()
    # Raw observations retain bytes; the finite receipt is a UTF-8 display field.
    return raw.decode(errors='replace').replace('\x00', '\\x00').encode()[:_OUTPUT].decode(errors='ignore')


class _Failure(RuntimeError):
    def __init__(self, message, unknown=False, exit_code=1):
        super().__init__(message)
        self.unknown, self.exit_code = unknown, exit_code


def _guard(deadline, monotonic=time.monotonic):
    if monotonic() >= deadline:
        raise _Failure('original120second preparation deadline; no further operation', True, None)


class _Record:
    """Records partial raw observations before validation or typed projection."""
    def __init__(self, outcome, step, emit):
        self.outcome, self.step, self.emit = outcome, step, emit
        self.pending = set()

    def raw(self, kind, value):
        observation = {'step': self.step['name'], 'kind': kind, 'value': _wire(value)}
        raw = _encode(observation)
        previous_bytes = self.outcome.get('observation_bytes', 0)
        oversized = len(raw) > _FRAME or previous_bytes + len(raw) > _AUDIT_BYTES
        if oversized:
            observation = {'step': self.step['name'], 'kind': kind, 'truncated': True,
                           'bytes': len(raw), 'sha256': _sha(raw),
                           'prefix_base64': base64.b64encode(raw[:_OUTPUT]).decode()}
        self.outcome['observations'].append(observation)
        self.outcome['observation_bytes'] = previous_bytes + len(_encode(observation))
        self.emit(observation)
        if oversized:
            raise _Failure('raw observation exceeded finite frame bound', True, None)

    def attempt(self, effect):
        self.pending.add(effect)
        self.raw('effect-attempted', effect)

    def known(self, effect):
        self.pending.discard(effect)
        if effect not in self.step['known_effects']:
            self.step['known_effects'].append(effect)
        self.raw('effect-known', effect)

    def command(self, value):
        self.raw('command-result', value)
        self.step['stdout'] = _text(self.step['stdout'] + _text(value.get('stdout', '')))
        self.step['stderr'] = _text(self.step['stderr'] + _text(value.get('stderr', '')))
        code = value.get('exit_code')
        if value.get('timed_out') or value.get('truncated') or code is None:
            raise _Failure('bounded operation timeout/truncation/unknown exit', True, code)
        if type(code) is not int or code != 0:
            raise _Failure('fixed operation returned nonzero; no retry', bool(self.pending), code)

    def partial(self, key, observed):
        """Keep valid partial identities without trusting rejected observations."""
        _require(key in ('installed_files', 'daemons', 'sockets', 'networks'), 'partial identity field')
        self.raw('partial-' + key, observed)
        candidate = copy.deepcopy(self.outcome['receipt'])
        candidate[key] = copy.deepcopy(observed)
        candidate['elapsed_ms'] = max(candidate['elapsed_ms'], candidate['steps'][-1]['finished_ms'])
        _validate_partial(candidate)
        self.outcome['receipt'][key] = candidate[key]

    def partial_listeners(self, observed):
        """Atomically retain the latest valid listener sample before API proof."""
        self.raw('partial-listeners', observed)
        validate_listeners_v2(observed)
        _require(self.step['name'] in ('containerd_ready', 'engine_ready')
            and observed['phase'] == self.step['name']
            and observed['boot_id'] == self.outcome['receipt']['boot_id'],
                 'partial listener phase/boot mismatch')
        candidate = copy.deepcopy(self.outcome['receipt'])
        keys = ('daemons', 'sockets', 'socket_parents')
        for key in keys:
            candidate[key] = copy.deepcopy(observed[key])
        _validate_partial(candidate)
        self.outcome['receipt'].update({key: candidate[key] for key in keys})


def _empty_receipt(binding):
    _object(binding, _BINDING, 'verified bundle binding')
    value = {'schema': 'switchback.engine.deployment_receipt.v2', 'status': 'unknown',
             **binding, 'boot_id': None, 'filesystem': None, 'runtime_parent': None,
             'daemons': [], 'installed_files': [], 'sockets': [], 'socket_parents': [], 'engine': None,
             'networks': [], 'steps': [], 'elapsed_ms': 0, 'known_effects': [],
             'unknown_effects': [], **dict.fromkeys(_AUTHORITY, False)}
    return value


def _project(name, observed, receipt):
    """Validate first; rejected values remain exclusively in the raw audit."""
    if name == 'preflight':
        validate_preflight_v2(observed)
        for key in ('boot_id', 'filesystem', 'runtime_parent'):
            receipt[key] = copy.deepcopy(observed[key])
    elif name == 'install_files':
        receipt['installed_files'] = copy.deepcopy(observed)
    elif name == 'effective_configuration':
        validate_configuration(observed)
    elif name in ('containerd_ready', 'engine_ready'):
        facts = observed if name == 'containerd_ready' else observed['listeners']
        validate_listeners_v2(facts)
        _require(facts['phase'] == name and facts['boot_id'] == receipt['boot_id'], 'ready phase/boot mismatch')
        for key in ('daemons', 'sockets', 'socket_parents'):
            receipt[key] = copy.deepcopy(facts[key])
        if name == 'engine_ready':
            receipt['engine'] = copy.deepcopy(observed['engine'])
    elif name == 'networks':
        receipt['networks'] = copy.deepcopy(observed)
    elif name == 'final_identity':
        _object(observed, ('boot_id', 'filesystem', 'runtime_parent', 'daemons', 'sockets', 'socket_parents',
                          'engine', 'networks', 'installed_files'), 'final identity')
        _require(all(observed[key] == receipt[key] for key in observed), 'final live identity/config drift')


def _validate_partial(receipt):
    value = copy.deepcopy(receipt)
    value['status'] = value['steps'][-1]['status'] = 'unknown'
    value['steps'][-1]['exit_code'] = None
    value['known_effects'] = list(dict.fromkeys(e for step in value['steps'] for e in step['known_effects']))
    value['unknown_effects'] = list(dict.fromkeys(e for step in value['steps'] for e in step['unknown_effects']))
    validate_receipt_v2(value)


def _run_window(ops, monotonic):
    """Real guest control flow, injectable only through this private test seam.

    The guest adapter receives an absolute deadline once. No failure path retries,
    resumes, probes after deadline, or stops a remote daemon.
    """
    origin = getattr(ops, 'origin', monotonic())
    deadline = getattr(ops, 'deadline', origin + BUDGET_SECONDS)
    outcome = {'schema': 'switchback.engine.preparation_outcome.v1', 'status': 'unknown',
               'receipt': _empty_receipt(ops.binding), 'observations': [], 'publication': None,
               'error': None, **dict.fromkeys(_AUTHORITY, False)}
    receipt = outcome['receipt']
    elapsed = lambda: max(0, int((monotonic() - origin) * 1000))
    current, record = None, None
    try:
        _require(origin < deadline <= origin + BUDGET_SECONDS, 'fixed window bound')
        for name in STEP_NAMES:
            current = {'name': name, 'status': 'unknown', 'started_ms': elapsed(),
                       'finished_ms': elapsed(), 'exit_code': None, 'stdout': '', 'stderr': '',
                       'known_effects': [], 'unknown_effects': []}
            receipt['steps'].append(current)
            record = _Record(outcome, current, getattr(ops, 'emit', lambda _: None))
            _guard(deadline, monotonic)
            observed = ops.call(name, deadline, record)
            record.raw('step-observation', observed)
            _guard(deadline, monotonic)
            # Project into a copy so a failed DTO validation cannot leak malformed
            # owner/network/API observations into trusted receipt identity fields.
            candidate = copy.deepcopy(receipt)
            _project(name, observed, candidate)
            candidate['elapsed_ms'] = elapsed()
            candidate['steps'][-1]['finished_ms'] = candidate['elapsed_ms']
            _validate_partial(candidate)
            for key in ('boot_id', 'filesystem', 'runtime_parent', 'daemons', 'sockets', 'socket_parents',
                        'engine', 'networks', 'installed_files'):
                receipt[key] = candidate[key]
            if record.pending:
                raise _Failure('unconfirmed effect remains; no continuation', True, None)
            current.update(status='succeeded', exit_code=0, finished_ms=elapsed())
            receipt['elapsed_ms'] = elapsed()
        receipt['status'] = 'engine_ready'
        receipt['known_effects'] = list(dict.fromkeys(e for s in receipt['steps'] for e in s['known_effects']))
        receipt['unknown_effects'] = []
        validate_receipt_v2(receipt)
        _guard(deadline, monotonic)
        publication = {'name': 'receipt_publication', 'status': 'unknown', 'started_ms': elapsed(),
                       'finished_ms': None, 'exit_code': None, 'known_effects': [], 'unknown_effects': []}
        outcome['publication'] = publication
        record = _Record(outcome, publication, getattr(ops, 'emit', lambda _: None))
        ops.publish(copy.deepcopy(receipt), deadline, record)
        _guard(deadline, monotonic)
        _require(not record.pending, 'unconfirmed final receipt publication')
        publication.update(status='succeeded', exit_code=0, finished_ms=elapsed())
        outcome['status'] = 'engine_ready'
    except BaseException as error:
        unknown = (isinstance(error, (TimeoutError, OSError)) or getattr(error, 'unknown', False)
                   or bool(record and record.pending))
        status = 'unknown' if unknown else 'failed'
        outcome['status'], outcome['error'] = status, _text(error)
        publishing = record is not None and record.step is outcome['publication']
        if not publishing:
            receipt['status'] = status
        else:
            # Keep all12 actual prepared-candidate step results unchanged.
            # Publication is not one of those steps and cannot rewrite history.
            outcome['receipt_role'] = 'prepared_body_not_publication_attestation'
            outcome['publication'].update(status=status, finished_ms=elapsed(),
                exit_code=getattr(error, 'exit_code', None if unknown else 1))
        if current is not None and not publishing:
            current.update(status=status, exit_code=getattr(error, 'exit_code', None if unknown else 1),
                           finished_ms=elapsed())
            if current['exit_code'] in (0, None) and not unknown:
                current['exit_code'] = 1
            current['stderr'] = _text(current['stderr'] + '\n' + _text(error))
        if record is not None:
            record.step['unknown_effects'] = sorted(record.pending)
        if not publishing:
            receipt['elapsed_ms'] = elapsed()
        receipt['known_effects'] = list(dict.fromkeys(e for s in receipt['steps'] for e in s['known_effects']))
        receipt['unknown_effects'] = list(dict.fromkeys(e for s in receipt['steps'] for e in s['unknown_effects']))
    finally:
        # Evidence persistence may finish late; it authorizes no new guest action.
        outcome['observed_elapsed_ms'] = elapsed()
        try:
            ops.preserve(outcome)
        except BaseException as error:
            outcome['status'] = 'unknown'
            outcome['audit_error'] = _text(error)
    return outcome


def _control_envelope(sample, host_deadline_ns, host_received_ns, binding):
    _object(sample, ('schema', 'monotonic_ns'), 'guest clock sample')
    _require(sample['schema'] == 'switchback.engine.clock_sample.v1', 'guest clock sample schema')
    remaining = host_deadline_ns - host_received_ns
    _require(type(sample['monotonic_ns']) is int and 0 < sample['monotonic_ns'] < (1 << 63)
        and type(remaining) is int and 0 < remaining <= BUDGET_SECONDS * 1000000000,
        'guest clock sample/original remaining budget')
    return {'schema': 'switchback.engine.clock_control.v1', 'sample_ns': sample['monotonic_ns'],
            'deadline_ns': sample['monotonic_ns'] + remaining, 'binding': binding}


def _safe_directory(path, uid, private=False, observe=lambda _: None, check=lambda: None):
    path = Path(path)
    for parent in reversed((path,) + tuple(path.parents)):
        check()
        value = parent.lstat()
        observe({'path': str(parent), 'device': value.st_dev, 'inode': value.st_ino,
                 'mode': value.st_mode, 'uid': value.st_uid, 'gid': value.st_gid})
        check()
        _require(stat.S_ISDIR(value.st_mode) and value.st_uid in (0, uid)
                 and not value.st_mode & 0o022, 'unsafe directory ancestor: ' + str(parent))
    check()
    value = path.lstat()
    check()
    if private:
        _require(value.st_uid == uid and stat.S_IMODE(value.st_mode) == 0o700, 'private directory owner/mode')
    return value.st_dev, value.st_ino, value.st_uid, value.st_gid, stat.S_IMODE(value.st_mode)


def _read(path, maximum, uid, check=lambda: None, mode=None, observe=lambda _: None):
    path = Path(path)
    parent = _safe_directory(path.parent, uid, observe=observe, check=check)
    fd = os.open(str(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(fd)
        observe({'path': str(path), 'device': before.st_dev, 'inode': before.st_ino,
            'mode': before.st_mode, 'uid': before.st_uid, 'gid': before.st_gid,
            'nlink': before.st_nlink, 'bytes': before.st_size})
        _require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == uid
                 and not before.st_mode & 0o022 and 0 < before.st_size <= maximum
                 and (mode is None or stat.S_IMODE(before.st_mode) == mode), 'unsafe bounded leaf: ' + str(path))
        data = bytearray()
        while True:
            check()
            chunk = os.read(fd, min(65536, maximum + 1 - len(data)))
            if not chunk:
                break
            data.extend(chunk)
            _require(len(data) <= maximum, 'bounded file grew')
        after, current = os.fstat(fd), path.lstat()
        identity = lambda s: (s.st_dev, s.st_ino, s.st_mode, s.st_size, s.st_uid,
                               s.st_gid, s.st_nlink, s.st_mtime_ns, s.st_ctime_ns)
        _require(identity(before) == identity(after) == identity(current)
            and parent == _safe_directory(path.parent, uid, check=check) and len(data) == before.st_size,
            'file/parent identity changed')
        return bytes(data), before
    finally:
        os.close(fd)


def _exclusive(path, raw, mode, uid, check=lambda: None):
    path = Path(path)
    parent = _safe_directory(path.parent, uid, check=check)
    directory = os.open(str(path.parent), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        check()
        _require(parent == _safe_directory(path.parent, uid, check=check), 'exclusive parent drift')
        fd = os.open(path.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode,
                     dir_fd=directory)
        try:
            os.fchmod(fd, mode)
            view = memoryview(raw)
            while view:
                check()
                count = os.write(fd, view[:65536])
                _require(count > 0, 'short exclusive write')
                view = view[count:]
            check()
            os.fsync(fd)
        finally:
            os.close(fd)
        check()
        os.fsync(directory)
    finally:
        os.close(directory)
    observed, value = _read(path, max(1, len(raw)), uid, check, mode)
    _require(observed == raw and parent == _safe_directory(path.parent, uid, check=check), 'exclusive readback drift')
    return value


class _ChildIdentityLost(_Failure):
    pass


def _waitid_reader():
    """Lazy non-reaping POSIX reader; actual only, never import/self-check.

    Python omits os.waitid on Darwin. Its fixed LP64 fallback follows the SDK
    sys/{signal,wait}.h ABI, also published in Apple XNU bsd/sys headers.
    """
    if sys.platform == 'linux':
        _require(callable(getattr(os, 'waitid', None)), 'Linux non-reaping waitid unavailable')
        return lambda pid: os.waitid(os.P_PID, pid, os.WEXITED | os.WNOWAIT | os.WNOHANG)
    _require(sys.platform == 'darwin', 'unsupported owned-process observation platform')
    import ctypes
    _require(os.uname().machine in ('arm64', 'x86_64') and ctypes.sizeof(ctypes.c_void_p) == 8
        and ctypes.sizeof(ctypes.c_long) == 8 and ctypes.sizeof(ctypes.c_int) == 4,
        'unsupported Darwin64 process observation ABI')
    _require((os.P_PID, os.WEXITED, os.WNOWAIT, os.WNOHANG,
        os.CLD_EXITED, os.CLD_KILLED, os.CLD_DUMPED) == (1, 4, 32, 1, 1, 2, 3),
        'Darwin waitid constants differ from supported SDK')
    class Sigval(ctypes.Union):
        _fields_ = [('integer', ctypes.c_int), ('pointer', ctypes.c_void_p)]
    class Siginfo(ctypes.Structure):
        _fields_ = [('si_signo', ctypes.c_int), ('si_errno', ctypes.c_int),
            ('si_code', ctypes.c_int), ('si_pid', ctypes.c_int), ('si_uid', ctypes.c_uint),
            ('si_status', ctypes.c_int), ('si_addr', ctypes.c_void_p), ('si_value', Sigval),
            ('si_band', ctypes.c_long), ('reserved', ctypes.c_ulong * 7)]
    _require(ctypes.sizeof(Siginfo) == 104 and Siginfo.si_pid.offset == 12
        and Siginfo.si_status.offset == 20 and Siginfo.reserved.offset == 48,
        'Darwin siginfo layout differs from supported SDK')
    library = ctypes.CDLL('/usr/lib/libSystem.B.dylib', use_errno=True)
    waitid = library.waitid
    waitid.argtypes = (ctypes.c_int, ctypes.c_uint, ctypes.POINTER(Siginfo), ctypes.c_int)
    waitid.restype = ctypes.c_int
    def read(pid):
        info = Siginfo()  # Entire, zero-initialized 104-byte SDK structure.
        if waitid(1, pid, ctypes.byref(info), 4 | 32 | 1) != 0:
            code = ctypes.get_errno()
            raise OSError(code, os.strerror(code))
        return info if info.si_pid else None
    return read


def _observe_child(process, reader, observe):
    if process.returncode is not None:
        raise _ChildIdentityLost('owned child already reaped; numeric PGID is untrusted', True, None)
    try:
        info = reader(process.pid)
    except OSError as error:
        observe({'requested_pid': process.pid, 'error': _text(error), 'errno': error.errno})
        if error.errno == errno.ECHILD:
            raise _ChildIdentityLost('owned child identity lost (ECHILD); no group signal', True, None) from error
        raise
    if info is None:
        observe({'requested_pid': process.pid, 'terminal': False})
        return None
    raw = {name: getattr(info, name) for name in ('si_pid', 'si_uid', 'si_signo', 'si_code', 'si_status')}
    observe(raw)
    if raw['si_pid'] != process.pid:
        raise _ChildIdentityLost('waitid child PID differs; no group signal', True, None)
    _require(all(type(value) is int for value in raw.values()) and raw['si_signo'] == signal.SIGCHLD
        and 0 <= raw['si_uid'] < (1 << 32) and raw['si_code'] in (os.CLD_EXITED, os.CLD_KILLED, os.CLD_DUMPED),
        'nonterminal/invalid owned waitid observation')
    exited = raw['si_code'] == os.CLD_EXITED
    _require((0 <= raw['si_status'] <= 255) if exited else (0 < raw['si_status'] <= 64),
             'owned waitid exit/signal status bound')
    return dict(raw, exit_code=raw['si_status'] if exited else -raw['si_status'])


def _wait_leader(process, reader, deadline, monotonic, observe):
    while True:
        _guard(deadline, monotonic)
        terminal = _observe_child(process, reader, observe)
        _guard(deadline, monotonic)
        if terminal is not None:
            return terminal
        time.sleep(min(.01, max(0, deadline - monotonic())))


def _darwin_group_reader():
    """Lazy, fixed SDK proc_listpids binding; never resolved on import/self-check."""
    _require(sys.platform == 'darwin', 'Darwin group observation requires Darwin')
    import ctypes
    _require(os.uname().machine in ('arm64', 'x86_64') and sys.byteorder == 'little'
        and ctypes.sizeof(ctypes.c_void_p) == 8 and ctypes.sizeof(ctypes.c_long) == 8
        and ctypes.sizeof(ctypes.c_int) == 4 and ctypes.sizeof(ctypes.c_uint) == 4,
        'unsupported Darwin64 group observation ABI')
    library = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
    listpids = library.proc_listpids
    listpids.argtypes = (ctypes.c_uint, ctypes.c_uint, ctypes.c_void_p, ctypes.c_int)
    listpids.restype = ctypes.c_int
    def read(pid):
        _require(type(pid) is int and 0 < pid < (1 << 31), 'invalid owned group PID')
        buffer = ctypes.create_string_buffer(8192)  # All 2048 signed32 PID slots start zero.
        ctypes.set_errno(0)
        count = listpids(2, pid, buffer, 8192)  # PROC_PGRP_ONLY, exact retained leader.
        code = ctypes.get_errno()
        # Preserve the entire bounded buffer, even for zero/error/oversized returns.
        return {'byte_count': count, 'errno': code, 'raw': buffer.raw}
    return read


def _prove_darwin_terminal_group(process, reader, terminal, deadline, monotonic, observe):
    """Prove a sole terminal retained leader; never signal or reap any process."""
    _guard(deadline, monotonic)
    _require(sys.platform == 'darwin', 'Darwin terminal-group proof requires Darwin')
    _require(type(process.pid) is int and 0 < process.pid < (1 << 31), 'invalid owned child PID')
    pid = process.pid
    def retained():
        if process.returncode is not None or process.pid != pid:
            raise _ChildIdentityLost('owned child identity lost/reaped during group proof', True, None)
    _object(terminal, ('si_pid', 'si_uid', 'si_signo', 'si_code', 'si_status', 'exit_code'),
            'retained owned terminal observation')
    _require(all(type(value) is int for value in terminal.values()), 'invalid retained terminal types')
    expected = dict(terminal)
    retained()
    before = _observe_child(process, reader, lambda value: observe({'kind': 'leader-before-snapshot', 'value': value}))
    _guard(deadline, monotonic)
    _require(before is not None and before == expected, 'retained owned terminal changed before snapshot')
    retained()
    group_reader = _darwin_group_reader()
    _guard(deadline, monotonic)
    retained()
    facts = group_reader(pid)
    # Record the actual bounded raw result before count, errno, type or PID checks.
    observe({'kind': 'darwin-group-snapshot', 'requested_pid': pid, 'value': _wire(facts)})
    _guard(deadline, monotonic)
    _object(facts, ('byte_count', 'errno', 'raw'), 'Darwin owned-group snapshot')
    _require(type(facts['byte_count']) is int and type(facts['errno']) is int
        and type(facts['raw']) is bytes, 'invalid Darwin owned-group snapshot types')
    _require(facts['errno'] == 0 and facts['byte_count'] == 4,
             'owned group is not proven sole terminal leader')
    _require(len(facts['raw']) == 8192
        and int.from_bytes(facts['raw'][:4], sys.byteorder, signed=True) == pid,
        'owned group leader mismatch')
    retained()
    after = _observe_child(process, reader, lambda value: observe({'kind': 'leader-after-snapshot', 'value': value}))
    _guard(deadline, monotonic)
    _require(after is not None and after == expected, 'retained owned terminal changed after snapshot')
    retained()
    return {'members': [pid], 'terminal': expected, 'byte_count': 4, 'errno': 0,
            'signalled': False}


def _cleanup_process(process, selector, reader, terminal, deadline, monotonic, identity_lost=False):
    """Converge only our new-session child group; preserve every cleanup result.

    Never poll before signalling: poll reaps an exited leader and can release its
    PID for reuse while descendants retain the pipes. Here returncode=None means
    our child has not been reaped; its PID still reserves this original PGID.
    Normal EOF first observes terminal status with WNOWAIT. Cleanup signals the
    original group while that leader still reserves its PID, then reaps it and
    compares its actual status. Timeout also probes without reaping before its
    evidence-only cleanup. ECHILD/already-reaped identity never permits a signal.
    No remote daemon or systemd unit is stopped by this local cleanup.
    """
    events = []
    def attempt(operation, call):
        failure = None
        try:
            call()
            events.append({'operation': operation, 'status': 'succeeded'})
        except BaseException as error:
            failure = error
            absent = operation == 'kill_owned_group' and isinstance(error, OSError) and error.errno == errno.ESRCH
            events.append({'operation': operation, 'status': 'already_absent' if absent else 'failed',
                           'error': _text(error), 'errno': getattr(error, 'errno', None)})
        if operation == 'kill_owned_group':
            events[-1].update(signal_status='succeeded' if events[-1]['status'] == 'succeeded' else 'failed',
                              signalled=events[-1]['status'] == 'succeeded')
        return failure
    if process is not None:
        reserved = not identity_lost and process.returncode is None
        if reserved and terminal is None:
            try:
                terminal = _observe_child(process, reader, lambda value: events.append(
                    {'operation': 'observe_owned_child', 'status': 'observed', 'value': value}))
            except _ChildIdentityLost as error:
                reserved = False
                events.append({'operation': 'retain_child_identity', 'status': 'failed', 'error': _text(error)})
            except BaseException as error:
                # A non-reaping error other than ECHILD cannot release our
                # unreaped child PID. Preserve it and still fence that group.
                events.append({'operation': 'observe_owned_child', 'status': 'failed', 'error': _text(error)})
        if reserved:
            signal_error = attempt('kill_owned_group', lambda: os.killpg(process.pid, signal.SIGKILL))
            signal_result = events[-1]
            if sys.platform == 'darwin' and isinstance(signal_error, OSError) and signal_error.errno == errno.EPERM and terminal is not None:
                try:
                    proof = _prove_darwin_terminal_group(process, reader, terminal, deadline, monotonic,
                        lambda value: events.append({'operation': 'observe_darwin_terminal_group',
                                                     'status': 'observed', 'value': value}))
                except BaseException as error:
                    if isinstance(error, _ChildIdentityLost):
                        reserved = False
                    events.append({'operation': 'prove_darwin_terminal_group', 'status': 'failed',
                                   'error': _text(error), 'errno': getattr(error, 'errno', None)})
                else:
                    # status describes convergence, never a successful signal.
                    # Keep the original failed signal/errno separate from its proof.
                    signal_result['status'] = 'already_terminal'
                    events.append({'operation': 'prove_darwin_terminal_group',
                                   'status': 'already_terminal', 'value': proof})
        if reserved:
            # A bounded evidence-only reap never grants a new execution window.
            attempt('reap_owned_child', lambda: process.wait(timeout=max(.001, min(1, deadline - monotonic()))))
            if terminal is not None and process.returncode != terminal['exit_code']:
                events.append({'operation': 'leader_exit_readback', 'status': 'failed',
                    'expected': terminal['exit_code'], 'actual': process.returncode})
        else:
            events.append({'operation': 'retain_child_identity', 'status': 'failed',
                           'error': 'child identity lost/reaped; group not signalled or reaped again'})
    attempt('close_selector', selector.close)
    if process is not None:
        for label in ('stdin', 'stdout', 'stderr'):
            stream = getattr(process, label)
            if stream is not None and not stream.closed:
                attempt('close_' + label, stream.close)
    return events


def _bounded_run(argv, deadline, record, monotonic=time.monotonic, input_data=None):
    """No shell, ambient environment, unbounded communicate, or remote cleanup."""
    _guard(deadline, monotonic)
    started = monotonic()
    stdout_limit = _UNIT_CATALOG_STDOUT if tuple(argv) in _UNIT_CATALOG_COMMANDS else _OUTPUT
    out, err, process, selector = bytearray(), bytearray(), None, selectors.DefaultSelector()
    timed_out, truncated, failure, reader, terminal = False, False, None, None, None
    env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LC_ALL': 'C', 'LANG': 'C',
           'HOME': '/root', 'PYTHONDONTWRITEBYTECODE': '1'}
    if argv[0] == _LIMA:
        env.update(HOME=str(_host_home()), LIMA_HOME=_LIMA_HOME)
    try:
        reader = _waitid_reader()
        process = subprocess.Popen(argv, stdin=subprocess.PIPE if input_data is not None else subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, start_new_session=True, close_fds=True)
        for stream, label in ((process.stdout, 'stdout'), (process.stderr, 'stderr')):
            os.set_blocking(stream.fileno(), False)
            selector.register(stream, selectors.EVENT_READ, label)
        sent = 0
        if input_data is not None:
            os.set_blocking(process.stdin.fileno(), False)
            selector.register(process.stdin, selectors.EVENT_WRITE, 'stdin')
        while selector.get_map():
            _guard(deadline, monotonic)
            for key, _ in selector.select(min(.1, max(0, deadline - monotonic()))):
                if key.data == 'stdin':
                    count = os.write(key.fd, input_data[sent:sent + 65536])
                    sent += count
                    if sent == len(input_data):
                        selector.unregister(key.fileobj)
                        key.fileobj.close()
                    continue
                data = os.read(key.fd, 65536)
                if not data:
                    selector.unregister(key.fileobj)
                    key.fileobj.close()
                    continue
                target = out if key.data == 'stdout' else err
                bound = stdout_limit if key.data == 'stdout' else _OUTPUT
                if len(target) + len(data) > bound:
                    target.extend(data[:max(0, bound - len(target))])
                    truncated = True
                    raise _Failure('fixed command output exceeded bound', True, None)
                target.extend(data)
        terminal = _wait_leader(process, reader, deadline, monotonic,
                               lambda value: record.raw('owned-leader-before-reap', value))
    except BaseException as error:
        failure = error
        timed_out = isinstance(error, subprocess.TimeoutExpired) or monotonic() >= deadline
    finally:
        cleanup = _cleanup_process(process, selector, reader, terminal, deadline, monotonic,
                                   isinstance(failure, _ChildIdentityLost))
        timed_out = timed_out or monotonic() >= deadline
        if failure is None and timed_out:
            failure = _Failure('original120second deadline during owned-process convergence', True, None)
        if failure is None and any(item['status'] == 'failed' for item in cleanup):
            failure = _Failure('owned local process cleanup failed/unknown', True, None)
        value = {'argv': list(argv), 'started_monotonic': started, 'finished_monotonic': monotonic(),
                 'deadline_monotonic': deadline, 'exit_code': process.returncode if process else None,
                 'stdout': bytes(out), 'stderr': bytes(err), 'timed_out': timed_out,
                 'truncated': truncated, 'error': str(failure) if failure else None, 'cleanup': cleanup,
                 'leader_terminal': terminal}
        # A recorder can reject the command after preserving its raw result.
        # Such rejection must not replace the original timeout/output failure.
        recording_failure = None
        try:
            record.command(value)
        except BaseException as error:
            recording_failure = error
    if failure:
        raise _Failure(str(failure), True, value['exit_code'])
    if recording_failure:
        raise recording_failure
    return bytes(out)


def _self_check():
    here = Path(__file__).resolve().parent
    validate_configuration({name: (here / name).read_bytes() for name in FILES})
    validate_receipt_v2(_json((here / 'testdata/receipt-v2.json').read_bytes()))
    for name in ('contract.py', 'bundle.py', 'store.py', 'binaries.py', 'fd_lifecycle.py', 'guest.py', 'prepare.py'):
        ast.parse((here / name).read_bytes(), filename=name, feature_version=(3, 9))
    return {'self_check': 'static_configuration_schema_and_python39_syntax',
            **dict.fromkeys(_AUTHORITY, False)}


class _Host:
    """Fixed Darwin coordinator; construction happens only in explicit actual."""
    def __init__(self, origin_ns):
        self.origin_ns, self.deadline_ns = origin_ns, origin_ns + BUDGET_SECONDS * 1000000000
        self.deadline = self.deadline_ns / 1000000000
        self.home = _host_home()
        self.root = self.home / '.switchback' / 'engine-preparation'
        self.frames, self.bytes, self.process = [], 0, None
        self.stage_owned = False
        self.result = {'schema': 'switchback.engine.host_outcome.v1', 'status': 'unknown',
                       'started_monotonic_ns': origin_ns, 'deadline_monotonic_ns': self.deadline_ns,
                       'observations': self.frames, **dict.fromkeys(_AUTHORITY, False)}

    def raw(self, kind, value):
        self.emit({'kind': kind, 'value': _wire(value)})

    def command(self, value):
        self.emit({'kind': 'host-command-result', 'value': _wire(value)})
        if value.get('exit_code') != 0 or value.get('timed_out') or value.get('truncated'):
            raise _Failure('fixed host command failed/unknown', True, value.get('exit_code'))

    def emit(self, value):
        raw = _encode(value)
        oversized = len(raw) > _FRAME or self.bytes + len(raw) > _AUDIT_BYTES or len(self.frames) >= 65536
        if oversized:
            value = {'kind': 'bounded-host-observation-rejected', 'bytes': len(raw),
                     'sha256': _sha(raw), 'prefix_base64': base64.b64encode(raw[:_OUTPUT]).decode()}
            raw = _encode(value)
        self.bytes += len(raw)
        self.frames.append(value)
        if self.stage_owned:
            _exclusive(self.root / ('observation-%05d.json' % len(self.frames)), raw, 0o600, os.getuid())
        if oversized:
            raise _Failure('host audit bound; raw prefix/hash retained', True, None)

    def preflight(self):
        _guard(self.deadline)
        _require(sys.platform == 'darwin', 'actual requires Darwin host')
        from bundle import verify_bundle
        home = self.home / '.switchback'
        _safe_directory(home, os.getuid(), True)
        _require(not os.path.lexists(self.root), 'host preparation already claimed; no retry/resume')
        bundle, kit = home / 'engine-bundle', home / 'qualification-kit'
        manifest = verify_bundle(bundle, kit, check=lambda: _guard(self.deadline))
        raw, _ = _read(bundle / 'manifest.json', 1 << 20, os.getuid(), lambda: _guard(self.deadline), 0o600)
        self.binding = {'bundle_manifest_sha256': _sha(raw),
                        **{key: manifest[key] for key in _BINDING if key != 'bundle_manifest_sha256'}}
        observe = lambda value: self.emit({'kind': 'host-file-observation-before-validation', 'value': value})
        config, _ = _read(Path(_VM_CONFIG), 1 << 20, os.getuid(), lambda: _guard(self.deadline), observe=observe)
        _read(Path(_LIMA), 64 << 20, os.getuid(), lambda: _guard(self.deadline), observe=observe)
        listing = _bounded_run([_LIMA, 'list', _VM, '--json'], self.deadline, self)
        # limactl emits one object per selected instance; no second query/start.
        vm = _json(listing)
        _require(vm.get('name') == _VM and vm.get('status') == 'Running'
            and vm.get('cpus') == 4 and vm.get('memory') == 10737418240
            and vm.get('dir') == _LIMA_HOME + '/' + _VM, 'exact VM must already be Running with fixed geometry')
        config_after, _ = _read(Path(_VM_CONFIG), 1 << 20, os.getuid(), lambda: _guard(self.deadline))
        _require(config_after == config, 'VM configuration drift')
        self.vm = {'name': _VM, 'vcpus': vm['cpus'], 'memory_bytes': vm['memory']}
        _guard(self.deadline)
        self.root.mkdir(mode=0o700, exist_ok=False)
        self.stage_owned = True
        self.root.chmod(0o700)
        _exclusive(self.root / 'claim.json', _encode({'binding': self.binding,
            'origin_monotonic_ns': self.origin_ns, 'deadline_monotonic_ns': self.deadline_ns,
            'vm_config_sha256': _sha(config), 'vm': vm}), 0o600, os.getuid())
        for index, frame in enumerate(self.frames, 1):
            _exclusive(self.root / ('observation-%05d.json' % index), _encode(frame), 0o600, os.getuid())

    def transport(self):
        _guard(self.deadline)
        argv = [_LIMA, 'shell', _VM, 'sudo', '--', '/usr/bin/python3', '-I', '-B', '-c', _GUEST_ENTRY]
        env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LC_ALL': 'C', 'LANG': 'C',
               'HOME': str(self.home), 'LIMA_HOME': _LIMA_HOME, 'PYTHONDONTWRITEBYTECODE': '1'}
        selector, pending, stderr = selectors.DefaultSelector(), bytearray(), bytearray()
        sample_seen, final_seen, control = False, False, None
        failure, recording_failure, reader, terminal = None, None, None, None
        try:
            reader = _waitid_reader()
            self.process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE, env=env, start_new_session=True, close_fds=True)
            for stream, label in ((self.process.stdout, 'stdout'), (self.process.stderr, 'stderr')):
                os.set_blocking(stream.fileno(), False)
                selector.register(stream, selectors.EVENT_READ, label)
            while selector.get_map():
                _guard(self.deadline)
                for key, _ in selector.select(min(.1, max(0, self.deadline - time.monotonic()))):
                    if key.data == 'control':
                        count = os.write(key.fd, control)
                        control = control[count:]
                        if not control:
                            selector.unregister(key.fileobj)
                            key.fileobj.close()
                        continue
                    raw = os.read(key.fd, 65536)
                    if not raw:
                        selector.unregister(key.fileobj)
                        key.fileobj.close()
                        continue
                    if key.data == 'stderr':
                        stderr.extend(raw[:max(0, _OUTPUT - len(stderr))])
                        self.emit({'kind': 'guest-stderr', 'raw': _wire(raw[:_OUTPUT])})
                        _require(len(raw) + len(stderr) <= _OUTPUT, 'guest stderr bound')
                        continue
                    pending.extend(raw)
                    _require(len(pending) <= _FRAME, 'guest protocol frame bound')
                    while b'\n' in pending:
                        line, _, tail = pending.partition(b'\n')
                        pending = bytearray(tail)
                        received = time.monotonic_ns()
                        # Preserve raw rejected input before strict decoding.
                        self.emit({'kind': 'guest-frame-raw', 'raw': _wire(bytes(line))})
                        value = _json(bytes(line))
                        if not sample_seen:
                            control_value = _control_envelope(value, self.deadline_ns, received, self.binding)
                            control_value['vm'] = self.vm
                            control = _encode(control_value) + b'\n'
                            os.set_blocking(self.process.stdin.fileno(), False)
                            selector.register(self.process.stdin, selectors.EVENT_WRITE, 'control')
                            self.emit({'kind': 'clock-control', 'value': control_value})
                            sample_seen = True
                        elif value.get('kind') == 'outcome' and not final_seen:
                            validate_receipt_v2(value['value']['receipt'])
                            self.result['guest'] = value['value']
                            final_seen = True
                        else:
                            _require(not final_seen and value.get('kind') == 'observation', 'unexpected/duplicate guest protocol')
            terminal = _wait_leader(self.process, reader, self.deadline, time.monotonic,
                lambda value: self.emit({'kind': 'owned-transport-leader-before-reap', 'value': value}))
            code = terminal['exit_code']
            self.emit({'kind': 'guest-transport-exit', 'exit_code': code, 'stderr': _wire(bytes(stderr)),
                       'trailing_stdout': _wire(bytes(pending))})
            _require(not pending and sample_seen and final_seen, 'incomplete guest protocol; remote effects unknown')
            _guard(self.deadline)
            _require(code == 0 and self.result['guest']['status'] == 'engine_ready', 'guest preparation failed/unknown')
            self.result['status'] = 'engine_ready'
        except BaseException as error:
            failure = error
        finally:
            cleanup = _cleanup_process(self.process, selector, reader, terminal, self.deadline,
                                       time.monotonic, isinstance(failure, _ChildIdentityLost))
            if failure is None and time.monotonic() >= self.deadline:
                failure = _Failure('original120second deadline during transport convergence', True, None)
            if failure is None and any(item['status'] == 'failed' for item in cleanup):
                failure = _Failure('owned local transport cleanup failed/unknown', True, None)
            if failure:
                self.result['status'] = 'unknown'
            final = {'kind': 'guest-transport-final-observation',
                'exit_code': self.process.returncode if self.process else None,
                'finished_monotonic_ns': time.monotonic_ns(), 'deadline_monotonic_ns': self.deadline_ns,
                'stderr': _wire(bytes(stderr)), 'trailing_stdout': _wire(bytes(pending)),
                'error': _text(failure) if failure else None, 'cleanup': cleanup, 'leader_terminal': terminal,
                'remote_effects': 'not_cleaned_or_retried'}
            try:
                self.emit(final)
            except BaseException as error:
                recording_failure = error
        if failure:
            raise failure
        if recording_failure:
            raise recording_failure

    def preserve(self):
        self.result['finished_monotonic_ns'] = time.monotonic_ns()
        raw = _encode(self.result)
        if self.stage_owned:
            _exclusive(self.root / 'outcome.json', raw, 0o600, os.getuid())
        else:
            # A rejected/occupied fixed stage cannot be reused. Preserve a new
            # audit-only leaf under the existing private parent, without resuming.
            parent = self.home / '.switchback'
            _safe_directory(parent, os.getuid(), True)
            _exclusive(parent / ('engine-rejected-%d.json' % self.origin_ns), raw, 0o600, os.getuid())


def _actual():
    host = _Host(time.monotonic_ns())
    try:
        host.preflight()
        host.transport()
    except BaseException as error:
        host.result['status'] = 'unknown'
        host.result['error'] = _text(error)
        host.result['remote_effects'] = 'unknown' if host.process is not None else 'none_attempted'
    finally:
        try:
            host.preserve()
        except BaseException as error:
            host.result['status'] = 'unknown'
            sys.stderr.write(_encode({'audit_failure': _text(error), 'outcome': host.result}).decode() + '\n')
    return 0 if host.result['status'] == 'engine_ready' else 1


def main():
    if sys.argv[1:] == ['self-check']:
        print(_encode(_self_check()).decode())
        return 0
    if sys.argv[1:] == ['actual']:
        return _actual()
    raise SystemExit('usage: prepare.py self-check|actual')


if __name__ == '__main__':
    raise SystemExit(main())
