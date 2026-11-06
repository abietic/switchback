"""Offline seams only. ss is captured; filesystem/process metadata is synthetic."""
import ast
import copy
import importlib.util
import json
from pathlib import Path
import re
import stat
import tempfile
import hashlib
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[3]
FIXTURES = Path(__file__).resolve().parent / 'fixtures'
INET_HEADERS = {
    'tcp': 'sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode',
    'tcp6': 'sl local_address remote_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode',
    'udp': 'sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode ref pointer drops',
    'udp6': 'sl local_address remote_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode ref pointer drops',
}
PRIVATE = '/run/switchback-engine'
DOCKER_PATHS = [PRIVATE + '/docker.sock', PRIVATE + '/docker/extensions/callback.sock',
                PRIVATE + '/docker/metrics.sock', PRIVATE + '/docker/libnetwork/ea06f27642d8.sock']


def captured_rows():
    fixture = json.loads((FIXTURES / 'captured_engine_listeners.json').read_text())
    rows = []
    for line in fixture['ss_records']:
        fields = line.split()
        pid, fd = re.search(r'pid=(\d+),fd=(\d+)', line).groups()
        rows.append({'path': fields[4], 'inode': fields[5], 'pid': int(pid), 'fd': fd})
    return fixture, rows


def load_checker():
    spec = importlib.util.spec_from_file_location('engine_listener', ROOT / 'deploy/experience/engine_listener_check.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def synthetic_stat(path, mode):
    # Deliberately not equal to the ss socket inode: filesystem and socket inodes differ.
    return {'mode': mode, 'uid': 0, 'gid': 0, 'ino': 900000 + sum(map(ord, path)),
            'dev': 42, 'ctime_ns': 100, 'mtime_ns': 100, 'nlink': 1}


def synthetic_observation():
    _, rows = captured_rows()
    identity = {'daemon': 'dockerd', 'version': '29.9.0',
                'source_revision': 'a5b58c94d334674eae3a3207d4f05417643a90d0',
                'executable_sha256': '1' * 64, 'expected_start_token': '12345'}
    process = {'start_token': '12345', 'exe_sha256': '1' * 64, 'exe_link': '/usr/bin/dockerd',
               'exe_stat': synthetic_stat('/usr/bin/dockerd', stat.S_IFREG | 0o755),
               'uid': [0, 0, 0, 0], 'gid': [0, 0, 0, 0], 'net_namespace': 'net:[4026531840]'}
    unix = 'Num       RefCount Protocol Flags    Type St Inode Path\n'
    unix += ''.join(f"0000000000000000: 00000002 00000000 00010000 0001 01 {r['inode']} {r['path']}\n" for r in rows)
    tables = {'unix': unix}
    for name in ('tcp', 'tcp6', 'udp', 'udp6'):
        tables[name] = INET_HEADERS[name] + '\n'
    fds = {r['fd']: f"socket:[{r['inode']}]" for r in rows if r['pid'] == 2882}
    metadata = {}
    for path in DOCKER_PATHS:
        for parent in [*reversed(Path(path).parents)]:
            name = str(parent)
            mode = 0o700 if name in (PRIVATE, PRIVATE + '/docker/extensions') else 0o755
            if name == PRIVATE + '/docker/libnetwork':
                mode = 0o600
            metadata[name] = synthetic_stat(name, stat.S_IFDIR | mode)
        mode = 0o660 if path == PRIVATE + '/docker.sock' else 0o600 if '/libnetwork/' in path else 0o777
        metadata[path] = synthetic_stat(path, stat.S_IFSOCK | mode)
    observation = {'pid': 2882, 'approved_identity': identity, 'process_before': process,
                   'process_after': copy.deepcopy(process), 'fd_before': fds, 'fd_after': dict(fds),
                   'tables_before': tables, 'tables_after': dict(tables),
                   'metadata_before': metadata, 'metadata_after': copy.deepcopy(metadata), 'errors': [],
                   'fd_socket_stats_before': {r['fd']: synthetic_stat(r['path'], stat.S_IFSOCK | 0o777) | {'ino': int(r['inode'])} for r in rows if r['pid'] == 2882},
                   'fd_socket_stats_after': {r['fd']: synthetic_stat(r['path'], stat.S_IFSOCK | 0o777) | {'ino': int(r['inode'])} for r in rows if r['pid'] == 2882},
                   'evidence_kind': 'captured_ss_rows_with_synthetic_other_metadata'}
    return identity, observation


def containerd_observation():
    identity, obs = synthetic_observation()
    identity.update(daemon='containerd', version='2.4.1',
                    source_revision='f2551031d7276a770f65f98c9b52e57e7dad07e8')
    obs['pid'] = 2871
    for phase in ('before', 'after'):
        obs['process_' + phase]['exe_link'] = '/usr/bin/containerd'
        rows = [r for r in captured_rows()[1] if r['pid'] == 2871]
        obs['fd_' + phase] = {r['fd']: f"socket:[{r['inode']}]" for r in rows}
        obs['fd_socket_stats_' + phase] = {r['fd']: synthetic_stat(r['path'], stat.S_IFSOCK | 0o777) | {'ino': int(r['inode'])} for r in rows}
        for row in rows:
            obs['metadata_' + phase][PRIVATE + '/containerd'] = synthetic_stat(PRIVATE + '/containerd', stat.S_IFDIR | 0o755)
            obs['metadata_' + phase][row['path']] = synthetic_stat(row['path'], stat.S_IFSOCK | 0o660)
    return identity, obs


def add_listener(obs, path, inode='99999', fd='99'):
    for phase in ('before', 'after'):
        obs['tables_' + phase]['unix'] += f'0000000000000000: 00000002 00000000 00010000 0001 01 {inode}' + (' ' + path if path else '') + '\n'
        obs['fd_' + phase][fd] = f'socket:[{inode}]'
        obs['fd_socket_stats_' + phase][fd] = synthetic_stat(path, stat.S_IFSOCK | 0o600) | {'ino': int(inode)}
        obs['metadata_' + phase][path] = synthetic_stat(path, stat.S_IFSOCK | 0o600)


def inet_row(table, inode='99998'):
    # Synthetic complete kernel-format rows; not captured runtime network evidence.
    address = '0' * (32 if table.endswith('6') else 8)
    prefix = f'0: {address}:0016 {address}:0000 0A 00000000:00000000 00:00000000 00000000 0 0 {inode}'
    tail = '1 0000000000000000 100 0 0 10 -1' if table.startswith('tcp') else '2 0000000000000000 0'
    return prefix + ' ' + tail + '\n'


def ambient_observation():
    identity, obs = synthetic_observation()
    fixture = json.loads((FIXTURES / 'captured_ambient_net_table_drift.json').read_text())
    for phase in ('before', 'after'):
        for path, pair in fixture['tables'].items():
            table = path.rsplit('/', 1)[-1]
            raw = pair[phase]
            if table == 'unix':
                raw += '\n'.join(obs['tables_' + phase][table].splitlines()[1:]) + '\n'
            obs['tables_' + phase][table] = raw
    obs['evidence_kind'] = 'actual_ambient_tables_with_synthetic_daemon_identity_fd_metadata'
    return identity, obs


class ListenerTests(unittest.TestCase):
    def test_actual_ambient_drift_fixture_provenance_and_public_validate(self):
        fixture = json.loads((FIXTURES / 'captured_ambient_net_table_drift.json').read_text())
        self.assertEqual(fixture['evidence_kind'], 'actual_readonly_ambient_net_tables_no_daemon_collection')
        self.assertTrue(fixture['provenance']['no_engine_operation'])
        self.assertLess(fixture['provenance']['elapsed_seconds'], 0.1)
        for pair in fixture['tables'].values():
            for phase in ('before', 'after'):
                data = pair[phase].encode()
                self.assertEqual(len(data), pair[phase + '_bytes'])
                self.assertEqual(hashlib.sha256(data).hexdigest(), pair[phase + '_sha256'])
        tcp = fixture['tables']['/proc/self/net/tcp']
        self.assertIn('02:00003452', tcp['before'])
        self.assertIn('02:0000344F', tcp['after'])
        identity, obs = ambient_observation()
        result = load_checker().validate(obs, identity)
        self.assertTrue(result['ok'], result['failures'])
        self.assertEqual(result['observation'], obs)
        self.assertTrue(any('tcp' in d and 'non-authoritative' in d for d in result['diagnostics']))

    def test_ambient_add_remove_change_and_owned_unix_volatile_fields_accept(self):
        identity, base = ambient_observation()
        for phase in ('before', 'after'):
            for table in INET_HEADERS:
                with self.subTest(phase=phase, table=table):
                    obs = copy.deepcopy(base)
                    obs['tables_' + phase][table] += inet_row(table)
                    result = load_checker().validate(obs, identity)
                    self.assertTrue(result['ok'], result['failures'])
        obs = copy.deepcopy(base)
        owned = next(iter(obs['fd_before'].values()))[8:-1]
        rows = obs['tables_after']['unix'].splitlines()
        for i, row in enumerate(rows[1:], 1):
            fields = row.split()
            if fields[6] == owned:
                fields[0] = 'ffffffffffffffff:'
                fields[1] = '00000009'
                rows[i] = ' '.join(fields)
        obs['tables_after']['unix'] = '\n'.join(rows) + '\n'
        result = load_checker().validate(obs, identity)
        self.assertTrue(result['ok'], result['failures'])

    def test_each_snapshot_strict_grammar_is_required_despite_ambient_drift(self):
        identity, base = ambient_observation()
        for phase in ('before', 'after'):
            for table in ('unix', *INET_HEADERS):
                good = base['tables_' + phase][table]
                variants = [None, good.rstrip('\n'), 'bad header\n', good + 'malformed\n']
                if table != 'unix':
                    fields = inet_row(table).split(); fields[3] = 'FF'
                    variants.append(INET_HEADERS[table] + '\n' + ' '.join(fields) + '\n')
                for raw in variants:
                    with self.subTest(phase=phase, table=table, raw=str(raw)[:20]):
                        obs = copy.deepcopy(base)
                        obs['tables_' + phase][table] = raw
                        self.assert_denied(obs, identity, table)

    def test_owned_inet_in_either_snapshot_denies_all_four_tables(self):
        identity, base = ambient_observation()
        inode = next(iter(base['fd_before'].values()))[8:-1]
        for phase in ('before', 'after'):
            for table in INET_HEADERS:
                with self.subTest(phase=phase, table=table):
                    obs = copy.deepcopy(base)
                    obs['tables_' + phase][table] += inet_row(table, inode)
                    self.assert_denied(obs, identity, table + ' daemon INET socket')

    def test_owned_unix_security_identity_changes_or_disappears_deny(self):
        identity, base = ambient_observation()
        inode = next(iter(base['fd_before'].values()))[8:-1]
        for index, value in ((2, '00000001'), (3, '00000000'), (3, '00010001'),
                             (4, '0002'), (5, '03'), (6, '99997'), (7, PRIVATE + '/changed.sock'),
                             (7, '@abstract'), (7, '')):
            with self.subTest(index=index, value=value):
                obs = copy.deepcopy(base)
                lines = obs['tables_after']['unix'].splitlines()
                for i, line in enumerate(lines[1:], 1):
                    fields = line.split()
                    if fields[6] == inode:
                        fields[index] = value
                        lines[i] = ' '.join(fields).rstrip()
                obs['tables_after']['unix'] = '\n'.join(lines) + '\n'
                self.assert_denied(obs, identity, 'owned Unix socket identity changed')
        obs = copy.deepcopy(base)
        obs['tables_after']['unix'] = '\n'.join(line for line in obs['tables_after']['unix'].splitlines()
                                                   if len(line.split()) < 7 or line.split()[6] != inode) + '\n'
        self.assert_denied(obs, identity, 'owned Unix socket identity changed')
        # An owned non-listener is also part of the stable inventory, not dropped
        # by intersecting listener sets across snapshots.
        obs = copy.deepcopy(base)
        add_listener(obs, PRIVATE + '/extra.sock')
        obs['tables_before']['unix'] = '\n'.join(line for line in obs['tables_before']['unix'].splitlines()
                                                    if '99999' not in line) + '\n'
        self.assert_denied(obs, identity, 'owned Unix socket identity changed')

    def test_after_only_profile_collision_and_owned_duplicates_deny(self):
        identity, base = ambient_observation()
        for flags, state in (('00010000', '01'), ('00000000', '03')):
            obs = copy.deepcopy(base)
            obs['tables_after']['unix'] += f'0: 00000002 00000000 {flags} 0001 {state} 99996 {DOCKER_PATHS[0]}\n'
            self.assert_denied(obs, identity, 'profile listener belongs to other PID/inode')
        obs = copy.deepcopy(base)
        row = next(line for line in obs['tables_after']['unix'].splitlines() if DOCKER_PATHS[0] in line)
        obs['tables_after']['unix'] += row + '\n'
        self.assert_denied(obs, identity, 'duplicate Unix socket inode')

    def test_ambient_drift_cannot_mask_fd_stat_inventory_or_private_root_drift(self):
        identity, base = ambient_observation()
        fd = next(iter(base['fd_before']))
        for category, change in (('FD inventory', 'link'), ('FD inventory', 'alias'),
                                 ('FD socket stat', 'stat'), ('metadata unavailable/changed', 'root')):
            obs = copy.deepcopy(base)
            if change == 'link': obs['fd_after'][fd] = 'socket:[99995]'
            elif change == 'alias': obs['fd_after']['999'] = obs['fd_after'][fd]
            elif change == 'stat': obs['fd_socket_stats_after'][fd]['ino'] += 1
            else: obs['metadata_after'][PRIVATE]['mode'] = stat.S_IFDIR | 0o755
            self.assert_denied(obs, identity, category)

    def test_collect_then_validate_reads_actual_ambient_drift_fixture(self):
        checker = load_checker()
        identity, base = ambient_observation()
        with tempfile.TemporaryDirectory() as temp:
            proc = Path(temp) / str(base['pid'])
            (proc / 'net').mkdir(parents=True)
            for table, raw in base['tables_before'].items():
                (proc / 'net' / table).write_text(raw)
            original_read = checker._bounded_text
            reads = []
            def read_table(path):
                table = Path(path).name
                reads.append(table)
                raw = original_read(path)
                # One actual fixture-backed before read, then a portable
                # deterministic replacement representing the captured after.
                Path(path).write_text(base['tables_after'][table])
                return raw
            inventory = (base['fd_before'], base['fd_socket_stats_before'], [])
            with mock.patch.object(checker, '_process', return_value=base['process_before']), \
                    mock.patch.object(checker, '_fd_inventory', return_value=inventory), \
                    mock.patch.object(checker, '_path_stat', side_effect=base['metadata_before'].__getitem__), \
                    mock.patch.object(checker, '_bounded_text', side_effect=read_table):
                obs = checker.collect(base['pid'], identity, proc_root=temp)
        self.assertEqual(reads, list(checker.TABLES) * 2)
        self.assertEqual(obs['errors'], [])
        self.assertEqual(obs['tables_before'], base['tables_before'])
        self.assertEqual(obs['tables_after'], base['tables_after'])
        result = checker.validate(obs, identity)
        self.assertTrue(result['ok'], result['failures'])

    def test_real_old_failure_is_preserved_and_complete_profile_validates(self):
        receipt, rows = captured_rows()
        self.assertEqual(receipt['primary_failure']['message'], 'extra daemon Unix listener')
        source = ast.parse((FIXTURES / 'old_daemon_listeners.py').read_text())
        function = next(n for n in source.body if isinstance(n, ast.FunctionDef) and n.name == 'daemon_listeners')
        # Execute the actual frozen old function against ss-derived proc/fd views, never VM I/O.
        class FakePath:
            def __init__(self, path): self.path = path
            def iterdir(self): return [FakePath('/proc/2882/fd/' + r['fd']) for r in rows if r['pid'] == 2882]
            def read_text(self): return synthetic_observation()[1]['tables_before']['unix']
        links = {'/proc/2882/fd/' + r['fd']: f"socket:[{r['inode']}]" for r in rows if r['pid'] == 2882}
        class FakeOS:
            @staticmethod
            def readlink(path): return links[path.path]
        def require(ok, message):
            if not ok: raise RuntimeError(message)
        ns = {'Path': FakePath, 'os': FakeOS, 'require': require}
        exec(compile(ast.Module(body=[function], type_ignores=[]), 'frozen-old-checker', 'exec'), ns)
        with self.assertRaisesRegex(RuntimeError, 'extra daemon Unix listener'):
            ns['daemon_listeners'](2882, [PRIVATE + '/docker.sock'])
        identity, observation = synthetic_observation()
        result = load_checker().validate(observation, identity)
        self.assertTrue(result['ok'], result['failures'])
        self.assertEqual(len(result['listeners']), 4)
        self.assertIn('finite', result['scope'])
        self.assertEqual(result['observation']['evidence_kind'], observation['evidence_kind'])

    def test_deliverable_fixtures_preserve_captured_bytes_and_exact_old_function(self):
        fixture, rows = captured_rows()
        self.assertEqual(len(rows), 6)
        raw = ('\n'.join(fixture['ss_records']) + '\n').encode()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), fixture['provenance']['captured_six_rows']['sha256'])
        function = (FIXTURES / 'old_daemon_listeners.py').read_bytes()
        self.assertEqual(hashlib.sha256(function).hexdigest(), fixture['provenance']['old_function']['sha256'])
        self.assertEqual(len(function), fixture['provenance']['old_function']['bytes'])
        self.assertEqual(fixture['evidence_kind'], 'captured_ss_records_and_original_failure_only')

    def test_malformed_inet_public_fields_and_incomplete_tails_deny_whole_observation(self):
        identity, base = synthetic_observation()
        reviewer_rows = [
            '0: 00000000:0016 00000000:0000 0A BAD_QUEUE BAD_TIMER BAD_RETRY 0 0 99998 1 00000000',
            '0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 99998',
        ]
        for table in INET_HEADERS:
            good = inet_row(table).split()
            mutations = [(0, '١:'), (0, '2147483648:'), (1, 'BAD_ADDRESS'), (3, 'GG'),
                         (4, 'BAD_QUEUE'), (4, '00000000:'), (4, '100000000:00000000'),
                         (5, 'BAD_TIMER'), (5, '00:0000000'), (5, '00:10000000000000000'),
                         (6, 'BAD_RETRY'), (6, '100000000'), (7, '-1'), (7, '4294967296'),
                         (8, '2147483648'), (9, '18446744073709551616'), (9, '١'),
                         (10, '2147483648'), (11, 'BAD_POINTER'), (11, '00000000'), (11, 'pK-error'),
                         (11, '(ptrval)'), (11, '(____ptrval___)')]
            mutations += ([(12, '-1'), (13, '18446744073709551616'), (14, '4294967296'),
                           (15, '4294967296'), (16, '-2147483649')] if table.startswith('tcp')
                          else [(12, '-1'), (12, '4294967296'), (5, '01:00000000'), (6, '00000001'), (8, '1')])
            bad_rows = []
            for index, value in mutations:
                fields = list(good); fields[index] = value
                bad_rows.append(' '.join(fields))
            bad_rows += [' '.join(good[:size]) for size in range(10, len(good))]
            bad_rows += [' '.join(good + ['0'])]
            if table == 'tcp': bad_rows += reviewer_rows
            for raw in bad_rows:
                with self.subTest(table=table, raw=raw):
                    obs = copy.deepcopy(base)
                    for phase in ('before', 'after'):
                        obs['tables_' + phase][table] += raw + '\n'
                    self.assert_denied(obs, identity, table + ' malformed row')

    def test_supported_complete_inet_variants_accept_and_retain_rows(self):
        checker = load_checker()
        for table in INET_HEADERS:
            rows = [inet_row(table).strip()]
            hidden = inet_row(table).split(); hidden[11] = '(____ptrval____)'
            rows.append(' '.join(hidden))  # Exact source-defined early hash-key placeholder.
            if table.startswith('tcp'):
                full = inet_row(table).split(); full[3] = '03'; full[5] = '01:0000000012345678'
                rows.append(' '.join(full))  # Full SYN_RECV is not a short request.
                address = '0' * (32 if table.endswith('6') else 8)
                for state in ('05', '06'):
                    rows.append(f'0: {address}:0016 {address}:0000 {state} 00000000:00000000 03:0000000012345678 00000000 0 0 0 1 0000000000000000')
                rows.append(f'0: {address}:0016 {address}:0000 03 00000000:00000000 01:00000001 00000002 1000 0 0 0 0000000000000000')
            identity, obs = synthetic_observation()
            for phase in ('before', 'after'):
                obs['tables_' + phase][table] += '\n'.join(rows) + '\n'
            result = checker.validate(obs, identity)
            self.assertTrue(result['ok'], result['failures'])
            parsed, errors = checker.parse_inet(obs['tables_before'][table], table)
            self.assertEqual(errors, [])
            self.assertEqual([row['raw'] for row in parsed], rows)

    def test_unsupported_inet_states_deny_the_complete_observation(self):
        for table in INET_HEADERS:
            # Kernel enum 0D is inet_diag-only, 0E the sentinel; others are unknown.
            states = ('00', '0D', '0E', '0F', 'FF')
            if table.startswith('tcp'):
                states += ('06', '0C')  # seq_show routes these to short formatters.
            for state in states:
                with self.subTest(table=table, state=state):
                    identity, obs = synthetic_observation()
                    fields = inet_row(table).split(); fields[3] = state
                    for phase in ('before', 'after'):
                        obs['tables_' + phase][table] += ' '.join(fields) + '\n'
                    self.assert_denied(obs, identity, table + ' malformed row')

    def test_supported_full_inet_states_include_established_listen_and_udp(self):
        for table in INET_HEADERS:
            # UDP's generic formatter prints sk_state: retain the ordinary enum
            # domain rather than inventing a narrower UDP reachability claim.
            states = ('01', '02', '03', '04', '05', '07', '08', '09', '0A', '0B')
            if table.startswith('udp'):
                states += ('06', '0C')
            identity, obs = synthetic_observation()
            rows = []
            for state in states:
                fields = inet_row(table).split(); fields[3] = state
                rows.append(' '.join(fields))
            for phase in ('before', 'after'):
                obs['tables_' + phase][table] += '\n'.join(rows) + '\n'
            result = load_checker().validate(obs, identity)
            self.assertTrue(result['ok'], result['failures'])

    def test_inet_headers_and_unsupported_short_variants_fail_closed(self):
        checker = load_checker()
        for table, header in INET_HEADERS.items():
            for bad in (header.replace('uid', 'UNKNOWN'), header + ' extra', header.replace('inode', ''),
                        header.replace('rem_address', 'remote_address') if table in ('tcp', 'udp') else header.replace('remote_address', 'rem_address')):
                identity, obs = synthetic_observation()
                for phase in ('before', 'after'): obs['tables_' + phase][table] = bad + '\n'
                self.assert_denied(obs, identity, table + ' invalid header')
        rows = [
            '0: 00000000:0016 00000000:0000 0A 00000000:00000000 03:00000000 00000000 0 0 0 1 0000000000000000',
            '0: 00000000:0016 00000000:0000 06 00000001:00000000 03:00000000 00000000 0 0 0 1 0000000000000000',
            '0: 00000000:0016 00000000:0000 06 00000000:00000000 03:00000000 00000001 0 0 0 1 0000000000000000',
            '0: 00000000:0016 00000000:0000 03 00000000:00000000 01:00000000 00000000 0 0 0 1 0000000000000000',
            '0: 00000000:0016 00000000:0000 03 00000000:00000000 01:00000000 00000000 0 0 1 0 0000000000000000',
        ]
        full = inet_row('tcp').split(); full[5] = '03:00000000'; rows.append(' '.join(full))
        for row in rows:
            identity, obs = synthetic_observation()
            for phase in ('before', 'after'): obs['tables_' + phase]['tcp'] += row + '\n'
            self.assert_denied(obs, identity, 'tcp malformed row')
        self.assertTrue(checker.parse_inet(INET_HEADERS['tcp'] + '\n', 'sctp')[1])

    def test_fd_socket_inode_must_bind_to_observed_descriptor_stat(self):
        identity, obs = synthetic_observation()
        first = next(iter(obs['fd_socket_stats_before']))
        obs['fd_socket_stats_before'][first]['ino'] += 1
        result = load_checker().validate(obs, identity)
        self.assertFalse(result['ok'])
        self.assertTrue(any('FD socket stat' in f for f in result['failures']), result['failures'])

    def test_fixed_profile_requires_every_source_declared_listener(self):
        checker = load_checker()
        identity, base = synthetic_observation()
        for path in DOCKER_PATHS:
            with self.subTest(missing=path):
                obs = copy.deepcopy(base)
                row = next(r for r in captured_rows()[1] if r['path'] == path)
                for phase in ('before', 'after'):
                    obs['tables_' + phase]['unix'] = ''.join(line + '\n' for line in obs['tables_' + phase]['unix'].splitlines() if path not in line)
                    obs['fd_' + phase].pop(row['fd'])
                    obs['fd_socket_stats_' + phase].pop(row['fd'])
                result = checker.validate(obs, identity)
                self.assertFalse(result['ok'], path)
                self.assertTrue(any('missing required' in f for f in result['failures']), result['failures'])

    def assert_denied(self, obs, identity, category):
        result = load_checker().validate(obs, identity)
        self.assertFalse(result['ok'], result['failures'])
        self.assertTrue(any(category in f for f in result['failures']), result['failures'])
        return result

    def test_containerd_profile_requires_grpc_and_ttrpc(self):
        identity, obs = containerd_observation()
        self.assertTrue(load_checker().validate(obs, identity)['ok'])
        for path in (PRIVATE + '/containerd/containerd.sock', PRIVATE + '/containerd/containerd.sock.ttrpc'):
            bad = copy.deepcopy(obs)
            for phase in ('before', 'after'):
                bad['tables_' + phase]['unix'] = ''.join(line + '\n' for line in bad['tables_' + phase]['unix'].splitlines() if line.split()[-1] != path)
            self.assert_denied(bad, identity, 'missing required')

    def test_foreign_abstract_unnamed_and_invalid_libnetwork_paths_reject(self):
        for path in ('/run/foreign.sock', PRIVATE + '/extra.sock', '@abstract', '',
                     PRIVATE + '/docker/libnetwork/ABCDEF123456.sock',
                     PRIVATE + '/docker/libnetwork/abcd.sock', PRIVATE + '/docker/libnetwork/../../escape.sock'):
            with self.subTest(path=path):
                identity, obs = synthetic_observation()
                add_listener(obs, path)
                self.assert_denied(obs, identity, 'unrecognized owned')
        identity, obs = synthetic_observation()
        add_listener(obs, PRIVATE + '/docker/libnetwork/abcdef123456.sock')
        self.assert_denied(obs, identity, 'multiple libnetwork')

    def test_paths_inodes_and_foreign_pid_cannot_substitute_for_owned_listener(self):
        identity, base = synthetic_observation()
        for change, category in (('duplicate_path', 'duplicate Unix listener path'),
                                 ('duplicate_inode', 'duplicate Unix socket inode'),
                                 ('foreign_pid', 'other PID/inode'),
                                 ('filesystem_inode', 'duplicate filesystem socket inode')):
            with self.subTest(change=change):
                obs = copy.deepcopy(base)
                if change == 'duplicate_path':
                    add_listener(obs, DOCKER_PATHS[0])
                elif change == 'duplicate_inode':
                    add_listener(obs, PRIVATE + '/unknown.sock', inode='24563')
                elif change == 'foreign_pid':
                    for phase in ('before', 'after'):
                        obs['fd_' + phase].pop('7')
                        obs['fd_socket_stats_' + phase].pop('7')
                else:
                    for phase in ('before', 'after'):
                        obs['metadata_' + phase][DOCKER_PATHS[1]]['ino'] = obs['metadata_' + phase][DOCKER_PATHS[0]]['ino']
                self.assert_denied(obs, identity, category)

    def test_all_inet_categories_survive_an_earlier_unix_mismatch(self):
        identity, obs = synthetic_observation()
        add_listener(obs, PRIVATE + '/unknown.sock')
        for name in ('tcp', 'tcp6', 'udp', 'udp6'):
            row = inet_row(name, inode='24563')
            for phase in ('before', 'after'):
                obs['tables_' + phase][name] += row
        result = self.assert_denied(obs, identity, 'unrecognized owned')
        for name in ('tcp', 'tcp6', 'udp', 'udp6'):
            self.assertTrue(any(f.startswith(name + ' daemon INET') for f in result['failures']))

    def test_private_barrier_symlink_parent_ownership_modes_missing_stat_and_replacement(self):
        identity, base = synthetic_observation()
        cases = [(PRIVATE, 'mode', stat.S_IFDIR | 0o755, 'exact0700'),
                 (PRIVATE, 'mode', stat.S_IFLNK | 0o777, 'symlink'),
                 (PRIVATE + '/docker', 'mode', stat.S_IFLNK | 0o777, 'symlink'),
                 (PRIVATE + '/docker', 'mode', stat.S_IFDIR | 0o775, 'unsafe ancestor'),
                 (DOCKER_PATHS[0], 'uid', 1000, 'owner'),
                 (PRIVATE + '/docker', 'gid', 1000, 'owner'),
                 (DOCKER_PATHS[1], 'mode', stat.S_IFSOCK | 0o700, 'socket mode'),
                 (DOCKER_PATHS[2], 'mode', stat.S_IFSOCK | 0o660, 'socket mode'),
                 (DOCKER_PATHS[3], 'mode', stat.S_IFSOCK | 0o777, 'socket mode'),
                 (DOCKER_PATHS[0], 'mode', stat.S_IFLNK | 0o660, 'symlink')]
        for path, key, value, category in cases:
            with self.subTest(path=path, key=key, value=value):
                obs = copy.deepcopy(base)
                for phase in ('before', 'after'):
                    obs['metadata_' + phase][path][key] = value
                self.assert_denied(obs, identity, category)
        obs = copy.deepcopy(base)
        obs['metadata_before'].pop(DOCKER_PATHS[1])
        self.assert_denied(obs, identity, 'metadata unavailable')
        obs = copy.deepcopy(base)
        obs['metadata_after'][DOCKER_PATHS[0]]['ino'] += 1
        self.assert_denied(obs, identity, 'metadata unavailable/changed')

    def test_identity_drift_unknown_build_and_fd_view_changes_reject(self):
        identity, base = synthetic_observation()
        for key, value in (('start_token', '12346'), ('exe_sha256', '2' * 64),
                           ('net_namespace', 'net:[999]'), ('uid', [0, 1, 0, 0])):
            obs = copy.deepcopy(base)
            obs['process_after'][key] = value
            self.assert_denied(obs, identity, 'identity missing or changed')
        obs = copy.deepcopy(base)
        obs['process_before']['stat_raw'] = 'CPU counters before'
        obs['process_after']['stat_raw'] = 'CPU counters after'
        self.assertTrue(load_checker().validate(obs, identity)['ok'], 'PID stat CPU counters may change')
        for field in ('version', 'source_revision', 'executable_sha256', 'expected_start_token'):
            bad_identity = dict(identity)
            bad_identity[field] = ''
            self.assert_denied(base, bad_identity, 'identity')
        obs = copy.deepcopy(base)
        obs['fd_after'].pop('7')
        self.assert_denied(obs, identity, 'FD inventory')
        obs = copy.deepcopy(base)
        obs['fd_socket_stats_before'].pop('7')
        self.assert_denied(obs, identity, 'FD socket stat')

    def test_parser_truncation_errors_oversized_and_changed_tables_reject(self):
        identity, base = synthetic_observation()
        for table in ('unix', 'tcp', 'tcp6', 'udp', 'udp6'):
            for raw in ('truncated', '', 'bad header\n', base['tables_before'][table] + 'malformed\n',
                        'x' * (load_checker().MAX_READ + 1) + '\n'):
                with self.subTest(table=table, prefix=raw[:20]):
                    obs = copy.deepcopy(base)
                    for phase in ('before', 'after'):
                        obs['tables_' + phase][table] = raw
                    self.assert_denied(obs, identity, table)
        obs = copy.deepcopy(base)
        obs['tables_after']['tcp'] += '\n'
        self.assert_denied(obs, identity, 'tcp malformed row')
        obs = copy.deepcopy(base)
        obs['errors'] = ['bounded FD read error: synthetic EACCES']
        self.assert_denied(obs, identity, 'synthetic EACCES')

    def test_collection_retains_partial_fd_mapping_and_all_failure_categories(self):
        # Explicit fake proc tree; never reads a daemon or a live socket pathname.
        checker = load_checker()
        identity, base = synthetic_observation()
        with tempfile.TemporaryDirectory() as temp:
            proc = Path(temp) / '2882'
            (proc / 'fd').mkdir(parents=True)
            (proc / 'net').mkdir()
            (proc / 'fd' / '7').symlink_to('socket:[24563]')  # intentional unavailable descriptor stat
            for name in ('unix', 'tcp', 'tcp6', 'udp', 'udp6'):
                (proc / 'net' / name).write_text(base['tables_before'][name] if name != 'unix' else base['tables_before'][name].splitlines()[0] + '\n')
            with (proc / 'net' / 'tcp').open('a') as stream:
                stream.write(inet_row('tcp', inode='24563'))
            obs = checker.collect(2882, identity, proc_root=temp)
        self.assertEqual(obs['fd_before']['7'], 'socket:[24563]')
        self.assertEqual(set(obs['tables_before']), {'unix', 'tcp', 'tcp6', 'udp', 'udp6'})
        self.assertEqual(set(obs['tables_after']), set(obs['tables_before']))
        result = self.assert_denied(obs, identity, 'FD unreadable/changed')
        self.assertTrue(any('tcp daemon INET' in f for f in result['failures']))
        self.assertTrue(any('process_before' in f for f in result['failures']))
        self.assertTrue(any('process_after' in f for f in result['failures']))

    def test_source_enforced_extension_directory_mode_and_numeric_network_id_reject(self):
        identity, obs = synthetic_observation()
        for phase in ('before', 'after'):
            obs['metadata_' + phase][PRIVATE + '/docker/extensions']['mode'] = stat.S_IFDIR | 0o755
        self.assert_denied(obs, identity, 'extensions directory')
        identity, obs = synthetic_observation()
        add_listener(obs, PRIVATE + '/docker/libnetwork/123456789012.sock')
        self.assert_denied(obs, identity, 'unrecognized owned')

    def test_collection_binds_pid_start_field_and_executable_bytes_in_fake_proc(self):
        checker = load_checker()
        identity, base = synthetic_observation()
        with tempfile.TemporaryDirectory() as temp:
            proc = Path(temp) / '2882'
            for name in ('fd', 'net', 'ns'):
                (proc / name).mkdir(parents=True, exist_ok=True)
            (proc / 'stat').write_text('2882 (synthetic daemon (name)) ' + ' '.join(['S'] + ['0'] * 18 + ['12345'] + ['0'] * 10) + '\n')
            (proc / 'status').write_text('Uid:\t0 0 0 0\nGid:\t0 0 0 0\n')
            binary = Path(temp) / 'synthetic-exe'
            binary.write_bytes(b'synthetic executable bytes, no execution')
            identity['executable_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
            (proc / 'exe').symlink_to(binary)
            (proc / 'ns/net').symlink_to('net:[4026531840]')
            for name in ('unix', 'tcp', 'tcp6', 'udp', 'udp6'):
                (proc / 'net' / name).write_text(base['tables_before'][name].splitlines()[0] + '\n')
            obs = checker.collect(2882, identity, proc_root=temp)
        self.assertEqual(obs['errors'], [])
        self.assertEqual(obs['process_before']['start_token'], '12345')
        self.assertEqual(obs['process_after']['exe_sha256'], identity['executable_sha256'])
        self.assert_denied(obs, identity, 'missing required')


if __name__ == '__main__':
    unittest.main()
