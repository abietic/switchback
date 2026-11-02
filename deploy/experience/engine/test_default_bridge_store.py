"""Exact saved record bytes inside explicitly synthetic bbolt/syscall envelopes.

The saved tree is real; no complete original raw DB was retained. All DB pages,
filesystem nodes, lifecycle wrapper and preflight facts below are offline synthetic.
"""
import ast
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import stat
import struct
from types import SimpleNamespace
import unittest
from unittest import mock

import store
from contract import DATA_ROOT, validate_store_inventory, validate_preflight, validate_preflight_v2
from test_store import leaf, fnv
from test_contract import preflight, preflight_v2

HERE = Path(__file__).parent
NETWORK_PATH = 'network/files/local-kv.db'


def saved_tree():
    fixture = json.loads((HERE / 'testdata/default-bridge-records.json').read_bytes())
    result = {}
    for bucket, records in fixture['tree'].items():
        result[ast.literal_eval(bucket)] = {}
        for key, record in records.items():
            raw = base64.b64decode(record['base64'], validate=True)
            assert len(raw) == record['bytes']
            assert 'sha256:' + hashlib.sha256(raw).hexdigest() == record['sha256']
            result[ast.literal_eval(bucket)][ast.literal_eval(key)] = raw
    return result


def synthetic_bolt(tree, second_tree=None):
    # Reuse the independent hand encoder, at 32768B/page to fit all seven leaves.
    size = 32768
    pages = [bytes(size) for _ in range(6)]
    for index, txid, root in ((0, 2, 4), (1, 3, 5)):
        meta = struct.pack('<IIIIQQQQQ', 0xED0CDAED, 2, size, 0, root, 0,
                           (1 << 64) - 1, 6, txid)
        pages[index] = (struct.pack('<QHHI', index, 4, 0, 0) + meta
                        + struct.pack('<Q', fnv(meta))).ljust(size, b'\0')
    for page, value in ((4, tree), (5, tree if second_tree is None else second_tree)):
        encoded = leaf(value, page, size)
        assert len(encoded) == size, 'synthetic fixture page overflow'
        pages[page] = encoded
    return b''.join(pages)


def update_record(tree, kind, change):
    records = tree[b'libnetwork']
    key = next(key for key in records if b'/v1.0/' + kind.encode() + b'/' in key
               and (kind == 'bridge' or (kind == 'network' and json.loads(records[key][8:])['name'] == 'bridge')))
    value = json.loads(records[key][8:]); change(value)
    records[key] = records[key][:8] + json.dumps(value, separators=(',', ':')).encode()


