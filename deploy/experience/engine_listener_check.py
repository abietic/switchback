"""Bounded, read-only Linux listener observation; never Engine/capacity authority.

The caller supplies a trusted build attestation (version, exact source revision,
executable SHA256, expected PID start token). A filename/version string alone is
not an attestation. All successful results cover only this finite observation;
repeat reads detect drift, not an atomic or all-time guarantee against root.
"""
import hashlib
import os
from pathlib import PurePosixPath
import re
import stat
import time

PRIVATE_ROOT = '/run/switchback-engine'
PINS = {
    'dockerd': ('29.9.0', 'a5b58c94d334674eae3a3207d4f05417643a90d0'),
    'containerd': ('2.4.1', 'f2551031d7276a770f65f98c9b52e57e7dad07e8'),
}
TABLES = ('unix', 'tcp', 'tcp6', 'udp', 'udp6')
MAX_READ = 2 * 1024 * 1024
MAX_ROWS = 16384
MAX_FDS = 8192
MAX_PATHS = 64
MAX_EXE = 128 * 1024 * 1024
SCOPE = ('finite read-only PID/FD/network-table/filesystem observation with repeat-read drift checks; '
         'not an atomic all-time race proof, Engine readiness or capacity admission; '
         'trusted caller build attestation and exclusive deployment-root ownership required; '
         'INET grammar is Linux v6.8 reference on 64-bit ABI, not exact Ubuntu kernel source attestation')


def parse_unix(raw):
    """Strict bounded /proc/PID/net/unix parser; retain every raw row."""
    rows, errors = [], []
    if not isinstance(raw, str) or len(raw.encode()) > MAX_READ or not raw.endswith('\n'):
        return rows, ['unix incomplete/oversized read']
    lines = raw.splitlines()
    if not lines or lines[0].split() != ['Num', 'RefCount', 'Protocol', 'Flags', 'Type', 'St', 'Inode', 'Path']:
        return rows, ['unix invalid header']
    if len(lines) > MAX_ROWS + 1:
        return rows, ['unix row limit exceeded']
    for line in lines[1:]:
        c = line.split()
        try:
            if len(c) not in (7, 8) or not re.fullmatch('[0-9a-fA-F]+:', c[0]):
                raise ValueError()
            if any(not re.fullmatch('[0-9a-fA-F]+', v) for v in c[1:6]):
                raise ValueError()
            if not c[6].isdigit() or int(c[6]) <= 0:
                raise ValueError()
            rows.append({'raw': line, 'inode': c[6], 'path': c[7] if len(c) == 8 else '',
                         'protocol': int(c[2], 16), 'flags': int(c[3], 16),
                         'type': int(c[4], 16), 'state': int(c[5], 16)})
        except (ValueError, IndexError):
            errors.append('unix malformed row: ' + line)
    return rows, errors


# Linux v6.8 reference grammar, not an exact Ubuntu SRU kernel attestation.
# https://github.com/torvalds/linux/blob/v6.8/net/ipv4/tcp_ipv4.c#L2829
# https://github.com/torvalds/linux/blob/v6.8/net/ipv6/datagram.c#L1048
# https://github.com/torvalds/linux/blob/v6.8/include/net/transp_v6.h#L59
# https://github.com/torvalds/linux/blob/v6.8/lib/vsprintf.c#L786
# This verifier supports the deployment's 64-bit Linux ABI (%lu and %pK).
# %pK includes its exact early hash-key placeholder; IRQ pK-error fails closed.
# Whitespace padding is immaterial; header names and all row fields are required.
INET_HEADERS = {
    'tcp': 'sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode',
    'tcp6': 'sl local_address remote_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode',
    'udp': 'sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode ref pointer drops',
    'udp6': 'sl local_address remote_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode ref pointer drops',
}

# https://github.com/torvalds/linux/blob/v6.8/include/net/tcp_states.h#L12
# Regular sk_state enum domain excludes 00, inet_diag-only 0D and sentinel 0E.
# UDP's generic formatter prints sk_state; this is a supported enum boundary,
# not a claim that every included state is reachable through a UDP operation.
INET_SOCKET_STATES = frozenset(('01', '02', '03', '04', '05', '06',
                                '07', '08', '09', '0A', '0B', '0C'))
