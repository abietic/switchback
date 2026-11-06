"""Pure/synthetic deployment policy tests. No daemon, guest or network access."""
import copy
import base64
import hashlib
import importlib
import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import sys
import tempfile
import unittest
import zlib
from unittest import mock

from contract import (
    BUDGET_SECONDS, DATA_ROOT, ENGINE_SOCKET, FILES, UNIT_NAMES, _json,
    SHIM_SOCKET_DIR, CONTAINERD_SOCKET, _fixed_toml, _linux_device_number,
    validate_configuration, validate_listeners, validate_preflight,
    validate_receipt, validate_store_inventory,
)
from bundle import build_bundle, verify_bundle

HERE = Path(__file__).parent
ROOT = HERE.parents[2]

# All 56 paths. Raw frame byte/hash and per-entry metadata derivation are
# verified by task-1/fixture-provenance.json. Classification and the DTO wrapper
# are offline derived; this is not a complete payload or runtime scan replay.
PACKET_DTO_ZLIB_BASE64 = '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'


def frozen_derived_inventory():
    raw = zlib.decompress(base64.b64decode(PACKET_DTO_ZLIB_BASE64))
    assert hashlib.sha256(raw).hexdigest() == '79f32e6289443d8382423c73838c5fffca40ea97c1ee49db0c6c82fc639191af'
    return json.loads(raw)


