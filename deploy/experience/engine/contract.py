"""Fixed, pure M1 Engine policy. Importing this module performs no I/O.

All dictionaries are closed DTOs. A collector must establish facts from complete,
bounded read-only observations; an API response never substitutes for /proc proof.
"""
import configparser
from functools import wraps
import ipaddress
import json
import math
from pathlib import PurePosixPath
import re
import shlex
from types import MappingProxyType

BUDGET_SECONDS = 120
ENGINE_SOCKET = 'unix:///run/switchback-engine/docker.sock'
RECEIPT_PATH = '/var/lib/switchback/m1-engine/deployment-receipt.json'
UNIT_NAMES = ('switchback-m1-containerd.service', 'switchback-m1-engine.service')
FILES = MappingProxyType({
    UNIT_NAMES[0]: ('/etc/systemd/system/' + UNIT_NAMES[0], 0o644),
    UNIT_NAMES[1]: ('/etc/systemd/system/' + UNIT_NAMES[1], 0o644),
    'containerd.toml': ('/etc/switchback/m1-engine/containerd.toml', 0o600),
    'daemon.json': ('/etc/switchback/m1-engine/daemon.json', 0o600),
})
SOURCE_PREFIX = 'deploy/experience/engine/'
DATA_ROOT = '/mnt/lima-sbquota/docker'
CONTAINERD_ROOT = DATA_ROOT + '/containerd'
RUNTIME_PARENT = '/run/switchback-engine'
RUNTIME_ROOT = RUNTIME_PARENT + '/m1-resident'
CONTAINERD_SOCKET = RUNTIME_ROOT + '/containerd.sock'
TTRPC_SOCKET = RUNTIME_ROOT + '/containerd.ttrpc'
SHIM_SOCKET_DIR = RUNTIME_ROOT + '/s'
DOCKER_RUNTIME_ROOT = RUNTIME_ROOT + '/docker'
CALLBACK_SOCKET = DOCKER_RUNTIME_ROOT + '/extensions/callback.sock'
METRICS_SOCKET = DOCKER_RUNTIME_ROOT + '/metrics.sock'
LIBNETWORK_SOCKET_PATTERN = re.compile(re.escape(DOCKER_RUNTIME_ROOT) + r'/libnetwork/[0-9a-f]{12}\.sock')
SOCKET_PARENT_PATHS = (RUNTIME_PARENT, RUNTIME_ROOT, DOCKER_RUNTIME_ROOT,
                       DOCKER_RUNTIME_ROOT + '/extensions', DOCKER_RUNTIME_ROOT + '/libnetwork')