# TCP seq_show dispatches internal TIME_WAIT/NEW_SYN_RECV to short formatters.
TCP_FULL_STATES = INET_SOCKET_STATES - {'06', '0C'}


def _inet_decimal(value, bits, signed=False):
    # C printf integer fields use ASCII decimal, never '+' or Unicode digits.
    pattern = r'-?(?:0|[1-9][0-9]*)' if signed else r'(?:0|[1-9][0-9]*)'
    if len(value) > (11 if bits == 32 and signed else 10 if bits == 32 else 20):
        return False
    if not re.fullmatch(pattern, value):
        return False
    number = int(value)
    return -(1 << (bits - 1)) <= number < (1 << (bits - 1)) if signed else number < (1 << bits)


def _inet_fields_supported(c, table):
    tcp = table.startswith('tcp')
    if len(c) not in ((12, 17) if tcp else (13,)):
        return False
    address = r'[0-9A-F]{32}:[0-9A-F]{4}' if table.endswith('6') else r'[0-9A-F]{8}:[0-9A-F]{4}'
    if (not c[0].endswith(':') or not _inet_decimal(c[0][:-1], 32, signed=True)
            or c[0].startswith('-') or not all(re.fullmatch(address, a) for a in c[1:3])
            or c[3] not in INET_SOCKET_STATES
            or not re.fullmatch(r'[0-9A-F]{8}:[0-9A-F]{8}', c[4])
            or not re.fullmatch(r'[0-9A-F]{2}:[0-9A-F]{8,16}', c[5])
            or not re.fullmatch(r'[0-9A-F]{8}', c[6])
            or not _inet_decimal(c[7], 32) or not _inet_decimal(c[8], 32, signed=True)
            or not _inet_decimal(c[9], 64) or not _inet_decimal(c[10], 32, signed=True)
            or not (re.fullmatch(r'[0-9a-f]{16}', c[11]) or c[11] == '(____ptrval____)')):
        return False
    timer = int(c[5].split(':')[0], 16)
    if tcp and len(c) == 17:
        return (c[3] in TCP_FULL_STATES and timer in (0, 1, 2, 4)
                and _inet_decimal(c[12], 64) and _inet_decimal(c[13], 64)
                and _inet_decimal(c[14], 32) and _inet_decimal(c[15], 32)
                and _inet_decimal(c[16], 32, signed=True))
    zero_queues = c[4] == '00000000:00000000'
    if tcp:
        # TIME_WAIT formatter prints tw_substate, including FIN_WAIT2 (05).
        # Request formatter prints SYN_RECV (03); full SYN_RECV remains valid above.
        timewait = (c[3] in ('05', '06') and timer == 3 and zero_queues
                    and c[6] == '00000000' and c[7:10] == ['0', '0', '0'])
        request = (c[3] == '03' and timer == 1 and zero_queues
                   and c[8:11] == ['0', '0', '0'])
        return timewait or request
    return (c[5] == '00:00000000' and c[6] == '00000000' and c[8] == '0'
            and _inet_decimal(c[12], 32))


def parse_inet(raw, table):
    """Fail closed on unsupported headers, fields, variants or incomplete tails."""
    rows, errors = [], []
    if table not in INET_HEADERS:
        return rows, [str(table) + ' unsupported INET table']
    if not isinstance(raw, str) or len(raw.encode()) > MAX_READ or not raw.endswith('\n'):
        return rows, [table + ' incomplete/oversized read']
    lines = raw.splitlines()
    if not lines or lines[0].split() != INET_HEADERS[table].split():
        return rows, [table + ' invalid header']
    if len(lines) > MAX_ROWS + 1:
        return rows, [table + ' row limit exceeded']
    for line in lines[1:]:
        c = line.split()
        if not _inet_fields_supported(c, table):
            errors.append(table + ' malformed row: ' + line)
        else:
            rows.append({'raw': line, 'inode': c[9]})
    return rows, errors