def sha(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


def encode(value):
    return json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode()


def receipt():
    return json.loads((HERE / 'testdata/receipt-v1.json').read_bytes())


def configuration():
    return {name: (HERE / name).read_bytes() for name in FILES}


def inventory():
    return {'schema': 'switchback.engine.store_inventory.v1',
            'root': '/mnt/lima-sbquota/docker',
            'containerd_root': '/mnt/lima-sbquota/docker/containerd',
            'docker_version': '29.9.0', 'containerd_version': '2.4.1',
            'scan_complete': True, 'entries': [],
            'restoration': dict.fromkeys(('containers', 'plugins', 'volumes', 'tasks',
                                         'sandboxes', 'external', 'unknown'), 0)}


def device_inventory():
    value = inventory()
    value['schema'] = 'switchback.engine.store_inventory.v2'
    value['backing_filesystem'] = copy.deepcopy(receipt()['filesystem'])
    root = value['root']
    zero = dict(value['restoration'])
    for path in (root, root + '/overlay2'):
        value['entries'].append({'path': path, 'kind': 'directory', 'uid': 0, 'gid': 0,
            'mode': 0o700, 'nlink': 3, 'bytes': 0, 'sha256': None,
            'classification': 'engine_directory', 'format': 'directory-v1',
            'rule_revision': 'm1-engine-store-v2', 'scan_complete': True,
            'restore_refs': dict(zero)})
    major, minor = value['backing_filesystem']['major'], value['backing_filesystem']['minor']
    device = _linux_device_number(major, minor)
    value['entries'].append({'path': root + '/overlay2/backingFsBlockDev',
        'kind': 'block_device', 'uid': 0, 'gid': 0, 'mode': 0o600, 'nlink': 1,
        'bytes': 0, 'sha256': None, 'classification': 'quota_backing_device',
        'format': 'docker-xfs-quota-backing-device-v1', 'rule_revision': 'm1-engine-store-v2',
        'scan_complete': True, 'restore_refs': dict(zero),
        'device_identity': {'device': device, 'inode': 67240078, 'rdev': device,
                            'major': major, 'minor': minor}})
    return value


def dual_device_inventory():
    """Synthetic v3 DTO; raw packet provenance lives in test_store.py."""
    value = device_inventory()
    value['schema'] = 'switchback.engine.store_inventory.v3'
    for entry in value['entries']:
        entry['rule_revision'] = 'm1-engine-store-v3'
    volume = copy.deepcopy(value['entries'][1])
    volume['path'] = value['root'] + '/volumes'
    value['entries'].append(volume)
    device = copy.deepcopy(value['entries'][2])
    device['path'] = volume['path'] + '/backingFsBlockDev'
    device['device_identity']['inode'] = 72192
    value['entries'].append(device)
    return value


def listeners(phase='engine_ready', version=1):
    r = receipt() if version == 1 else receipt_v2()
    daemons = r['daemons'] if phase == 'engine_ready' else r['daemons'][:1]
    sockets = r['sockets'] if phase == 'engine_ready' else r['sockets'][:2]
    if phase == 'preflight':
        daemons, sockets = [], []
    unix = []
    for i, s in enumerate(sockets):
        daemon = next(d for d in daemons if d['main_pid'] == s['owner_pid'])
        owner = {k: daemon[k] for k in ('main_pid', 'start_tick', 'exe', 'cgroup')}
        owner['fd'] = 10 + i
        unix.append({'path': s['path'], 'inode': s['kernel_inode'], 'listener': True,
                     'owners': [owner]})
    value = {'schema': 'switchback.engine.listeners.v%d' % version, 'phase': phase,
            'boot_id': r['boot_id'], 'current_boot_id': r['boot_id'],
            'complete': dict.fromkeys(('unix', 'tcp', 'tcp6', 'fd_owners', 'processes'), True),
            'daemons': copy.deepcopy(daemons), 'identity_before': copy.deepcopy(daemons),
            'identity_after': copy.deepcopy(daemons), 'sockets': copy.deepcopy(sockets),
            'unix': unix, 'tcp': [], 'tcp6': []}
    if version == 2:
        value['socket_parents'] = copy.deepcopy(r['socket_parents'][:{'preflight': 0,
            'containerd_ready': 2, 'engine_ready': 5}[phase]])
    return value


def receipt_v2():
    return _json((HERE / 'testdata/receipt-v2.json').read_bytes())


def listeners_v2(phase='engine_ready'):
    return listeners(phase, version=2)


def preflight_v2():
    value = preflight()
    value['listeners'] = listeners_v2('preflight')
    return value


def preflight():
    r = receipt()
    return {'schema': 'switchback.engine.preflight.v1', 'boot_id': r['boot_id'],
            'current_boot_id': r['boot_id'], 'vm': {'name': 'switchback-core',
            'vcpus': 4, 'memory_bytes': 10737418240}, 'filesystem': r['filesystem'],
            'core': {'cgroup': '/switchback.slice/switchback-core.slice',
                     'cpu_max': '120000 100000', 'memory_max': 3221225472,
                     'memory_swap_max': 0, 'pids_max': 1024, 'accounted_once': True},
            'masks': {name: {'unit_file_state': 'masked', 'active_state': 'inactive',
                             'main_pid': 0}
                      for name in ('docker.service', 'docker.socket', 'containerd.service')},
            'unit_catalog_complete': True, 'other_units': [], 'process_scan_complete': True,
            'writers': [], 'targets': [{'path': path, 'exists': False,
                'parents_safe': True} for path in (
                '/etc/switchback/m1-engine', '/var/lib/switchback/m1-engine',
                '/run/switchback-engine/m1-resident',
                '/etc/systemd/system/switchback-m1-containerd.service',
                '/etc/systemd/system/switchback-m1-engine.service')],
            'runtime_parent': r['runtime_parent'], 'binaries': [
                {'path': '/usr/bin/containerd', 'package': 'containerd.io',
                 'package_version': '2.4.1-2~ubuntu.24.04~noble', 'version': '2.4.1',
                 'architecture': 'arm64', 'uid': 0, 'gid': 0, 'mode': 493,
                 'nlink': 1, 'sha256': 'sha256:' + 'c' * 64,
                 'package_sha256': 'sha256:c19709945e4a41d27b2d82478d56e77d04ed039894af159c77f7302348cb1935',
                 'pin_verified': True},
                {'path': '/usr/bin/dockerd', 'package': 'docker-ce',
                 'package_version': '5:29.9.0-1~ubuntu.24.04~noble', 'version': '29.9.0',
                 'architecture': 'arm64', 'uid': 0, 'gid': 0, 'mode': 493,
                 'nlink': 1, 'sha256': 'sha256:' + 'd' * 64,
                 'package_sha256': 'sha256:7dcaed8a78cdd64999a2b7bc245bb066f3f6a05aaa7e14e3d164a3504c74b810',
                 'pin_verified': True}],
            'store': inventory(), 'listeners': listeners('preflight')}


def source_record(path, relative):
    raw = path.read_bytes()
    return {'path': relative, 'type': 'regular', 'mode': stat.S_IMODE(path.stat().st_mode),
            'bytes': len(raw), 'digest': sha(raw)}


def tiny_kit(parent):
    kit = parent / 'kit'
    kit.mkdir(mode=0o700)
    source = kit / 'source'
    source.mkdir(mode=0o700)
    for p in HERE.rglob('*'):
        if p.is_file() and '__pycache__' not in p.parts:
            dest = source / 'deploy/experience/engine' / p.relative_to(HERE)
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(p.read_bytes())
            dest.chmod(0o644)
    pins = source / 'deploy/experience/primary-pins.json'
    pins.write_bytes((HERE.parent / 'primary-pins.json').read_bytes())
    for leaf in ('guest.py', 'prepare.py', 'store.py', 'test_guest.py'):
        dest = source / 'deploy/experience/engine' / leaf
        if not dest.exists():
            dest.write_text('# synthetic future task source; never executed\n')
    template = source / 'deploy/experience/qualification/synthetic.py'
    template.parent.mkdir(parents=True)
    template.write_text('# synthetic qualification template\n')
    records = [source_record(p, str(p.relative_to(source)))
               for p in sorted(source.rglob('*')) if p.is_file()]
    sources = [r for r in records if '/qualification/' not in r['path']]
    templates = [r for r in records if '/qualification/' in r['path']]
    for arch in ('arm64', 'amd64'):
        (kit / ('kit-inputs-' + arch + '.json')).write_bytes(encode({
            'schema': 'switchback.qualification.kit_inputs.v1', 'architecture': arch,
            'toolchain': 'go1.26.4', 'source': sources, 'artifacts': [], 'templates': templates}))
    (kit / 'build-receipt.json').write_bytes(encode({
        'go': {'synthetic': True}, 'source_input_root': sha(encode(sources)),
        'qualification_templates_root': sha(encode(templates)), 'before': sources,
        'after': sources, 'templates_before': templates, 'templates_after': templates,
        'binaries': []}))
    rewrite_manifest(kit)
    return kit


def rewrite_manifest(kit):
    inputs = json.loads((kit / 'kit-inputs-arm64.json').read_bytes())
    files = [source_record(p, str(p.relative_to(kit))) for p in sorted(kit.rglob('*'))
             if p.is_file() and p.name != 'manifest.json']
    value = {'schema': 'switchback.qualification.offline_kit.v1',
             'runtime_qualification': False, 'toolchain': 'go1.26.4',
             'source_input_root': sha(encode(inputs['source'])),
             'qualification_templates_root': sha(encode(inputs['templates'])),
             'files': files, 'unresolved_runtime': dict.fromkeys((
                 'image_ids', 'container_ids', 'guest_boot', 'host_boot', 'public_identity',
                 'host_volume_readback', 'engine_identity', 'native_receipt', 'host_receipt',
                 'trust', 'manifest', 'journal', 'pg_preparation'))}
    (kit / 'manifest.json').write_bytes(encode(value))


class ConfigurationTests(unittest.TestCase):
    def test_import_has_no_execution_or_network_effects(self):
        with (mock.patch.object(socket, 'socket', side_effect=AssertionError('socket')),
              mock.patch.object(subprocess, 'run', side_effect=AssertionError('run')),
              mock.patch.object(subprocess, 'Popen', side_effect=AssertionError('Popen'))):
            importlib.reload(sys.modules['contract'])
            importlib.reload(sys.modules['bundle'])


    def test_fixed_configuration_is_effective(self):
        validate_configuration(configuration())
        self.assertEqual(BUDGET_SECONDS, 120)
        self.assertEqual(ENGINE_SOCKET, 'unix:///run/switchback-engine/docker.sock')
        self.assertEqual(UNIT_NAMES, ('switchback-m1-containerd.service', 'switchback-m1-engine.service'))

    def test_default_bridge_is_daemon_managed_without_precreated_interface(self):
        files = configuration()
        daemon = _json(files['daemon.json'])
        self.assertNotIn('bridge', daemon)
        validate_configuration(files)
        daemon['bridge'] = 'docker0'
        files['daemon.json'] = encode(daemon)
        with self.assertRaises(ValueError): validate_configuration(files)

    def test_configuration_rejects_extra_and_missing_files(self):
        for name in FILES:
            f = configuration(); del f[name]
            with self.assertRaises(ValueError): validate_configuration(f)
        f = configuration(); f['extra'] = b'x'
        with self.assertRaises(ValueError): validate_configuration(f)

    def test_json_duplicates_malformed_and_authority_are_rejected(self):
        for raw in (b'{', b'{"a":1,"a":2}', b'{"a":NaN}', b'[]'):
            with self.assertRaises(ValueError): _json(raw)

    def test_json_exponent_overflow_is_rejected(self):
        for number in (b'1e9999', b'-1e9999', b'1.234e9999'):
            with self.subTest(number=number), self.assertRaises(ValueError):
                _json(b'{"value":' + number + b'}')
        self.assertEqual(_json(b'{"value":1e308}')['value'], 1e308)

    def test_shim_actual_address_derivation_fits_unix_limit(self):
        config = _fixed_toml(configuration()['containerd.toml'])
        folder = config['plugins']['io.containerd.shim.v1.manager']['socket_dir']
        self.assertEqual(folder, SHIM_SOCKET_DIR)
        # Pinned CreateSocketAddress hashes join(socketPath, namespace, id[,debug]).
        for namespace in ('moby', 'plugins.moby'):
            for debug in (False, True):
                joined = '/'.join((CONTAINERD_SOCKET, namespace, 'f' * 64))
                if debug: joined += '/debug'
                address = folder + '/' + hashlib.sha256(joined.encode()).hexdigest()
                self.assertLessEqual(len(address.encode()), 106)
                self.assertLessEqual(len(address.encode()) + 1, 108)

    def test_duplicate_json_ini_and_toml_properties_rejected(self):
        for name, suffix in (('daemon.json', None), ('containerd.toml', b'\nenv = []\n'),
                             (UNIT_NAMES[0], b'\nRestart=no\n')):
            f = configuration()
            f[name] = (f[name].replace(b'{', b'{"iptables":true,', 1)
                       if suffix is None else f[name] + suffix)
            with self.assertRaises(ValueError): validate_configuration(f)

    def test_network_socket_lifecycle_and_resource_drift_rejected(self):
        for name, old, new in (
            ('daemon.json', b'"iptables": true', b'"iptables": false'),
            ('daemon.json', b'"overlay2"', b'"overlayfs"'),
            ('switchback-m1-engine.service', b'Requisite=', b'Requires='),
            ('switchback-m1-engine.service', b'Restart=no', b'Restart=always'),
            ('switchback-m1-engine.service', b'--group=root', b'--group=docker'),
            ('containerd.toml', b'imports = []', b'imports = ["/etc/containerd/*.toml"]'),
            ('containerd.toml', b'/m1-resident/s"', b'/m1-resident/containerd/shim"')):
            f = configuration(); self.assertIn(old, f[name]); f[name] = f[name].replace(old, new)
            with self.subTest(name=name, old=old), self.assertRaises(ValueError):
                validate_configuration(f)


class Task2IntegrationContractTests(unittest.TestCase):
    def test_pid1_zero_tick_owner_is_a_valid_complete_inventory(self):
        value = listeners()
        value['unix'].append({'path': '/run/systemd/private', 'inode': 900, 'listener': True,
            'owners': [{'main_pid': 1, 'start_tick': 0, 'exe': '/usr/lib/systemd/systemd',
                        'cgroup': '/init.scope', 'fd': 7}]})
        validate_listeners(value)
        for pid, tick in ((2, 0), (1, -1), (2, -1)):
            rejected = copy.deepcopy(value)
            rejected['unix'][-1]['owners'][0].update(main_pid=pid, start_tick=tick)
            with self.subTest(pid=pid, tick=tick), self.assertRaises(ValueError):
                validate_listeners(rejected)

    def test_approved_daemon_nonlistener_sockets_do_not_invent_peer_pairing(self):
        value = listeners()
        for index, path in ((0, value['sockets'][0]['path']), (1, '')):
            daemon = value['daemons'][index]
            owner = {key: daemon[key] for key in ('main_pid', 'start_tick', 'exe', 'cgroup')}
            owner['fd'] = 90 + index
            value['unix'].append({'path': path, 'inode': 950 + index, 'listener': False, 'owners': [owner]})
        validate_listeners(value)
        mutations = (
            lambda row: row['owners'][0].update(main_pid=999),
            lambda row: row['owners'].append({'main_pid': 999, 'start_tick': 99,
                'exe': '/usr/bin/foreign', 'cgroup': '/foreign.scope', 'fd': 98}),
            lambda row: row.update(path='/run/foreign.sock'),
            lambda row: row.update(listener=True))
        for mutate in mutations:
            rejected = copy.deepcopy(value)
            mutate(rejected['unix'][-1])
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                validate_listeners(rejected)

    def test_graphdriver_initializes_two_additional_empty_buildkit_indexes(self):
        for leaf in ('containerdmeta.db', 'snapshots.db'):
            value = inventory()
            for path in (value['root'], value['root'] + '/buildkit'):
                value['entries'].append({'path': path, 'kind': 'directory', 'uid': 0, 'gid': 0,
                    'mode': 0o700, 'nlink': 2, 'bytes': 0, 'sha256': None,
                    'classification': 'engine_directory', 'format': 'directory-v1',
                    'rule_revision': 'm1-engine-store-v1', 'scan_complete': True,
                    'restore_refs': copy.deepcopy(value['restoration'])})
            value['entries'].append({'path': value['root'] + '/buildkit/' + leaf, 'kind': 'regular',
                'uid': 0, 'gid': 0, 'mode': 0o600, 'nlink': 1, 'bytes': 24576, 'sha256': 'sha256:' + 'a' * 64,
                'classification': 'buildkit_empty_index', 'format': 'docker-29.9.0-buildkit-empty-bbolt-v1',
                'rule_revision': 'm1-engine-store-v1', 'scan_complete': True,
                'restore_refs': copy.deepcopy(value['restoration'])})
            validate_store_inventory(value)

    def test_historical_bootstrap_inputs_require_exact_path_and_digest(self):
        value = inventory()
        value['entries'] = [{'path': value['root'], 'kind': 'directory', 'uid': 0, 'gid': 0,
            'mode': 0o700, 'nlink': 2, 'bytes': 0, 'sha256': None, 'classification': 'engine_directory',
            'format': 'directory-v1', 'rule_revision': 'm1-engine-store-v1', 'scan_complete': True,
            'restore_refs': copy.deepcopy(value['restoration'])},
            {'path': value['root'] + '/.sb-proof-static-1013.bin', 'kind': 'regular', 'uid': 0, 'gid': 0,
            'mode': 0o600, 'nlink': 1, 'bytes': 1 << 20,
            'sha256': 'sha256:bf63d8a95fcc2e64619813aae35fdcbe871fdd9264caa3f365eb3aed0f679129',
            'classification': 'retained_bootstrap_input', 'format': 'switchback-retained-bootstrap-sha256-v1',
            'rule_revision': 'm1-engine-store-v1', 'scan_complete': True,
            'restore_refs': copy.deepcopy(value['restoration'])}]
        validate_store_inventory(value)
        for mutate in (lambda leaf: leaf.update(sha256='sha256:' + 'a' * 64),
                       lambda leaf: leaf.update(path=value['root'] + '/unknown-bootstrap.deb')):
            rejected = copy.deepcopy(value)
            mutate(rejected['entries'][-1])
            with self.assertRaises(ValueError):
                validate_store_inventory(rejected)



class ReceiptTests(unittest.TestCase):
    def test_shared_fixture_is_valid(self): validate_receipt(receipt())

    def test_receipt_does_not_grant_start(self):
        for field in ('runtime_qualification', 'capacity_ready', 'execution_enabled', 'paid_consumed'):
            value = receipt(); value[field] = True
            with self.subTest(field=field), self.assertRaises(ValueError): validate_receipt(value)

    def test_extra_missing_type_and_bound_rejected(self):
        for mutate in (lambda r: r.update(extra=True), lambda r: r.pop('status'),
                       lambda r: r.update(elapsed_ms=True), lambda r: r.update(elapsed_ms=120001),
                       lambda r: r['steps'][0].update(stdout='x' * 16385),
                       lambda r: r.update(unknown_effects=['unknown-start'])):
            value = receipt(); mutate(value)
            with self.assertRaises(ValueError): validate_receipt(value)

    def test_cross_field_identity_path_and_step_drift_rejected(self):
        for mutate in (lambda r: r['daemons'][1].update(boot_id='00000000-0000-4000-8000-000000000002'),
                       lambda r: r['sockets'][2].update(owner_pid=1),
                       lambda r: r['installed_files'][0].update(target_path='/etc/docker/daemon.json'),
                       lambda r: r['networks'][0].update(internal=False),
                       lambda r: r['steps'].reverse(),
                       lambda r: r['steps'][0].update(exit_code=1)):
            value = receipt(); mutate(value)
            with self.assertRaises(ValueError): validate_receipt(value)

    def test_failed_receipt_retains_actual_partial_results(self):
        value = receipt()
        value.update(status='failed', boot_id=None, filesystem=None, runtime_parent=None,
                     daemons=[], installed_files=[], sockets=[], engine=None, networks=[],
                     steps=value['steps'][:1], known_effects=[], unknown_effects=[], elapsed_ms=123)
        value['steps'][0].update(status='failed', finished_ms=123, exit_code=17,
                                  stderr='exact synthetic preflight failure')
        validate_receipt(value)

    def test_unknown_receipt_retains_possible_start_effect(self):
        value = receipt(); value['steps'] = value['steps'][:6]
        value.update(status='unknown', daemons=[], sockets=[], engine=None, networks=[], elapsed_ms=600)
        value['steps'][-1].update(status='unknown', exit_code=None,
                                  known_effects=[], unknown_effects=['containerd_start_result_unknown'])
        value['known_effects'] = [e for s in value['steps'] for e in s['known_effects']]
        value['unknown_effects'] = ['containerd_start_result_unknown']
        validate_receipt(value)

    def test_receipt_rejects_conflicting_shared_socket_parent_identity(self):
        for key in ('parent_inode', 'parent_device'):
            value = receipt(); value['sockets'][1][key] += 1
            with self.subTest(key=key), self.assertRaises(ValueError): validate_receipt(value)

    def test_late_failed_and_unknown_receipts_retain_actual_monotonic_times(self):
        for status in ('failed', 'unknown'):
            value = receipt(); value['steps'] = value['steps'][:6]
            value.update(status=status, daemons=[], sockets=[], engine=None, networks=[], elapsed_ms=120037)
            value['steps'][-1].update(status=status, started_ms=119999, finished_ms=120037,
                exit_code=17 if status == 'failed' else None,
                stdout='bounded raw observation after execution deadline', known_effects=[],
                unknown_effects=['containerd_start_result_unknown'])
            value['known_effects'] = [e for s in value['steps'] for e in s['known_effects']]
            value['unknown_effects'] = ['containerd_start_result_unknown']
            with self.subTest(status=status):
                validate_receipt(value)
                self.assertEqual(value['elapsed_ms'], 120037)
                self.assertEqual(value['steps'][-1]['finished_ms'], 120037)
                self.assertEqual(value['steps'][-1]['exit_code'], 17 if status == 'failed' else None)
                for later in (120001, (1 << 63) - 1):
                    observed = copy.deepcopy(value)
                    observed['elapsed_ms'] = observed['steps'][-1]['finished_ms'] = later
                    validate_receipt(observed)
                    self.assertEqual(observed['steps'][-1]['finished_ms'], later)
                for bad in (True, 120037.0, -1, 1 << 63):
                    drift = copy.deepcopy(value); drift['elapsed_ms'] = bad
                    with self.assertRaises(ValueError): validate_receipt(drift)
                drift = copy.deepcopy(value); drift['steps'][-1]['finished_ms'] = 119998
                with self.assertRaises(ValueError): validate_receipt(drift)
                drift = copy.deepcopy(value); drift['steps'][-1]['stdout'] = 'x' * 16385
                with self.assertRaises(ValueError): validate_receipt(drift)
        ready = receipt(); ready['elapsed_ms'] = 120001
        with self.assertRaises(ValueError): validate_receipt(ready)

    def test_every_public_validator_rejects_malformed_container_types(self):
        for function, factory, key in ((validate_receipt, receipt, 'sockets'),
                (validate_store_inventory, inventory, 'entries'),
                (validate_listeners, listeners, 'daemons'),
                (validate_preflight, preflight, 'targets')):
            for invalid in (None, 1, 'x', {'unexpected': 'mapping'}):
                value = factory(); value[key] = invalid
                with self.subTest(validator=function.__name__, invalid=invalid), self.assertRaises(ValueError):
                    function(value)


class PreflightTests(unittest.TestCase):
    def test_complete_preflight(self): validate_preflight(preflight())

    def test_mask_core_boot_writers_targets_and_owner_rejected(self):
        for mutate in (lambda r: r['masks']['docker.socket'].update(unit_file_state='enabled'),
                       lambda r: r['core'].update(memory_max=4294967296),
                       lambda r: r.update(current_boot_id='00000000-0000-4000-8000-000000000003'),
                       lambda r: r['writers'].append({'exe': '/usr/bin/dockerd'}),
                       lambda r: r['targets'][0].update(exists=True),
                       lambda r: r['targets'][0].update(parents_safe=False),
                       lambda r: r['runtime_parent'].update(uid=501),
                       lambda r: r['runtime_parent'].update(inodes=2048),
                       lambda r: r['binaries'][0].update(nlink=2),
                       lambda r: r.update(unit_catalog_complete=False)):
            value = preflight(); mutate(value)
            with self.assertRaises(ValueError): validate_preflight(value)


class StoreTests(unittest.TestCase):
    def test_frozen_56_path_derived_v3_inventory(self):
        value = frozen_derived_inventory()
        self.assertEqual(len(value['entries']), 56)
        validate_store_inventory(copy.deepcopy(value))

    def test_v3_empty_plugin_directories_are_optional(self):
        for included in ((), ('storage',), ('tmp',), ('storage', 'tmp')):
            with self.subTest(included=included):
                value = frozen_derived_inventory()
                value['entries'] = [entry for entry in value['entries']
                    if not entry['path'].startswith(DATA_ROOT + '/plugins/')
                    or entry['path'].rsplit('/', 1)[-1] in included]
                if not included:
                    value['entries'] = [entry for entry in value['entries']
                                        if entry['path'] != DATA_ROOT + '/plugins']
                else:
                    parent = next(entry for entry in value['entries']
                                  if entry['path'] == DATA_ROOT + '/plugins')
                    parent['nlink'] = 2 + len(included)
                validate_store_inventory(value)

    def test_v1_v2_keep_rejecting_plugin_subdirectories(self):
        derived = frozen_derived_inventory()
        parent = next(entry for entry in derived['entries']
                      if entry['path'] == DATA_ROOT + '/plugins')
        child = next(entry for entry in derived['entries']
                     if entry['path'] == DATA_ROOT + '/plugins/storage')
        for schema, value, revision in (
            ('v1', inventory(), 'm1-engine-store-v1'),
            ('v2', device_inventory(), 'm1-engine-store-v2'),
        ):
            with self.subTest(schema=schema):
                if schema == 'v1':
                    root = copy.deepcopy(derived['entries'][0])
                    root['rule_revision'] = revision
                    value['entries'].append(root)
                for entry in (parent, child):
                    added = copy.deepcopy(entry)
                    added['rule_revision'] = revision
                    if added['path'] == parent['path']:
                        added['nlink'] = 3
                    value['entries'].append(added)
                with self.assertRaisesRegex(ValueError, child['path']):
                    validate_store_inventory(value)

    def test_v3_plugin_directory_exact_metadata_and_coverage(self):
        for suffix in ('storage', 'tmp'):
            path = DATA_ROOT + '/plugins/' + suffix
            changes = {
                'kind': lambda e: e.update(kind='regular'),
                'uid': lambda e: e.update(uid=1),
                'bool-uid': lambda e: e.update(uid=False),
                'gid': lambda e: e.update(gid=1),
                'bool-gid': lambda e: e.update(gid=False),
                'mode': lambda e: e.update(mode=0o711),
                'bool-mode': lambda e: e.update(mode=True),
                'nlink': lambda e: e.update(nlink=3),
                'bool-nlink': lambda e: e.update(nlink=True),
                'bytes': lambda e: e.update(bytes=1),
                'bool-bytes': lambda e: e.update(bytes=False),
                'sha256': lambda e: e.update(sha256='sha256:' + 'a' * 64),
                'class': lambda e: e.update(classification='engine_directory'),
                'format': lambda e: e.update(format='unknown'),
                'incomplete': lambda e: e.update(scan_complete=False),
                'rule': lambda e: e.update(rule_revision='m1-engine-store-v2'),
                'refs': lambda e: e['restore_refs'].update(plugins=1),
            }
            for label, change in changes.items():
                with self.subTest(path=path, label=label):
                    value = frozen_derived_inventory()
                    entry = next(item for item in value['entries'] if item['path'] == path)
                    change(entry)
                    with self.assertRaisesRegex(ValueError, path):
                        validate_store_inventory(value)
        for suffix in ('/plugins/storage/child', '/plugins/tmp/nested/child',
                       '/plugins/other', '/plugins/storage-backup', '/plugins/a-real-id'):
            with self.subTest(suffix=suffix):
                value = frozen_derived_inventory()
                entry = copy.deepcopy(next(item for item in value['entries']
                    if item['path'] == DATA_ROOT + '/plugins/storage'))
                entry['path'] = DATA_ROOT + suffix
                value['entries'].append(entry)
                with self.assertRaisesRegex(ValueError, entry['path']):
                    validate_store_inventory(value)
        value = frozen_derived_inventory()
        value['entries'].append(copy.deepcopy(next(item for item in value['entries']
            if item['path'] == DATA_ROOT + '/plugins/storage')))
        with self.assertRaisesRegex(ValueError, DATA_ROOT + '/plugins/storage'):
            validate_store_inventory(value)
        value = frozen_derived_inventory()
        value['entries'] = [entry for entry in value['entries']
                            if entry['path'] != DATA_ROOT + '/plugins']
        with self.assertRaisesRegex(ValueError, DATA_ROOT + '/plugins/'):
            validate_store_inventory(value)

    def test_empty_store(self): validate_store_inventory(inventory())

    def test_fixed_device_v2_and_linux_device_encoding(self):
        value = device_inventory()
        validate_store_inventory(value)
        no_device = copy.deepcopy(value)
        no_device['entries'] = [no_device['entries'][0]]
        no_device['entries'][0]['classification'] = 'empty_directory'
        validate_store_inventory(no_device)
        self.assertEqual(_linux_device_number(253, 17), 64785)
        self.assertEqual(_linux_device_number(0x12345, 0x23456), 0x1200023434556)
        for major, minor in ((True, 1), (1, False), (-1, 1), (1 << 32, 1)):
            with self.subTest(major=major, minor=minor), self.assertRaises(ValueError):
                _linux_device_number(major, minor)

    def test_v3_two_fixed_devices_and_closed_version_matrix(self):
        value = dual_device_inventory()
        validate_store_inventory(value)
        for keep in ((), ('overlay2',), ('volumes',)):
            subset = copy.deepcopy(value)
            subset['entries'] = [e for e in subset['entries'] if e['path'] == subset['root']
                or any(e['path'].startswith(subset['root'] + '/' + name) for name in keep)]
            subset['entries'][0]['classification'] = ('engine_directory' if keep else 'empty_directory')
            validate_store_inventory(subset)
        v2 = device_inventory()
        validate_store_inventory(v2)
        v2['entries'].extend(copy.deepcopy(value['entries'][-2:]))
        for e in v2['entries'][-2:]: e['rule_revision'] = 'm1-engine-store-v2'
        with self.assertRaises(ValueError): validate_store_inventory(v2)
        v1 = inventory()
        v1['entries'] = copy.deepcopy(value['entries'])
        for e in v1['entries']: e['rule_revision'] = 'm1-engine-store-v1'
        with self.assertRaises(ValueError): validate_store_inventory(v1)

    def test_v3_rejects_unknown_schema_fields_rules_paths_and_identity(self):
        target = '/mnt/lima-sbquota/docker/volumes/backingFsBlockDev'
        cases = (
            lambda v: v.update(schema='switchback.engine.store_inventory.v5'),
            lambda v: v.update(extra=True),
            lambda v: v.pop('backing_filesystem'),
            lambda v: v['backing_filesystem'].update(extra=True),
            lambda v: v['entries'][-1].update(path=target + '/nested'),
            lambda v: v['entries'][-1].update(path=target.replace('volumes/', 'other/')),
            lambda v: v['entries'].append(copy.deepcopy(v['entries'][-1])),
            lambda v: v['entries'][-1].update(extra=True),
            lambda v: v['entries'][-1].update(rule_revision='m1-engine-store-v2'),
            lambda v: v['entries'][0].update(rule_revision='m1-engine-store-v2'),
            lambda v: v['entries'][-1]['device_identity'].update(inode=True),
            lambda v: v['entries'][-1]['device_identity'].update(rdev=False),
            lambda v: v['entries'][-1]['device_identity'].update(rdev=64786),
            lambda v: v['entries'][-1]['device_identity'].update(
                major=v['backing_filesystem']['major'] + 1),
            lambda v: v['entries'][-1].update(restore_refs={**v['restoration'], 'volumes': 1}),
            lambda v: v['entries'][2].update(classification='empty_directory'),
            lambda v: v['entries'].__delitem__(2),
            lambda v: v['entries'].__delitem__(3),
        )
        for index, change in enumerate(cases):
            value = dual_device_inventory(); change(value)
            with self.subTest(index=index), self.assertRaises(ValueError):
                validate_store_inventory(value)

    def test_v4_retains_v3_devices_and_empty_plugin_closed_policy(self):
        value = frozen_derived_inventory()
        value['schema'] = 'switchback.engine.store_inventory.v4'
        for entry in value['entries']:
            entry['rule_revision'] = 'm1-engine-store-v4'
        validate_store_inventory(value)
        for suffix in ('overlay2/backingFsBlockDev', 'volumes/backingFsBlockDev',
                       'plugins/storage', 'plugins/tmp'):
            for field, invalid in (('uid', True), ('gid', 1), ('mode', 0o777),
                                   ('nlink', True), ('rule_revision', 'm1-engine-store-v3')):
                changed = copy.deepcopy(value)
                entry = next(e for e in changed['entries'] if e['path'] == DATA_ROOT + '/' + suffix)
                entry[field] = invalid
                with self.subTest(path=suffix, field=field), self.assertRaises(ValueError):
                    validate_store_inventory(changed)
        changed = copy.deepcopy(value)
        entry = next(e for e in changed['entries'] if e['path'].endswith('/plugins/storage'))
        child = copy.deepcopy(entry); child['path'] += '/hidden'; changed['entries'].append(child)
        with self.assertRaises(ValueError): validate_store_inventory(changed)
        changed = copy.deepcopy(value)
        entry = next(e for e in changed['entries'] if e['path'].endswith('/volumes/backingFsBlockDev'))
        entry['path'] = DATA_ROOT + '/backingFsBlockDev'
        with self.assertRaises(ValueError): validate_store_inventory(changed)

    def test_preflight_v3_binds_complete_filesystem(self):
        value = preflight()
        value['store'] = dual_device_inventory()
        value['store']['backing_filesystem'] = copy.deepcopy(value['filesystem'])
        dev = _linux_device_number(value['filesystem']['major'], value['filesystem']['minor'])
        for entry in value['store']['entries']:
            if entry['kind'] == 'block_device':
                entry['device_identity'].update(device=dev, rdev=dev,
                    major=value['filesystem']['major'], minor=value['filesystem']['minor'])
        validate_preflight(value)
        value['store']['backing_filesystem']['uuid'] = '00000000-0000-4000-8000-000000000005'
        with self.assertRaises(ValueError): validate_preflight(value)

    def test_device_v2_closed_shape_identity_and_version_rejection(self):
        mutations = (
            lambda v: v['entries'][-1].update(path=v['root'] + '/backingFsBlockDev'),
            lambda v: v['entries'][-1].update(kind='regular'),
            lambda v: v['entries'][-1].update(uid=1),
            lambda v: v['entries'][-1].update(gid=1),
            lambda v: v['entries'][-1].update(mode=0o660),
            lambda v: v['entries'][-1].update(nlink=2),
            lambda v: v['entries'][-1].update(bytes=1),
            lambda v: v['entries'][-1].update(sha256='sha256:' + 'a' * 64),
            lambda v: v['entries'][-1].update(classification='content_cache'),
            lambda v: v['entries'][-1].update(format='block-device-v0'),
            lambda v: v['entries'][-1].update(extra=True),
            lambda v: v['entries'][-1]['device_identity'].update(device=64786),
            lambda v: v['entries'][-1]['device_identity'].update(device=True),
            lambda v: v['entries'][-1]['device_identity'].update(rdev=64786),
            lambda v: v['entries'][-1]['device_identity'].update(rdev=True),
            lambda v: v['entries'][-1]['device_identity'].update(inode=0),
            lambda v: v['entries'][-1]['device_identity'].update(major=True),
            lambda v: v['entries'][-1]['device_identity'].update(extra=True),
            lambda v: v['entries'][0].update(rule_revision='m1-engine-store-v1'),
            lambda v: v['entries'][-1].update(rule_revision='m1-engine-store-v1'),
            lambda v: v.update(schema='switchback.engine.store_inventory.v1'),
            lambda v: v.update(extra=True),
            lambda v: v['backing_filesystem'].update(major=9),
        )
        for index, mutate in enumerate(mutations):
            value = device_inventory(); mutate(value)
            with self.subTest(index=index), self.assertRaises(ValueError):
                validate_store_inventory(value)
        old = inventory()
        old['entries'] = [copy.deepcopy(device_inventory()['entries'][-1])]
        old['entries'][0]['rule_revision'] = 'm1-engine-store-v1'
        with self.assertRaises(ValueError): validate_store_inventory(old)
        empty = device_inventory(); empty['entries'] = []
        with self.assertRaises(ValueError): validate_store_inventory(empty)

    def test_preflight_v2_binds_the_same_filesystem(self):
        value = preflight()
        value['store'] = device_inventory()
        value['store']['backing_filesystem'] = copy.deepcopy(value['filesystem'])
        dev = _linux_device_number(value['filesystem']['major'], value['filesystem']['minor'])
        value['store']['entries'][-1]['device_identity'].update(device=dev, rdev=dev,
            major=value['filesystem']['major'], minor=value['filesystem']['minor'])
        validate_preflight(value)
        value['store']['backing_filesystem']['uuid'] = '00000000-0000-4000-8000-000000000005'
        with self.assertRaises(ValueError): validate_preflight(value)

    def test_classified_initialized_containerd_metadata(self):
        value = inventory()
        value['entries'] = [{'path': '/mnt/lima-sbquota/docker/containerd/io.containerd.metadata.v1.bolt/meta.db',
                            'kind': 'regular', 'uid': 0, 'gid': 0, 'mode': 384, 'nlink': 1,
                            'bytes': 32768, 'sha256': 'sha256:' + 'e' * 64,
                            'classification': 'containerd_empty_metadata',
                            'format': 'containerd-2.4.1-bbolt-v4',
                            'rule_revision': 'm1-engine-store-v1', 'scan_complete': True,
                            'restore_refs': copy.deepcopy(value['restoration'])}]
        # A complete inventory includes every ancestor within the fixed data root.
        for path in ('/mnt/lima-sbquota/docker', '/mnt/lima-sbquota/docker/containerd',
                     '/mnt/lima-sbquota/docker/containerd/io.containerd.metadata.v1.bolt'):
            value['entries'].append({'path': path, 'kind': 'directory', 'uid': 0, 'gid': 0,
                'mode': 448, 'nlink': 2, 'bytes': 0, 'sha256': None,
                'classification': 'engine_directory', 'format': 'directory-v1',
                'rule_revision': 'm1-engine-store-v1', 'scan_complete': True,
                'restore_refs': copy.deepcopy(value['restoration'])})
        validate_store_inventory(value)
        for field, replacement in (('scan_complete', False), ('sha256', None),
                                   ('classification', 'trusted'), ('nlink', 2)):
            drift = copy.deepcopy(value); drift['entries'][0][field] = replacement
            with self.assertRaises(ValueError): validate_store_inventory(drift)

    def test_restore_inputs_unknown_links_escape_and_incomplete_rejected(self):
        for field in inventory()['restoration']:
            value = inventory(); value['restoration'][field] = 1
            with self.subTest(field=field), self.assertRaises(ValueError): validate_store_inventory(value)
        value = inventory(); value['scan_complete'] = False
        with self.assertRaises(ValueError): validate_store_inventory(value)
        value = inventory(); value['entries'] = [{
            'path': '/mnt/lima-sbquota/docker/volumes/unknown-business-volume',
            'kind': 'directory', 'uid': 0, 'gid': 0, 'mode': 448, 'nlink': 2,
            'bytes': 0, 'sha256': None, 'classification': 'empty_directory',
            'format': 'directory-v1', 'rule_revision': 'm1-engine-store-v1',
            'scan_complete': True, 'restore_refs': dict(value['restoration'])}]
        with self.assertRaises(ValueError): validate_store_inventory(value)

    def test_directory_claims_must_match_complete_children(self):
        value = inventory()
        zero = value['restoration']
        def directory(path, classification):
            return {'path': path, 'kind': 'directory', 'uid': 0, 'gid': 0, 'mode': 448,
                    'nlink': 2, 'bytes': 0, 'sha256': None, 'classification': classification,
                    'format': 'directory-v1', 'rule_revision': 'm1-engine-store-v1',
                    'scan_complete': True, 'restore_refs': dict(zero)}
        value['entries'] = [directory('/mnt/lima-sbquota/docker', 'engine_directory'),
                            directory('/mnt/lima-sbquota/docker/containerd', 'empty_directory')]
        validate_store_inventory(value)
        value['entries'][0]['classification'] = 'empty_directory'
        with self.assertRaises(ValueError): validate_store_inventory(value)
        value['entries'].pop(0)
        with self.assertRaises(ValueError): validate_store_inventory(value)

    def test_initialized_empty_volume_index_requires_full_zero_reference_scan(self):
        value = inventory(); zero = value['restoration']
        value['entries'] = [{'path': '/mnt/lima-sbquota/docker/volumes/metadata.db',
            'kind': 'regular', 'uid': 0, 'gid': 0, 'mode': 384, 'nlink': 1, 'bytes': 32768,
            'sha256': 'sha256:' + 'e' * 64, 'classification': 'docker_empty_volume_index',
            'format': 'docker-29.9.0-volume-bbolt-v1', 'rule_revision': 'm1-engine-store-v1',
            'scan_complete': True, 'restore_refs': dict(zero)}]
        for path, classification in (('/mnt/lima-sbquota/docker', 'engine_directory'),
                ('/mnt/lima-sbquota/docker/volumes', 'engine_directory')):
            value['entries'].append({'path': path, 'kind': 'directory', 'uid': 0, 'gid': 0,
                'mode': 448, 'nlink': 3, 'bytes': 0, 'sha256': None, 'classification': classification,
                'format': 'directory-v1', 'rule_revision': 'm1-engine-store-v1',
                'scan_complete': True, 'restore_refs': dict(zero)})
        validate_store_inventory(value)
        for mutate in (lambda r: r['entries'][0]['restore_refs'].update(volumes=1),
                       lambda r: r['entries'][0]['restore_refs'].update(unknown=1),
                       lambda r: r['entries'][0].update(scan_complete=False)):
            bad = copy.deepcopy(value); mutate(bad)
            with self.assertRaises(ValueError): validate_store_inventory(bad)


class ListenerTests(unittest.TestCase):
    def test_all_three_phases(self):
        for phase in ('preflight', 'containerd_ready', 'engine_ready'):
            validate_listeners(listeners(phase))

    def test_no_api_fact_can_replace_preconnection_proof(self):
        value = listeners(); value['complete']['fd_owners'] = False; value['api_healthy'] = True
        with self.assertRaises(ValueError): validate_listeners(value)

    def test_hidden_tcp_ttrpc_owner_and_pid_drift_rejected(self):
        for mutate in (lambda r: r['tcp'].append({'address': '0.0.0.0', 'port': 2375,
                           'inode': 444, 'owners': [r['unix'][2]['owners'][0]]}),
                       lambda r: r['unix'].append(dict(r['unix'][0], path='/run/containerd/hidden.ttrpc', inode=455)),
                       lambda r: r['unix'][0]['owners'].append(dict(r['unix'][0]['owners'][0], main_pid=1)),
                       lambda r: r['identity_after'][0].update(start_tick=999),
                       lambda r: r['unix'][0]['owners'][0].update(cgroup='/system.slice/docker.service'),
                       lambda r: r['sockets'][2].update(mode=438),
                       lambda r: r['complete'].update(tcp6=False)):
            value = listeners(); mutate(value)
            with self.assertRaises(ValueError): validate_listeners(value)

    def test_cross_socket_fd_cannot_refer_to_two_kernel_inodes(self):
        value = listeners()
        value['unix'][1]['owners'][0]['fd'] = value['unix'][0]['owners'][0]['fd']
        with self.assertRaises(ValueError): validate_listeners(value)

    def test_same_numeric_fd_in_distinct_processes_is_valid(self):
        value = listeners()
        value['unix'][2]['owners'][0]['fd'] = value['unix'][0]['owners'][0]['fd']
        validate_listeners(value)

    def test_global_fd_consistency_spans_unrelated_listener_families(self):
        value = listeners()
        owner = {'main_pid': 555, 'start_tick': 2345, 'exe': '/usr/bin/sshd',
                 'cgroup': '/system.slice/sshd.service', 'fd': 7}
        value['unix'].append({'path': '/run/sshd-synthetic.sock', 'inode': 999,
                              'listener': True, 'owners': [dict(owner)]})
        value['tcp6'].append({'address': '::1', 'port': 2222, 'inode': 1000,
                             'owners': [dict(owner)]})
        with self.assertRaises(ValueError): validate_listeners(value)

    def test_listener_rejects_conflicting_shared_socket_parent_identity(self):
        for key in ('parent_inode', 'parent_device'):
            value = listeners(); value['sockets'][1][key] += 1
            with self.subTest(key=key), self.assertRaises(ValueError): validate_listeners(value)


class BundleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve(); self.kit = tiny_kit(self.root)

    def test_exclusive_acyclic_round_trip(self):
        before = (self.kit / 'manifest.json').read_bytes()
        output = self.root / 'bundle-0001'
        result = build_bundle(self.kit, output)
        self.assertEqual(result, verify_bundle(output, self.kit))
        self.assertEqual(result['kit_manifest_sha256'], sha(before))
        self.assertEqual(before, (self.kit / 'manifest.json').read_bytes())
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o700)
        self.assertFalse(any('deployment-receipt' in p.name for p in output.rglob('*')))
        with self.assertRaises(ValueError): build_bundle(self.kit, output)

    def test_default_bridge_sources_are_included_but_unknown_source_leaf_rejects(self):
        source = self.kit / 'source'
        for leaf in ('test_default_bridge_store.py', 'testdata/default-bridge-records.json'):
            self.assertEqual((source / 'deploy/experience/engine' / leaf).read_bytes(),
                             (HERE / leaf).read_bytes())
        build_bundle(self.kit, self.root / 'bundle-0001')
        (source / 'deploy/experience/engine/unapproved-source.py').write_text('# unknown\n')
        sources = [source_record(p, str(p.relative_to(source)))
                   for p in sorted(source.rglob('*')) if p.is_file() and '/qualification/' not in str(p)]
        for arch in ('arm64', 'amd64'):
            path = self.kit / ('kit-inputs-' + arch + '.json')
            inputs = json.loads(path.read_bytes()); inputs['source'] = sources
            path.write_bytes(encode(inputs))
        path = self.kit / 'build-receipt.json'; receipt_value = json.loads(path.read_bytes())
        receipt_value.update(source_input_root=sha(encode(sources)), before=sources, after=sources)
        path.write_bytes(encode(receipt_value)); rewrite_manifest(self.kit)
        with self.assertRaisesRegex(ValueError, 'incomplete/unknown Engine source inputs'):
            build_bundle(self.kit, self.root / 'bundle-0002')

    def test_missing_or_same_bytes_wrong_path_fd_helper_rejected(self):
        source = self.kit / 'source'
        helper = source / 'deploy/experience/engine/fd_lifecycle.py'
        raw = helper.read_bytes()
        for wrong_path in (False, True):
            with self.subTest(wrong_path=wrong_path):
                helper.unlink(missing_ok=True)
                wrong = helper.with_name('fd_lifecycle_wrong.py')
                if wrong_path:
                    wrong.write_bytes(raw)
                sources = [source_record(p, str(p.relative_to(source)))
                    for p in sorted(source.rglob('*')) if p.is_file() and '/qualification/' not in str(p)]
                for arch in ('arm64', 'amd64'):
                    path = self.kit / ('kit-inputs-' + arch + '.json')
                    inputs = json.loads(path.read_bytes()); inputs['source'] = sources
                    path.write_bytes(encode(inputs))
                path = self.kit / 'build-receipt.json'; receipt_value = json.loads(path.read_bytes())
                receipt_value.update(source_input_root=sha(encode(sources)), before=sources, after=sources)
                path.write_bytes(encode(receipt_value)); rewrite_manifest(self.kit)
                with self.assertRaisesRegex(ValueError, 'incomplete/unknown Engine source inputs'):
                    build_bundle(self.kit, self.root / ('bundle-0002' if wrong_path else 'bundle-0001'))
                wrong.unlink(missing_ok=True)

    def test_missing_extra_or_changed_kit_leaf_rejected(self):
        (self.kit / 'unlisted').write_text('unlisted')
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0001')
        (self.kit / 'unlisted').unlink()
        target = self.kit / 'source/deploy/experience/engine/daemon.json'
        target.write_bytes(target.read_bytes() + b'\n')
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0002')

    def test_symlink_hardlink_unsafe_owner_mode_rejected(self):
        target = self.kit / 'source/deploy/experience/engine/daemon.json'
        os.link(target, self.root / 'linked')
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0001')
        (self.root / 'linked').unlink()
        target.chmod(0o666)
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0002')
        target.chmod(0o644)
        target.unlink(); target.symlink_to(HERE / 'daemon.json')
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0003')

    def test_duplicate_input_and_cycle_rejected(self):
        p = self.kit / 'kit-inputs-arm64.json'
        value = json.loads(p.read_bytes()); value['source'].append(value['source'][0])
        p.write_bytes(encode(value)); rewrite_manifest(self.kit)
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0001')

    def test_runtime_receipt_may_never_be_an_upstream_source(self):
        p = self.kit / 'source/deploy/experience/engine/deployment-receipt.json'
        p.write_text('{}'); rewrite_manifest(self.kit)
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0001')

    def test_bundle_readback_detects_drift(self):
        output = self.root / 'bundle-0001'; build_bundle(self.kit, output)
        p = output / 'source/deploy/experience/engine/daemon.json'
        p.write_bytes(p.read_bytes() + b' ')
        with self.assertRaises(ValueError): verify_bundle(output, self.kit)

    def test_manifest_self_hash_cycle_rejected(self):
        p = self.kit / 'manifest.json'; value = json.loads(p.read_bytes())
        value['files'].append(source_record(p, 'manifest.json'))
        p.write_bytes(encode(value))
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0001')

    def test_unsafe_owner_and_unlisted_directory_rejected(self):
        with mock.patch('bundle.os.geteuid', return_value=os.geteuid() + 1):
            with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0001')
        (self.kit / 'unlisted-empty').mkdir()
        with self.assertRaises(ValueError): build_bundle(self.kit, self.root / 'bundle-0002')