class DefaultBridgeTests(unittest.TestCase):
    def test_exact_saved_records_and_legacy_separation(self):
        tree = saved_tree()
        self.assertEqual(len(tree[b'libnetwork']), 7)
        with self.assertRaises(ValueError): store._network_metadata(tree)
        self.assertIs(store._network_metadata_v2(tree), True)
        self.assertIs(store._network_metadata_v2({}), False)
        subset = {b'libnetwork': {k: v for k, v in tree[b'libnetwork'].items()
                  if b'/bridge/' not in k and (b'/v1.0/network/' not in k or json.loads(v[8:])['name'] != 'bridge')}}
        # Remove the bridge count using the network's saved ID, leaving strict host/none.
        ids = {k.split(b'/')[-2] for k in subset[b'libnetwork'] if b'/v1.0/network/' in k}
        subset[b'libnetwork'] = {k: v for k, v in subset[b'libnetwork'].items() if k.split(b'/')[-2] in ids}
        store._network_metadata(subset)
        self.assertIs(store._network_metadata_v2(subset), False)

    def test_real_decoder_classifier_observation_once_and_latest_tx(self):
        tree = saved_tree(); raw = synthetic_bolt({}, tree)
        self.assertEqual(store._decode_bbolt(raw), tree)
        observed = []
        with mock.patch('store._decode_bbolt', wraps=store._decode_bbolt) as decode:
            self.assertEqual(store._classify_v4(NETWORK_PATH, raw, observe=lambda *a: observed.append(a)),
                ('docker_default_bridge_metadata', 'docker-29.9.0-network-bbolt-v2'))
            self.assertEqual(decode.call_count, 1)
        self.assertEqual(observed, [('store-decoded-current-tree', {'path': NETWORK_PATH, 'tree': tree})])
        with self.assertRaises(ValueError): store._classify(NETWORK_PATH, raw)
        self.assertEqual(store._classify_v4('engine-id', b'00000000-0000-4000-8000-000000000001'),
                         store._classify('engine-id', b'00000000-0000-4000-8000-000000000001'))
        self.assertEqual(store._classify_v4(NETWORK_PATH, synthetic_bolt({})),
                         ('docker_network_metadata', 'docker-29.9.0-network-bbolt-v1'))

    def assert_rejected(self, tree):
        with self.assertRaises(ValueError): store._network_metadata_v2(tree)
        with self.assertRaises(ValueError): store._classify_v4(NETWORK_PATH, synthetic_bolt(tree))

    def test_missing_every_record_extra_bucket_unknown_endpoint_and_sandbox_reject(self):
        original = saved_tree()
        for key in original[b'libnetwork']:
            tree = copy.deepcopy(original); del tree[b'libnetwork'][key]
            with self.subTest(missing=key): self.assert_rejected(tree)
        for name in (b'endpoint', b'sandbox', b'network', b'endpoint_count', b'bridge', b'unknown'):
            tree = saved_tree(); tree[b'libnetwork'][b'docker/network/v1.0/' + name + b'/' + b'a' * 64 + b'/'] = struct.pack('<Q', 1) + b'{"Count":0}'
            with self.subTest(extra=name): self.assert_rejected(tree)
        for tree in ({b'unknown': {}}, {b'libnetwork': []}, {b'libnetwork': {b'bad': b'bad'}}, []):
            with self.subTest(tree=tree), self.assertRaises(ValueError): store._network_metadata_v2(tree)

    def test_every_network_and_driver_field_missing_extra_or_wrong_type_rejects(self):
        for kind in ('network', 'bridge'):
            original = saved_tree()
            key = next(k for k, raw in original[b'libnetwork'].items() if b'/v1.0/' + kind.encode() + b'/' in k
                and (kind == 'bridge' or json.loads(raw[8:])['name'] == 'bridge'))
            value = json.loads(original[b'libnetwork'][key][8:])
            if kind == 'bridge': self.assertEqual(len(value), 21)
            for field in value:
                for label, change in (('missing', lambda v, f=field: v.pop(f)),
                                      ('type', lambda v, f=field: v.update({f: []}))):
                    tree = saved_tree(); update_record(tree, kind, change)
                    with self.subTest(kind=kind, field=field, case=label): self.assert_rejected(tree)
            tree = saved_tree(); update_record(tree, kind, lambda v: v.update(extra=False))
            self.assert_rejected(tree)

    def test_host_none_legacy_fields_names_and_id_encoding_reject(self):
        for name in ('host', 'none'):
            tree = saved_tree()
            key = next(k for k, raw in tree[b'libnetwork'].items()
                       if b'/v1.0/network/' in k and json.loads(raw[8:])['name'] == name)
            original = json.loads(tree[b'libnetwork'][key][8:])
            for field in original:
                for operation in ('missing', 'type'):
                    tree = saved_tree(); value = dict(original)
                    if operation == 'missing': value.pop(field)
                    else: value[field] = []
                    tree[b'libnetwork'][key] = struct.pack('<Q', 1) + json.dumps(value).encode()
                    with self.subTest(name=name, field=field, operation=operation): self.assert_rejected(tree)
            for replacement in ('HOST', 'a' * 63, 'A' * 64, 'a' * 65):
                tree = saved_tree(); raw = tree[b'libnetwork'].pop(key)
                tree[b'libnetwork'][key.replace(key.split(b'/')[-2], replacement.encode())] = raw
                with self.subTest(identity=replacement): self.assert_rejected(tree)
            for field, value in (('id', 'a' * 64), ('name', 'none' if name == 'host' else 'host'),
                                 ('enableIPv4', 1), ('persist', 1), ('scope', 'global'),
                                 ('labels', {'business': 'x'}), ('generic', {'extra': {}})):
                tree = saved_tree(); body = dict(original); body[field] = value
                tree[b'libnetwork'][key] = struct.pack('<Q', 1) + json.dumps(body).encode()
                with self.subTest(name=name, field=field): self.assert_rejected(tree)
        tree = saved_tree(); driver_key = next(k for k in tree[b'libnetwork'] if b'/bridge/' in k)
        raw = tree[b'libnetwork'].pop(driver_key)
        tree[b'libnetwork'][driver_key.replace(driver_key.split(b'/')[-2], b'a' * 64)] = raw
        self.assert_rejected(tree)

    def test_driver_fixed_values_and_bool_integer_confusion_reject(self):
        raw = next(v for k, v in saved_tree()[b'libnetwork'].items() if b'/bridge/' in k)
        for field, value in json.loads(raw[8:]).items():
            invalid = not value if type(value) is bool else (True if type(value) is int else value + 'x')
            tree = saved_tree(); update_record(tree, 'bridge', lambda v, f=field, x=invalid: v.update({f: x}))
            with self.subTest(field=field): self.assert_rejected(tree)
        for count in (1, -1, False, 0.0, '0'):
            tree = saved_tree(); key = next(k for k in tree[b'libnetwork'] if b'/endpoint_count/' in k)
            tree[b'libnetwork'][key] = struct.pack('<Q', 1) + json.dumps({'Count': count}).encode()
            with self.subTest(count=count): self.assert_rejected(tree)

    def test_all_nested_schemas_and_fixed_bridge_values_are_closed(self):
        for layer in ('config', 'info', 'data'):
            original = json.loads(next(v for k, v in saved_tree()[b'libnetwork'].items()
                if b'/v1.0/network/' in k and json.loads(v[8:])['name'] == 'bridge')[8:])
            config = json.loads(original['ipamV4Config']); info = json.loads(original['ipamV4Info'])
            data = json.loads(info[0]['IPAMData'])
            fields = config[0] if layer == 'config' else info[0] if layer == 'info' else data
            for field in tuple(fields) + ('extra',):
                for operation in ('missing', 'type'):
                    if field == 'extra' and operation == 'missing': continue
                    def change(v, f=field, op=operation, level=layer):
                        config = json.loads(v['ipamV4Config']); info = json.loads(v['ipamV4Info'])
                        body = json.loads(info[0]['IPAMData'])
                        target = config[0] if level == 'config' else info[0] if level == 'info' else body
                        if op == 'missing': target.pop(f)
                        else: target[f] = []
                        if level == 'data': info[0]['IPAMData'] = json.dumps(body)
                        v.update(ipamV4Config=json.dumps(config), ipamV4Info=json.dumps(info))
                    tree = saved_tree(); update_record(tree, 'network', change)
                    with self.subTest(layer=layer, field=field, operation=operation): self.assert_rejected(tree)
        for field, value in original.items():
            if field in ('id', 'created', 'ipamV4Config', 'ipamV4Info', 'generic'): continue
            invalid = (not value if type(value) is bool else value + 'x' if type(value) is str else {})
            tree = saved_tree(); update_record(tree, 'network', lambda v, f=field, x=invalid: v.update({f: x}))
            with self.subTest(fixed_field=field): self.assert_rejected(tree)
        for field in ('ipamV4Config', 'ipamV4Info'):
            for invalid in ('null', '{}', '[]', '[{},{}]', '[' * 1100 + ']' * 1100,
                            '"' + '界' * 2800 + '"'):
                tree = saved_tree(); update_record(tree, 'network', lambda v, f=field, x=invalid: v.update({f: x}))
                with self.subTest(field=field, size=len(invalid)): self.assert_rejected(tree)

    def test_nested_json_and_ipam_cross_links_reject(self):
        def nested(v, field, change):
            data = json.loads(v[field]); change(data); v[field] = json.dumps(data)
        def data(v, change):
            def mutate(info):
                body = json.loads(info[0]['IPAMData']); change(body); info[0]['IPAMData'] = json.dumps(body)
            nested(v, 'ipamV4Info', mutate)
        changes = [lambda v: v.update(ipamV4Info=' ' * 8193),
            lambda v: v.update(ipamV4Info='[]'), lambda v: v.update(ipamV4Config='{}'),
            lambda v: v.update(ipamV4Info='[{"IPAMData":"{}","IPAMData":"{}","PoolID":"x"}]'),
            lambda v: v.update(ipamV4Config=v['ipamV4Config'] + 'x'),
            lambda v: v.update(ipamV4Config=v['ipamV4Config'] + ',"extra":true'),
            lambda v: nested(v, 'ipamV4Config', lambda a: a[0].update(Gateway='172.17.0.1')),
            lambda v: nested(v, 'ipamV4Config', lambda a: a[0].update(AuxAddresses={'x': '172.17.0.2'})),
            lambda v: nested(v, 'ipamV4Info', lambda a: a[0].update(PoolID='LocalDefault/172.18.0.0/16')),
            lambda v: nested(v, 'ipamV4Info', lambda a: a[0].update(IPAMData=' ' * 8193)),
            lambda v: nested(v, 'ipamV4Info', lambda a: a[0].update(extra=True)),
            lambda v: data(v, lambda d: d.update(AddressSpace='GlobalDefault')),
            lambda v: data(v, lambda d: d.update(AuxAddresses={'x':'172.17.0.2'}))]
        for field, values in (('Pool', ('8.8.0.0/16', '100.64.0.0/10', '127.0.0.0/8', '172.0.0.0/8', '172.17.0.1/16', '172.17.0.0/255.255.0.0', 'fd00::/64', 1)),
                              ('Gateway', ('172.18.0.1/16', '172.17.0.0/16', '172.17.255.255/16', '172.17.0.1/24', '172.17.0.2/16', '172.17.0.1/255.255.0.0', '::1/16', 1))):
            changes += [lambda v, f=field, x=value: data(v, lambda d: d.update({f: x})) for value in values]
        for index, change in enumerate(changes):
            tree = saved_tree(); update_record(tree, 'network', change)
            with self.subTest(index=index): self.assert_rejected(tree)

    def test_generic_closed_fields_types_identity_revision_and_json_reject(self):
        changes = [lambda v: v.update(name='custom'), lambda v: v.update(networkType='host'),
            lambda v: v.update(id='a' * 64), lambda v: v.update(labels={}),
            lambda v: v.update(ipamOptions={}), lambda v: v.update(created='invalid'),
            lambda v: v.update(persist=1), lambda v: v.update(enableIPv4=1),
            lambda v: v['generic'].update(extra=True),
            lambda v: v['generic'].update({'com.docker.network.enable_ipv4': 1}),
            lambda v: v['generic'].pop('com.docker.network.enable_ipv6')]
        raw = next(v for k, v in saved_tree()[b'libnetwork'].items() if b'/v1.0/network/' in k and json.loads(v[8:])['name'] == 'bridge')
        opts = json.loads(raw[8:])['generic']['com.docker.network.generic']
        for option in opts:
            changes += [lambda v, k=option: v['generic']['com.docker.network.generic'].pop(k),
                        lambda v, k=option: v['generic']['com.docker.network.generic'].update({k: True})]
        for index, change in enumerate(changes):
            tree = saved_tree(); update_record(tree, 'network', change)
            with self.subTest(index=index): self.assert_rejected(tree)
        for payload in (b'{}', b'{"Count":0,"Count":0}', b'{"Count":0}garbage', b'[]'):
            tree = saved_tree(); key = next(k for k in tree[b'libnetwork'] if b'/endpoint_count/' in k)
            tree[b'libnetwork'][key] = struct.pack('<Q', 1) + payload
            self.assert_rejected(tree)
        tree = saved_tree(); key = next(iter(tree[b'libnetwork']))
        tree[b'libnetwork'][key] = bytes(8) + tree[b'libnetwork'][key][8:]
        self.assert_rejected(tree)

    def test_dynamic_ids_revision_time_and_entire_rfc1918_pool_are_supported(self):
        for pool, gateway in (('10.20.0.0/16', '10.20.0.1/16'), ('192.168.5.0/24', '192.168.5.1/24')):
            tree = saved_tree()
            def change(v):
                info = json.loads(v['ipamV4Info']); data = json.loads(info[0]['IPAMData'])
                data.update(Pool=pool, Gateway=gateway); info[0].update(IPAMData=json.dumps(data), PoolID='LocalDefault/' + pool)
                v.update(ipamV4Info=json.dumps(info), created='2026-01-01T00:00:00Z')
            update_record(tree, 'network', change)
            update_record(tree, 'bridge', lambda v: v.update(AddressIPv4=gateway))
            replaced = {}
            for key, raw in tree[b'libnetwork'].items():
                identity = key.split(b'/')[-2]; new_id = hashlib.sha256(identity).hexdigest().encode()
                value = json.loads(raw[8:])
                if 'id' in value: value['id'] = new_id.decode()
                if 'ID' in value: value['ID'] = new_id.decode()
                replaced[key.replace(identity, new_id)] = struct.pack('<Q', 99) + json.dumps(value).encode()
            self.assertIs(store._network_metadata_v2({b'libnetwork': replaced}), True)

    def test_check_interrupts_record_and_nested_parsing(self):
        calls = []
        store._network_metadata_v2(saved_tree(), lambda: calls.append(1))
        self.assertGreater(len(calls), 10)
        for stop in range(1, len(calls) + 1):
            count = [0]
            def check():
                count[0] += 1
                if count[0] == stop: raise TimeoutError('bounded parse deadline')
            with self.subTest(stop=stop), self.assertRaises(TimeoutError):
                store._network_metadata_v2(saved_tree(), check)