def _profile(identity):
    daemon = identity.get('daemon')
    if daemon not in PINS or (identity.get('version'), identity.get('source_revision')) != PINS[daemon]:
        raise ValueError('unknown/missing exact source build identity')
    if not re.fullmatch('[0-9a-f]{64}', identity.get('executable_sha256', '')):
        raise ValueError('approved executable SHA256 required')
    if not re.fullmatch('[1-9][0-9]*', identity.get('expected_start_token', '')):
        raise ValueError('approved PID start token required')
    if daemon == 'dockerd':
        # Exact source: API chmod0660; daemon resets umask0, net.Listen callback/metrics0777;
        # libnetwork explicitly chmod0600. All families are required in this fixed profile.
        required = {PRIVATE_ROOT + '/docker.sock': 0o660,
                    PRIVATE_ROOT + '/docker/extensions/callback.sock': 0o777,
                    PRIVATE_ROOT + '/docker/metrics.sock': 0o777}
        optional = {}
        dynamic = re.compile(re.escape(PRIVATE_ROOT) + r'/docker/libnetwork/(?=[0-9a-f]{0,11}[a-f])[0-9a-f]{12}\.sock')
    else:
        required = {PRIVATE_ROOT + '/containerd/containerd.sock': 0o660,
                    PRIVATE_ROOT + '/containerd/containerd.sock.ttrpc': 0o660}
        optional, dynamic = {}, None
    return required, optional, dynamic


def validate(observation, approved_identity):
    """Pure validation, reporting all categories even after a Unix mismatch."""
    failures = list(observation.get('errors', []))
    result = {'ok': False, 'failures': failures, 'listeners': [], 'diagnostics': [],
              'scope': SCOPE, 'observation': observation}
    try:
        required, optional, dynamic = _profile(approved_identity)
    except (ValueError, TypeError, AttributeError) as exc:
        failures.append('identity: ' + str(exc))
        required, optional, dynamic = {}, {}, None
    if observation.get('approved_identity') != approved_identity:
        failures.append('caller identity differs from collected identity')
    if not isinstance(observation.get('pid'), int) or observation['pid'] <= 0:
        failures.append('invalid PID')
    before, after = observation.get('process_before'), observation.get('process_after')
    identity_fields = ('start_token', 'exe_sha256', 'exe_link', 'exe_stat', 'uid', 'gid', 'net_namespace')
    if (not isinstance(before, dict) or not isinstance(after, dict) or
            any(before.get(k) != after.get(k) for k in identity_fields)):
        failures.append('PID/executable/namespace identity missing or changed')
    for process in (before, after):
        if not isinstance(process, dict):
            continue
        if process.get('start_token') != approved_identity.get('expected_start_token'):
            failures.append('PID start token differs from approved identity')
        if process.get('exe_sha256') != approved_identity.get('executable_sha256'):
            failures.append('executable digest differs from approved build')
        if process.get('uid') != [0] * 4 or process.get('gid') != [0] * 4:
            failures.append('process ownership outside root profile')
        if (not process.get('exe_link') or not process.get('net_namespace') or
                not isinstance(process.get('exe_stat'), dict) or
                not stat.S_ISREG(process.get('exe_stat', {}).get('mode', 0))):
            failures.append('process executable/namespace evidence missing')
    fd_before, fd_after = observation.get('fd_before'), observation.get('fd_after')
    if not isinstance(fd_before, dict) or fd_before != fd_after or len(fd_before) > MAX_FDS:
        failures.append('FD inventory missing/changed/oversized')
    owned = set()
    for fd, target in (fd_before if isinstance(fd_before, dict) else {}).items():
        if not isinstance(fd, str) or not fd.isdigit() or not isinstance(target, str):
            failures.append('invalid FD entry')
            continue
        if target.startswith('socket:'):
            match = re.fullmatch(r'socket:\[([1-9][0-9]*)\]', target)
            if match:
                owned.add(match[1])
                before_stat = observation.get('fd_socket_stats_before', {}).get(fd)
                after_stat = observation.get('fd_socket_stats_after', {}).get(fd)
                if (not isinstance(before_stat, dict) or before_stat != after_stat or
                        before_stat.get('ino') != int(match[1]) or
                        not stat.S_ISSOCK(before_stat.get('mode', 0))):
                    failures.append('FD socket stat missing/changed/inode mismatch: ' + fd)
            else:
                failures.append('malformed socket FD link')
    # Equality of complete FD inventories remains mandatory. Use the union for
    # error reporting so changed descriptors never disappear through intersection.
    if isinstance(fd_after, dict):
        for target in fd_after.values():
            match = re.fullmatch(r'socket:\[([1-9][0-9]*)\]', target) if isinstance(target, str) else None
            if match:
                owned.add(match[1])
    tables_before, tables_after = observation.get('tables_before', {}), observation.get('tables_after', {})
    parsed = {}
    for phase, tables in (('before', tables_before), ('after', tables_after)):
        parsed[phase] = {}
        for table in TABLES:
            raw = tables.get(table) if isinstance(tables, dict) else None
            rows, errors = parse_unix(raw) if table == 'unix' else parse_inet(raw, table)
            parsed[phase][table] = rows
            failures.extend(error + ' (' + phase + ')' for error in errors)
            if table != 'unix':
                failures.extend(table + ' daemon INET socket (' + phase + '): ' + r['raw']
                                for r in rows if r['inode'] in owned)
    # Kernel addresses/refcounts (Unix) and ambient traffic/timers (INET) are
    # volatile. Retain complete raw bytes and strict grammar in BOTH snapshots;
    # stability is over every owned Unix socket's security identity, not counters.
    security_fields = ('inode', 'path', 'protocol', 'flags', 'type', 'state')
    def owned_unix_identity(phase):
        return sorted(tuple(row[key] for key in security_fields)
                      for row in parsed[phase]['unix'] if row['inode'] in owned)
    if owned_unix_identity('before') != owned_unix_identity('after'):
        failures.append('owned Unix socket identity changed')
    for phase in ('before', 'after'):
        listeners = _validate_unix_profile(parsed[phase]['unix'], owned, required,
                                           optional, dynamic, observation, failures)
        if phase == 'before':
            result['listeners'] = listeners
    if isinstance(tables_before, dict) and isinstance(tables_after, dict):
        for table in TABLES:
            if tables_before.get(table) != tables_after.get(table):
                result['diagnostics'].append(table + ' raw table bytes changed; non-authoritative '
                                             'observation only, never readiness or capacity evidence')
    result['ok'] = not failures
    return result


