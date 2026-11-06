"""Read-only supported store subset, not a storage-capacity or recovery promise.

bbolt format2 layout follows etcd-io/bbolt v1.5.0 internal/common/{meta,page,
bucket}.go. Semantic rules follow containerd v2.4.1 core/metadata/{buckets,db}.go
and Docker's pinned BuildKit v0.34.0 cache/metadata and bboltcachestorage.
Unknown current records reject. Historical bytes on free/unreachable pages are
never used as active records. This module never opens a database for writing.
"""
import hashlib
import ipaddress
import base64
import datetime
import os
from pathlib import Path, PurePosixPath
import re
import stat
import struct

from contract import (CONTAINERD_ROOT, DATA_ROOT, MAX_STORE_BYTES, MAX_STORE_ENTRIES,
                      EMPTY_PLUGIN_DIRECTORY_MODES, RESTORE_CLASSES, RETAINED_INPUTS,
                      STORE_RULE_REVISION, STORE_RULE_REVISION_V4,
                      QUOTA_BACKING_DEVICE_PATHS,
                      _filesystem, _linux_device_number, _json, _require,
                      validate_store_inventory)

_DB_BYTES = 64 << 20
_RECORDS = 65536
_DEPTH = 32
_NO_FREELIST = (1 << 64) - 1