SOCKET_PARENT_KEYS = ('path', 'device', 'inode', 'kind', 'uid', 'gid', 'mode')
CORE_CGROUP = '/switchback.slice/switchback-core.slice'
NETWORKS = (('switchback-core1011_core', True), ('switchback-m1-worker-control', False))
RFC1918_SUBNETS = tuple(ipaddress.IPv4Network(value) for value in
                       ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'))
NETWORK_LABELS = MappingProxyType({'com.switchback.owner': 'm1-resident-engine',
                                  'com.switchback.engine-profile': 'm1-resident-engine-v1'})
STEP_NAMES = ('preflight', 'claim_stage', 'install_files', 'daemon_reload',
              'effective_configuration', 'containerd_start', 'containerd_ready',
              'engine_start', 'engine_ready', 'networks', 'final_identity', 'write_receipt')
RESTORE_CLASSES = ('containers', 'plugins', 'volumes', 'tasks', 'sandboxes', 'external', 'unknown')
STORE_RULE_REVISION = 'm1-engine-store-v1'
STORE_RULE_REVISION_V2 = 'm1-engine-store-v2'
STORE_RULE_REVISION_V3 = 'm1-engine-store-v3'
STORE_RULE_REVISION_V4 = 'm1-engine-store-v4'
EMPTY_PLUGIN_DIRECTORY_MODES = {
    DATA_ROOT + '/plugins/storage': 0o755,
    DATA_ROOT + '/plugins/tmp': 0o700,
}
QUOTA_BACKING_DEVICE_PATHS = frozenset((
    DATA_ROOT + '/overlay2/backingFsBlockDev',
    DATA_ROOT + '/volumes/backingFsBlockDev',
))
RETAINED_INPUTS = MappingProxyType({
    '.sb-proof-static-1013.bin': 'sha256:bf63d8a95fcc2e64619813aae35fdcbe871fdd9264caa3f365eb3aed0f679129',
    'bootstrap-1013/containerd.io_2.4.1-2~ubuntu.24.04~noble_arm64.deb': 'sha256:c19709945e4a41d27b2d82478d56e77d04ed039894af159c77f7302348cb1935',
    'bootstrap-1013/docker-buildx-plugin_0.38.0-1~ubuntu.24.04~noble_arm64.deb': 'sha256:6329557882fa9cb3e00fdc2edbad1de891779f7805e211c05f15f319bf6b725a',
    'bootstrap-1013/docker-ce-cli_29.9.0-1~ubuntu.24.04~noble_arm64.deb': 'sha256:42c2c42552901e7ad06a68a410be3ea3ea9b6f044923d2f96a1ade09466dd579',
    'bootstrap-1013/docker-ce_29.9.0-1~ubuntu.24.04~noble_arm64.deb': 'sha256:7dcaed8a78cdd64999a2b7bc245bb066f3f6a05aaa7e14e3d164a3504c74b810',
    'bootstrap-1013/docker-compose-plugin_5.6.0-1~ubuntu.24.04~noble_arm64.deb': 'sha256:eda8bb0d68dbba07c38d810eff11b00377f8255db802c49707a2f137d219e9d6',
})
MAX_STORE_ENTRIES = 8192
MAX_STORE_BYTES = 1 << 30
MAX_JSON_BYTES = 1 << 20
DAEMON_KEYS = ('unit', 'main_pid', 'start_tick', 'exe', 'exe_sha256', 'cgroup', 'boot_id')
SOCKET_KEYS = ('path', 'device', 'inode', 'kernel_inode', 'kind', 'nlink', 'uid', 'gid',
               'mode', 'owner_unit', 'owner_pid', 'owner_start_tick', 'parent_path',
               'parent_device', 'parent_inode', 'parent_uid', 'parent_gid', 'parent_mode')
PACKAGE_PINS = MappingProxyType({
    '/usr/bin/containerd': ('containerd.io', '2.4.1-2~ubuntu.24.04~noble', '2.4.1',
        'sha256:c19709945e4a41d27b2d82478d56e77d04ed039894af159c77f7302348cb1935'),
    '/usr/bin/dockerd': ('docker-ce', '5:29.9.0-1~ubuntu.24.04~noble', '29.9.0',
        'sha256:7dcaed8a78cdd64999a2b7bc245bb066f3f6a05aaa7e14e3d164a3504c74b810'),
})
DISABLED_PLUGINS = ('io.containerd.grpc.v1.cri', 'io.containerd.cri.v1.images',
                    'io.containerd.cri.v1.runtime', 'io.containerd.nri.v1.nri',
                    'io.containerd.internal.v1.opt', 'io.containerd.image-verifier.v1.bindir',
                    'io.containerd.monitor.container.v1.restart')


def _require(condition, message):
    if not condition:
        raise ValueError(message)


def _consistent(identities, key, identity, label):
    _require(identities.setdefault(key, identity) == identity, label + ': inconsistent snapshot identity')


def _closed_validation(function):
    @wraps(function)
    def validate(value):
        try:
            return function(value)
        except (TypeError, KeyError, IndexError, AttributeError, RecursionError) as exc:
            raise ValueError('malformed ' + function.__name__ + ' DTO') from exc
    return validate


def _object(value, keys, label):
    _require(type(value) is dict and set(value) == set(keys), label + ': exact properties required')
    return value


def _list(value, maximum, label):
    _require(type(value) is list and len(value) <= maximum, label + ': list bound/type')
    return value


def _integer(value, minimum, maximum, label):
    _require(type(value) is int and minimum <= value <= maximum, label + ': integer bound/type')
    return value


def _string(value, maximum, label, empty=False):
    _require(type(value) is str and (empty or bool(value)) and len(value.encode()) <= maximum
             and '\x00' not in value, label + ': string bound/type')
    return value


def _digest(value):
    _require(type(value) is str and re.fullmatch(r'sha256:[0-9a-f]{64}', value) is not None,
             'invalid SHA256 digest')


def _boot(value):
    _require(type(value) is str and re.fullmatch(
        r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', value) is not None,
        'invalid boot identity')


def _path(value):
    _string(value, 512, 'path')
    _require(value.startswith('/') and str(PurePosixPath(value)) == value
             and '..' not in PurePosixPath(value).parts and '//' not in value,
             'unsafe absolute path')


def _json(raw):
    """Private shared decoder: rejects duplicates, non-finite numbers and huge input."""
    _require(type(raw) is bytes and 0 < len(raw) <= MAX_JSON_BYTES, 'JSON byte bound/type')

    def pairs(items):
        value = {}
        for key, item in items:
            _require(key not in value, 'duplicate JSON key: ' + key)
            value[key] = item
        return value

    def finite_float(number):
        value = float(number)
        _require(math.isfinite(value), 'non-finite JSON number')
        return value

    try:
        value = json.loads(raw.decode('utf-8'), object_pairs_hook=pairs, parse_float=finite_float,
                           parse_constant=lambda _: _require(False, 'non-finite JSON number'))
    except (UnicodeError, json.JSONDecodeError, RecursionError) as exc:
        raise ValueError('invalid JSON') from exc
    _require(type(value) is dict, 'JSON root must be object')
    return value


def _fixed_toml(raw):
    """Parse the deliberately finite TOML subset used here, including strict keys.

    This keeps the offline contract executable with macOS Python 3.9 as well as
    Ubuntu Python 3.12. Strings, booleans, integers and single-line arrays use the
    common JSON/TOML grammar; unsupported TOML extensions are rejected.
    """
    _require(type(raw) is bytes and 0 < len(raw) <= 16384, 'TOML byte bound/type')
    try:
        root, section = {}, None
        for line in raw.decode().splitlines():
            line = line.strip()
            if not line or line.startswith('#'):
                continue
            match = re.fullmatch(r'\[plugins\.("[^"]+")\]', line)
            if match:
                key = json.loads(match.group(1))
                plugins = root.setdefault('plugins', {})
                _require(key not in plugins, 'duplicate TOML section')
                section = plugins[key] = {}
                continue
            match = re.fullmatch(r'([a-z_]+)\s*=\s*(.+)', line)
            _require(match is not None, 'unsupported TOML grammar')
            dest = root if section is None else section
            key, value = match.groups()
            _require(key not in dest, 'duplicate TOML key')
            parsed = json.loads(value)
            _require(type(parsed) in (str, bool, int) or
                (type(parsed) is list and all(type(v) is str for v in parsed)),
                'unsupported TOML value grammar')
            dest[key] = parsed
        return root
    except (UnicodeError, json.JSONDecodeError, RecursionError) as exc:
        raise ValueError('invalid TOML') from exc


def _unit(raw):
    _require(type(raw) is bytes and 0 < len(raw) <= 16384, 'unit byte bound/type')
    parser = configparser.ConfigParser(interpolation=None, strict=True, delimiters=('=',))
    parser.optionxform = str
    try:
        parser.read_string(raw.decode())
    except (UnicodeError, configparser.Error) as exc:
        raise ValueError('invalid unit INI') from exc
    _require(not parser.defaults() and set(parser.sections()) == {'Unit', 'Service'}, 'unit sections')
    return {s: dict(parser[s]) for s in parser.sections()}


@_closed_validation
def validate_configuration(files: dict[str, bytes]) -> None:
    _object(files, FILES, 'configuration leaves')
    daemon = _json(files['daemon.json'])
    _require(daemon == {'data-root': DATA_ROOT, 'storage-driver': 'overlay2',
        'features': {'containerd-snapshotter': False}, 'cgroup-parent': 'switchback-core.slice',
        'exec-opts': ['native.cgroupdriver=systemd'], 'iptables': True,
        'ip6tables': True, 'ip-forward': True, 'ip-masq': True, 'live-restore': False},
        'Docker effective policy differs')
    # Equality alone treats 0/1 as bool in Python; reject non-boolean JSON explicitly.
    for key in ('iptables', 'ip6tables', 'ip-forward', 'ip-masq', 'live-restore'):
        _require(type(daemon[key]) is bool, 'Docker boolean type')
    _require(daemon['features']['containerd-snapshotter'] is False, 'snapshotter boolean type')
    containerd = _fixed_toml(files['containerd.toml'])
    _require(containerd == {'version': 4, 'root': CONTAINERD_ROOT,
        'state': RUNTIME_ROOT + '/containerd', 'temp': RUNTIME_ROOT + '/tmp', 'imports': [],
        'disabled_plugins': list(DISABLED_PLUGINS),
        'required_plugins': ['io.containerd.server.v1.grpc', 'io.containerd.server.v1.ttrpc',
                             'io.containerd.shim.v1.manager'],
        'plugins': {'io.containerd.server.v1.grpc': {'address': CONTAINERD_SOCKET, 'uid': 0, 'gid': 0},
                    'io.containerd.server.v1.ttrpc': {'address': TTRPC_SOCKET, 'uid': 0, 'gid': 0},
                    'io.containerd.server.v1.grpc-tcp': {'address': ''},
                    'io.containerd.server.v1.debug': {'address': '', 'uid': 0, 'gid': 0},
                    'io.containerd.server.v1.metrics': {'address': ''},
                    'io.containerd.shim.v1.manager': {'socket_dir': SHIM_SOCKET_DIR, 'env': []}}},
        'containerd effective policy differs')
    _integer(containerd['version'], 4, 4, 'containerd version')
    for name in ('grpc', 'ttrpc', 'debug'):
        for key in ('uid', 'gid'):
            _integer(containerd['plugins']['io.containerd.server.v1.' + name][key], 0, 0, key)
    # containerd appends '/' + a 64-hex address hash; include the terminating NUL.
    _require(len((SHIM_SOCKET_DIR + '/' + 'f' * 64).encode()) <= 106
        and len((SHIM_SOCKET_DIR + '/' + 'f' * 64).encode()) + 1 <= 108, 'shim AF_UNIX length')
    service = {'Type': 'notify', 'Slice': 'switchback-core.slice', 'Delegate': 'yes',
               'User': 'root', 'Group': 'root', 'UMask': '0077', 'Restart': 'no',
               'TimeoutStartSec': '30', 'TimeoutStopSec': '10', 'KillMode': 'control-group'}
    for index, name in enumerate(UNIT_NAMES):
        unit = _unit(files[name])
        expected_unit = ({'Description': 'Switchback M1 resident containerd', 'After': 'local-fs.target'}
            if index == 0 else {'Description': 'Switchback M1 resident Engine',
                'Requisite': UNIT_NAMES[0], 'After': UNIT_NAMES[0]})
        command = (['/usr/bin/containerd', '--config', '/etc/switchback/m1-engine/containerd.toml']
            if index == 0 else ['/usr/bin/dockerd', '-H', ENGINE_SOCKET, '--group=root',
                '--containerd=' + CONTAINERD_SOCKET, '--pidfile=' + RUNTIME_ROOT + '/docker.pid',
                '--exec-root=' + RUNTIME_ROOT + '/docker',
                '--config-file=/etc/switchback/m1-engine/daemon.json'])
        actual = dict(unit['Service'])
        _require('ExecStart' in actual and shlex.split(actual.pop('ExecStart')) == command,
                 'fixed ExecStart differs')
        _require(actual == service and unit['Unit'] == expected_unit, 'unit effective policy differs')


def _filesystem(value):
    _object(value, ('mountpoint', 'fs_type', 'uuid', 'major', 'minor', 'root_inode', 'quota_enforced'), 'filesystem')
    _require(value['mountpoint'] == '/mnt/lima-sbquota' and value['fs_type'] == 'xfs'
             and value['quota_enforced'] is True, 'filesystem/quota differs')
    _boot(value['uuid'])
    for key in ('major', 'minor'):
        _integer(value[key], 0, (1 << 32) - 1, key)
    _integer(value['root_inode'], 1, (1 << 63) - 1, 'root inode')


def _linux_device_number(major, minor):
    """Linux's full 64-bit dev_t encoding, independent of the validator host."""
    _integer(major, 0, (1 << 32) - 1, 'Linux device major')
    _integer(minor, 0, (1 << 32) - 1, 'Linux device minor')
    return ((major & 0xfff) << 8) | (minor & 0xff) | ((minor & ~0xff) << 12) | ((major & ~0xfff) << 32)


def _runtime_parent(value):
    _object(value, ('path', 'device', 'inode', 'uid', 'gid', 'mode', 'fs_type',
                    'size_bytes', 'inodes'), 'runtime parent')
    _require(value['path'] == RUNTIME_PARENT and value['uid'] == value['gid'] == 0
        and value['mode'] == 0o700 and value['fs_type'] == 'tmpfs'
        and value['size_bytes'] == 134217728 and value['inodes'] == 4096,
        'existing tmpfs identity/policy differs')
    for key in ('device', 'inode'):
        _integer(value[key], 1, (1 << 63) - 1, 'runtime parent ' + key)
    for key in ('uid', 'gid', 'mode', 'size_bytes', 'inodes'):
        _integer(value[key], 0, (1 << 63) - 1, 'runtime parent ' + key)


def _daemons(values, boot, count):
    _list(values, 2, 'daemons')
    _require(len(values) == count, 'daemon count')
    for index, daemon in enumerate(values):
        _object(daemon, DAEMON_KEYS, 'daemon')
        _require(daemon['unit'] == UNIT_NAMES[index] and daemon['boot_id'] == boot,
                 'daemon unit/boot differs')
        _require(daemon['exe'] == ('/usr/bin/containerd', '/usr/bin/dockerd')[index]
                 and daemon['cgroup'] == CORE_CGROUP + '/' + UNIT_NAMES[index], 'daemon exe/cgroup differs')
        _integer(daemon['main_pid'], 1, (1 << 31) - 1, 'daemon PID')
        _integer(daemon['start_tick'], 1, (1 << 63) - 1, 'daemon start tick')
        _digest(daemon['exe_sha256'])
    _require(len({d['main_pid'] for d in values}) == count, 'duplicate daemon PID')


def _sockets(values, daemons, parent, count, version=1):
    expected = (CONTAINERD_SOCKET, TTRPC_SOCKET, ENGINE_SOCKET.removeprefix('unix://'))
    if version == 2:
        _require(count in (0, 2, 6), 'complete v2 socket subset required')
        expected += (CALLBACK_SOCKET, METRICS_SOCKET)
    _list(values, 6 if version == 2 else 3, 'sockets')
    _require(len(values) == count, 'socket count')
    parents = {}
    if parent is not None:
        parents[parent['path']] = tuple(parent[key] for key in ('device', 'inode', 'uid', 'gid', 'mode'))
    for index, sock in enumerate(values):
        _object(sock, SOCKET_KEYS, 'socket')
        daemon = daemons[0 if index < 2 else 1]
        path_ok = (bool(LIBNETWORK_SOCKET_PATTERN.fullmatch(sock['path']))
                   if version == 2 and index == 5 else sock['path'] == expected[index])
        mode = (0o660 if index < 3 else (0o600 if index == 5 else 0o777))
        _require(path_ok and sock['kind'] == 'socket'
            and sock['nlink'] == 1 and sock['uid'] == sock['gid'] == 0 and sock['mode'] == mode,
            'socket path/type/ownership differs')
        _integer(sock['owner_pid'], 1, (1 << 31) - 1, 'socket owner PID')
        _integer(sock['owner_start_tick'], 1, (1 << 63) - 1, 'socket owner start tick')
        _require(sock['owner_unit'] == daemon['unit'] and sock['owner_pid'] == daemon['main_pid']
            and sock['owner_start_tick'] == daemon['start_tick'], 'socket daemon binding differs')
        parent_path = (RUNTIME_ROOT if index < 2 else RUNTIME_PARENT)
        if version == 2 and index >= 3:
            parent_path = (DOCKER_RUNTIME_ROOT + '/extensions' if index == 3 else
                           DOCKER_RUNTIME_ROOT if index == 4 else DOCKER_RUNTIME_ROOT + '/libnetwork')
        _require(sock['parent_path'] == parent_path
            and sock['parent_uid'] == sock['parent_gid'] == 0 and sock['parent_mode'] == 0o700,
            'socket parent differs')
        for key in ('device', 'inode', 'kernel_inode', 'parent_device', 'parent_inode'):
            _integer(sock[key], 1, (1 << 63) - 1, 'socket ' + key)
        for key in ('uid', 'gid', 'mode', 'nlink', 'parent_uid', 'parent_gid', 'parent_mode'):
            _integer(sock[key], 0, (1 << 32) - 1, 'socket ' + key)
        _consistent(parents, sock['parent_path'], tuple(sock['parent_' + key]
            for key in ('device', 'inode', 'uid', 'gid', 'mode')), 'socket parent path')
        if index == 2 and parent is not None:
            _require(sock['parent_device'] == parent['device'] and sock['parent_inode'] == parent['inode'],
                     'Engine socket tmpfs binding differs')
    _require(len({s['kernel_inode'] for s in values}) == count, 'duplicate socket kernel inode')
    _require(len({(s['device'], s['inode']) for s in values}) == count, 'duplicate socket filesystem inode')


def _socket_parents(values, sockets, parent=None):
    """Complete ordered private directory chain, bound to the socket stat facts."""
    _list(values, 5, 'socket parents')
    _require(len(sockets) in (0, 2, 6), 'complete v2 socket subset required')
    count = {0: 0, 2: 2, 6: 5}[len(sockets)]
    _require(len(values) == count, 'socket parent count')
    for index, directory in enumerate(values):
        _object(directory, SOCKET_PARENT_KEYS, 'socket parent')
        _require(directory['path'] == SOCKET_PARENT_PATHS[index] and directory['kind'] == 'directory'
                 and directory['uid'] == directory['gid'] == 0 and directory['mode'] == 0o700,
                 'socket parent path/type/ownership differs')
        for key in ('device', 'inode'):
            _integer(directory[key], 1, (1 << 63) - 1, 'socket parent ' + key)
        for key in ('uid', 'gid', 'mode'):
            _integer(directory[key], 0, (1 << 32) - 1, 'socket parent ' + key)
    identities = [(item['device'], item['inode']) for item in values + sockets]
    _require(len(set(identities)) == len(identities), 'colliding filesystem identity')
    _require(len({item['device'] for item in values + sockets}) <= 1, 'socket/parent tmpfs device differs')
    by_path = {item['path']: item for item in values}
    if values and parent is not None:
        _require(all(values[0][key] == parent[key] for key in ('path', 'device', 'inode', 'uid', 'gid', 'mode')),
                 'socket parent runtime tmpfs binding differs')
    for sock in sockets:
        directory = by_path.get(sock['parent_path'])
        _require(directory is not None and all(sock['parent_' + key] == directory[key]
                 for key in ('device', 'inode', 'uid', 'gid', 'mode')), 'inconsistent socket parent chain')


def _network(value, index):
    _object(value, ('name', 'id', 'driver', 'scope', 'internal', 'labels', 'enable_ipv6',
                    'attachable', 'ingress', 'options', 'ipam'), 'network')
    _object(value['labels'], NETWORK_LABELS, 'network labels')
    _require(type(value['options']) is dict and value['name'] == NETWORKS[index][0]
        and value['internal'] is NETWORKS[index][1]
        and value['driver'] == 'bridge' and value['scope'] == 'local'
        and value['labels'] == dict(NETWORK_LABELS) and value['enable_ipv6'] is False
        and value['attachable'] is False and value['ingress'] is False and value['options'] == {},
        'network ownership/config differs')
    _require(type(value['id']) is str and re.fullmatch('[0-9a-f]{64}', value['id']) is not None, 'network ID')
    ipam = _object(value['ipam'], ('driver', 'options', 'config'), 'IPAM')
    _require(type(ipam['options']) is dict and ipam['driver'] == 'default' and ipam['options'] == {}, 'IPAM driver/options')
    _list(ipam['config'], 1, 'IPAM config')
    _require(len(ipam['config']) == 1, 'IPAM IPv4 allocation required')
    allocation = _object(ipam['config'][0], ('subnet', 'gateway'), 'IPAM allocation')
    _string(allocation['subnet'], 64, 'IPAM subnet')
    _string(allocation['gateway'], 64, 'IPAM gateway')
    try:
        subnet = ipaddress.IPv4Network(allocation['subnet'], strict=True)
        gateway = ipaddress.IPv4Address(allocation['gateway'])
        _require(allocation['subnet'] == str(subnet) and allocation['gateway'] == str(gateway),
                 'IPAM canonical IPv4 text required')
        _require(any(subnet.subnet_of(private) for private in RFC1918_SUBNETS)
                 and gateway in subnet and gateway not in (subnet.network_address,
                 subnet.broadcast_address), 'IPAM allocation differs')
    except (TypeError, ipaddress.AddressValueError, ipaddress.NetmaskValueError) as exc:
        raise ValueError('invalid IPAM allocation') from exc


@_closed_validation
def validate_receipt(value: dict) -> None:
    return _validate_deployment_receipt(value, version=1)


@_closed_validation
def validate_receipt_v2(value: dict) -> None:
    return _validate_deployment_receipt(value, version=2)


def _validate_deployment_receipt(value, version):
    _object(value, ('schema', 'status', 'bundle_manifest_sha256', 'kit_manifest_sha256',
        'source_root', 'templates_root', 'boot_id', 'filesystem', 'runtime_parent', 'daemons',
        'installed_files', 'sockets', 'engine', 'networks', 'steps', 'elapsed_ms',
        'known_effects', 'unknown_effects', 'runtime_qualification', 'capacity_ready',
        'execution_enabled', 'paid_consumed') + (('socket_parents',) if version == 2 else ()), 'deployment receipt')
    _require(value['schema'] == 'switchback.engine.deployment_receipt.v%d' % version
             and value['status'] in ('engine_ready', 'failed', 'unknown'), 'receipt schema/status')
    for key in ('runtime_qualification', 'capacity_ready', 'execution_enabled', 'paid_consumed'):
        _require(value[key] is False, 'operational receipt cannot grant authority: ' + key)
    for key in ('bundle_manifest_sha256', 'kit_manifest_sha256', 'source_root', 'templates_root'):
        _digest(value[key])
    ready = value['status'] == 'engine_ready'
    for key, validator in (('boot_id', _boot), ('filesystem', _filesystem), ('runtime_parent', _runtime_parent)):
        if value[key] is not None:
            validator(value[key])
        else:
            _require(not ready, 'ready receipt missing ' + key)
    _daemons(value['daemons'], value['boot_id'], 2 if ready else len(value['daemons']))
    _list(value['installed_files'], 4, 'installed files')
    _require(not ready or len(value['installed_files']) == 4, 'installed file count')
    seen = set()
    for item in value['installed_files']:
        _object(item, ('source_path', 'target_path', 'sha256', 'mode', 'uid', 'gid', 'bytes'), 'installed file')
        source = item['source_path']
        _require(type(source) is str and source.startswith(SOURCE_PREFIX), 'installed source prefix')
        leaf = source[len(SOURCE_PREFIX):]
        _require(leaf in FILES and leaf not in seen, 'installed source duplicate/unknown')
        seen.add(leaf)
        _require((item['target_path'], item['mode']) == FILES[leaf] and item['uid'] == item['gid'] == 0,
                 'installed target policy differs')
        _digest(item['sha256']); _integer(item['bytes'], 1, 16384, 'installed bytes')
        for key in ('mode', 'uid', 'gid'):
            _integer(item[key], 0, (1 << 32) - 1, 'installed ' + key)
    _sockets(value['sockets'], value['daemons'], value['runtime_parent'],
             (6 if version == 2 else 3) if ready else len(value['sockets']), version)
    if version == 2:
        _socket_parents(value['socket_parents'], value['sockets'], value['runtime_parent'])
    engine = value['engine']
    if engine is not None:
        _object(engine, ('id', 'version', 'data_root', 'storage_driver', 'containerd_snapshotter',
                         'cgroup_parent', 'containers', 'volumes', 'plugins'), 'Engine')
        _string(engine['id'], 128, 'Engine ID')
        _require(engine['version'] == '29.9.0' and engine['data_root'] == DATA_ROOT
            and engine['storage_driver'] == 'overlay2' and engine['containerd_snapshotter'] is False
            and engine['cgroup_parent'] == 'switchback-core.slice', 'Engine effective identity differs')
        for key in ('containers', 'volumes', 'plugins'):
            _integer(engine[key], 0, 0, 'restored Engine ' + key)
    _require(not ready or engine is not None, 'ready Engine identity missing')
    _list(value['networks'], 2, 'networks')
    _require(not ready or len(value['networks']) == 2, 'network count')
    for index, network in enumerate(value['networks']):
        _network(network, index)
    if len(value['networks']) == 2:
        _require(value['networks'][0]['id'] != value['networks'][1]['id'], 'duplicate network ID')
        subnets = [ipaddress.IPv4Network(n['ipam']['config'][0]['subnet']) for n in value['networks']]
        _require(not subnets[0].overlaps(subnets[1]), 'overlapping network allocations')
    # Failure observation/receipt persistence can finish after the action deadline.
    # This records actual times; the executor still owns the unchanged120s gate.
    observation_bound = BUDGET_SECONDS * 1000 if ready else (1 << 63) - 1
    elapsed = _integer(value['elapsed_ms'], 0, observation_bound, 'elapsed_ms')
    _list(value['steps'], len(STEP_NAMES), 'steps')
    _require(bool(value['steps']) and (not ready or len(value['steps']) == len(STEP_NAMES)), 'step count')
    previous = 0
    for index, step in enumerate(value['steps']):
        _object(step, ('name', 'status', 'started_ms', 'finished_ms', 'exit_code', 'stdout',
                       'stderr', 'known_effects', 'unknown_effects'), 'step')
        _require(step['name'] == STEP_NAMES[index] and step['status'] in ('succeeded', 'failed', 'unknown'),
                 'step order/status')
        start = _integer(step['started_ms'], previous, elapsed, 'step start')
        previous = _integer(step['finished_ms'], start, elapsed, 'step finish')
        if step['exit_code'] is not None:
            _integer(step['exit_code'], -255, 255, 'step exit code')
        _string(step['stdout'], 16384, 'step stdout', empty=True)
        _string(step['stderr'], 16384, 'step stderr', empty=True)
        _effects(step['known_effects']); _effects(step['unknown_effects'])
        if ready or index < len(value['steps']) - 1:
            _require(step['status'] == 'succeeded' and step['exit_code'] == 0
                     and not step['unknown_effects'], 'unsuccessful/unknown completed step')
        else:
            _require(step['status'] == value['status'], 'failure outcome differs from final step')
            _require(step['status'] != 'failed' or step['exit_code'] not in (None, 0), 'failed step exit')
    _effects(value['known_effects']); _effects(value['unknown_effects'])
    _require(not ready or not value['unknown_effects'], 'ready receipt has unknown effects')
    _require(set(value['known_effects']) == {e for s in value['steps'] for e in s['known_effects']}
             and set(value['unknown_effects']) == {e for s in value['steps'] for e in s['unknown_effects']},
             'aggregate effects differ from steps')


def _effects(value):
    _list(value, 64, 'effects')
    for effect in value:
        _string(effect, 512, 'effect')
    _require(len(set(value)) == len(value), 'duplicate effect')


@_closed_validation
def validate_store_inventory(facts: dict) -> None:
    schema_rules = {
        'switchback.engine.store_inventory.v1': STORE_RULE_REVISION,
        'switchback.engine.store_inventory.v2': STORE_RULE_REVISION_V2,
        'switchback.engine.store_inventory.v3': STORE_RULE_REVISION_V3,
        'switchback.engine.store_inventory.v4': STORE_RULE_REVISION_V4,
    }
    schema = facts['schema']
    _require(schema in schema_rules, 'unknown store inventory schema')
    bound = schema != 'switchback.engine.store_inventory.v1'
    _object(facts, ('schema', 'root', 'containerd_root', 'docker_version', 'containerd_version',
                    'scan_complete', 'entries', 'restoration') + (('backing_filesystem',) if bound else ()),
            'store inventory')
    _require(facts['root'] == DATA_ROOT
        and facts['containerd_root'] == CONTAINERD_ROOT and facts['docker_version'] == '29.9.0'
        and facts['containerd_version'] == '2.4.1' and facts['scan_complete'] is True, 'incomplete/wrong store')
    if bound:
        _filesystem(facts['backing_filesystem'])
        filesystem = facts['backing_filesystem']
        expected_device = _linux_device_number(filesystem['major'], filesystem['minor'])
    _zero_refs(facts['restoration'])
    entries = _list(facts['entries'], MAX_STORE_ENTRIES, 'store entries')
    seen, directories, total = set(), {}, 0
    exact = {
        'engine_identity': (DATA_ROOT + '/engine-id', 'docker-29.9.0-engine-id-v1'),
        'docker_empty_index': (DATA_ROOT + '/image/overlay2/repositories.json', 'docker-29.9.0-repositories-v1'),
        'docker_empty_volume_index': (DATA_ROOT + '/volumes/metadata.db', 'docker-29.9.0-volume-bbolt-v1'),
        'containerd_empty_metadata': (CONTAINERD_ROOT + '/io.containerd.metadata.v1.bolt/meta.db', 'containerd-2.4.1-bbolt-v4'),
        'docker_network_metadata': (DATA_ROOT + '/network/files/local-kv.db', 'docker-29.9.0-network-bbolt-v1'),
    }
    if schema == 'switchback.engine.store_inventory.v4':
        exact['docker_default_bridge_metadata'] = (DATA_ROOT + '/network/files/local-kv.db',
                                                    'docker-29.9.0-network-bbolt-v2')
    for item in entries:
        _object(item, ('path', 'kind', 'uid', 'gid', 'mode', 'nlink', 'bytes', 'sha256',
            'classification', 'format', 'rule_revision', 'scan_complete', 'restore_refs')
            + (('device_identity',) if bound and item.get('kind') == 'block_device' else ()), 'store entry')
        path, classification = item['path'], item['classification']
        _path(path)
        _require(path == DATA_ROOT or path.startswith(DATA_ROOT + '/'), 'store path escape')
        _require(path not in seen, 'duplicate store path: ' + path); seen.add(path)
        if schema in ('switchback.engine.store_inventory.v3', 'switchback.engine.store_inventory.v4'):
            for allowed in EMPTY_PLUGIN_DIRECTORY_MODES:
                _require(not path.startswith(allowed + '/'), 'unsupported plugin descendant: ' + path)
        empty_plugin_mode = (EMPTY_PLUGIN_DIRECTORY_MODES.get(path)
                             if schema in ('switchback.engine.store_inventory.v3', 'switchback.engine.store_inventory.v4') else None)
        if empty_plugin_mode is not None:
            _require(item['kind'] == 'directory'
                     and all(type(item[key]) is int for key in ('uid', 'gid', 'mode', 'nlink', 'bytes'))
                     and item['uid'] == item['gid'] == 0
                     and item['mode'] == empty_plugin_mode and item['nlink'] == 2
                     and item['bytes'] == 0 and item['sha256'] is None
                     and classification == 'empty_directory' and item['format'] == 'directory-v1'
                     and item['scan_complete'] is True
                     and item['rule_revision'] == schema_rules[schema],
                     'unsafe empty plugin directory: ' + path)
        _require(item['uid'] == item['gid'] == 0 and item['scan_complete'] is True
                 and item['rule_revision'] == schema_rules[schema],
                 'unsafe/incomplete store entry')
        for key in ('uid', 'gid'):
            _integer(item[key], 0, 0, 'store ' + key)
        mode = _integer(item['mode'], 0, 0o777, 'store mode')
        _require(mode & 0o022 == 0, 'writable store entry')
        size = _integer(item['bytes'], 0, MAX_STORE_BYTES, 'store bytes'); total += size
        _require(total <= MAX_STORE_BYTES, 'store scan byte bound')
        try:
            _zero_refs(item['restore_refs'])
        except ValueError as error:
            raise ValueError(str(error) + ': ' + path) from error
        if bound and item['kind'] == 'block_device':
            allowed_paths = (QUOTA_BACKING_DEVICE_PATHS if schema in ('switchback.engine.store_inventory.v3', 'switchback.engine.store_inventory.v4')
                             else {DATA_ROOT + '/overlay2/backingFsBlockDev'})
            _require(path in allowed_paths
                and classification == 'quota_backing_device'
                and item['format'] == 'docker-xfs-quota-backing-device-v1'
                and mode == 0o600 and type(item['nlink']) is int and item['nlink'] == 1
                and size == 0 and item['sha256'] is None, 'unsafe quota backing device')
            identity = _object(item['device_identity'], ('device', 'inode', 'rdev', 'major', 'minor'),
                               'backing device identity')
            for key in ('device', 'rdev'):
                _require(_integer(identity[key], 0, (1 << 64) - 1, 'backing ' + key) == expected_device,
                         'backing device/filesystem differs')
            _integer(identity['inode'], 1, (1 << 64) - 1, 'backing inode')
            _require((_integer(identity['major'], 0, (1 << 32) - 1, 'backing major'),
                      _integer(identity['minor'], 0, (1 << 32) - 1, 'backing minor'))
                     == (filesystem['major'], filesystem['minor']), 'backing major/minor differs')
            continue
        if item['kind'] == 'directory':
            _integer(item['nlink'], 2, MAX_STORE_ENTRIES + 2, 'directory links')
            _require(classification in ('empty_directory', 'engine_directory') and item['format'] == 'directory-v1'
                and size == 0 and item['sha256'] is None, 'directory classification differs')
            _require(empty_plugin_mode is not None or not any(path.startswith(DATA_ROOT + '/' + name + '/')
                for name in ('containers', 'volumes', 'plugins')),
                'unexplained business/volume/plugin directory: ' + path)
            directories[path] = classification
            continue
        _require(item['kind'] == 'regular' and item['nlink'] == 1 and type(item['nlink']) is int
                 and size > 0, 'unsafe store leaf')
        _digest(item['sha256'])
        if classification in exact:
            _require((path, item['format']) == exact[classification], 'store classifier/path differs')
        elif classification == 'buildkit_cache':
            _require(path in tuple(DATA_ROOT + '/buildkit/' + leaf for leaf in
                ('cache.db', 'metadata_v2.db', 'history.db')) and item['format'] == 'docker-29.9.0-buildkit-bbolt-v1',
                'BuildKit classifier/path differs')
        elif classification == 'buildkit_empty_index':
            _require(path in tuple(DATA_ROOT + '/buildkit/' + leaf for leaf in
                ('containerdmeta.db', 'snapshots.db'))
                and item['format'] == 'docker-29.9.0-buildkit-empty-bbolt-v1',
                'BuildKit empty index classifier/path differs')
        elif classification == 'retained_bootstrap_input':
            _require(item['format'] == 'switchback-retained-bootstrap-sha256-v1'
                and RETAINED_INPUTS.get(path.removeprefix(DATA_ROOT + '/')) == item['sha256'],
                'retained bootstrap origin/path/hash differs')
        elif classification == 'content_cache':
            _require(item['format'] == 'containerd-2.4.1-content-sha256-v1'
                and re.fullmatch(re.escape(CONTAINERD_ROOT) + r'/io\.containerd\.content\.v1\.content/blobs/sha256/[0-9a-f]{64}', path)
                and item['sha256'] == 'sha256:' + PurePosixPath(path).name, 'content cache path/digest differs')
        else:
            raise ValueError('unclassified or executable store input')
    for path in seen:
        if path != DATA_ROOT:
            _require(str(PurePosixPath(path).parent) in directories, 'missing store parent coverage: ' + path)
    if bound:
        _require(DATA_ROOT in directories, 'missing bound store root coverage')
    for path, classification in directories.items():
        children = any(str(PurePosixPath(p).parent) == path for p in seen)
        _require(classification == ('engine_directory' if children else 'empty_directory'),
                 'directory children/class mismatch: ' + path)


def _zero_refs(value):
    _object(value, RESTORE_CLASSES, 'restoration references')
    for key, count in value.items():
        _integer(count, 0, 0, 'restoration ' + key)


@_closed_validation
def validate_listeners(facts: dict) -> None:
    return _validate_listener_inventory(facts, version=1)


@_closed_validation
def validate_listeners_v2(facts: dict) -> None:
    return _validate_listener_inventory(facts, version=2)


def _validate_listener_inventory(facts, version):
    _object(facts, ('schema', 'phase', 'boot_id', 'current_boot_id', 'complete', 'daemons',
        'identity_before', 'identity_after', 'sockets', 'unix', 'tcp', 'tcp6')
        + (('socket_parents',) if version == 2 else ()), 'listener inventory')
    _require(facts['schema'] == 'switchback.engine.listeners.v%d' % version
        and facts['phase'] in ('preflight', 'containerd_ready', 'engine_ready'), 'listener schema/phase')
    _boot(facts['boot_id'])
    _require(facts['boot_id'] == facts['current_boot_id'], 'stale listener boot')
    _object(facts['complete'], ('unix', 'tcp', 'tcp6', 'fd_owners', 'processes'), 'complete tables')
    _require(all(v is True for v in facts['complete'].values()), 'incomplete process/listener ownership scan')
    count = {'preflight': 0, 'containerd_ready': 1, 'engine_ready': 2}[facts['phase']]
    _daemons(facts['daemons'], facts['boot_id'], count)
    if version == 2:
        _daemons(facts['identity_before'], facts['boot_id'], count)
        _daemons(facts['identity_after'], facts['boot_id'], count)
    _require(facts['identity_before'] == facts['daemons'] == facts['identity_after'], 'daemon identity drift')
    _sockets(facts['sockets'], facts['daemons'], None, ((0, 2, 6) if version == 2 else (0, 2, 3))[count], version)
    if version == 2:
        _socket_parents(facts['socket_parents'], facts['sockets'])
    protected_paths = {s['path']: s['kernel_inode'] for s in facts['sockets']}
    by_inode = {s['kernel_inode']: s for s in facts['sockets']}
    pids = {d['main_pid']: d for d in facts['daemons']}
    found, all_inodes, owner_fds = set(), set(), {}
    for family in ('unix', 'tcp', 'tcp6'):
        for listener in _list(facts[family], 8192, family + ' listeners'):
            inode = listener['inode']
            _require(inode not in all_inodes, 'duplicate kernel socket inode')
            all_inodes.add(inode)
            if _listener_negative(family, listener, protected_paths, by_inode, pids, owner_fds, version):
                found.add(inode)
    _require(found == set(by_inode), 'required Unix listener/owner missing')


def _listener_negative(family, listener, protected_paths, by_inode, pids, owner_fds, version, partial=False):
    """Reject violations already proved by a row and its observed owners.

    A partial scan can prove negatives but cannot prove complete coverage.
    """
    _object(listener, (('path', 'inode', 'listener', 'owners') if family == 'unix'
        else ('address', 'port', 'inode', 'owners')), family + ' listener')
    inode = _integer(listener['inode'], 1, (1 << 63) - 1, 'listener inode')
    owners = _list(listener['owners'], 64, 'listener FD owners')
    if not partial:
        _require(bool(owners), 'unowned/unreadable listener')
    related, seen_fds = False, set()
    for owner in owners:
        _object(owner, ('main_pid', 'start_tick', 'exe', 'cgroup', 'fd'), 'FD owner')
        _integer(owner['main_pid'], 1, (1 << 31) - 1, 'owner PID')
        _integer(owner['start_tick'], 0 if owner['main_pid'] == 1 else 1,
                 (1 << 63) - 1, 'owner start')
        _integer(owner['fd'], 0, (1 << 20) - 1, 'owner FD')
        _path(owner['exe']); _path(owner['cgroup'])
        _consistent(owner_fds, (owner['main_pid'], owner['start_tick'], owner['fd']),
                    inode, 'process FD kernel inode')
        fd = (owner['main_pid'], owner['fd'])
        _require(fd not in seen_fds, 'duplicate owner FD'); seen_fds.add(fd)
        daemon = pids.get(owner['main_pid'])
        daemonish = re.fullmatch(r'(dockerd|containerd|containerd-shim.*)', PurePosixPath(owner['exe']).name)
        if daemon is not None or daemonish:
            _require(daemon is not None and all(owner[k] == daemon[k]
                for k in ('main_pid', 'start_tick', 'exe', 'cgroup')), 'foreign/drifted daemon listener owner')
            related = True
    if family != 'unix':
        _string(listener['address'], 128, 'TCP address')
        _integer(listener['port'], 1, 65535, 'TCP port')
        _require(not related, 'daemon has TCP/TCP6 listener')
        return False
    _string(listener['path'], 512, 'Unix path', empty=True)
    _require(type(listener['listener']) is bool, 'Unix listener type')
    if version == 2 and listener['listener'] and listener['path'] in protected_paths:
        _require(inode == protected_paths[listener['path']], 'protected socket pathname has different kernel inode')
    sensitive = any(x in listener['path'] for x in ('docker', 'containerd', 'switchback-engine'))
    if partial and not owners:
        if sensitive and listener['listener']:
            _require(inode in by_inode, 'hidden/foreign Unix listener')
        return False
    if related and listener['listener'] is False:
        allowed_paths = (('',) + tuple(protected_paths) if version == 2 else
            ('', CONTAINERD_SOCKET, TTRPC_SOCKET, ENGINE_SOCKET.removeprefix('unix://')))
        _require(listener['path'] in allowed_paths
            and all(owner['main_pid'] in pids and all(owner[key] == pids[owner['main_pid']][key]
                for key in ('main_pid', 'start_tick', 'exe', 'cgroup')) for owner in owners),
            'foreign/unapproved daemon nonlistener socket owner/path')
        return False
    if related or sensitive or inode in by_inode:
        _require(listener['listener'] is True and inode in by_inode, 'hidden/foreign Unix listener')
        sock = by_inode[inode]
        _require(listener['path'] == sock['path'] and all(o['main_pid'] == sock['owner_pid']
            and o['start_tick'] == sock['owner_start_tick'] for o in owners), 'Unix ownership/path differs')
        return True
    return False


@_closed_validation
def validate_preflight(facts: dict) -> None:
    return _validate_preflight(facts, version=1)


@_closed_validation
def validate_preflight_v2(facts: dict) -> None:
    return _validate_preflight(facts, version=2)


def _validate_preflight(facts, version):
    _object(facts, ('schema', 'boot_id', 'current_boot_id', 'vm', 'filesystem', 'core', 'masks',
        'unit_catalog_complete', 'other_units', 'process_scan_complete', 'writers', 'targets',
        'runtime_parent', 'binaries', 'store', 'listeners'), 'preflight')
    _require(facts['schema'] == 'switchback.engine.preflight.v1', 'preflight schema')
    _boot(facts['boot_id']); _require(facts['boot_id'] == facts['current_boot_id'], 'stale preflight boot')
    _require(facts['vm'] == {'name': 'switchback-core', 'vcpus': 4, 'memory_bytes': 10737418240}, 'VM geometry')
    _require(facts['core'] == {'cgroup': CORE_CGROUP, 'cpu_max': '120000 100000',
        'memory_max': 3221225472, 'memory_swap_max': 0, 'pids_max': 1024, 'accounted_once': True}, 'core limits/accounting')
    _require(facts['core']['accounted_once'] is True, 'core accounted-once type')
    for obj, keys in ((facts['vm'], ('vcpus', 'memory_bytes')),
                      (facts['core'], ('memory_max', 'memory_swap_max', 'pids_max'))):
        for key in keys: _integer(obj[key], 0, 1 << 40, key)
    _filesystem(facts['filesystem']); _runtime_parent(facts['runtime_parent'])
    _object(facts['masks'], ('docker.service', 'docker.socket', 'containerd.service'), 'default masks')
    for state in facts['masks'].values():
        _require(state == {'unit_file_state': 'masked', 'active_state': 'inactive', 'main_pid': 0}
                 and type(state['main_pid']) is int, 'default mask/active state differs')
    _require(facts['unit_catalog_complete'] is True and facts['process_scan_complete'] is True,
             'incomplete unit/process inventory')
    for unit in _list(facts['other_units'], 256, 'other daemon units'):
        _object(unit, ('unit', 'active_state', 'main_pid'), 'other unit')
        _string(unit['unit'], 128, 'other unit')
        _require(unit['active_state'] in ('inactive', 'failed') and unit['main_pid'] == 0
                 and type(unit['main_pid']) is int, 'active/unknown daemon unit')
    _require(facts['writers'] == [] and type(facts['writers']) is list, 'active/foreign writers')
    targets = ('/etc/switchback/m1-engine', '/var/lib/switchback/m1-engine', RUNTIME_ROOT,
               FILES[UNIT_NAMES[0]][0], FILES[UNIT_NAMES[1]][0])
    _list(facts['targets'], len(targets), 'exclusive targets')
    _require(len(facts['targets']) == len(targets), 'target coverage')
    for item, path in zip(facts['targets'], targets):
        _object(item, ('path', 'exists', 'parents_safe'), 'target')
        _require(item['path'] == path and item['exists'] is False and item['parents_safe'] is True,
                 'occupied/unsafe exclusive target')
    _list(facts['binaries'], 2, 'binaries'); _require(len(facts['binaries']) == 2, 'binary coverage')
    for item, (path, pin) in zip(facts['binaries'], PACKAGE_PINS.items()):
        _object(item, ('path', 'package', 'package_version', 'version', 'architecture', 'uid', 'gid',
                       'mode', 'nlink', 'sha256', 'package_sha256', 'pin_verified'), 'binary')
        _require(item['path'] == path and (item['package'], item['package_version'], item['version'],
            item['package_sha256']) == pin and item['architecture'] == 'arm64'
            and item['pin_verified'] is True, 'installed package/pin identity differs')
        _require(item['uid'] == item['gid'] == 0 and item['nlink'] == 1
            and item['mode'] == 0o755, 'unsafe binary ownership/type')
        for key in ('uid', 'gid', 'nlink', 'mode'): _integer(item[key], 0, 65535, 'binary ' + key)
        _digest(item['sha256'])
    validate_store_inventory(facts['store']); _validate_listener_inventory(facts['listeners'], version)
    if facts['store']['schema'] in ('switchback.engine.store_inventory.v2',
                                   'switchback.engine.store_inventory.v3',
                                   'switchback.engine.store_inventory.v4'):
        _require(facts['store']['backing_filesystem'] == facts['filesystem'],
                 'store backing filesystem differs from preflight')
    _require(facts['listeners']['phase'] == 'preflight' and facts['listeners']['boot_id'] == facts['boot_id'],
             'preflight listener binding differs')