def _validate_unix_profile(rows, owned, required, optional, dynamic, observation, failures):
    """Apply complete private listener policy independently to each snapshot."""
    listeners = []
    seen_paths, seen_inodes, matched, libnetwork_count = set(), set(), set(), 0
    all_listeners = [r for r in rows if r['flags'] & 0x10000]
    # A profile pathname under an unowned inode is a collision even when its
    # row is not a listener. Check each complete snapshot independently.
    for row in rows:
        if (row['inode'] not in owned and
                (row['path'] in required or row['path'] in optional or
                 (dynamic and dynamic.fullmatch(row['path'])))):
            failures.append('profile listener belongs to other PID/inode: ' + row['path'])
    owned_inodes = set()
    for row in rows:
        if row['inode'] in owned:
            if row['inode'] in owned_inodes:
                failures.append('duplicate Unix socket inode: ' + row['inode'])
            owned_inodes.add(row['inode'])
    for row in all_listeners:
        if row['path'] in seen_paths and row['path']:
            failures.append('duplicate Unix listener path: ' + row['path'])
        if row['inode'] in seen_inodes:
            failures.append('duplicate Unix socket inode: ' + row['inode'])
        seen_paths.add(row['path'])
        seen_inodes.add(row['inode'])
        if row['inode'] not in owned:
            continue
        path = row['path']
        listeners.append(row)
        if row['protocol'] != 0 or row['type'] != 1 or row['state'] != 1:
            failures.append('owned listener outside stream LISTEN profile: ' + path)
        mode = {**required, **optional}.get(path)
        if dynamic and dynamic.fullmatch(path):
            mode = 0o600
            libnetwork_count += 1
        if mode is None:
            failures.append('unrecognized owned Unix listener: ' + repr(path))
            continue
        matched.add(path)
        _validate_metadata(path, mode, observation, failures)
    if dynamic and libnetwork_count != 1:
        failures.append('missing required or multiple libnetwork listeners: expected exactly one')
    failures.extend('missing required owned listener: ' + path for path in required if path not in matched)
    filesystem_inodes = set()
    for row in listeners:
        metadata = observation.get('metadata_before', {}).get(row['path'])
        if isinstance(metadata, dict) and isinstance(metadata.get('ino'), int):
            inode = (metadata.get('dev'), metadata['ino'])
            if inode in filesystem_inodes:
                failures.append('duplicate filesystem socket inode: ' + row['path'])
            filesystem_inodes.add(inode)
    return listeners