def _sha(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


def _fnv(raw):
    value = 14695981039346656037
    for byte in raw:
        value = ((value ^ byte) * 1099511628211) & _NO_FREELIST
    return value


class _Bolt:
    def __init__(self, raw, check):
        _require(type(raw) is bytes and 8192 <= len(raw) <= _DB_BYTES, 'bbolt file bound')
        self.raw, self.check, self.used, self.records = raw, check, {0, 1}, 0
        # Both committed meta pages must be intact for this conservative subset.
        # Never fall back to an older convenient empty root after corruption.
        size = struct.unpack_from('<I', raw, 24)[0]
        _require(size in (4096, 8192, 16384, 32768, 65536)
                 and len(raw) % size == 0, 'bbolt page size/file alignment')
        metas = [self.meta(index, size) for index in (0, 1)]
        _require(metas[0]['txid'] != metas[1]['txid'], 'duplicate bbolt meta transaction')
        self.meta_value = max(metas, key=lambda value: value['txid'])
        self.size, self.high = size, self.meta_value['high']
        _require(all(meta['high'] <= len(raw) // size for meta in metas), 'bbolt high-water file bounds')
        self.free = set()
        if self.meta_value['freelist'] != _NO_FREELIST:
            self.freelist(self.meta_value['freelist'])

    def meta(self, index, size):
        self.check()
        offset = index * size
        _require(offset + 80 <= len(self.raw), 'bbolt meta truncated')
        page, flags, count, overflow = struct.unpack_from('<QHHI', self.raw, offset)
        values = struct.unpack_from('<IIIIQQQQQQ', self.raw, offset + 16)
        magic, version, page_size, meta_flags, root, sequence, free, high, txid, checksum = values
        _require((page, flags, count, overflow) == (index, 4, 0, 0)
            and magic == 0xED0CDAED and version == 2 and page_size == size and meta_flags == 0
            and checksum == _fnv(self.raw[offset + 16:offset + 72])
            and txid % 2 == index and 2 <= root < high
            and (2 <= free < high or free == _NO_FREELIST), 'bbolt metadata/checksum')
        return {'root': root, 'freelist': free, 'high': high, 'txid': txid, 'sequence': sequence}

    def page(self, pgid, expected=None):
        self.check()
        _require(type(pgid) is int and 2 <= pgid < self.high, 'bbolt page reference bound')
        offset = pgid * self.size
        page, flags, count, overflow = struct.unpack_from('<QHHI', self.raw, offset)
        _require(page == pgid and (expected is None or flags == expected)
            and pgid + overflow < self.high, 'bbolt page identity/overflow')
        extent = set(range(pgid, pgid + overflow + 1))
        _require(not extent & self.used and not extent & self.free, 'bbolt page cycle/overlap/free reference')
        self.used.update(extent)
        return self.raw[offset:(pgid + overflow + 1) * self.size], flags, count

    def freelist(self, pgid):
        raw, _, count = self.page(pgid, 16)
        offset = 16
        if count == 0xffff:
            _require(len(raw) >= 24, 'bbolt long freelist bound')
            count = struct.unpack_from('<Q', raw, 16)[0]
            offset += 8
        _require(count <= self.high and offset + count * 8 <= len(raw), 'bbolt freelist count bound')
        previous = 1
        for index in range(count):
            self.check()
            number = struct.unpack_from('<Q', raw, offset + index * 8)[0]
            _require(previous < number < self.high and number not in self.used,
                     'bbolt freelist order/active overlap')
            self.free.add(number)
            previous = number

    def tree(self, pgid, depth=0, inline=None):
        self.check()
        _require(depth <= _DEPTH, 'bbolt tree depth bound')
        if inline is None:
            raw, flags, count = self.page(pgid)
        else:
            raw = inline
            _require(len(raw) >= 16, 'bbolt inline page bound')
            ident, flags, count, overflow = struct.unpack_from('<QHHI', raw)
            _require((ident, flags, overflow) == (0, 2, 0), 'bbolt inline page identity')
        _require(flags in (1, 2) and 16 + 16 * count <= len(raw), 'bbolt page type/element bound')
        _require(flags != 1 or count > 0, 'empty bbolt branch')
        result, previous, spans = {}, None, []
        for index in range(count):
            self.check()
            self.records += 1
            _require(self.records <= _RECORDS, 'bbolt total enumeration bound')
            offset = 16 + index * 16
            if flags == 2:
                leaf_flags, position, key_size, value_size = struct.unpack_from('<IIII', raw, offset)
                _require(leaf_flags in (0, 1), 'unknown bbolt leaf flags')
            else:
                position, key_size, child = struct.unpack_from('<IIQ', raw, offset)
                value_size = 0
            start, end = offset + position, offset + position + key_size + value_size
            _require(0 < key_size <= 32768 and 16 + count * 16 <= start < end <= len(raw),
                     'bbolt key/value bounds')
            _require(all(end <= lo or start >= hi for lo, hi in spans), 'overlapping bbolt element payload')
            spans.append((start, end))
            key = raw[start:start + key_size]
            _require(previous is None or previous < key, 'bbolt duplicate/unordered key')
            previous = key
            if flags == 1:
                subtree = self.tree(child, depth + 1)
                _require(bool(subtree) and key == next(iter(subtree))
                    and (not result or next(reversed(result)) < key), 'bbolt branch range/key differs')
                _require(not result.keys() & subtree.keys(), 'duplicate hidden bbolt branch key')
                result.update(subtree)
                continue
            value = raw[start + key_size:end]
            if leaf_flags:
                _require(len(value) >= 16, 'bbolt bucket header bound')
                root, _ = struct.unpack_from('<QQ', value)
                _require(root == 0 or len(value) == 16, 'noninline bbolt bucket trailing bytes')
                value = self.tree(root, depth + 1, value[16:] if root == 0 else None)
            result[key] = value
        if inline is not None:
            _require(max((end for _, end in spans), default=16) == len(raw), 'bbolt inline hidden/trailing payload')
        return result


def _decode_bbolt(raw, check=lambda: None):
    """Fully enumerate the highest committed, checksummed tree (private API)."""
    try:
        reader = _Bolt(raw, check)
        return reader.tree(reader.meta_value['root'])
    except (struct.error, IndexError, RecursionError, OverflowError) as error:
        raise ValueError('malformed bounded bbolt format') from error


def _empty_buckets(tree, allowed, required=()):
    _require(type(tree) is dict and set(required) <= set(tree) <= set(allowed)
             and all(type(value) is dict and not value for value in tree.values()),
             'unknown/nonempty initialized index')


def _containerd(tree):
    _require(set(tree) == {b'v1'} and type(tree[b'v1']) is dict, 'containerd schema root')
    schema = tree[b'v1']
    # encodeInt uses Go binary.PutVarint: signed4 is zigzag encoded as08.
    _require(schema.get(b'version') == b'\x08', 'containerd signed version4 encoding')
    for namespace, objects in schema.items():
        if namespace == b'version':
            continue
        _require(namespace in (b'moby', b'plugins.moby', b'buildkit', b'buildkit_history')
                 and type(objects) is dict, 'unknown containerd namespace')
        _empty_buckets(objects, (b'labels', b'images', b'containers', b'snapshots',
                                 b'content', b'leases', b'sandboxes', b'indexes'))


def _network_metadata(tree):
    """Pinned libnetwork records: only host/null builtins with no endpoints.

    Docker29.9.0 daemon/libnetwork/kvstore/boltdb prepends8 bytes of LE index
    before network.MarshalJSON / endpointCnt JSON. Count zero alone is not a
    proof: every key must be enumerated and belong to these two builtin records.
    """
    _require(set(tree) <= {b'libnetwork'} and type(tree.get(b'libnetwork', {})) is dict,
             'unknown libnetwork database bucket')
    networks, counts, names = {}, {}, set()
    for key, raw in tree.get(b'libnetwork', {}).items():
        _require(type(raw) is bytes and 8 < len(raw) <= 65536
                 and struct.unpack_from('<Q', raw)[0] > 0, 'libnetwork index/payload encoding')
        match = re.fullmatch(rb'docker/network/v1\.0/(network|endpoint_count)/([0-9a-f]{64})/', key)
        _require(match is not None, 'endpoint/sandbox/business/unknown network restoration key')
        kind, identity = match.groups()
        value = _json(raw[8:])
        if kind == b'endpoint_count':
            _require(value == {'Count': 0} and type(value['Count']) is int, 'nonzero/malformed network endpoint count')
            counts[identity] = value
            continue
        fields = ('name', 'id', 'created', 'networkType', 'scope', 'labels', 'ipamType',
            'ipamOptions', 'addrSpace', 'enableIPv4', 'enableIPv6', 'generic', 'persist',
            'internal', 'attachable', 'inDelete', 'ingress', 'configOnly', 'configFrom',
            'loadBalancerIP', 'loadBalancerMode', 'skipGwAllocIPv4', 'skipGwAllocIPv6')
        _require(set(value) == set(fields), 'unknown/nonbuiltin network schema')
        _require((value['name'], value['networkType']) in (('host', 'host'), ('none', 'null'))
            and value['name'] not in names and value['id'].encode() == identity
            and value['scope'] == 'local' and value['labels'] in ({}, None)
            and value['ipamOptions'] in ({}, None) and value['ipamType'] in ('null', 'default')
            and value['addrSpace'] == '' and value['enableIPv4'] is True
            and value['generic'] == {'com.docker.network.generic': {}} and value['persist'] is True
            and value['configFrom'] == '' and value['loadBalancerIP'] == ''
            and value['loadBalancerMode'] == 'NAT', 'builtin network identity/driver/options differ')
        _require(all(value[key] is False for key in ('enableIPv6', 'internal', 'attachable', 'inDelete',
            'ingress', 'configOnly', 'skipGwAllocIPv4', 'skipGwAllocIPv6')), 'builtin network behavior differs')
        created = value['created']
        _require(type(created) is str and re.fullmatch(
            r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})', created),
            'network creation time encoding')
        datetime.datetime.fromisoformat(re.sub(r'(\.[0-9]{6})[0-9]+', r'\1', created).replace('Z', '+00:00'))
        names.add(value['name'])
        networks[identity] = value
    _require(set(networks) == set(counts) and len(networks) <= 2, 'network/count complete ID coverage differs')


def _classify(relative, raw, check=lambda: None, observe=lambda *args: None):
    """Recognize exact nonexecuting formats. A filename alone proves nothing."""
    _require(type(relative) is str and str(PurePosixPath(relative)) == relative
             and not relative.startswith('/') and '..' not in PurePosixPath(relative).parts,
             'store relative path')
    if relative in RETAINED_INPUTS:
        _require(_sha(raw) == RETAINED_INPUTS[relative], 'retained bootstrap exact content hash differs')
        return 'retained_bootstrap_input', 'switchback-retained-bootstrap-sha256-v1'
    if relative == 'engine-id':
        _require(re.fullmatch(rb'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', raw)
                 is not None, 'unknown Engine identity format')
        return 'engine_identity', 'docker-29.9.0-engine-id-v1'
    if relative == 'image/overlay2/repositories.json':
        _require(_json(raw) == {'Repositories': {}}, 'nonempty Docker repository index')
        return 'docker_empty_index', 'docker-29.9.0-repositories-v1'
    blob = re.fullmatch(r'containerd/io\.containerd\.content\.v1\.content/blobs/sha256/([0-9a-f]{64})', relative)
    if blob:
        _require(hashlib.sha256(raw).hexdigest() == blob.group(1), 'content blob filename/hash differs')
        return 'content_cache', 'containerd-2.4.1-content-sha256-v1'
    tree = _decode_bbolt(raw, check)
    observe('store-decoded-current-tree', {'path': relative, 'tree': tree})
    if relative == 'containerd/io.containerd.metadata.v1.bolt/meta.db':
        _containerd(tree)
        return 'containerd_empty_metadata', 'containerd-2.4.1-bbolt-v4'
    if relative == 'volumes/metadata.db':
        _empty_buckets(tree, (b'volumes',), (b'volumes',))
        return 'docker_empty_volume_index', 'docker-29.9.0-volume-bbolt-v1'
    if relative in ('buildkit/containerdmeta.db', 'buildkit/snapshots.db'):
        _require(tree == {}, 'nonempty BuildKit graphdriver restoration index')
        return 'buildkit_empty_index', 'docker-29.9.0-buildkit-empty-bbolt-v1'
    if relative == 'network/files/local-kv.db':
        _network_metadata(tree)
        return 'docker_network_metadata', 'docker-29.9.0-network-bbolt-v1'
    if relative == 'buildkit/cache.db':
        names = (b'_result', b'_links', b'_byresult', b'_backlinks')
        _empty_buckets(tree, names, names)
    elif relative == 'buildkit/metadata_v2.db':
        _empty_buckets(tree, (b'_main', b'_index', b'_external'))
    elif relative == 'buildkit/history.db':
        _require(set(tree) <= {b'_records', b'_version'}, 'unknown BuildKit history schema')
        _require(tree.get(b'_records', {}) == {} and tree.get(b'_version', {b'version': b'2'})
                 == {b'version': b'2'}, 'BuildKit history restoration input')
    else:
        raise ValueError('unknown store origin/format: ' + relative)
    return 'buildkit_cache', 'docker-29.9.0-buildkit-bbolt-v1'


def _network_metadata_v2(tree, check=lambda: None):
    """Closed M1 default-bridge subset; legacy parsing remains independent.

    Return True only for all seven linked default records, False for the strict
    host/none subset. Saved record bytes define this conservative private ABI.
    """
    check()
    _require(type(tree) is dict and set(tree) <= {b'libnetwork'}
             and type(tree.get(b'libnetwork', {})) is dict, 'unknown libnetwork database bucket')
    records = tree.get(b'libnetwork', {})
    _require(len(records) <= 7, 'default network record bound')
    networks, counts, drivers, names = {}, {}, {}, set()
    common = {'name', 'id', 'created', 'networkType', 'scope', 'labels', 'ipamType',
        'ipamOptions', 'addrSpace', 'enableIPv4', 'enableIPv6', 'generic', 'persist',
        'internal', 'attachable', 'inDelete', 'ingress', 'configOnly', 'configFrom',
        'loadBalancerIP', 'loadBalancerMode', 'skipGwAllocIPv4', 'skipGwAllocIPv6'}
    disabled = ('enableIPv6', 'internal', 'attachable', 'inDelete', 'ingress',
                'configOnly', 'skipGwAllocIPv4', 'skipGwAllocIPv6')
    for key, raw in records.items():
        check()
        _require(type(key) is bytes and type(raw) is bytes and 8 < len(raw) <= 65536
                 and struct.unpack_from('<Q', raw)[0] > 0, 'libnetwork index/payload encoding')
        match = re.fullmatch(rb'docker/network/v1\.0/(network|endpoint_count|bridge)/([0-9a-f]{64})/', key)
        _require(match is not None, 'endpoint/sandbox/business/unknown network restoration key')
        kind, identity = match.groups()
        check()
        value = _json(raw[8:])
        check()
        if kind == b'endpoint_count':
            _require(value == {'Count': 0} and type(value['Count']) is int,
                     'nonzero/malformed network endpoint count')
            counts[identity] = value
            continue
        if kind == b'bridge':
            drivers[identity] = value
            continue
        bridge = value.get('name') == 'bridge'
        _require(set(value) == common | ({'ipamV4Config', 'ipamV4Info'} if bridge else set()),
                 'unknown/nonbuiltin network schema')
        name = value['name']
        _require(type(name) is str and name not in names and type(value['id']) is str
                 and value['id'].encode() == identity and value['scope'] == 'local'
                 and value['enableIPv4'] is True and value['persist'] is True
                 and value['configFrom'] == '' and value['loadBalancerIP'] == ''
                 and value['loadBalancerMode'] == 'NAT'
                 and all(value[field] is False for field in disabled),
                 'builtin network identity/behavior differs')
        if bridge:
            options = {'com.docker.network.bridge.default_bridge': 'true',
                'com.docker.network.bridge.enable_icc': 'true',
                'com.docker.network.bridge.enable_ip_masquerade': 'true',
                'com.docker.network.bridge.host_binding_ipv4': '0.0.0.0',
                'com.docker.network.bridge.name': 'docker0', 'com.docker.network.driver.mtu': '1500'}
            generic = value['generic']
            _require(value['networkType'] == 'bridge' and value['addrSpace'] == 'LocalDefault'
                and value['ipamType'] == 'default' and value['labels'] is None
                and value['ipamOptions'] is None and type(generic) is dict
                and set(generic) == {'com.docker.network.enable_ipv4', 'com.docker.network.enable_ipv6',
                                     'com.docker.network.generic'}
                and generic['com.docker.network.enable_ipv4'] is True
                and generic['com.docker.network.enable_ipv6'] is False
                and generic['com.docker.network.generic'] == options,
                'default bridge driver/options differ')
        else:
            _require((name, value['networkType']) in (('host', 'host'), ('none', 'null'))
                and value['labels'] in ({}, None) and value['ipamOptions'] in ({}, None)
                and value['ipamType'] in ('null', 'default') and value['addrSpace'] == ''
                and value['generic'] == {'com.docker.network.generic': {}},
                'builtin network identity/driver/options differ')
        created = value['created']
        _require(type(created) is str and re.fullmatch(
            r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})', created),
            'network creation time encoding')
        datetime.datetime.fromisoformat(re.sub(r'(\.[0-9]{6})[0-9]+', r'\1', created).replace('Z', '+00:00'))
        names.add(name)
        networks[identity] = value
    _require(set(networks) == set(counts), 'network/count complete ID coverage differs')
    if 'bridge' not in names:
        _require(not drivers and len(networks) <= 2, 'orphan bridge driver/nonbuiltin network')
        return False
    _require(len(records) == 7 and len(networks) == 3 and names == {'host', 'none', 'bridge'},
             'incomplete default bridge network set')
    bridge_id = next(identity for identity, value in networks.items() if value['name'] == 'bridge')
    _require(set(drivers) == {bridge_id}, 'default bridge driver ID coverage differs')
    bridge, driver = networks[bridge_id], drivers[bridge_id]

    def nested(text):
        check()
        _require(type(text) is str, 'nested network JSON string type')
        raw = text.encode('utf-8')
        _require(0 < len(raw) <= 8192, 'nested network JSON byte bound')
        # The shared duplicate-safe bounded decoder requires an object root.
        wrapper = _json(b'{"value":' + raw + b'}')
        _require(set(wrapper) == {'value'}, 'nested network JSON trailing content')
        check()
        return wrapper['value']

    config = nested(bridge['ipamV4Config'])
    _require(type(config) is list and len(config) == 1 and type(config[0]) is dict
             and set(config[0]) == {'PreferredPool', 'SubPool', 'Gateway', 'AuxAddresses'},
             'unsupported default bridge IPAM config')
    info = nested(bridge['ipamV4Info'])
    _require(type(info) is list and len(info) == 1 and type(info[0]) is dict
             and set(info[0]) == {'IPAMData', 'PoolID'}, 'default bridge IPAM info schema')
    data = nested(info[0]['IPAMData'])
    _require(type(data) is dict and set(data) == {'AddressSpace', 'AuxAddresses', 'Gateway', 'Pool'}
        and data['AddressSpace'] == 'LocalDefault' and data['AuxAddresses'] == {}
        and type(data['Pool']) is str and type(data['Gateway']) is str,
        'default bridge IPAM data schema')
    check()
    pool = ipaddress.IPv4Network(data['Pool'], strict=True)
    gateway = ipaddress.IPv4Interface(data['Gateway'])
    _require(str(pool) == data['Pool'] and str(gateway) == data['Gateway']
        and any(pool.subnet_of(ipaddress.IPv4Network(private))
                for private in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'))
        and gateway.network == pool and gateway.ip not in (pool.network_address, pool.broadcast_address)
        and info[0]['PoolID'] == 'LocalDefault/' + str(pool), 'default bridge IPAM allocation differs')
    empty_request = {'PreferredPool': '', 'SubPool': '', 'Gateway': '', 'AuxAddresses': {}}
    explicit_request = {'PreferredPool': str(pool), 'SubPool': '',
                        'Gateway': str(gateway.ip), 'AuxAddresses': {}}
    _require(config[0] == empty_request or config[0] == explicit_request,
             'unsupported default bridge IPAM config')
    fixed = {'ID': bridge_id.decode(), 'AddressIPv4': data['Gateway'],
        'BridgeName': 'docker0', 'BridgeIfaceCreator': 1, 'Mtu': 1500,
        'DefaultBridge': True, 'EnableICC': True, 'EnableIPMasquerade': True, 'EnableIPv4': True,
        'EnableIPv6': False, 'InhibitIPv4': False, 'Internal': False, 'DefaultBindingIP': '0.0.0.0',
        'ContainerIfacePrefix': '', 'GwModeIPv4': '', 'GwModeIPv6': '', 'TrustedHostInterfaces': '',
        'DefaultGatewayIPv4': '<nil>', 'DefaultGatewayIPv6': '<nil>', 'HostIP': '<nil>', 'HostIPv6': '<nil>'}
    _require(set(driver) == set(fixed), 'default bridge driver fields differ')
    for field, expected in fixed.items():
        check()
        if field == 'BridgeIfaceCreator':
            _require(type(driver[field]) is int and driver[field] in (1, 2),
                     'default bridge driver value differs: ' + field)
            continue
        _require(type(driver[field]) is type(expected) and driver[field] == expected,
                 'default bridge driver value differs: ' + field)
    return True


def _classify_v4(relative, raw, check=lambda: None, observe=lambda *args: None):
    """Only the bound scanner selects the versioned default-bridge policy."""
    if relative != 'network/files/local-kv.db':
        return _classify(relative, raw, check, observe)
    tree = _decode_bbolt(raw, check)
    observe('store-decoded-current-tree', {'path': relative, 'tree': tree})
    if _network_metadata_v2(tree, check):
        return 'docker_default_bridge_metadata', 'docker-29.9.0-network-bbolt-v2'
    return 'docker_network_metadata', 'docker-29.9.0-network-bbolt-v1'


def _identity(value):
    return (value.st_dev, value.st_ino, value.st_mode, value.st_nlink, value.st_uid,
            value.st_gid, value.st_size, getattr(value, 'st_rdev', None),
            value.st_mtime_ns, value.st_ctime_ns)


def _device_metadata_ints(value):
    return all(type(getattr(value, field, None)) is int for field in (
        'st_dev', 'st_ino', 'st_mode', 'st_nlink', 'st_uid', 'st_gid',
        'st_size', 'st_rdev', 'st_mtime_ns', 'st_ctime_ns'))


def _same_identity(before, current):
    return (_identity(before) == _identity(current)
            and (not stat.S_ISBLK(before.st_mode)
                 or (_device_metadata_ints(before) and _device_metadata_ints(current))))


def _scan_store(root=Path(DATA_ROOT), logical_root=DATA_ROOT, uid=0, gid=0,
                check=lambda: None, observe=lambda *args: None, *, expected_filesystem=None):
    """Descriptor-based, complete read-only walk; path/UID seams are test-only."""
    root = Path(root)
    _require(logical_root == DATA_ROOT, 'fixed store logical root')
    if expected_filesystem is not None:
        _filesystem(expected_filesystem)
        expected_filesystem = dict(expected_filesystem)
        expected_device = _linux_device_number(expected_filesystem['major'], expected_filesystem['minor'])
    else:
        expected_device = None
    entries, identities, total = [], {}, 0

    def observed_stat(path, logical_path, value):
        # Preserve raw type bits and rejected ownership before any validation.
        # Paths are display-bounded; the full path identity remains hash-bound.
        raw = os.fsencode(path)
        display = lambda value: os.fsencode(value)[:4096].decode('utf-8', errors='backslashreplace')[:4096]
        observe('store-stat-before-validation', {'path': display(logical_path),
            'filesystem_path': display(path), 'path_base64': base64.b64encode(raw[:4096]).decode('ascii'),
            'path_bytes': len(raw), 'path_sha256': _sha(raw), 'path_truncated': len(raw) > 4096,
            'stat': {'device': value.st_dev, 'inode': value.st_ino, 'rdev': getattr(value, 'st_rdev', None),
                     'mode': value.st_mode,
                     'uid': value.st_uid, 'gid': value.st_gid, 'nlink': value.st_nlink,
                     'bytes': value.st_size, 'mtime_ns': value.st_mtime_ns, 'ctime_ns': value.st_ctime_ns}})
        # The supported store classes use UTF-8 paths. Unknown filesystem byte
        # names reject before opening payload, after their actual bytes/stat are
        # safely encoded and persisted by the bounded raw audit above.
        try:
            raw.decode('utf-8')
        except UnicodeDecodeError as error:
            raise ValueError('unsupported non-UTF8 store path; raw stat retained') from error

    def names_in(fd):
        names = []
        with os.scandir(fd) as iterator:
            for entry in iterator:
                check()
                names.append(entry.name)
                _require(len(names) <= MAX_STORE_ENTRIES, 'store directory entry bound')
        check()
        return sorted(names)

    def walk(path, relative, parent_fd=None):
        nonlocal total
        check()
        before = os.stat(path.name if parent_fd is not None else str(path),
                         dir_fd=parent_fd, follow_symlinks=False)
        logical_path = logical_root + ('/' + relative if relative else '')
        observed_stat(path, logical_path, before)
        check()
        bound_directory = expected_device is not None and stat.S_ISDIR(before.st_mode)
        if bound_directory:
            _require(_device_metadata_ints(before), 'unsafe store directory metadata: ' + logical_path)
        empty_plugin_mode = (EMPTY_PLUGIN_DIRECTORY_MODES.get(logical_path)
                             if expected_device is not None else None)
        if empty_plugin_mode is not None:
            _require(_device_metadata_ints(before)
                     and before.st_dev == expected_device
                     and before.st_mode == (stat.S_IFDIR | empty_plugin_mode)
                     and before.st_uid == before.st_gid == 0
                     and before.st_nlink == 2,
                     'unsafe empty plugin directory: ' + logical_path)
        _require(before.st_uid == uid and before.st_gid == gid
                 and not before.st_mode & 0o6022, 'unsafe store owner/mode: ' + logical_path)
        if expected_device is not None:
            _require(type(before.st_dev) is int and before.st_dev == expected_device,
                     'store node differs from backing filesystem: ' + logical_path)
        _require(len(entries) < MAX_STORE_ENTRIES, 'store entry bound')
        item = {'path': logical_path,
                'uid': before.st_uid, 'gid': before.st_gid,
                'mode': stat.S_IMODE(before.st_mode), 'nlink': before.st_nlink,
                'bytes': 0, 'sha256': None, 'format': 'directory-v1',
                'classification': 'empty_directory', 'rule_revision':
                    STORE_RULE_REVISION_V4 if expected_device is not None else STORE_RULE_REVISION,
                'scan_complete': True, 'restore_refs': dict.fromkeys(RESTORE_CLASSES, 0)}
        entries.append(item)
        identities[item['path']] = _identity(before)
        flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
        if stat.S_ISDIR(before.st_mode):
            fd = os.open(path.name if parent_fd is not None else str(path), flags | os.O_DIRECTORY,
                         dir_fd=parent_fd)
            try:
                current = os.fstat(fd)
                observed_stat(path, logical_path, current)
                if bound_directory:
                    _require(_device_metadata_ints(current),
                             'unsafe store directory fd metadata: ' + logical_path)
                _require(_same_identity(before, current), 'store directory raced: ' + logical_path)
                names = names_in(fd)
                _require(len(names) <= MAX_STORE_ENTRIES and all(name not in ('.', '..') for name in names),
                         'store directory name/entry bound')
                if empty_plugin_mode is not None:
                    _require(not names, 'nonempty plugin directory: ' + logical_path)
                item['kind'] = 'directory'
                item['classification'] = 'engine_directory' if names else 'empty_directory'
                for name in names:
                    walk(path / name, relative + '/' + name if relative else name, fd)
                _require(names == names_in(fd), 'store directory coverage drift: ' + logical_path)
                current = os.fstat(fd)
                observed_stat(path, logical_path, current)
                if bound_directory:
                    _require(_device_metadata_ints(current),
                             'unsafe store directory fd metadata: ' + logical_path)
                _require(_same_identity(before, current), 'store directory identity drift: ' + logical_path)
            finally:
                os.close(fd)
        elif (stat.S_ISBLK(before.st_mode) and expected_device is not None
              and logical_path in QUOTA_BACKING_DEVICE_PATHS):
            _require(parent_fd is not None and _device_metadata_ints(before)
                     and before.st_mode == (stat.S_IFBLK | 0o600)
                     and before.st_nlink == 1 and before.st_size == 0
                     and type(before.st_ino) is int and before.st_ino > 0
                     and type(getattr(before, 'st_rdev', None)) is int
                     and before.st_rdev == expected_device,
                     'unsafe quota backing device identity')
            _require(type(getattr(os, 'O_PATH', None)) is int and os.O_PATH > 0,
                     'O_PATH required for backing device metadata')
            fd = os.open(path.name, os.O_PATH | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent_fd)
            try:
                current = os.fstat(fd)
                observed_stat(path, logical_path, current)
                _require(_same_identity(before, current), 'quota backing device raced')
            finally:
                os.close(fd)
            item.update(kind='block_device', classification='quota_backing_device',
                        format='docker-xfs-quota-backing-device-v1',
                        device_identity={'device': before.st_dev, 'inode': before.st_ino,
                            'rdev': before.st_rdev, 'major': expected_filesystem['major'],
                            'minor': expected_filesystem['minor']})
            observe('store-device', {'path': item['path'], 'identity': list(_identity(before))})
        else:
            _require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1
                     and 0 < before.st_size <= MAX_STORE_BYTES, 'unsafe store leaf type/size/links')
            total += before.st_size
            _require(total <= MAX_STORE_BYTES, 'store scan total byte bound')
            blob = re.fullmatch(r'containerd/io\.containerd\.content\.v1\.content/blobs/sha256/[0-9a-f]{64}', relative)
            opaque = bool(blob) or relative in RETAINED_INPUTS
            _require(opaque or before.st_size <= _DB_BYTES, 'store decoder byte bound')
            fd = os.open(path.name, flags, dir_fd=parent_fd)
            try:
                current = os.fstat(fd)
                observed_stat(path, logical_path, current)
                _require(_same_identity(before, current), 'store leaf raced')
                digest, raw, count = hashlib.sha256(), bytearray(), 0
                while True:
                    check()
                    chunk = os.read(fd, 65536)
                    if not chunk:
                        break
                    count += len(chunk)
                    _require(count <= before.st_size, 'store growing during bounded read')
                    digest.update(chunk)
                    if not opaque:
                        raw.extend(chunk)
                current = os.fstat(fd)
                observed_stat(path, logical_path, current)
                _require(count == before.st_size and _same_identity(before, current),
                         'store payload identity drift')
                item['kind'], item['bytes'], item['sha256'] = 'regular', count, 'sha256:' + digest.hexdigest()
                observe('store-leaf', {'path': item['path'], 'identity': list(_identity(before)),
                                       'sha256': item['sha256']})
                if blob:
                    _require(digest.hexdigest() == path.name, 'content filename digest mismatch')
                    item['classification'], item['format'] = 'content_cache', 'containerd-2.4.1-content-sha256-v1'
                elif relative in RETAINED_INPUTS:
                    _require(item['sha256'] == RETAINED_INPUTS[relative], 'retained bootstrap exact content hash differs')
                    item['classification'], item['format'] = 'retained_bootstrap_input', 'switchback-retained-bootstrap-sha256-v1'
                else:
                    observe('store-bounded-payload-prefix', {'path': item['path'], 'raw': bytes(raw[:4096]),
                                                            'bytes': len(raw), 'sha256': item['sha256']})
                    classifier = _classify_v4 if expected_device is not None else _classify
                    item['classification'], item['format'] = classifier(relative, bytes(raw), check, observe)
            finally:
                os.close(fd)
        after = os.stat(path.name if parent_fd is not None else str(path), dir_fd=parent_fd,
                        follow_symlinks=False)
        observed_stat(path, logical_path, after)
        if bound_directory:
            _require(_device_metadata_ints(after),
                     'unsafe store directory pathname metadata: ' + logical_path)
        _require(_same_identity(before, after), 'store pathname drift: ' + logical_path)

    walk(root, '')
    # Recheck every observed leaf/path after the full traversal, including bytes.
    for item in entries:
        check()
        path = root / str(PurePosixPath(item['path']).relative_to(logical_root))
        current = path.lstat()
        observed_stat(path, item['path'], current)
        if expected_device is not None and item['kind'] == 'directory':
            _require(_device_metadata_ints(current),
                     'unsafe store directory final metadata: ' + item['path'])
        if item['kind'] == 'block_device':
            _require(_device_metadata_ints(current), 'quota backing device final metadata type drift')
        _require(_identity(current) == identities[item['path']],
                 'store changed during complete scan: ' + item['path'])
    result = {'schema': ('switchback.engine.store_inventory.v4' if expected_device is not None
                         else 'switchback.engine.store_inventory.v1'), 'root': logical_root,
              'containerd_root': CONTAINERD_ROOT, 'docker_version': '29.9.0',
              'containerd_version': '2.4.1', 'scan_complete': True, 'entries': entries,
              'restoration': dict.fromkeys(RESTORE_CLASSES, 0)}
    if expected_device is not None:
        result['backing_filesystem'] = expected_filesystem
    if uid == gid == 0:
        validate_store_inventory(result)
    return result