def scan_synthetic_default_store(tree=None, *, mutate=None, observe=lambda *a: None,
                                 bound=True, enumeration_drift=False, content_drift=False):
    """Actual scanner and decoders; only filesystem syscalls are synthetic."""
    tree = saved_tree() if tree is None else tree
    payloads = {
        NETWORK_PATH: synthetic_bolt({}, tree),
        'engine-id': b'00000000-0000-4000-8000-000000000001',
        'image/overlay2/repositories.json': b'{"Repositories":{}}',
        'volumes/metadata.db': synthetic_bolt({b'volumes': {}}),
        'containerd/io.containerd.metadata.v1.bolt/meta.db': synthetic_bolt({b'v1': {
            b'version': b'\x08', b'moby': {b'images': {}, b'containers': {}, b'content': {}}}}),
        'buildkit/containerdmeta.db': synthetic_bolt({}),
        'buildkit/snapshots.db': synthetic_bolt({}),
        'buildkit/cache.db': synthetic_bolt({key: {} for key in (b'_result', b'_links', b'_byresult', b'_backlinks')}),
        'buildkit/metadata_v2.db': synthetic_bolt({}),
        'buildkit/history.db': synthetic_bolt({b'_records': {}, b'_version': {b'version': b'2'}}),
    }
    blob = b'synthetic inert content cache'
    payloads['containerd/io.containerd.content.v1.content/blobs/sha256/' + hashlib.sha256(blob).hexdigest()] = blob
    payloads = {DATA_ROOT + '/' + key: raw for key, raw in payloads.items()}
    devices = {DATA_ROOT + '/' + prefix + '/backingFsBlockDev' for prefix in ('overlay2', 'volumes')}
    directories = {DATA_ROOT, DATA_ROOT + '/plugins', DATA_ROOT + '/plugins/storage', DATA_ROOT + '/plugins/tmp'}
    for path in set(payloads) | devices:
        parent = str(Path(path).parent)
        while parent.startswith(DATA_ROOT):
            directories.add(parent)
            if parent == DATA_ROOT: break
            parent = str(Path(parent).parent)
    stats = {}
    for index, path in enumerate(sorted(directories | set(payloads) | devices)):
        directory = path in directories
        mode = stat.S_IFDIR | (0o755 if path.endswith('/plugins/storage') else 0o700) if directory else stat.S_IFREG | 0o600
        if path in devices: mode = stat.S_IFBLK | 0o600
        stats[path] = SimpleNamespace(st_dev=64785, st_ino=index + 100, st_rdev=64785 if path in devices else 0,
            st_mode=mode, st_uid=0, st_gid=0, st_nlink=2 if directory else 1,
            st_size=len(payloads.get(path, b'')), st_mtime_ns=1000, st_ctime_ns=1000)
    if mutate: mutate(stats, payloads)
    fds, offsets, enumerations = {}, {}, {}
    def resolve(path, dir_fd=None):
        return str(Path(fds[dir_fd]) / path) if dir_fd is not None else str(path)
    def lookup(path, dir_fd=None, follow_symlinks=False):
        return copy.copy(stats[resolve(path, dir_fd)])
    def opening(path, flags, dir_fd=None):
        key = resolve(path, dir_fd)
        if key in devices:
            assert flags == 0x200000 | os.O_NOFOLLOW | os.O_CLOEXEC
        else:
            assert not flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC)
        fd = len(fds) + 100; fds[fd] = key; offsets[fd] = 0
        return fd
    def read(fd, count):
        path = fds[fd]; assert path in payloads, 'device/directory payload read'
        offset = offsets[fd]; raw = payloads[path][offset:offset + count]; offsets[fd] += len(raw)
        if content_drift and path == DATA_ROOT + '/' + NETWORK_PATH and not raw:
            stats[path].st_mtime_ns += 1
        return raw
    class Names:
        def __init__(self, fd): self.path = fds[fd]
        def __enter__(self):
            names = sorted(Path(path).name for path in stats if path != self.path and str(Path(path).parent) == self.path)
            calls = enumerations.get(self.path, 0); enumerations[self.path] = calls + 1
            if enumeration_drift and calls and self.path == DATA_ROOT + '/network/files': names = []
            return iter(SimpleNamespace(name=name) for name in names)
        def __exit__(self, *args): return False
    filesystem = {'mountpoint': '/mnt/lima-sbquota', 'fs_type': 'xfs',
        'uuid': '8782738b-dfc2-4426-966f-6a7eb0889ddd', 'major': 253,
        'minor': 17, 'root_inode': 128, 'quota_enforced': True}
    with (mock.patch('store.os.O_PATH', 0x200000, create=True),
          mock.patch('store.os.stat', side_effect=lookup), mock.patch('store.os.open', side_effect=opening),
          mock.patch('store.os.fstat', side_effect=lambda fd: copy.copy(stats[fds[fd]])),
          mock.patch('store.os.scandir', side_effect=Names), mock.patch('store.os.close'),
          mock.patch('store.os.read', side_effect=read),
          mock.patch.object(Path, 'lstat', new=lambda path: copy.copy(stats[str(path)]))):
        return store._scan_store(expected_filesystem=filesystem if bound else None, observe=observe)