class SharedReceiptParityTests(unittest.TestCase):
    """The same receipt-v1 fixture and vectors are exercised by the Go consumer."""
    def assert_rejected(self, value):
        with self.assertRaises(ValueError):
            validate_receipt(_json(encode(value)))

    def test_socket_owner_pid_requires_exact_integer(self):
        for invalid in (101.0, 101.5, '101', None, True, False):
            value = receipt()
            if type(invalid) is bool:
                value['daemons'][0]['main_pid'] = 1
                for item in value['sockets'][:2]: item['owner_pid'] = 1
            value['sockets'][0]['owner_pid'] = invalid
            with self.subTest(owner_pid=invalid, type=type(invalid).__name__):
                self.assert_rejected(value)
        facts = listeners(); facts['sockets'][0]['owner_pid'] = 101.0
        with self.assertRaises(ValueError): validate_listeners(_json(encode(facts)))

    def test_socket_owner_start_tick_requires_exact_integer(self):
        for invalid in (1001.0, 1001.5, '1001', None, True, False):
            value = receipt()
            if type(invalid) is bool:
                value['daemons'][0]['start_tick'] = 1
                for item in value['sockets'][:2]: item['owner_start_tick'] = 1
            value['sockets'][0]['owner_start_tick'] = invalid
            with self.subTest(owner_start_tick=invalid, type=type(invalid).__name__):
                self.assert_rejected(value)
        facts = listeners(); facts['sockets'][0]['owner_start_tick'] = 1001.0
        with self.assertRaises(ValueError): validate_listeners(_json(encode(facts)))

    def test_ipam_fields_require_strings_before_address_parsing(self):
        for key, invalid in (('gateway', 2887647233), ('gateway', True), ('gateway', 2887647233.0),
                             ('gateway', None), ('gateway', []), ('subnet', 2887647232),
                             ('subnet', True), ('subnet', None), ('subnet', [])):
            value = receipt(); value['networks'][0]['ipam']['config'][0][key] = invalid
            with self.subTest(field=key, value=invalid): self.assert_rejected(value)

    def test_ipam_text_requires_canonical_ipv4_prefix_and_gateway(self):
        for key, invalid in (
                ('subnet', '172.30.0.0/255.255.0.0'), ('subnet', '172.30.0.0/0.0.255.255'),
                ('subnet', '172.30.0.0/016'), ('subnet', '172.30.0.0/+16'),
                ('subnet', '172.030.0.0/16'), ('gateway', '172.030.0.1'),
                ('gateway', '172.30.0.01'), ('gateway', ' 172.30.0.1'),
                ('gateway', '172.30.0.1 '), ('gateway', '+172.30.0.1')):
            value = receipt(); value['networks'][0]['ipam']['config'][0][key] = invalid
            with self.subTest(field=key, value=invalid): self.assert_rejected(value)

    def test_ipam_accepts_only_entire_rfc1918_ipv4_subnets(self):
        for subnet, gateway in (
                ('0.0.0.0/24', '0.0.0.1'), ('127.0.0.0/24', '127.0.0.1'),
                ('169.254.1.0/24', '169.254.1.1'), ('192.0.0.0/29', '192.0.0.1'),
                ('192.0.2.0/24', '192.0.2.1'), ('198.51.100.0/24', '198.51.100.1'),
                ('203.0.113.0/24', '203.0.113.1'), ('198.18.0.0/24', '198.18.0.1'),
                ('240.0.0.0/24', '240.0.0.1'), ('255.255.255.0/24', '255.255.255.1'),
                ('224.0.0.0/24', '224.0.0.1'), ('100.64.0.0/24', '100.64.0.1'),
                ('8.8.8.0/24', '8.8.8.1'), ('10.0.0.0/7', '10.0.0.1'),
                ('172.0.0.0/8', '172.16.0.1'), ('192.0.0.0/8', '192.168.0.1'),
                ('::1/128', '::1'), ('::ffff:172.30.0.0/120', '::ffff:172.30.0.1')):
            value = receipt(); value['networks'][0]['ipam']['config'][0] = {'subnet': subnet, 'gateway': gateway}
            with self.subTest(subnet=subnet): self.assert_rejected(value)
        for subnet, gateway in (('10.20.0.0/24', '10.20.0.1'), ('172.16.0.0/24', '172.16.0.1'),
                                ('192.168.0.0/24', '192.168.0.1')):
            value = receipt(); value['networks'][0]['ipam']['config'][0] = {'subnet': subnet, 'gateway': gateway}
            with self.subTest(valid_subnet=subnet): validate_receipt(_json(encode(value)))
        validate_receipt(_json(encode(receipt())))


if __name__ == '__main__':
    unittest.main()