def _validate_metadata(path, socket_mode, observation, failures):
    before, after = observation.get('metadata_before', {}), observation.get('metadata_after', {})
    # Include / and /run; no-follow collection never resolves symlink ancestry.
    paths = [str(p) for p in reversed(PurePosixPath(path).parents)] + [path]
    for current in paths:
        entry = before.get(current)
        if not isinstance(entry, dict) or entry != after.get(current):
            failures.append('filesystem metadata unavailable/changed: ' + current)
            continue
        needed = ('mode', 'uid', 'gid', 'ino', 'dev', 'ctime_ns', 'mtime_ns', 'nlink')
        if any(not isinstance(entry.get(k), int) for k in needed) or entry['ino'] <= 0:
            failures.append('incomplete filesystem stat: ' + current)
            continue
        expected_type = stat.S_ISSOCK if current == path else stat.S_ISDIR
        if not expected_type(entry['mode']) or entry['uid'] != 0 or entry['gid'] != 0:
            failures.append('filesystem type/symlink/owner outside profile: ' + current)
        mode = stat.S_IMODE(entry['mode'])
        if current == path:
            if mode != socket_mode:
                failures.append('socket mode outside exact source profile: ' + current)
        elif current == PRIVATE_ROOT:
            if mode != 0o700:
                failures.append('private root must be exact0700: ' + current)
        elif current == PRIVATE_ROOT + '/docker/extensions' and mode != 0o700:
            failures.append('extensions directory must be source-enforced0700: ' + current)
        elif mode & 0o022 or mode & 0o7000:
            failures.append('unsafe ancestor mode: ' + current)


def _stat(s):
    return {'mode': s.st_mode, 'uid': s.st_uid, 'gid': s.st_gid, 'ino': s.st_ino,
            'dev': s.st_dev, 'ctime_ns': s.st_ctime_ns, 'mtime_ns': s.st_mtime_ns, 'nlink': s.st_nlink}


def _path_stat(path):
    """Walk with dirfd O_NOFOLLOW, keeping symlinks as metadata for rejection."""
    parts = PurePosixPath(path).parts
    if not path.startswith('/') or len(parts) > 16 or '..' in parts:
        raise ValueError('invalid metadata path')
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if path == '/':
            return _stat(os.fstat(fd))
        for part in parts[1:-1]:
            next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        return _stat(os.stat(parts[-1], dir_fd=fd, follow_symlinks=False))
    finally:
        os.close(fd)


def _bounded_text(path):
    with open(path, 'rb') as stream:
        data = stream.read(MAX_READ + 1)
    # Retain the bounded prefix on oversize so the failure receipt carries observed bytes.
    return data.decode('ascii', errors='strict')


def _fd_inventory(path, deadline):
    entries, socket_stats, errors = {}, {}, []
    with os.scandir(path) as scanner:
        for count, entry in enumerate(scanner):
            if count >= MAX_FDS or time.monotonic() >= deadline:
                errors.append('FD count/time limit exceeded; inventory incomplete')
                break
            try:
                entries[entry.name] = os.readlink(entry.path)
                if entries[entry.name].startswith('socket:'):
                    socket_stats[entry.name] = _stat(os.stat(entry.path))
                    if entries[entry.name] != os.readlink(entry.path):
                        errors.append('FD target changed during stat: ' + entry.name)
            except OSError as exc:
                errors.append('FD unreadable/changed: ' + entry.name + ': ' + str(exc))
    return entries, socket_stats, errors