class DefaultBridgeScannerTests(unittest.TestCase):
    def test_full_decoder_scanner_dto_and_both_preflight_versions(self):
        observations = []
        value = scan_synthetic_default_store(observe=lambda *a: observations.append(a))
        self.assertEqual(value['schema'], 'switchback.engine.store_inventory.v4')
        self.assertTrue(all(item['rule_revision'] == 'm1-engine-store-v4' for item in value['entries']))
        self.assertEqual(len([item for item in value['entries'] if item['kind'] == 'block_device']), 2)
        self.assertEqual(len([item for item in value['entries'] if item['path'].endswith(('/plugins/storage', '/plugins/tmp'))]), 2)
        network = next(item for item in value['entries'] if item['path'] == DATA_ROOT + '/' + NETWORK_PATH)
        self.assertEqual(network['classification'], 'docker_default_bridge_metadata')
        self.assertEqual(network['format'], 'docker-29.9.0-network-bbolt-v2')
        validate_store_inventory(value)
        decoded = [data for kind, data in observations if kind == 'store-decoded-current-tree' and data['path'] == NETWORK_PATH]
        self.assertEqual(decoded, [{'path': NETWORK_PATH, 'tree': saved_tree()}])
        for factory, validator in ((preflight, validate_preflight), (preflight_v2, validate_preflight_v2)):
            facts = factory(); facts['store'] = copy.deepcopy(value); facts['filesystem'] = copy.deepcopy(value['backing_filesystem'])
            validator(facts)
            facts['filesystem']['uuid'] = '00000000-0000-4000-8000-000000000005'
            with self.assertRaisesRegex(ValueError, 'backing filesystem'): validator(facts)

    def test_bound_host_none_and_empty_tree_keep_v1_network_format_in_v4_inventory(self):
        original = saved_tree()[b'libnetwork']
        for names in ((), ('host',), ('none',), ('host', 'none')):
            ids = {key.split(b'/')[-2] for key, raw in original.items()
                   if b'/v1.0/network/' in key and json.loads(raw[8:])['name'] in names}
            tree = {b'libnetwork': {k: v for k, v in original.items() if k.split(b'/')[-2] in ids}}
            with self.subTest(names=names):
                store._network_metadata(tree)
                self.assertIs(store._network_metadata_v2(tree), False)
                value = scan_synthetic_default_store(tree)
                self.assertEqual(value['schema'], 'switchback.engine.store_inventory.v4')
                entry = next(e for e in value['entries'] if e['path'] == DATA_ROOT + '/' + NETWORK_PATH)
                self.assertEqual((entry['classification'], entry['format']),
                                 ('docker_network_metadata', 'docker-29.9.0-network-bbolt-v1'))
                validate_store_inventory(value)

    def test_three_string_downgrade_and_new_class_path_format_cannot_pass_old_dto(self):
        original = scan_synthetic_default_store()
        # A minimal network-only inventory isolates new class rejection from device/plugin policy.
        paths = {DATA_ROOT, DATA_ROOT + '/network', DATA_ROOT + '/network/files', DATA_ROOT + '/' + NETWORK_PATH}
        original['entries'] = [e for e in original['entries'] if e['path'] in paths]
        validate_store_inventory(original)
        for version in (1, 2, 3):
            value = copy.deepcopy(original); value['schema'] = 'switchback.engine.store_inventory.v%d' % version
            if version == 1: value.pop('backing_filesystem')
            for item in value['entries']:
                item['rule_revision'] = 'm1-engine-store-v%d' % version
                if item['classification'] == 'docker_default_bridge_metadata': item['format'] = 'docker-29.9.0-network-bbolt-v1'
            with self.subTest(version=version), self.assertRaises(ValueError): validate_store_inventory(value)
        for change in (lambda e: e.update(path=e['path'] + '.other'),
                       lambda e: e.update(format='docker-29.9.0-network-bbolt-v1'),
                       lambda e: e.update(classification='docker_network_metadata')):
            value = copy.deepcopy(original); change(value['entries'][-1])
            with self.assertRaises(ValueError): validate_store_inventory(value)

    def test_hidden_active_record_corrupt_latest_metadata_stat_content_enumeration_reject(self):
        tree = saved_tree(); tree[b'libnetwork'][b'docker/network/v1.0/endpoint/' + b'a' * 64 + b'/'] = struct.pack('<Q', 1) + b'{}'
        with self.assertRaises(ValueError): scan_synthetic_default_store(tree)
        def corrupt(stats, payloads):
            path = DATA_ROOT + '/' + NETWORK_PATH
            raw = bytearray(payloads[path]); raw[32768 + 72] ^= 1; payloads[path] = bytes(raw)
        def foreign(stats, payloads): stats[DATA_ROOT + '/' + NETWORK_PATH].st_dev += 1
        for case in ({'mutate': corrupt}, {'mutate': foreign}, {'content_drift': True}, {'enumeration_drift': True}):
            with self.subTest(case=case), self.assertRaises(ValueError): scan_synthetic_default_store(**case)
        # The unbound legacy scanner still rejects the complete bridge, even without devices/plugins.
        def network_only(stats, payloads):
            keep = {DATA_ROOT, DATA_ROOT + '/network', DATA_ROOT + '/network/files', DATA_ROOT + '/' + NETWORK_PATH}
            for path in list(stats):
                if path not in keep: del stats[path]
        with self.assertRaises(ValueError): scan_synthetic_default_store(mutate=network_only, bound=False)