def _process(proc, deadline):
    raw_stat = _bounded_text(proc + '/stat')
    if len(raw_stat) > MAX_READ:
        raise ValueError('PID stat byte limit exceeded')
    split = raw_stat.rsplit(')', 1)
    if len(split) != 2 or len(split[1].split()) < 20:
        raise ValueError('malformed PID stat')
    start = split[1].split()[19]
    status = _bounded_text(proc + '/status')
    if len(status) > MAX_READ:
        raise ValueError('PID status byte limit exceeded')
    ids = {}
    for line in status.splitlines():
        if line.startswith(('Uid:', 'Gid:')):
            key, values = line.split(':', 1)
            ids[key.lower()] = [int(value) for value in values.split()]
    exe = proc + '/exe'
    link = os.readlink(exe)
    digest = hashlib.sha256()
    with open(exe, 'rb') as stream:
        info = _stat(os.fstat(stream.fileno()))
        count = 0
        while True:
            chunk = stream.read(1024 * 1024)
            if not chunk:
                break
            count += len(chunk)
            if count > MAX_EXE or time.monotonic() >= deadline:
                raise ValueError('executable byte/time limit exceeded')
            digest.update(chunk)
        if info != _stat(os.fstat(stream.fileno())):
            raise ValueError('executable metadata changed while hashing')
    return {'start_token': start, 'uid': ids.get('uid'), 'gid': ids.get('gid'),
            'exe_link': link, 'exe_stat': info, 'exe_sha256': digest.hexdigest(),
            'net_namespace': os.readlink(proc + '/ns/net'), 'stat_raw': raw_stat, 'status_raw': status}


def collect(pid, approved_identity, *, proc_root='/proc', timeout_seconds=5):
    """Complete bounded collection before validation, even if earlier categories fail.

    proc_root supports offline fixtures; production caller must use /proc. File
    reads are finite byte/count observations on local procfs/filesystem, not a
    guarantee against an uninterruptible hostile filesystem read.
    """
    observation = {'pid': pid, 'approved_identity': dict(approved_identity), 'errors': [],
                   'scope': SCOPE, 'proc_root': proc_root, 'started_monotonic': time.monotonic(),
                   'metadata_before': {}, 'metadata_after': {}}
    if not isinstance(pid, int) or pid <= 0 or not 0 < timeout_seconds <= 30:
        observation['errors'].append('invalid PID/time bound')
        return observation
    deadline = time.monotonic() + timeout_seconds
    proc = str(proc_root).rstrip('/') + '/' + str(pid)

    def attempt(category, action):
        try:
            if time.monotonic() >= deadline:
                raise ValueError('collection deadline exceeded')
            return action()
        except (OSError, ValueError, UnicodeError) as exc:
            observation['errors'].append(category + ': ' + type(exc).__name__ + ': ' + str(exc))
            return None

    observation['process_before'] = attempt('process_before', lambda: _process(proc, deadline))
    inventory = attempt('fd_before', lambda: _fd_inventory(proc + '/fd', deadline))
    observation['fd_before'], observation['fd_socket_stats_before'], errors = inventory if inventory else (None, {}, [])
    observation['errors'].extend(errors)
    observation['tables_before'] = {t: attempt(t + '_before', lambda t=t: _bounded_text(proc + '/net/' + t)) for t in TABLES}
    rows, errors = parse_unix(observation['tables_before']['unix'])
    observation['errors'].extend(errors)
    owned = set()
    for value in (observation['fd_before'] or {}).values():
        match = re.fullmatch(r'socket:\[([1-9][0-9]*)\]', value)
        if match:
            owned.add(match[1])
    paths = set()
    for row in rows:
        if row['inode'] in owned and row['flags'] & 0x10000 and row['path'].startswith('/'):
            paths.update(str(p) for p in PurePosixPath(row['path']).parents)
            paths.add(row['path'])
    if len(paths) > MAX_PATHS:
        observation['errors'].append('metadata path limit exceeded')
        paths = set(sorted(paths)[:MAX_PATHS])
    for phase in ('before', 'after'):
        for path in sorted(paths):
            observation['metadata_' + phase][path] = attempt('metadata_' + phase + ':' + path, lambda path=path: _path_stat(path))
    observation['tables_after'] = {t: attempt(t + '_after', lambda t=t: _bounded_text(proc + '/net/' + t)) for t in TABLES}
    inventory = attempt('fd_after', lambda: _fd_inventory(proc + '/fd', deadline))
    observation['fd_after'], observation['fd_socket_stats_after'], errors = inventory if inventory else (None, {}, [])
    observation['errors'].extend(errors)
    observation['process_after'] = attempt('process_after', lambda: _process(proc, deadline))
    observation['ended_monotonic'] = time.monotonic()
    return observation
